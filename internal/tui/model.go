package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awsint "github.com/treyp/ssm-tool/internal/aws"
	"github.com/treyp/ssm-tool/internal/tunnel"
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
)

// ---- list item -------------------------------------------------------------

type item struct {
	title string
	desc  string
	value string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title }

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

	// list widget (reused across screens)
	list list.Model
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
	l := list.New(nil, delegate, 0, 0)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)

	m := &Model{
		ctx:         ctx,
		ssoSessions: ssoSessions,
		profiles:    profiles,
		manager:     tunnel.NewManager(),
		spin:        sp,
		list:        l,
	}

	// if only one SSO session and no profiles, skip method screen
	if len(ssoSessions) == 1 && len(profiles) == 0 {
		m.selSession = &ssoSessions[0]
		m.screen = screenAccount
	} else {
		m.screen = screenMethod
		m.buildMethodList()
	}

	return m, nil
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick}
	switch m.screen {
	case screenAccount:
		cmds = append(cmds, m.fetchToken())
	}
	return tea.Batch(cmds...)
}

// ---- update ----------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, msg.Height-4)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// screen-specific key handling
		switch m.screen {
		case screenMain:
			return m.updateMain(msg)
		default:
			if msg.String() == "esc" {
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
		if len(m.ssoSessions) > 1 || len(m.profiles) > 0 {
			m.screen = screenMethod
			m.buildMethodList()
		}
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
	}
	return nil
}

// ---- screen-specific selectors ---------------------------------------------

func (m *Model) selectMethod(val string) tea.Cmd {
	if val == "" {
		return tea.Quit
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
	return tea.ExecProcess(m.buildShellCmd(), func(err error) tea.Msg {
		return msgShellDone{}
	})
}

func (m *Model) buildShellCmd() *exec.Cmd {
	cmd, err := tunnel.ShellCmd(m.selInstance.ID, m.awsSess)
	if err != nil {
		// fallback: will show error when exec'd
		return exec.Command("echo", "plugin error: "+err.Error())
	}
	return cmd
}

func (m *Model) startSSH(user string) tea.Cmd {
	m.loading = true
	port := tunnel.FreePort(2222)
	return func() tea.Msg {
		t, err := tunnel.StartPortForward(m.selInstance.ID, 22, port, m.awsSess)
		if err != nil {
			return msgTunnelReady{err: err}
		}
		if err := tunnel.WaitPort(port, 15*time.Second); err != nil {
			t.Kill()
			return msgTunnelReady{err: err}
		}
		t.Kind = tunnel.KindSSH
		t.InstanceID = m.selInstance.ID
		t.InstanceName = m.selInstance.Name
		t.LocalPort = port
		t.AuthLabel = m.awsSess.Label
		fmt.Fprintf(os.Stderr, "\n[ssh] ready: ssh -o StrictHostKeyChecking=no -p %d %s@localhost\n", port, user)
		return msgTunnelReady{t: t}
	}
}

func (m *Model) startRDP() tea.Cmd {
	m.loading = true
	port := tunnel.FreePort(13389)
	return func() tea.Msg {
		t, err := tunnel.StartPortForward(m.selInstance.ID, 3389, port, m.awsSess)
		if err != nil {
			return msgTunnelReady{err: err}
		}
		// give the SSM session 3s to establish
		if err := tunnel.WaitPort(port, 10*time.Second); err != nil {
			t.Kill()
			return msgTunnelReady{err: err}
		}
		t.Kind = tunnel.KindRDP
		t.InstanceID = m.selInstance.ID
		t.InstanceName = m.selInstance.Name
		t.LocalPort = port
		t.AuthLabel = m.awsSess.Label
		return msgTunnelReady{t: t}
	}
}

// ---- main screen -----------------------------------------------------------

func (m *Model) updateMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	m.list.Title = "Select authentication method"
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
	m.list.Title = "Select AWS account  •  Esc=back"
	m.list.SetItems(items)
}

func (m *Model) buildRoleList() {
	var items []list.Item
	for _, r := range m.roles {
		items = append(items, item{title: r, value: r})
	}
	m.list.Title = fmt.Sprintf("Select role for %s  •  Esc=back", m.selAccount.Name)
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
	m.list.Title = "Select instance  •  Esc=back"
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

// ---- view ------------------------------------------------------------------

func (m *Model) View() string {
	if m.loading {
		return fmt.Sprintf("\n  %s loading...\n", m.spin.View())
	}
	if m.err != nil {
		return fmt.Sprintf("\n  %s\n\n  %s\n",
			styleErr.Render("Error: "+m.err.Error()),
			styleDim.Render("press any key to continue"),
		)
	}
	header := styleTitle.Render("▶ ssm-tool") + "\n"
	return header + m.list.View()
}
