package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awsint "github.com/treyperrone/ssm-tool/internal/aws"
	"github.com/treyperrone/ssm-tool/internal/buildinfo"
	"github.com/treyperrone/ssm-tool/internal/tunnel"
)

// ---- screens ---------------------------------------------------------------

type screen int

const (
	screenMethod   screen = iota // pick SSO session or profile
	screenAccount                // pick AWS account
	screenRole                   // pick role
	screenInstance               // pick EC2 instance
	screenConnType               // pick connection type
	screenSSHUser                // pick SSH username
	screenMain                   // main tunnel manager
	// screenSetup is last so that screenMethod stays the zero value: a Model that somehow
	// reaches Update without New() should fall into the normal picker, not the config writer.
	screenSetup  // first run — no sso-session and no profile in ~/.aws/config
	screenRegion // region picker, opened from the setup form
)

// ---- list item -------------------------------------------------------------

type item struct {
	title string
	desc  string
	value string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }

// FilterValue is what "/" searches. It spans the description as well as the title so
// every field on screen is searchable: an account by name *or* ID, an instance by name,
// instance ID, private IP, or type. Matching only the title — which is all the account
// name, or all the instance name — meant the IDs you can plainly see were unsearchable.
//
// Match highlighting still only paints the title (bubbles maps the match indices onto it),
// so a description-only hit filters correctly but highlights nothing. Out-of-range indices
// are ignored rather than fatal, so this is cosmetic.
func (i item) FilterValue() string { return i.title + " " + i.desc }

// ---- messages --------------------------------------------------------------

type msgAccounts struct {
	accounts []awsint.Account
	token    string
	err      error
}
type msgRoles struct {
	roles []string
	err   error
}
type msgInstances struct {
	instances []awsint.Instance
	err       error
}
type msgToken struct {
	token string
	err   error
}
type msgTunnelReady struct {
	t   *tunnel.Tunnel
	err error
}
type msgError struct{ err error }

// ---- styles ----------------------------------------------------------------

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stylePrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// ---- model -----------------------------------------------------------------

type Model struct {
	ctx      context.Context
	width    int
	height   int
	screen   screen
	err      error
	loading  bool
	spin     spinner.Model

	// auth state
	ssoSessions []awsint.SSOSessionConfig
	profiles    []awsint.ProfileConfig
	selSession  *awsint.SSOSessionConfig
	token       string
	accounts    []awsint.Account
	selAccount  *awsint.Account
	roles       []string
	awsSess     *awsint.Session

	// instance selection
	instances   []awsint.Instance
	selInstance *awsint.Instance
	connType    tunnel.Kind

	// tunnel manager
	manager *tunnel.Manager

	// last instance connected to (for banner)
	lastInstance string

	// list widget (reused across screens)
	list list.Model

	// first-run SSO config form
	setup setupForm
}

func New(ctx context.Context) (*Model, error) {
	ssoSessions, profiles, err := awsint.ParseConfig()
	if err != nil {
		return nil, err
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Background(lipgloss.Color("63")).
		Foreground(lipgloss.Color("230")).
		Bold(true).
		PaddingRight(100) // forces highlight to fill line width
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Background(lipgloss.Color("63")).
		Foreground(lipgloss.Color("189")).
		PaddingRight(100)
	delegate.Styles.NormalTitle = delegate.Styles.NormalTitle.
		Foreground(lipgloss.Color("252"))
	delegate.Styles.NormalDesc = delegate.Styles.NormalDesc.
		Foreground(lipgloss.Color("240"))
	l := list.New(nil, delegate, 0, 0)
	l.SetShowHelp(false)
	// The status bar carries the "x/y items" count and the active filter term. With 50
	// accounts, knowing a search narrowed to 3 is the difference between trusting the
	// list and re-reading it.
	l.SetShowStatusBar(true)

	m := &Model{
		ctx:         ctx,
		ssoSessions: ssoSessions,
		profiles:    profiles,
		manager:     tunnel.NewManager(),
		spin:        sp,
		list:        l,
	}

	// Nothing configured at all. Previously this was a dead end — a missing ~/.aws/config
	// aborted before the TUI started, and a config with no sso-session or profile left an
	// empty picker with nothing to select. Offer to write the block instead.
	if len(ssoSessions) == 0 && len(profiles) == 0 {
		m.screen = screenSetup
		m.setup = newSetupForm()
		return m, nil
	}

	// The method screen always comes first, including for a single SSO session. Skipping it
	// saved one keystroke and cost the user any view of which identity they were about to
	// use, and hid "+ Add SSO session" from exactly the person with one session who wants a
	// second.
	m.screen = screenMethod
	m.buildMethodList()

	return m, nil
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick}
	switch m.screen {
	// Startup no longer opens on the account list — the method screen always comes first —
	// but goBack and StartSetup can still land here, so the fetch stays wired up.
	case screenAccount:
		cmds = append(cmds, m.fetchToken())
	case screenSetup:
		cmds = append(cmds, m.setup.init())
	}
	return tea.Batch(cmds...)
}

// ---- update ----------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, msg.Height-5) // -1 for banner, -4 for list chrome

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// The setup form owns the keyboard outright: every keystroke is text entry, and
		// the list shortcuts below (including "/" and "q") would eat characters.
		if m.screen == screenSetup {
			return m.updateSetup(msg)
		}
		// The error view promises "press any key"; make that true. Only esc and enter
		// cleared it before, so every other key looked like a hang.
		if m.err != nil {
			m.err = nil
			return m, nil
		}
		// While the search input has focus, every keystroke belongs to it. Without this
		// guard the shortcuts below eat them: "esc" navigates back instead of cancelling
		// the search, "enter" selects whatever is highlighted mid-typing rather than
		// applying the filter, and on the main screen "n"/"p"/"q" fire their commands
		// instead of appearing in the box.
		if m.list.SettingFilter() {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		// screen-specific key handling
		switch m.screen {
		case screenMain:
			return m.updateMain(msg)
		default:
			// With a search applied, esc clears it (the list's own binding) before it
			// means "go back a screen".
			if msg.String() == "esc" && !m.list.IsFiltered() {
				return m, m.goBack()
			}
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case msgToken:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.token = msg.token
		m.loading = true
		return m, m.fetchAccounts()

	case msgAccounts:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.accounts = msg.accounts
		m.token = msg.token
		m.buildAccountList()
		m.screen = screenAccount

	case msgRoles:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.roles = msg.roles
		if len(m.roles) == 1 {
			return m, m.selectRole(m.roles[0])
		}
		m.buildRoleList()
		m.screen = screenRole

	case msgCredsReady:
		m.loading = true
		return m, m.fetchInstances()

	case msgInstances:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.instances = msg.instances
		m.buildInstanceList()
		m.screen = screenInstance

	case msgShellDone:
		m.buildMainList()
		m.screen = screenMain
		return m, nil

	case msgTunnelReady:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.manager.Add(msg.t)
		m.buildMainList()
		m.screen = screenMain

	case msgError:
		m.loading = false
		m.err = msg.err
	}

	// Non-key messages on the setup screen — cursor blink, in particular — belong to the
	// focused input, not the list. Keys already returned above.
	if m.screen == screenSetup {
		var cmd tea.Cmd
		m.setup.inputs[m.setup.focus], cmd = m.setup.inputs[m.setup.focus].Update(msg)
		return m, cmd
	}

	// delegate to list widget
	if !m.loading {
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "enter" {
			return m, m.handleSelect()
		}
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	return m, m.spin.Tick
}

func (m *Model) goBack() tea.Cmd {
	m.err = nil
	switch m.screen {
	case screenAccount:
		// Always go back, even with a single session and no profiles. This used to be a
		// one-way door in that case: the method list is where "+ Add SSO session" lives,
		// so it was unreachable for precisely the single-session user wanting a second.
		m.screen = screenMethod
		m.buildMethodList()
	case screenRole:
		m.buildAccountList()
		m.screen = screenAccount
	case screenInstance:
		m.screen = screenMain
		m.buildMainList()
	case screenSSHUser:
		m.buildInstanceList()
		m.screen = screenInstance
	case screenConnType:
		m.buildInstanceList()
		m.screen = screenInstance
	case screenRegion:
		// Backing out of the picker leaves the region field as it was. Restart the blink:
		// the form's cursor is still focused, but nothing is driving it after the detour.
		m.screen = screenSetup
		return m.setup.init()
	}
	return nil
}

func (m *Model) handleSelect() tea.Cmd {
	selected, ok := m.list.SelectedItem().(item)
	if !ok {
		return nil
	}
	m.err = nil

	switch m.screen {
	case screenMethod:
		return m.selectMethod(selected.value)
	case screenAccount:
		return m.selectAccount(selected.value)
	case screenRole:
		return m.selectRole(selected.value)
	case screenInstance:
		return m.selectInstance(selected.value)
	case screenConnType:
		return m.selectConnType(selected.value)
	case screenSSHUser:
		return m.startSSH(selected.value)
	case screenMain:
		return m.handleMainSelect(selected.value)
	case screenRegion:
		return m.selectRegion(selected.value)
	}
	return nil
}

// ---- screen-specific selectors ---------------------------------------------

func (m *Model) selectMethod(val string) tea.Cmd {
	if val == "" {
		return tea.Quit
	}
	if val == methodAddSession {
		return m.StartSetup()
	}
	// profile
	for _, p := range m.profiles {
		if "profile:"+p.Name == val {
			_ = os.Setenv("AWS_PROFILE", p.Name)
			_ = os.Unsetenv("AWS_ACCESS_KEY_ID")
			_ = os.Unsetenv("AWS_SECRET_ACCESS_KEY")
			_ = os.Unsetenv("AWS_SESSION_TOKEN")
			m.awsSess = &awsint.Session{
				Region: "us-east-1",
				Label:  "profile:" + p.Name,
			}
			m.loading = true
			return m.fetchInstances()
		}
	}
	// SSO session
	for i, s := range m.ssoSessions {
		if s.Name == val {
			m.selSession = &m.ssoSessions[i]
			m.loading = true
			return m.fetchToken()
		}
	}
	return nil
}

func (m *Model) selectAccount(val string) tea.Cmd {
	for _, a := range m.accounts {
		if a.ID == val {
			m.selAccount = &awsint.Account{ID: a.ID, Name: a.Name}
			m.loading = true
			return m.fetchRoles()
		}
	}
	return nil
}

type msgCredsReady struct{}

func (m *Model) selectRole(role string) tea.Cmd {
	m.loading = true
	return func() tea.Msg {
		creds, err := awsint.GetRoleCredentials(m.ctx, *m.selSession, m.token, m.selAccount.ID, role)
		if err != nil {
			return msgError{err}
		}
		creds.AccountID = m.selAccount.ID
		creds.BuildLabel(m.selAccount.Name, role)
		m.awsSess = creds
		return msgCredsReady{}
	}
}

func (m *Model) selectInstance(id string) tea.Cmd {
	for _, inst := range m.instances {
		if inst.ID == id {
			m.selInstance = &awsint.Instance{
				ID:        inst.ID,
				Name:      inst.Name,
				PrivateIP: inst.PrivateIP,
				Type:      inst.Type,
			}
			m.buildConnTypeList()
			m.screen = screenConnType
			return nil
		}
	}
	return nil
}

func (m *Model) selectConnType(val string) tea.Cmd {
	switch val {
	case "shell":
		return m.startShell()
	case "ssh":
		m.connType = tunnel.KindSSH
		m.buildSSHUserList()
		m.screen = screenSSHUser
	case "rdp":
		m.connType = tunnel.KindRDP
		return m.startRDP()
	case "quit":
		return tea.Quit
	}
	return nil
}

type msgShellDone struct{}

func (m *Model) startShell() tea.Cmd {
	m.lastInstance = m.selInstance.Name
	return tea.ExecProcess(m.buildShellCmd(), func(err error) tea.Msg {
		return msgShellDone{}
	})
}

func (m *Model) buildShellCmd() *exec.Cmd {
	cmd, err := tunnel.ShellCmd(m.ctx, m.selInstance.ID, m.awsSess)
	if err != nil {
		return exec.Command("echo", "plugin error: "+err.Error())
	}
	return m.wrapWithHeader(cmd, m.selInstance.Name)
}

// wrapWithHeader tries to wrap cmd in a tmux session with a persistent purple
// status bar. If tmux is not available it falls back to a plain header print.
// On Windows it returns cmd unchanged.
func (m *Model) wrapWithHeader(cmd *exec.Cmd, instanceName string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return cmd
	}

	authLabel := ""
	if m.awsSess != nil {
		authLabel = m.awsSess.Label
	}

	tmuxBin, err := exec.LookPath("tmux")
	if err == nil {
		return m.wrapWithTmux(tmuxBin, cmd, instanceName, authLabel)
	}

	// tmux not available — print a header line and accept it may scroll
	const script = `
printf '\033]0;ssm-tool · %s · %s\007' "$_SSM_INSTANCE" "$_SSM_AUTH"
printf '\033[1;48;5;63;38;5;230m ▶ ssm-tool  \033[38;5;99m│  \033[38;5;189m%s  \033[38;5;99m│  \033[38;5;189m%s\033[0m\n' "$_SSM_AUTH" "$_SSM_INSTANCE"
"$_SSM_BIN" "$@"
`
	args := append([]string{"/bin/sh", "-c", script, "ssm-tool"}, cmd.Args[1:]...)
	wrapped := exec.Command(args[0], args[1:]...)
	wrapped.Env = append(cmd.Env,
		"_SSM_BIN="+cmd.Path,
		"_SSM_AUTH="+authLabel,
		"_SSM_INSTANCE="+instanceName,
	)
	return wrapped
}

// wrapWithTmux creates a named tmux session running cmd and attaches to it.
// The tmux status bar shows the persistent purple banner, immune to remote
// shell resets. The session is destroyed on detach/exit.
func (m *Model) wrapWithTmux(tmuxBin string, cmd *exec.Cmd, instanceName, authLabel string) *exec.Cmd {
	sessionName := fmt.Sprintf("ssm-%d", os.Getpid())

	// Build the inner command string: set env vars then exec the plugin.
	// We use env(1) to pass the plugin args cleanly without shell quoting issues.
	innerEnv := ""
	for _, e := range cmd.Env {
		// only pass the SSM-specific vars we set; inherit the rest via tmux
		if strings.HasPrefix(e, "AWS_") {
			innerEnv += "export " + e + "; "
		}
	}
	// Build plugin arg list safe for single-quoted shell embedding.
	// The JSON blobs contain no single-quotes so this is safe.
	quotedArgs := ""
	for _, a := range cmd.Args {
		quotedArgs += "'" + a + "' "
	}
	innerCmd := innerEnv + quotedArgs

	statusText := fmt.Sprintf(" ▶ ssm-tool  │  %s  │  %s ", authLabel, instanceName)

	// new-session runs the plugin, then destroy-session on exit so attach returns.
	newSession := exec.Command(tmuxBin,
		"new-session", "-d",
		"-s", sessionName,
		"-x", fmt.Sprintf("%d", m.width),
		"-y", fmt.Sprintf("%d", m.height-1), // -1 for status bar
		innerCmd,
	)
	newSession.Env = cmd.Env
	if err := newSession.Run(); err != nil {
		// fallback: just run cmd directly
		return cmd
	}

	// Configure status bar: bottom, purple, fixed text.
	for _, args := range [][]string{
		{"set-option", "-t", sessionName, "status", "on"},
		{"set-option", "-t", sessionName, "status-position", "bottom"},
		{"set-option", "-t", sessionName, "status-style", "bg=colour63,fg=colour230,bold"},
		{"set-option", "-t", sessionName, "status-left", statusText},
		{"set-option", "-t", sessionName, "status-right", ""},
		{"set-option", "-t", sessionName, "status-left-length", "200"},
		// destroy session automatically when the plugin exits
		{"set-option", "-t", sessionName, "remain-on-exit", "off"},
	} {
		_ = exec.Command(tmuxBin, args...).Run()
	}

	// attach-session is what tea.ExecProcess will run foreground.
	attach := exec.Command(tmuxBin, "attach-session", "-t", sessionName)
	attach.Env = os.Environ()
	return attach
}

func (m *Model) startSSH(user string) tea.Cmd {
	m.loading = true
	m.lastInstance = m.selInstance.Name
	port := tunnel.FreePort(2222)
	instID := m.selInstance.ID
	instName := m.selInstance.Name
	authLabel := m.awsSess.Label
	return func() tea.Msg {
		t, err := tunnel.StartPortForward(m.ctx, instID, 22, port, m.awsSess)
		if err != nil {
			return msgTunnelReady{err: err}
		}
		if err := tunnel.WaitPort(port, 15*time.Second); err != nil {
			t.Kill()
			return msgTunnelReady{err: err}
		}
		t.Kind = tunnel.KindSSH
		t.InstanceID = instID
		t.InstanceName = instName
		t.LocalPort = port
		t.AuthLabel = authLabel
		t.SSHUser = user
		return msgTunnelReady{t: t}
	}
}

func (m *Model) startRDP() tea.Cmd {
	m.loading = true
	m.lastInstance = m.selInstance.Name
	port := tunnel.FreePort(13389)
	instID := m.selInstance.ID
	instName := m.selInstance.Name
	authLabel := m.awsSess.Label
	return func() tea.Msg {
		t, err := tunnel.StartPortForward(m.ctx, instID, 3389, port, m.awsSess)
		if err != nil {
			return msgTunnelReady{err: err}
		}
		if err := tunnel.WaitPort(port, 10*time.Second); err != nil {
			t.Kill()
			return msgTunnelReady{err: err}
		}
		t.Kind = tunnel.KindRDP
		t.InstanceID = instID
		t.InstanceName = instName
		t.LocalPort = port
		t.AuthLabel = authLabel
		return msgTunnelReady{t: t}
	}
}

// ---- main screen -----------------------------------------------------------

func (m *Model) updateMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// With a search applied, esc clears it rather than quitting the tool — losing your
	// active tunnels to a stray keystroke meant for the search box would be rude.
	if m.list.IsFiltered() && msg.String() == "esc" {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "n":
		// new connection — go to instance picker
		m.loading = true
		return m, m.fetchInstances()
	case "p":
		// switch auth
		m.screen = screenMethod
		m.buildMethodList()
		return m, nil
	case "q", "esc":
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	if msg.String() == "enter" {
		return m, m.handleMainSelect("")
	}
	return m, cmd
}

func (m *Model) handleMainSelect(val string) tea.Cmd {
	selected, ok := m.list.SelectedItem().(item)
	if !ok {
		return nil
	}
	switch selected.value {
	case "new":
		m.loading = true
		return m.fetchInstances()
	case "quit":
		return tea.Quit
	default:
		// kill tunnel by PID stored in value
		for _, t := range m.manager.Active() {
			if fmt.Sprintf("%d", t.PID) == selected.value {
				t.Kill()
				m.manager.Remove(t)
				m.buildMainList()
				return nil
			}
		}
	}
	return nil
}

// ---- async commands --------------------------------------------------------

func (m *Model) fetchToken() tea.Cmd {
	sess := *m.selSession
	return func() tea.Msg {
		token, err := awsint.LiveToken(m.ctx, sess)
		return msgToken{token: token, err: err}
	}
}

func (m *Model) fetchAccounts() tea.Cmd {
	sess := *m.selSession
	token := m.token
	return func() tea.Msg {
		accounts, err := awsint.ListAccounts(m.ctx, sess, token)
		return msgAccounts{accounts: accounts, token: token, err: err}
	}
}

func (m *Model) fetchRoles() tea.Cmd {
	sess := *m.selSession
	token := m.token
	accountID := m.selAccount.ID
	return func() tea.Msg {
		roles, err := awsint.ListRoles(m.ctx, sess, token, accountID)
		return msgRoles{roles: roles, err: err}
	}
}

func (m *Model) fetchInstances() tea.Cmd {
	awsSess := m.awsSess
	return func() tea.Msg {
		instances, err := awsint.ListInstances(m.ctx, awsSess)
		return msgInstances{instances: instances, err: err}
	}
}

// ---- list builders ---------------------------------------------------------

func (m *Model) buildMethodList() {
	var items []list.Item
	for _, s := range m.ssoSessions {
		items = append(items, item{
			title: "SSO: " + s.Name,
			desc:  s.StartURL,
			value: s.Name,
		})
	}
	for _, p := range m.profiles {
		items = append(items, item{
			title: "Profile: " + p.Name,
			desc:  "named AWS profile",
			value: "profile:" + p.Name,
		})
	}
	// Adding a second SSO session — a prod range alongside the lab one — has to be
	// reachable without hand-editing ~/.aws/config, so the form lives here as well as on
	// first run. See goBack: this screen is always reachable from the account list, or
	// this entry would be invisible to exactly the person most likely to need it.
	items = append(items, item{
		title: "+ Add SSO session",
		desc:  "append a new [sso-session] block to ~/.aws/config",
		value: methodAddSession,
	})

	m.list.Title = "Select authentication method"
	m.list.SetStatusBarItemName("method", "methods")
	m.list.SetItems(items)
}

func (m *Model) buildAccountList() {
	var items []list.Item
	for _, a := range m.accounts {
		items = append(items, item{
			title: a.Name,
			desc:  a.ID,
			value: a.ID,
		})
	}
	m.list.Title = "Select AWS account  •  /=search name or ID  •  Esc=back"
	m.list.SetStatusBarItemName("account", "accounts")
	m.list.SetItems(items)
}

func (m *Model) buildRoleList() {
	var items []list.Item
	for _, r := range m.roles {
		items = append(items, item{title: r, value: r})
	}
	m.list.Title = fmt.Sprintf("Select role for %s  •  Esc=back", m.selAccount.Name)
	m.list.SetStatusBarItemName("role", "roles")
	m.list.SetItems(items)
}

func (m *Model) buildInstanceList() {
	var items []list.Item
	for _, i := range m.instances {
		items = append(items, item{
			title: i.Name,
			desc:  fmt.Sprintf("%s  %s  %s", i.ID, i.PrivateIP, i.Type),
			value: i.ID,
		})
	}
	m.list.Title = "Select instance  •  /=search name, ID, or IP  •  Esc=back"
	m.list.SetStatusBarItemName("instance", "instances")
	m.list.SetItems(items)
}

func (m *Model) buildConnTypeList() {
	items := []list.Item{
		item{title: "Shell session", desc: "interactive SSM shell (foreground)", value: "shell"},
		item{title: "SSH tunnel", desc: "forward port 22, connect with your SSH client", value: "ssh"},
		item{title: "RDP tunnel", desc: "forward port 3389 → localhost:13389+", value: "rdp"},
		item{title: "Quit", desc: "", value: "quit"},
	}
	m.list.Title = fmt.Sprintf("Connect to %s  •  Esc=back", m.selInstance.Name)
	m.list.SetItems(items)
}

func (m *Model) buildSSHUserList() {
	items := []list.Item{
		item{title: "ec2-user", desc: "Amazon Linux, RHEL, CentOS, SUSE", value: "ec2-user"},
		item{title: "ubuntu", desc: "Ubuntu", value: "ubuntu"},
		item{title: "admin", desc: "Debian", value: "admin"},
		item{title: "kali", desc: "Kali Linux", value: "kali"},
		item{title: "root", desc: "root access", value: "root"},
	}
	m.list.Title = "Select SSH username  •  Esc=back"
	m.list.SetItems(items)
}

func (m *Model) buildMainList() {
	var items []list.Item
	for _, t := range m.manager.Active() {
		items = append(items, item{
			title: t.Label(),
			desc:  fmt.Sprintf("PID %d", t.PID),
			value: fmt.Sprintf("%d", t.PID),
		})
	}
	items = append(items,
		item{title: "[n] New connection", desc: "pick account → instance → type", value: "new"},
		item{title: "[q] Quit", desc: "active tunnels keep running", value: "quit"},
	)
	auth := ""
	if m.awsSess != nil {
		auth = m.awsSess.Label
	}
	m.list.Title = fmt.Sprintf("SSM  •  %s  •  n=new  p=switch auth  q=quit", auth)
	m.list.SetItems(items)
}

// ---- styles for banner -----------------------------------------------------

var (
	styleBanner     = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("230")).Bold(true).PaddingLeft(1).PaddingRight(1)
	styleBannerDim  = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("189")).PaddingLeft(1).PaddingRight(1)
	styleBannerSep  = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("99")).PaddingLeft(1).PaddingRight(1)
	styleBannerFill = lipgloss.NewStyle().Background(lipgloss.Color("63"))
	styleBannerVer  = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("104")).PaddingRight(1)
)

// ---- view ------------------------------------------------------------------

func (m *Model) View() string {
	if m.screen == screenSetup {
		return m.banner() + m.setup.view(m.width)
	}
	if m.loading {
		return m.banner() + fmt.Sprintf("\n  %s loading...\n", m.spin.View())
	}
	if m.err != nil {
		// AWS SDK errors are long single-line strings. Unwrapped, the terminal hard-truncates
		// them at the right edge, so the useful half is never seen — and bubbletea, which
		// counts logical lines rather than rendered rows, then leaves stale rows on screen
		// (the duplicated "press any key"). Giving the style an explicit Width makes lipgloss
		// word-wrap, which fixes both.
		width := m.width - 4
		if width < 20 {
			width = 76 // no WindowSizeMsg yet
		}
		return m.banner() + "\n" +
			styleErr.Width(width).MarginLeft(2).Render("Error: "+m.err.Error()) + "\n\n" +
			styleDim.MarginLeft(2).Render("press any key to continue") + "\n"
	}
	return m.banner() + m.list.View()
}

// banner renders the purple header row. withVersion is false only on the retry when the
// terminal is too narrow to hold everything — see the caller.
func (m *Model) banner() string {
	row := m.bannerRow(true)
	// Too narrow for the version: drop it rather than let the row overflow into a second
	// line. A half-printed version number is worse than none.
	if lipgloss.Width(row) > m.width {
		row = m.bannerRow(false)
	}

	// fill remaining width with banner background
	if visibleLen := lipgloss.Width(row); m.width > visibleLen {
		row += styleBannerFill.Render(strings.Repeat(" ", m.width-visibleLen))
	}

	return row + "\n"
}

func (m *Model) bannerRow(withVersion bool) string {
	title := styleBanner.Render("▶ ssm-tool")
	sep := styleBannerSep.Render("│")

	var parts []string
	parts = append(parts, title)

	// The version belongs next to the name it qualifies — that is where anyone looks for
	// it, and at the right edge it read as decoration.
	if withVersion {
		parts = append(parts, styleBannerVer.Render(buildinfo.Version()))
	}

	if m.awsSess != nil && m.awsSess.Label != "" {
		parts = append(parts, sep, styleBannerDim.Render(m.awsSess.Label))
	}

	if m.lastInstance != "" {
		parts = append(parts, sep, styleBannerDim.Render("last: "+m.lastInstance))
	}

	active := len(m.manager.Active())
	if active > 0 {
		parts = append(parts, sep, styleBannerDim.Render(fmt.Sprintf("%d active", active)))
	}

	row := ""
	for _, p := range parts {
		row += p
	}
	return row
}
