package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
	"github.com/treyperrone/warren/internal/buildinfo"
	"github.com/treyperrone/warren/internal/credserver"
	"github.com/treyperrone/warren/internal/termwin"
	"github.com/treyperrone/warren/internal/tunnel"
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
	screenSetup          // first run — no sso-session and no profile in ~/.aws/config
	screenRegion         // region picker, opened from the setup form
	screenAction         // what to do with the credentials just resolved
	screenBuildService   // command builder: pick a service
	screenBuildTask      // command builder: pick a task within that service
	screenBuildParams    // command builder: fill in parameters and run
	screenAbout          // version, keys, and where to report a problem
	screenBrowser        // which browser SSO sign-in opens in (the ⚙ setting)
	screenBrowserProfile // which profile inside that browser
	screenLoginBrowser   // the same choice, asked inline because a sign-in is needed NOW
	screenLoginProfile   // profile step of the inline ask
	screenLoginRemember  // "just this once" vs "always" after an inline pick
	screenS3Buckets      // S3 browser: pick a bucket
	screenS3Objects      // S3 browser: one delimiter level of a bucket
	screenS3Upload       // S3 browser: path box that accepts a dragged file
	screenFavorites      // all favorites, when too many to inline on the method screen
	screenFavoriteRemove // pick favorites to delete
	screenProfileRemove  // pick an AWS profile to remove from ~/.aws/config
	screenProfileConfirm // the exact doomed lines + keep/remove
	screenSessionActions // what to do with an active tunnel: reconnect / favorite / disconnect
)

// resumeKind is what a successful re-authentication should do once it has rebuilt
// credentials — pick up the action that hit the expired session, rather than dumping the
// user back at the method screen to re-navigate.
type resumeKind int

const (
	resumeNone      resumeKind = iota
	resumeActionHub            // the "what next?" screen
	resumeInstances            // re-list EC2 instances
	resumeConnect              // re-run the connection: selInstance + connType (+ resumeSSHUser)
	resumeS3Buckets            // re-list S3 buckets
)

// ---- list plumbing ---------------------------------------------------------

// setListItems installs a screen's rows, clearing any search left over from the previous one.
//
// One list widget is shared by every screen, and its filter was shared too: SetItems re-applies
// the existing term to whatever it is handed (see bubbles/list.SetItems). So searching "globo"
// to find an account left that term filtering the *next* screen, which hid every row and read
// as a broken menu rather than as a search still being active. Resetting first leaves SetItems
// nothing to re-apply.
//
// The cursor is only moved when a search was actually active, because resetting the filter
// renumbers the rows and a cursor from the filtered view would land on an unrelated entry.
// With no filter it is left alone, so backing out to a screen keeps your place in it.
func (m *Model) setListItems(items []list.Item) {
	if m.list.FilterState() != list.Unfiltered {
		m.list.ResetFilter()
		m.list.Select(0)
	}
	m.list.SetItems(items)
}

// ---- list item -------------------------------------------------------------

type item struct {
	title string
	desc  string
	value string
	// search is extra text "/" matches but the row does not display. Instance tags go here:
	// an instance can easily carry a dozen CloudFormation-managed tags, which would bury the
	// ID and IP if rendered, but are exactly what you want to search by.
	search string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }

// FilterValue is what "/" searches. It spans the description as well as the title so
// every field on screen is searchable: an account by name *or* ID, an instance by name,
// instance ID, private IP, or type. Matching only the title — which is all the account
// name, or all the instance name — meant the IDs you can plainly see were unsearchable.
// It also spans the hidden search text, so instances match on any tag.
//
// Match highlighting still only paints the title (bubbles maps the match indices onto it),
// so a description-only hit filters correctly but highlights nothing. Out-of-range indices
// are ignored rather than fatal, so this is cosmetic.
func (i item) FilterValue() string {
	// Appended only when present, so rows without hidden text are byte-for-byte what they
	// were before search text existed — a trailing space would be harmless to the fuzzy
	// matcher but makes the value awkward to assert on.
	if i.search == "" {
		return i.title + " " + i.desc
	}
	return i.title + " " + i.desc + " " + i.search
}

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
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	// Green rather than the banner's purple: this reports that something succeeded elsewhere,
	// and it must not read as part of the chrome the way a dim grey line would.
	styleNotice = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// ---- model -----------------------------------------------------------------

type Model struct {
	ctx     context.Context
	width   int
	height  int
	screen  screen
	err     error
	loading bool
	spin    spinner.Model

	// auth state
	ssoSessions []awsint.SSOSessionConfig
	profiles    []awsint.ProfileConfig
	selSession  *awsint.SSOSessionConfig
	token       string
	accounts    []awsint.Account
	selAccount  *awsint.Account
	roles       []string
	selRole     string // the role most recently chosen, kept so a re-auth can rebuild the same credentials
	awsSess     *awsint.Session

	// browser preference for SSO sign-in
	browsers        []browser.Browser
	selBrowser      *browser.Browser
	browserProfiles []browser.Profile

	// pendingLogin is a device authorization awaiting approval. Non-nil is what switches the
	// view from the plain spinner to the sign-in screen with the URL and code on it; loginNote
	// says what happened to the browser (opened, suppressed, failed), because "nothing visibly
	// happened" needs an explanation right where the user is looking.
	pendingLogin *awsint.PendingLogin
	loginNote    string
	// loginChoice is the answer the inline ask produced, overriding the saved preference for
	// exactly one sign-in; loginChoiceDraft carries it between the pick and the remember
	// question. Cleared when the token arrives, so the next ask starts clean.
	loginChoice      *browser.Pref
	loginChoiceDraft browser.Pref
	// loginAskShownAt guards the inline picker against the Enter that selected the session
	// arriving twice (double-tap, key repeat) — see selectLoginBrowser.
	loginAskShownAt time.Time
	// pendingProfile is the profile whose credential resolution is waiting on the sign-in
	// currently in flight; profileLoginSess anchors selSession for that detour, since the
	// session may be synthesized (legacy inline SSO) and point into no slice.
	pendingProfile   string
	profileLoginSess awsint.SSOSessionConfig
	// favorites is the method screen's snapshot of the saved bookmarks, indexed by the
	// fav:N row values; pendingFavRole is the role a favorite promised, fetched the moment
	// the token lands — favorites skip the account and role screens, that is their point.
	favorites      []browser.Favorite
	pendingFavRole string
	// pendingFavConn is the connection half of a selected favorite, carried across the
	// creds fetch: once credentials land, the flow resolves the instance by Name tag and
	// starts the connection instead of stopping at the action screen.
	pendingFavConn *browser.Favorite
	// connFavCandidate is the connection that could be starred right now — built when a
	// session/tunnel starts, offered as a row on the tunnel manager.
	connFavCandidate *browser.Favorite

	// S3 browser state: the bucket roster, the level being shown (bucket/region/prefix and
	// its entries), and the upload screen's path box with its inline validation error.
	s3Buckets   []string
	s3Bucket    string
	s3Region    string
	s3Prefix    string
	s3Entries   []awsint.S3Entry
	s3Input     textinput.Model
	s3UploadErr string

	// profile removal in flight: which block, and its exact text for the confirm screen.
	profileRemoveName  string
	profileRemoveBlock string

	// liveSess is the thread-safe mirror of awsSess for transfer goroutines: stored on the
	// event loop, loaded from S3 credential providers mid-transfer, so a download that
	// crosses the hour mark picks up the background renewal instead of dying on the keys
	// it started with.
	liveSess atomic.Value
	// loginCancel aborts the Wait poll. Quitting is the only key the loading guard lets
	// through while a login is pending, and without this the polling goroutine outlives the
	// screen — m.ctx is main's context.Background and nothing ever cancels it.
	loginCancel context.CancelFunc

	// instance selection
	instances   []awsint.Instance
	selInstance *awsint.Instance
	connType    tunnel.Kind

	// tunnel manager
	manager *tunnel.Manager

	// sessionActionTunnel is the active tunnel whose row was opened on the manager screen —
	// the target of a reconnect, favorite, or disconnect on screenSessionActions.
	sessionActionTunnel *tunnel.Tunnel

	// resume is what a successful re-auth should do once credentials are rebuilt, and
	// resumeSSHUser is the username to reconnect an SSH tunnel as. Set by startReauth,
	// consumed in the msgCredsReady handler.
	resume        resumeKind
	resumeSSHUser string

	// last instance connected to (for banner)
	lastInstance string

	// notice is a one-line transient confirmation, cleared by the next keypress. It exists for
	// actions whose result happens somewhere the user is not looking — a session opening in
	// another window is otherwise indistinguishable from nothing having happened.
	notice string

	// list widget (reused across screens)
	list list.Model

	// first-run SSO config form
	setup setupForm

	// AWS CLI command builder
	builder builder

	// credsOnly stops the flow once credentials are resolved, instead of going on to list
	// instances. `warren exec` and `warren shell` want an account and a role and nothing
	// after that — API access has no instance to pick.
	credsOnly bool

	// background credential renewal
	refreshingCreds bool
	credRefreshErr  error

	// credSrv serves credentials to child processes over loopback, so a shell is not stuck with
	// the copy it started with. Started on first use, not at launch.
	credSrv *credserver.Server
	// credRefreshStop halts the endpoint's own renewal goroutine, which covers the window where
	// a child holds the terminal and the event loop cannot tick.
	credRefreshStop context.CancelFunc

	// aboutReturn is the screen "?" was pressed on, so esc goes back to it rather than to a
	// fixed screen. Reachable from anywhere means there is no single sensible place to return to.
	aboutReturn screen
	// aboutVP scrolls the about screen. Its content is taller than a 24-row terminal, and without
	// this the top — including the warren version, the single most useful line on it — scrolled
	// off the screen with no way to get it back.
	aboutVP viewport.Model
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
	// credTick re-arms itself for the life of the program, which is what keeps credentials
	// fresh for as long as the TUI is open. It no-ops until there are credentials to renew.
	cmds := []tea.Cmd{m.spin.Tick, credTick()}
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
		m.resizeList()

	case tea.MouseMsg:
		// Only the about screen scrolls, and mouse reporting is only enabled while it is up.
		if m.screen == screenAbout {
			var cmd tea.Cmd
			m.aboutVP, cmd = m.aboutVP.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			// Stop a pending sign-in poll on the way out, or the goroutine keeps polling
			// AWS until the device code expires — after the screen it reports to is gone.
			if m.loginCancel != nil {
				m.loginCancel()
			}
			return m, tea.Quit
		}
		// Nothing but ctrl+c while a request is in flight.
		//
		// m.loading was set in nineteen places and read only by View, so keys kept being handled
		// during an async fetch — and a second Enter on an SSO session started a *second* device
		// authorization. That opens a second browser tab and prints a second user code, so the
		// code on screen and the code the browser is asking about belong to different
		// authorizations: they visibly do not match, and neither one can be confirmed. Easy to
		// trigger by pressing Enter again when the first call is slow.
		//
		// Device auth polls until the code expires, so this can hold the keyboard for a while.
		// That is the right trade: ctrl+c always works, and the alternative is two live
		// authorizations racing each other.
		if m.loading {
			return m, nil
		}
		// The setup form owns the keyboard outright: every keystroke is text entry, and
		// the list shortcuts below (including "/" and "q") would eat characters.
		if m.screen == screenSetup {
			return m.updateSetup(msg)
		}
		// Same for the builder's parameter screen: it is text entry, and the list shortcuts
		// below would eat the characters.
		if m.screen == screenBuildParams {
			return m.updateBuildParams(msg)
		}
		// And the S3 upload box — where a dragged file arrives as pasted text, every byte
		// of which belongs to the input.
		if m.screen == screenS3Upload {
			return m.updateS3Upload(msg)
		}
		// A notice has been read by the time the next key arrives; leaving it up would make it
		// look like it applied to whatever happens next. Cleared without consuming the key,
		// unlike an error, because it is a confirmation rather than something to acknowledge.
		m.notice = ""

		// The error view promises "press any key"; make that true. Only esc and enter
		// cleared it before, so every other key looked like a hang.
		if m.err != nil {
			m.err = nil
			return m, nil
		}
		// The about screen is a dead end by design: it takes no action, so anything that means
		// "I am done here" should leave it.
		if m.screen == screenAbout {
			switch msg.String() {
			case "esc", "q", AboutKey, "enter":
				m.screen = m.aboutReturn
				return m, tea.DisableMouse
			}
			// Everything else goes to the viewport, which owns up/down/pgup/pgdn/home/end.
			var cmd tea.Cmd
			m.aboutVP, cmd = m.aboutVP.Update(msg)
			return m, cmd
		}
		// Open it from any list screen. Checked before the list gets the key because bubbles
		// binds "?" to its own full-help toggle, which would swallow it.
		if msg.String() == AboutKey && !m.list.SettingFilter() {
			m.aboutReturn = m.screen
			m.screen = screenAbout
			m.resizeAbout()
			m.aboutVP.GotoTop()
			// Mouse reporting is turned on for this screen alone, and off again on the way out.
			// Enabling it for the whole program would take click-drag text selection away from
			// the terminal everywhere — and copying an account ID, an instance ID or a built
			// command out of a list is worth more than wheel scrolling on a reference screen.
			return m, tea.EnableMouseCellMotion
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
		// r re-authenticates when the background renewal has given up on the SSO session
		// (ErrLoginRequired). The credentials on hand may still have minutes left, so this is
		// offered rather than forced — the header note advertises the key.
		if msg.String() == "r" && errors.Is(m.credRefreshErr, awsint.ErrLoginRequired) && m.canReauth() {
			m.credRefreshErr = nil
			return m, m.startReauth(resumeActionHub)
		}
		// x on a highlighted favorite row deletes it, wherever favorites render — the row's
		// own description advertises the key, because a keybind nobody can see is a feature
		// nobody has. Guarded on the filter not having focus (typed search text must never
		// delete things), which the SettingFilter branch above already ensures.
		if (m.screen == screenMethod || m.screen == screenFavorites) && msg.String() == "x" {
			if sel, ok := m.list.SelectedItem().(item); ok {
				if idx, isFav := strings.CutPrefix(sel.value, "fav:"); isFav {
					m.removeFavoriteAt(idx)
					return m, nil
				}
			}
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

	case msgCredTick:
		// Always re-arm, whether or not a renewal is due, or the clock stops after the first
		// check and nothing renews for the rest of the session.
		if m.needsCredRefresh(time.Time(msg)) {
			return m, tea.Batch(m.refreshCreds(), credTick())
		}
		return m, credTick()

	case msgCredsRefreshed:
		m.applyCredRefresh(msg)
		return m, nil

	case msgProfileLoginNeeded:
		// Route the profile into the ordinary sign-in flow — ask screen, per-session
		// browser override, code on screen — and remember to come back for its
		// credentials once the token lands. selSession points at a Model-owned copy
		// because a legacy profile's session is synthesized and lives in no slice.
		m.profileLoginSess = msg.sess
		m.selSession = &m.profileLoginSess
		m.pendingProfile = msg.profile.Name
		m.loading = true
		return m, m.fetchToken()

	case msgLoginAsk:
		m.startLoginAsk()
		return m, nil

	case msgLoginPending:
		// Put the URL and code on screen *before* anything opens: if the browser launch
		// fails or lands somewhere invisible, the screen already holds everything needed
		// to finish by hand. OpenForLogin never errors — failures come back as the note.
		m.pendingLogin = msg.pending
		var pref browser.Pref
		switch {
		case m.loginChoice != nil:
			// The inline ask already answered for this one sign-in.
			pref = *m.loginChoice
		case m.selSession != nil:
			pref, _ = browser.ResolvePrefFor(m.selSession.StartURL)
		default:
			pref = browser.LoadPref()
		}
		m.loginNote = browser.OpenForLogin(pref, msg.pending.VerificationURL)
		if w := msg.pending.RegistrationWarning; w != "" {
			m.loginNote += "\n" + w
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.loginCancel = cancel
		return m, m.waitForLogin(ctx, msg.pending)

	case msgToken:
		m.loading = false
		m.pendingLogin = nil
		m.loginNote = ""
		m.loginChoice = nil
		if m.loginCancel != nil {
			// Wait already returned; cancelling only releases the derived context.
			m.loginCancel()
			m.loginCancel = nil
		}
		if msg.err != nil {
			m.err = msg.err
			// A failed sign-in ends any profile detour with it, or a LATER token success
			// would resurrect a retry nobody is waiting for.
			if m.pendingProfile != "" {
				m.pendingProfile = ""
				m.selSession = nil
			}
			m.pendingFavRole = ""
			m.pendingFavConn = nil
			m.resume = resumeNone
			return m, nil
		}
		m.token = msg.token
		// A favorite already names its account and role: fetch the credentials directly,
		// skipping the account and role screens — that skip is what a favorite is.
		if m.pendingFavRole != "" {
			role := m.pendingFavRole
			m.pendingFavRole = ""
			return m, m.selectRole(role)
		}
		// A sign-in that ran on behalf of a profile goes back for the profile's
		// credentials, not to the account list — accounts belong to the session flow, and
		// the profile already names its account and role. selSession reverts to nil so
		// goBack keeps treating this as the profile flow it is.
		if m.pendingProfile != "" {
			var pending *awsint.ProfileConfig
			for i := range m.profiles {
				if m.profiles[i].Name == m.pendingProfile {
					pending = &m.profiles[i]
					break
				}
			}
			m.pendingProfile = ""
			m.selSession = nil
			if pending == nil {
				// Config changed under us mid-flight; the method list is the only honest place.
				m.buildMethodList()
				m.screen = screenMethod
				return m, nil
			}
			m.loading = true
			return m, m.fetchProfileSession(*pending)
		}
		m.loading = true
		return m, m.fetchAccounts()

	case msgAccounts:
		m.loading = false
		m.resume = resumeNone
		if msg.err != nil {
			if awsint.NeedsReauth(msg.err) && m.selSession != nil {
				m.err = nil
				m.loading = true
				return m, m.fetchToken()
			}
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
			if awsint.NeedsReauth(msg.err) && m.selSession != nil {
				m.err = nil
				m.loading = true
				return m, m.fetchToken()
			}
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
		// Credentials are the whole point in creds-only mode; quitting here hands control
		// back to main, which runs the command with them.
		if m.credsOnly {
			return m, tea.Quit
		}
		// A connection favorite is not done at credentials: go find its instance.
		if m.pendingFavConn != nil {
			m.loading = true
			return m, m.fetchInstances()
		}
		// A re-auth rebuilt these credentials to pick up something that was interrupted by an
		// expired session — resume it rather than dumping the user on the action hub.
		switch m.resume {
		case resumeInstances:
			m.resume = resumeNone
			// A tunnel that outlived the timeout is the thing most worth landing on, and the
			// manager is the only screen that shows it — its "n" gets to the instance list.
			if len(m.manager.Active()) > 0 {
				m.notice = "signed back in — your active tunnels are still here"
				m.buildMainList()
				m.screen = screenMain
				return m, nil
			}
			m.loading = true
			m.screen = screenInstance
			return m, m.fetchInstances()
		case resumeConnect:
			m.resume = resumeNone
			if m.selInstance != nil {
				switch m.connType {
				case tunnel.KindRDP:
					return m, m.startRDP()
				case tunnel.KindSSH:
					return m, m.startSSH(m.resumeSSHUser)
				}
			}
		case resumeS3Buckets:
			m.resume = resumeNone
			m.loading = true
			return m, m.fetchS3Buckets()
		case resumeActionHub:
			m.resume = resumeNone
			if len(m.manager.Active()) > 0 {
				m.notice = "signed back in — your active tunnels are still here"
				m.buildMainList()
				m.screen = screenMain
				return m, nil
			}
		}
		m.loading = false
		m.buildActionList()
		m.screen = screenAction

	case msgProfileReady:
		m.awsSess = msg.sess
		if m.credsOnly {
			return m, tea.Quit
		}
		m.loading = false
		m.buildActionList()
		m.screen = screenAction

	case msgCredsShellDone:
		// Back to the action list, not the tunnel manager: you were doing API work, and the
		// likely next step is another command, not a connection.
		m.endCredRefresh()
		m.buildActionList()
		m.screen = screenAction
		return m, nil

	case msgBuildDone:
		// Stay on the parameter screen: the usual next step is the same query with a
		// different value, or the same command with an edit.
		m.endCredRefresh()
		return m, textinput.Blink

	case msgInstances:
		m.loading = false
		if msg.err != nil {
			m.pendingFavConn = nil
			if awsint.NeedsReauth(msg.err) && m.canReauth() {
				return m, m.startReauth(resumeInstances)
			}
			m.err = msg.err
			return m, nil
		}
		m.instances = msg.instances
		// A favorite names its instance by Name TAG and resolves it here, against what is
		// actually running — ids rot in a range that repaves, names are policy. Exactly one
		// running match connects; zero or several drop to the list with the reason on
		// screen, because guessing between two hosts called "kali" helps nobody.
		if f := m.pendingFavConn; f != nil {
			m.pendingFavConn = nil
			var matches []awsint.Instance
			for _, inst := range m.instances {
				if inst.Name == f.InstanceName {
					matches = append(matches, inst)
				}
			}
			if len(matches) == 1 {
				m.selectInstance(matches[0].ID)
				// The one auto-connect a favorite must NOT make: RDP into a box now
				// reporting as Linux — likely a repave changed the OS under the name.
				// Pausing on the connection screen turns it into enter-to-continue (the
				// RDP row carries the warning) or esc-to-cancel, instead of a tunnel to a
				// port nothing listens on.
				if f.ConnType == "rdp" && matches[0].Platform == "linux" &&
					!browser.RDPLinuxAcked(m.ackAccountID(), matches[0].Name) {
					m.notice = f.InstanceName + " reports as a Linux box — RDP favorite paused; Enter connects anyway, Esc backs out"
					// "Enter connects anyway" is a promise about the CURSOR: park it on
					// the RDP row, or Enter acts on whatever index the previous screen
					// left behind.
					m.list.Select(2) // shell, ssh, RDP
					return m, nil    // selectInstance already built the conn-type screen
				}
				switch f.ConnType {
				case "shell":
					return m, m.startShell()
				case "ssh":
					return m, m.startSSH(f.SSHUser)
				case "rdp":
					m.connType = tunnel.KindRDP
					return m, m.startRDP()
				}
			}
			if len(matches) == 0 {
				m.notice = "favorite target " + f.InstanceName + " is not running here — pick manually"
			} else {
				m.notice = fmt.Sprintf("%d running instances are named %s — pick manually", len(matches), f.InstanceName)
			}
		}
		m.buildInstanceList()
		m.screen = screenInstance

	case msgShellWindowed:
		m.noteConnFavCandidate(msg.name, "shell", "")
		// The terminal was never handed over, so there is nothing to restore — just say where the
		// session went and stay put. Landing back on the instance list is what makes opening a
		// second one a single keypress, which is the whole reason for the window.
		m.loading = false
		m.notice = fmt.Sprintf("%s opened in a new %s window", msg.name, msg.where)
		if len(m.instances) > 0 {
			m.buildInstanceList()
			m.screen = screenInstance
		} else {
			m.buildMainList()
			m.screen = screenMain
		}
		return m, nil

	case msgShellDone:
		// Back to the instance list rather than the tunnel manager. A foreground shell
		// registers no tunnel, so the manager has nothing new to show, and the likely next
		// step is another host. It is also the safer landing: unlike the manager, no single
		// keystroke there ends the program, which matters when a terminal handed back from a
		// raw-mode child can emit escape sequences that read as keypresses.
		if len(m.instances) > 0 {
			m.buildInstanceList()
			m.screen = screenInstance
		} else {
			m.buildMainList()
			m.screen = screenMain
		}
		return m, nil

	case msgTunnelReady:
		m.loading = false
		if msg.err != nil {
			if awsint.NeedsReauth(msg.err) && m.canReauth() {
				return m, m.startReauth(resumeConnect)
			}
			m.err = msg.err
			return m, nil
		}
		m.manager.Add(msg.t)
		m.noteConnFavCandidate(msg.t.InstanceName, strings.ToLower(string(msg.t.Kind)), msg.t.SSHUser)
		if msg.t.Kind == tunnel.KindRDP {
			// The tunnel connecting itself is the point of an RDP favorite — and of RDP
			// tunnels generally: "now paste localhost:13389 somewhere" was the residue of
			// not finishing the job. The note reports which client opened, or falls back
			// to the manual instruction when none is installed.
			m.notice = tunnel.OpenRDPClient(msg.t.LocalPort, msg.t.SSHUser, msg.t.InstanceName, browser.LoadRDPScreen() == browser.RDPFullscreen)
		}
		m.buildMainList()
		m.screen = screenMain

	case msgS3Buckets:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.s3Buckets = msg.buckets
		m.buildS3BucketList()
		m.screen = screenS3Buckets

	case msgS3Objects:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.s3Bucket, m.s3Region, m.s3Prefix = msg.bucket, msg.region, msg.prefix
		m.s3Entries = msg.entries
		m.buildS3ObjectList()
		m.screen = screenS3Objects

	case msgS3Done:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.notice = msg.note
		if msg.refresh {
			// An upload changed the level on screen; relist so the new object is visible
			// proof rather than a claim in a notice.
			m.loading = true
			return m, m.fetchS3Level(m.s3Bucket, m.s3Region, m.s3Prefix)
		}
		return m, nil

	case msgError:
		m.loading = false
		// An expired SSO session that surfaces here — a failed GetRoleCredentials, an S3 call
		// on a stale token — is recoverable: run the sign-in and pick up where we left off,
		// rather than dead-ending on "press any key". resumeConnect is not offered from here
		// because a raw msgError carries no instance context; the tunnel path has its own hook.
		if awsint.NeedsReauth(msg.err) && m.canReauth() {
			what := resumeActionHub
			if m.screen == screenS3Buckets || m.screen == screenS3Objects || m.screen == screenS3Upload {
				what = resumeS3Buckets
			}
			m.pendingFavConn = nil
			return m, m.startReauth(what)
		}
		m.err = msg.err
		// Any error ends whatever detour was in flight. A stale pendingFavConn surviving
		// here hijacked the NEXT credential flow into an automatic connection — in whatever
		// account the user had moved on to — the moment its instance name happened to
		// match. Pending state must never outlive the flow that created it.
		m.pendingProfile = ""
		m.pendingFavRole = ""
		m.pendingFavConn = nil
		m.resume = resumeNone
	}

	// Non-key messages on the setup screen — cursor blink, in particular — belong to the
	// focused input, not the list. Keys already returned above.
	if m.screen == screenSetup {
		var cmd tea.Cmd
		m.setup.inputs[m.setup.focus], cmd = m.setup.inputs[m.setup.focus].Update(msg)
		return m, cmd
	}
	if m.screen == screenBuildParams {
		var cmd tea.Cmd
		if m.builder.editing {
			m.builder.edit, cmd = m.builder.edit.Update(msg)
		} else if len(m.builder.inputs) > 0 {
			m.builder.inputs[m.builder.focus], cmd = m.builder.inputs[m.builder.focus].Update(msg)
		}
		return m, cmd
	}
	if m.screen == screenS3Upload {
		var cmd tea.Cmd
		m.s3Input, cmd = m.s3Input.Update(msg)
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
	case screenAction:
		// Back to wherever the credentials came from. A profile is picked on the method
		// screen and has no account or role step, a single-role account skips the role
		// screen (see msgRoles), and a favorite skips both — so nothing here is a safe
		// unconditional target.
		switch {
		case m.selSession == nil:
			m.screen = screenMethod
			m.buildMethodList()
		case len(m.roles) > 1:
			m.buildRoleList()
			m.screen = screenRole
		case len(m.accounts) == 0:
			// No account list was ever fetched — the favorite flow jumps straight from the
			// method screen to credentials — so home is the method screen, and an empty
			// account screen would be a wall.
			m.screen = screenMethod
			m.buildMethodList()
		default:
			m.buildAccountList()
			m.screen = screenAccount
		}
	case screenInstance:
		// The action list, not the tunnel manager — that is the screen this one was reached
		// from, and it is where "Run AWS CLI commands" lives.
		m.buildActionList()
		m.screen = screenAction
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
	case screenBrowser:
		m.screen = screenMethod
		m.buildMethodList()
	case screenBrowserProfile:
		m.buildBrowserList()
		m.screen = screenBrowser
	case screenLoginBrowser:
		// Backing out of the ask abandons the sign-in — the authorization has not started
		// yet, so there is nothing to cancel beyond returning to the method list. That
		// includes any profile detour that was waiting on it.
		if m.pendingProfile != "" {
			m.pendingProfile = ""
			m.selSession = nil
		}
		m.pendingFavRole = ""
		m.pendingFavConn = nil
		m.screen = screenMethod
		m.buildMethodList()
	case screenLoginProfile:
		m.buildLoginBrowserList()
		m.screen = screenLoginBrowser
	case screenLoginRemember:
		m.buildLoginBrowserList()
		m.screen = screenLoginBrowser
	case screenS3Buckets:
		m.buildActionList()
		m.screen = screenAction
	case screenS3Objects:
		// Esc walks up one delimiter level until the bucket root, then out to the buckets.
		// The target is only PASSED to the fetch — m.s3Prefix commits in the msgS3Objects
		// success handler, so a failed listing leaves the screen and the state agreeing
		// (an upload after a failed ascent used to target a level nobody was looking at).
		if m.s3Prefix != "" {
			m.loading = true
			return m.fetchS3Level(m.s3Bucket, m.s3Region, parentPrefix(m.s3Prefix))
		}
		m.buildS3BucketList()
		m.screen = screenS3Buckets
	case screenS3Upload:
		m.buildS3ObjectList()
		m.screen = screenS3Objects
	case screenFavorites, screenProfileRemove:
		m.buildMethodList()
		m.screen = screenMethod
	case screenFavoriteRemove:
		m.buildFavoritesList()
		m.screen = screenFavorites
	case screenProfileConfirm:
		m.buildProfileRemoveList()
		m.screen = screenProfileRemove
	case screenBuildService:
		m.buildActionList()
		m.screen = screenAction
	case screenBuildTask:
		m.buildServiceList()
		m.screen = screenBuildService
	case screenSessionActions:
		m.sessionActionTunnel = nil
		m.buildMainList()
		m.screen = screenMain
	case screenMain:
		// "Back" is the hub the manager was reached from: the action screen once credentials
		// exist, otherwise the method screen — which the "Active tunnels" row on the method
		// screen (persisted tunnels, no session yet) can now land here from.
		if m.awsSess != nil {
			m.buildActionList()
			m.screen = screenAction
		} else {
			m.buildMethodList()
			m.screen = screenMethod
		}
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
	case screenAction:
		return m.selectAction(selected.value)
	case screenBuildService:
		return m.selectBuildService(selected.value)
	case screenBuildTask:
		return m.selectBuildTask(selected.value)
	case screenInstance:
		return m.selectInstance(selected.value)
	case screenConnType:
		return m.selectConnType(selected.value)
	case screenSSHUser:
		return m.startSSH(selected.value)
	case screenMain:
		return m.handleMainSelect(selected.value)
	case screenSessionActions:
		return m.selectSessionAction(selected.value)
	case screenRegion:
		return m.selectRegion(selected.value)
	case screenBrowser:
		return m.selectBrowser(selected.value)
	case screenBrowserProfile:
		return m.selectBrowserProfile(selected.value)
	case screenLoginBrowser:
		return m.selectLoginBrowser(selected.value)
	case screenLoginProfile:
		return m.selectLoginProfile(selected.value)
	case screenLoginRemember:
		return m.selectLoginRemember(selected.value)
	case screenS3Buckets:
		return m.selectS3Bucket(selected.value)
	case screenS3Objects:
		return m.selectS3Entry(selected.value)
	case screenFavorites:
		if selected.value == "favrm" {
			m.buildFavoriteRemoveList()
			m.screen = screenFavoriteRemove
			return nil
		}
		if idx, ok := strings.CutPrefix(selected.value, "fav:"); ok {
			return m.selectFavorite(idx)
		}
		return nil
	case screenFavoriteRemove:
		return m.selectFavoriteRemove(selected.value)
	case screenProfileRemove:
		return m.selectProfileRemove(selected.value)
	case screenProfileConfirm:
		return m.selectProfileConfirm(selected.value)
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
	if val == methodTunnels {
		m.buildMainList()
		m.screen = screenMain
		return nil
	}
	if val == methodBrowserPref {
		return m.startBrowserPref()
	}
	if val == methodRDPScreen {
		return m.toggleRDPScreen()
	}
	if idx, ok := strings.CutPrefix(val, "fav:"); ok {
		return m.selectFavorite(idx)
	}
	if val == methodFavList {
		m.buildFavoritesList()
		m.screen = screenFavorites
		return nil
	}
	if val == methodRemoveProfil {
		m.buildProfileRemoveList()
		m.screen = screenProfileRemove
		return nil
	}
	// profile
	for _, p := range m.profiles {
		if "profile:"+p.Name == val {
			m.loading = true
			return m.fetchProfileSession(p)
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

// selectFavorite is the one-Enter path: resolve the favorite's session, stage its account
// and role, and go get a token — the ordinary sign-in flow (ask screen, overrides, code on
// screen) runs unchanged if the token is cold, and msgToken finishes the job by fetching
// role credentials directly instead of listing accounts.
func (m *Model) selectFavorite(idx string) tea.Cmd {
	var f *browser.Favorite
	for i := range m.favorites {
		if fmt.Sprintf("%d", i) == idx {
			f = &m.favorites[i]
			break
		}
	}
	if f == nil {
		return nil
	}
	for i := range m.ssoSessions {
		if m.ssoSessions[i].StartURL == f.StartURL {
			m.selSession = &m.ssoSessions[i]
			m.selAccount = &awsint.Account{ID: f.AccountID, Name: f.AccountName}
			// A favorite skips the account and role screens, so any lists a PREVIOUS flow
			// left behind are lies about this one — goBack from the action screen would
			// otherwise offer another account's roles under this account's title.
			m.accounts = nil
			m.roles = nil
			m.pendingFavRole = f.Role
			if f.Connects() && !m.credsOnly {
				// The connection half rides along; creds-only callers (exec/shell) stop at
				// credentials no matter what the favorite carries.
				fc := *f
				m.pendingFavConn = &fc
			}
			m.loading = true
			return m.fetchToken()
		}
	}
	// The favorite outlived its sso-session: say what to fix rather than guessing at a
	// start URL nothing in ~/.aws/config vouches for.
	m.err = fmt.Errorf("favorite %s points at %s, which no [sso-session] in ~/.aws/config uses anymore", f.Nickname, f.StartURL)
	return nil
}

type msgCredsReady struct{}

type msgProfileReady struct{ sess *awsint.Session }

// msgProfileLoginNeeded means a profile's credentials failed BECAUSE its SSO session needs a
// sign-in — the one failure a device auth actually fixes. It used to surface as a dead-end
// error ("login session has expired, please reauthenticate" + press-any-key), telling the
// user to do by hand exactly what the tool exists to do.
type msgProfileLoginNeeded struct {
	profile awsint.ProfileConfig
	sess    awsint.SSOSessionConfig
}

// fetchProfileSession resolves a profile's credentials, diagnosing an expired SSO session as
// the reauth case rather than an error. Resolve real credentials rather than setting
// AWS_PROFILE in this process and carrying an empty Session: every consumer passes the
// Session's fields to a static credentials provider, which rejects empty values. Async
// because an SSO-backed or assume-role profile can reach the network here.
func (m *Model) fetchProfileSession(p awsint.ProfileConfig) tea.Cmd {
	ctx := m.ctx
	sessions := m.ssoSessions
	return func() tea.Msg {
		sess, err := awsint.ProfileSession(ctx, p.Name)
		if err == nil {
			return msgProfileReady{sess: sess}
		}
		// Only claim "sign in fixes this" when it plausibly does: the profile is SSO-backed
		// AND its underlying session genuinely has no silent path left. Any other failure —
		// bad keys, a broken role chain, a live session with a different problem — stays an
		// error, because routing it into a sign-in would burn a device code on a dead end.
		if loginSess, ssoBacked := p.LoginSession(sessions); ssoBacked && loginSess != nil {
			if _, serr := awsint.SilentToken(ctx, *loginSess); errors.Is(serr, awsint.ErrLoginRequired) {
				return msgProfileLoginNeeded{profile: p, sess: *loginSess}
			}
		}
		return msgError{err}
	}
}

// StartCredsMode stops the flow at the point credentials exist, for `exec` and `shell`.
func (m *Model) StartCredsMode() {
	m.credsOnly = true
}

// Session returns the resolved credentials, or nil if the user quit before choosing. A nil
// return is an ordinary cancellation, not a failure.
func (m *Model) Session() *awsint.Session {
	return m.awsSess
}

func (m *Model) selectRole(role string) tea.Cmd {
	m.loading = true
	m.selRole = role
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

// startReauth runs the interactive device-auth flow for the SSO session behind the current
// credentials, then rebuilds role credentials and resumes `what`. It is the sso-session
// counterpart of the profile flow's msgProfileLoginNeeded recovery: an expired session that
// surfaces as an API error used to be a dead end ("press any key") with esc-esc-esc back to
// the method screen as the only way forward.
//
// It reuses the ordinary sign-in path — fetchToken renders the code on screen and honours
// the per-session browser override — and the pendingFavRole branch in the msgToken handler,
// which already knows how to turn a fresh token straight into role credentials.
func (m *Model) startReauth(what resumeKind) tea.Cmd {
	m.err = nil
	m.resume = what
	role := m.selRole
	if role == "" && m.awsSess != nil {
		role = m.awsSess.RoleName
	}
	// With a role to reassume, msgToken fetches its credentials directly and msgCredsReady
	// runs the resume. Without one — an expired token caught while still on the account or
	// role screen — the plain cold-token path lands back on the account list.
	m.pendingFavRole = role
	m.loading = true
	return m.fetchToken()
}

// canReauth reports whether startReauth has enough context to rebuild the current identity:
// a known SSO session and account. Without both, an expired-session error stays an error.
func (m *Model) canReauth() bool {
	return m.selSession != nil && m.selAccount != nil
}

func (m *Model) selectInstance(id string) tea.Cmd {
	for _, inst := range m.instances {
		if inst.ID == id {
			m.selInstance = &awsint.Instance{
				ID:              inst.ID,
				Name:            inst.Name,
				PrivateIP:       inst.PrivateIP,
				Type:            inst.Type,
				Platform:        inst.Platform,
				PlatformDetails: inst.PlatformDetails,
			}
			m.buildConnTypeList()
			m.screen = screenConnType
			return nil
		}
	}
	return nil
}

// ackAccountID is the account half of an RDP acknowledgment key: the resolved account when
// one is known, "" in profile flows — consistent either way, which is all a key needs.
func (m *Model) ackAccountID() string {
	if m.awsSess != nil && m.awsSess.AccountID != "" {
		return m.awsSess.AccountID
	}
	if m.selAccount != nil {
		return m.selAccount.ID
	}
	return ""
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
		// Proceeding past the Linux warning IS the "don't show this again" answer: the
		// user just declared this box runs xrdp, and asking a second time on a box they
		// use daily would be warren forgetting on purpose. Keyed by account + Name tag,
		// so the acknowledgment survives repaves alongside the favorite that uses it.
		if m.selInstance.Platform == "linux" {
			_ = browser.AckRDPLinux(m.ackAccountID(), m.selInstance.Name)
		}
		m.connType = tunnel.KindRDP
		return m.startRDP()
	case "quit":
		return tea.Quit
	}
	return nil
}

type msgShellDone struct{}

// msgShellWindowed reports that the session opened in its own window and warren kept the terminal.
type msgShellWindowed struct {
	where string
	name  string
}

func (m *Model) startShell() tea.Cmd {
	m.lastInstance = m.selInstance.Name
	name := m.selInstance.Name

	// StartSession is called exactly once, here, and the resulting command is used by whichever
	// path follows. Building it separately per path would open a second SSM session and orphan
	// the first — the session is created by the API call, not by running the plugin.
	cmd, err := tunnel.ShellCmd(m.ctx, m.selInstance.ID, m.awsSess)
	if err != nil {
		m.err = err
		return nil
	}

	// A window first, where the platform can make one. An SSM shell is interactive, so running it
	// in place means handing over the whole terminal and freezing warren for the duration — which
	// is why only one could ever be open at a time. In its own window, several can be.
	//
	// The windowed session is not wrapped in tmux the way an in-place one is. The wrapper exists
	// to stop the banner scrolling away, and a window carries its own persistent title instead,
	// which does the same job without a dependency. warren also does not track the window: doing
	// so would mean holding the pty to learn when it exits, which is the multiplexer that
	// `warren ssm-shell` exists precisely to avoid writing.
	auth := ""
	if m.awsSess != nil {
		auth = m.awsSess.Label
	}
	title := "warren · " + name
	if auth != "" {
		title += " · " + auth
	}
	if ok, lerr := termwin.Launch(termwin.OSEnv(), cmd, title); ok {
		where := termwin.Choose(termwin.OSEnv()).Name
		return func() tea.Msg { return msgShellWindowed{where: where, name: name} }
	} else if lerr != nil {
		// A window was possible but could not be opened. Report it rather than silently running
		// in place, because "it opened somewhere I cannot see" is the confusing outcome — then
		// connect in place anyway, since the session is already open.
		m.err = lerr
	}

	return tea.ExecProcess(m.wrapWithHeader(cmd, name), func(err error) tea.Msg {
		return msgShellDone{}
	})
}

// TmuxVar controls whether SSM sessions run inside tmux, which is what keeps the banner pinned
// instead of scrolling away when the remote shell clears the screen. Set it to "0" to disable.
//
// Enabled by default where tmux is available, having briefly not been. The reason it was disabled
// was not tmux but how it was invoked: the session was created detached and `tmux attach-session`
// was run as the foreground command, so the terminal became a client whose lifetime was tied to a
// session it did not own. Exiting the remote shell destroyed the session and took the client with
// it, which on an SSH login reads as the terminal being killed. Running new-session in the
// foreground on a private socket fixes that properly, so the banner does not have to be given up
// to keep exits well behaved.
const TmuxVar = "WARREN_TMUX"

// wrapWithHeader runs the session inside tmux so the banner stays pinned as a status bar, falling
// back to printing a header line when tmux is unavailable, disabled, or cannot be set up. On
// Windows it returns cmd unchanged.
func (m *Model) wrapWithHeader(cmd *exec.Cmd, instanceName string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return cmd
	}

	authLabel := ""
	if m.awsSess != nil {
		authLabel = m.awsSess.Label
	}

	// Never nest. A foreground new-session has to attach a client, and tmux refuses while $TMUX
	// is set ("sessions should be nested with care, unset $TMUX to force"). Inside tmux the user
	// already has a status line of their own, so there is nothing to add.
	if os.Getenv(TmuxVar) != "0" && os.Getenv("TMUX") == "" {
		if tmuxBin, err := exec.LookPath("tmux"); err == nil {
			if wrapped := m.wrapWithTmux(tmuxBin, cmd, instanceName, authLabel); wrapped != nil {
				return wrapped
			}
		}
	}

	// Print a header line and accept it may scroll.
	const script = `
printf '\033]0;warren · %s · %s\007' "$_WARREN_INSTANCE" "$_WARREN_AUTH"
printf '\033[1;48;5;63;38;5;230m ▶ warren  \033[38;5;99m│  \033[38;5;189m%s  \033[38;5;99m│  \033[38;5;189m%s\033[0m\n' "$_WARREN_AUTH" "$_WARREN_INSTANCE"
"$_WARREN_BIN" "$@"
`
	args := append([]string{"/bin/sh", "-c", script, "warren"}, cmd.Args[1:]...)
	wrapped := exec.Command(args[0], args[1:]...)
	wrapped.Env = append(cmd.Env,
		"_WARREN_BIN="+cmd.Path,
		"_WARREN_AUTH="+authLabel,
		"_WARREN_INSTANCE="+instanceName,
	)
	return wrapped
}

// tmuxSocketName is the private tmux socket warren runs its sessions on.
//
// A socket of our own (-L) rather than the user's default server, for three reasons: warren's
// session never appears in their `tmux ls`, killing it can never touch their work, and -f is
// honoured — tmux only reads a config file when it actually starts a server, so on a shared server
// the status bar settings would be silently ignored.
func tmuxSocketName() string { return fmt.Sprintf("warren-%d", os.Getpid()) }

// tmuxNewSessionArgs builds the argv for a foreground `tmux new-session`, passing the environment
// through -e and the command as argv after "--".
//
// Foreground, and no attach-session. The previous version created the session detached and then
// ran `tmux attach-session` as the foreground command, which made the terminal a tmux client whose
// lifetime was tied to a session it did not own: exiting the remote shell destroyed the session and
// took the client with it, which on an SSH login reads as the terminal being killed rather than as
// a return to the picker. Running new-session in the foreground means the command warren waits on
// *is* the session, so it ends when the plugin ends and control comes back here.
//
// Also deliberately not a shell command string. tmux hands a single trailing argument to
// /bin/sh -c, and an older version built one by concatenating "export "+e+"; " with arguments
// wrapped in single quotes on the assumption none contained a quote. That put AWS-derived values
// on a shell boundary: a profile name containing a space was enough to break it, and Session.Label
// is always "name (account-id)/role" — a space and parentheses. Passing argv directly means
// nothing here is re-parsed by a shell, whatever the values contain.
//
// height is passed already adjusted for the status bar.
func tmuxNewSessionArgs(socket, confPath string, width, height int, env, argv []string) []string {
	args := []string{
		"-L", socket,
		"-f", confPath,
		"new-session",
		"-x", fmt.Sprintf("%d", width),
		"-y", fmt.Sprintf("%d", height),
	}
	for _, e := range env {
		// Only the SSM-specific vars we set; the rest are inherited. WARREN_ is included so a
		// credentialed shell can still name its own session in a prompt.
		if strings.HasPrefix(e, "AWS_") || strings.HasPrefix(e, "WARREN_") {
			args = append(args, "-e", e)
		}
	}
	return append(append(args, "--"), argv...)
}

// tmuxConf is the config that paints the status bar. It is a file rather than a series of
// set-option calls because those need a session to target, which is what forced the old
// create-detached-then-attach sequence in the first place.
//
// "#" is doubled: tmux reads it as the start of a format specifier like #S or #{session_name}, so
// an account or instance name containing one would render as something else entirely, or eat the
// text after it.
func tmuxConf(authLabel, instanceName string) string {
	esc := func(s string) string { return strings.ReplaceAll(s, "#", "##") }
	return fmt.Sprintf(`set -g status on
set -g status-position bottom
set -g status-style "bg=colour63,fg=colour230,bold"
set -g status-left " ▶ warren  │  %s  │  %s "
set -g status-right ""
set -g status-left-length 200
set -g destroy-unattached off
`, esc(authLabel), esc(instanceName))
}

// wrapWithTmux runs cmd inside a foreground tmux session whose status bar carries the warren
// banner, so it stays pinned instead of scrolling away when the remote shell clears the screen.
//
// Returns nil if the session cannot be prepared, leaving the caller to fall back to the plain
// header. Nothing is run here — the returned command is what tea.ExecProcess executes — so unlike
// the old version there is no half-created session to leave behind when something goes wrong.
func (m *Model) wrapWithTmux(tmuxBin string, cmd *exec.Cmd, instanceName, authLabel string) *exec.Cmd {
	confPath, err := writeTmuxConf(tmuxConf(authLabel, instanceName))
	if err != nil {
		return nil
	}

	// -1 on the height leaves the row the status bar occupies.
	args := tmuxNewSessionArgs(tmuxSocketName(), confPath, m.width, m.height-1, cmd.Env, cmd.Args)
	wrapped := exec.Command(tmuxBin, args...)
	wrapped.Env = cmd.Env
	return wrapped
}

// writeTmuxConf writes the status-bar config and returns its path.
//
// It goes in the user's cache directory under a fixed name, not a temp file: the command is run by
// tea.ExecProcess after this returns, so there is no point at which we could safely delete a temp
// file, and a per-session one would accumulate. A fixed path is simply overwritten each time. 0600
// because the banner text contains the account name and ID.
func writeTmuxConf(conf string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "warren")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Model) startSSH(user string) tea.Cmd {
	m.loading = true
	m.lastInstance = m.selInstance.Name
	// Remembered so a re-auth triggered by an expired session mid-connect can reconnect the
	// SSH tunnel as the same user without asking again.
	m.resumeSSHUser = user
	port := tunnel.FreePort(2222)
	instID := m.selInstance.ID
	instName := m.selInstance.Name
	authLabel := m.awsSess.Label
	startURL, accountID, accountName, role := m.tunnelIdentity()
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
		t.StartURL, t.AccountID, t.AccountName, t.RoleName = startURL, accountID, accountName, role
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
	startURL, accountID, accountName, role := m.tunnelIdentity()
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
		t.StartURL, t.AccountID, t.AccountName, t.RoleName = startURL, accountID, accountName, role
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
		// new connection — go to instance picker. Needs credentials; without them (the
		// manager was opened from the method screen to reach a persisted tunnel) send the
		// user to pick an identity first.
		if m.awsSess == nil {
			m.notice = "pick an authentication method first"
			m.buildMethodList()
			m.screen = screenMethod
			return m, nil
		}
		m.loading = true
		return m, m.fetchInstances()
	case "p":
		// switch auth
		m.screen = screenMethod
		m.buildMethodList()
		return m, nil
	case "q":
		return m, tea.Quit
	case "esc":
		// esc goes back, as it does on every other screen — it must not quit.
		//
		// This screen is where an interactive session returns to, and a terminal being handed
		// back from a raw-mode child emits escape sequences as it is restored. Read as a
		// keypress, a single one of those used to end the program, which looks exactly like
		// "exiting the remote shell killed the tool". q and ctrl+c still quit, and the README
		// always described esc as going back.
		return m, m.goBack()
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
	case "favconn":
		if c := m.connFavCandidate; c != nil {
			if err := browser.AddFavorite(*c); err != nil {
				m.err = err
				return nil
			}
			m.notice = "favorited as " + c.Nickname + " — pinned to the picker; Enter there replays this whole connection"
			m.buildMainList() // the row disappears once saved
		}
		return nil
	case "new":
		if m.awsSess == nil {
			m.notice = "pick an authentication method first"
			m.buildMethodList()
			m.screen = screenMethod
			return nil
		}
		m.loading = true
		return m.fetchInstances()
	case "quit":
		return tea.Quit
	default:
		// A tunnel row. Enter used to kill it outright, which is a surprising amount of
		// destruction for the default action — and no help at all when an RDP client window
		// was closed but the tunnel behind it is fine. Open a small menu instead: reconnect
		// (re-open the client), favorite, or an explicit disconnect.
		for _, t := range m.manager.Active() {
			if fmt.Sprintf("%d", t.PID) == selected.value {
				m.sessionActionTunnel = t
				m.buildSessionActionsList()
				m.screen = screenSessionActions
				return nil
			}
		}
	}
	return nil
}

// buildSessionActionsList is the menu shown when Enter opens an active tunnel on the manager
// screen. Reconnect leads — it is the reason the menu exists — with favorite under it and a
// deliberate, spelled-out Disconnect last.
func (m *Model) buildSessionActionsList() {
	t := m.sessionActionTunnel
	var items []list.Item

	// Reconnect only means something for RDP: warren launched a client there and can launch
	// it again. An SSH or Shell tunnel is a bare port forward the user drives themselves —
	// there is nothing for warren to re-open.
	if t.Kind == tunnel.KindRDP {
		desc := fmt.Sprintf("re-open your RDP client on localhost:%d", t.LocalPort)
		if t.Restored {
			// Set expectations: this one did not come up under this run of warren, so
			// Reconnect is about to do more than open a window.
			desc = "re-authenticate if needed and rebuild the tunnel, then open your RDP client"
		}
		items = append(items, item{title: "Reconnect", desc: desc, value: "reconnect"})
	}

	// Favorite this connection, when the current identity lines up with the tunnel so a
	// favorite could actually replay it. A local candidate, not m.connFavCandidate: opening
	// this menu must not disturb the star offered on the manager screen itself.
	if c := m.connFavCandidateFor(t.InstanceName, strings.ToLower(string(t.Kind)), t.SSHUser); c != nil {
		if _, saved := browser.FindFavorite(*c); !saved {
			items = append(items, item{
				title: "☆ Favorite this connection",
				desc:  c.InstanceName + " (" + c.ConnType + ") — one Enter from launch next time, as " + c.Nickname,
				value: "favconn",
			})
		}
	}

	items = append(items, item{
		title: "Disconnect",
		desc:  "end this tunnel and remove it from the list",
		value: "disconnect",
	})

	m.list.Title = t.Label() + "  •  Esc=back"
	m.setListItems(items)
	m.list.Select(0)
}

// selectSessionAction handles a pick on the active-tunnel menu.
func (m *Model) selectSessionAction(val string) tea.Cmd {
	t := m.sessionActionTunnel
	if t == nil {
		m.screen = screenMain
		m.buildMainList()
		return nil
	}
	var cmd tea.Cmd
	switch val {
	case "reconnect":
		cmd = m.reconnectTunnel(t)
	case "favconn":
		if c := m.connFavCandidateFor(t.InstanceName, strings.ToLower(string(t.Kind)), t.SSHUser); c != nil {
			if err := browser.AddFavorite(*c); err != nil {
				m.err = err
				return nil
			}
			m.notice = "favorited as " + c.Nickname + " — pinned to the picker; Enter there replays this whole connection"
		}
	case "disconnect":
		t.Kill()
		m.manager.Remove(t)
		m.notice = t.InstanceName + " tunnel disconnected"
	}
	m.sessionActionTunnel = nil
	m.buildMainList()
	m.screen = screenMain
	return cmd
}

// reconnectTunnel re-opens the RDP client for t, rebuilding the tunnel first when it cannot
// be trusted at face value.
//
// A tunnel warren started this run (t.Restored == false) is trusted: warren watched
// StartPortForward and WaitPort succeed for it, so this is the fast path — the same call
// msgTunnelReady makes when the tunnel first comes up.
//
// A Restored tunnel survived to this launch from a previous one. Its plugin process being
// alive proves nothing about the SSM channel behind it, which commonly dies out from under
// an otherwise-running plugin — an expired SSO session, SSM's own idle timeout — and handing
// that to an RDP client is what used to spin until the client's own timeout. So it is rebuilt
// instead: reassume the identity stamped on it (session → account → role, via the ordinary
// sign-in flow — StartReauth prints a device code if the token needs it), then a fresh
// StartPortForward, then the client. Everything after "reassume" is the existing resumeConnect
// path: the same code a re-auth mid-connection already uses.
func (m *Model) reconnectTunnel(t *tunnel.Tunnel) tea.Cmd {
	if !t.Restored {
		m.notice = tunnel.OpenRDPClient(t.LocalPort, t.SSHUser, t.InstanceName, browser.LoadRDPScreen() == browser.RDPFullscreen)
		return nil
	}
	if t.StartURL == "" || t.AccountID == "" || t.RoleName == "" {
		// Written by an older warren before these fields existed, or a profile-flow
		// connection, which never had a session to replay in the first place.
		m.notice = t.InstanceName + " predates this warren session and can't be re-authenticated automatically — Disconnect it and start a new connection"
		return nil
	}
	var sess *awsint.SSOSessionConfig
	for i := range m.ssoSessions {
		if m.ssoSessions[i].StartURL == t.StartURL {
			sess = &m.ssoSessions[i]
			break
		}
	}
	if sess == nil {
		m.err = fmt.Errorf("%s's SSO session is no longer in ~/.aws/config — Disconnect it and start a new connection", t.InstanceName)
		return nil
	}
	// Superseded by whatever this produces, one way or another — kept around it would be a
	// second, permanently dead row once the rebuild lands. Kill it first: Remove alone only
	// drops it from the manager's list, leaving the old session-manager-plugin process (and
	// its bound port) running and untracked forever.
	_ = t.Kill()
	m.manager.Remove(t)
	m.selSession = sess
	m.selAccount = &awsint.Account{ID: t.AccountID, Name: t.AccountName}
	// startReauth reassumes m.selRole (falling back to the CURRENT session's role otherwise),
	// so this has to be set explicitly: the tunnel being reconnected may belong to a
	// different account/role than whatever this Model last authenticated as.
	m.selRole = t.RoleName
	m.selInstance = &awsint.Instance{ID: t.InstanceID, Name: t.InstanceName}
	m.connType = tunnel.KindRDP
	m.resumeSSHUser = t.SSHUser
	return m.startReauth(resumeConnect)
}

// ---- async commands --------------------------------------------------------

// fetchToken resolves an SSO token in two visible stages instead of one opaque LiveToken
// call. Silent renewal stays a plain spinner; only when a real sign-in is unavoidable does
// StartLogin run, and the pending authorization comes back as a message so the URL and user
// code are rendered *on* the alt screen — LiveToken printed them to stderr behind it, where
// they were unreadable and the flow survived only because the auto-opened browser carried
// the code in its URL.
func (m *Model) fetchToken() tea.Cmd {
	sess := *m.selSession
	ctx := m.ctx
	return func() tea.Msg {
		token, err := awsint.SilentToken(ctx, sess)
		if err == nil {
			return msgToken{token: token}
		}
		if !errors.Is(err, awsint.ErrLoginRequired) {
			return msgToken{err: err}
		}
		// The inline picker runs before the device authorization, not after: codes live
		// about ten minutes, and a picker left up over lunch must not resume into a code
		// that is already dead.
		if pref, _ := browser.ResolvePrefFor(sess.StartURL); browser.ShouldAsk(pref) {
			return msgLoginAsk{}
		}
		pending, err := awsint.StartLogin(ctx, sess)
		if err != nil {
			return msgToken{err: err}
		}
		return msgLoginPending{pending: pending}
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
	// Active tunnels above everything when there are any: after "p" (switch auth) or a
	// startup that inherited tunnels from a previous run, this is the only way back to the
	// manager without connecting to something new.
	if n := len(m.manager.Active()); n > 0 {
		items = append(items, item{
			title: fmt.Sprintf("Active tunnels (%d)", n),
			desc:  "open the tunnel manager — reconnect, favorite, or disconnect a live session",
			value: methodTunnels,
		})
	}
	// Favorites first: they exist to be the first thing Enter lands on. Refreshed from disk
	// on every build so a star toggled on the action screen shows up on the way back. Past
	// favInlineMax they collapse into a single row — a dozen bookmarks must not bury the
	// sessions and profiles this screen is named for.
	m.favorites = browser.Favorites()
	if len(m.favorites) > favInlineMax {
		items = append(items, item{
			title: fmt.Sprintf("★ Favorites (%d)", len(m.favorites)),
			desc:  "your bookmarked destinations — Enter, Enter connects the first",
			value: methodFavList,
		})
	} else {
		for i, f := range m.favorites {
			items = append(items, item{
				title: "★ " + favTitle(f),
				desc:  favDesc(f) + " — " + f.Nickname + ", via " + m.sessionLabelFor(f.StartURL) + "  •  x removes",
				value: fmt.Sprintf("fav:%d", i),
				// The nickname is invisible on the row but exactly what fingers type into "/".
				search: f.Nickname,
			})
		}
	}
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
	// The sign-in browser setting sits with the sign-in methods because that is where you
	// are when it is wrong: sign-in just opened in the wrong place, and re-selecting the
	// session is the retry.
	items = append(items, browserPrefRow(), rdpScreenRow())
	// Removal earns a row only when there is something to remove; an always-present
	// destructive entry on the home screen is dead weight ninety-nine days in a hundred.
	if len(m.profiles) > 0 {
		items = append(items, item{
			title: "✕ Remove an AWS profile",
			desc:  "delete a [profile] block from ~/.aws/config — preview first, backup taken",
			value: methodRemoveProfil,
		})
	}

	m.list.Title = "Select authentication method"
	m.list.SetStatusBarItemName("method", "methods")
	m.setListItems(items)
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
	m.setListItems(items)
}

func (m *Model) buildRoleList() {
	var items []list.Item
	for _, r := range m.roles {
		items = append(items, item{title: r, value: r})
	}
	m.list.Title = fmt.Sprintf("Select role for %s  •  Esc=back", m.selAccount.Name)
	m.list.SetStatusBarItemName("role", "roles")
	m.setListItems(items)
}

func (m *Model) buildInstanceList() {
	var items []list.Item
	for _, i := range m.instances {
		items = append(items, item{
			title: i.Name,
			// Platform leads the row rather than trailing it. It is the field that decides
			// which connection type is worth trying, so it wants to be scannable down the
			// column; the id and IP are things you copy once you have already chosen a row.
			desc:  fmt.Sprintf("%s %s  %s  %s", i.PlatformBadge(), i.ID, i.PrivateIP, i.Type),
			value: i.ID,
			// Tags are searchable but not shown — see item.search. "key=value" means a search
			// for "prod" hits any tag whose value contains it, and "env=prod" narrows to the
			// one tag, without needing a query syntax of our own.
			//
			// PlatformDetails joins them for the same reason: "/windows" then narrows to the
			// hosts worth an RDP tunnel, and the longer strings ("Red Hat Enterprise Linux")
			// are searchable without costing a row's width to display.
			search: strings.Join(append(i.TagPairs(), i.PlatformDetails), " "),
		})
	}
	m.list.Title = "Select instance  •  /=search name, ID, IP, platform, or any tag  •  Esc=back"
	m.list.SetStatusBarItemName("instance", "instances")
	m.setListItems(items)
}

func (m *Model) buildConnTypeList() {
	// The mismatch note goes on the row itself, where the decision is being made: the
	// platform in the title proved too subtle to stop an RDP pick on a Linux box. Warned,
	// not blocked — xrdp exists — and only when the platform is positively known, because
	// crying wolf on unknowns teaches people to ignore the warning that matters.
	rdpDesc := "forward port 3389 → localhost:13389+"
	if m.selInstance.Platform == "linux" && !browser.RDPLinuxAcked(m.ackAccountID(), m.selInstance.Name) {
		rdpDesc += "  ⚠ this reports as a Linux box — RDP will fail unless it runs xrdp"
	}
	items := []list.Item{
		item{title: "Shell session", desc: "interactive SSM shell (foreground)", value: "shell"},
		item{title: "SSH tunnel", desc: "forward port 22, connect with your SSH client", value: "ssh"},
		item{title: "RDP tunnel", desc: rdpDesc, value: "rdp"},
		item{title: "Quit", desc: "", value: "quit"},
	}
	// The platform is named here, on the one screen where it changes the decision, but nothing
	// on this list is removed because of it: Windows Server ships an optional OpenSSH server and
	// xrdp exists for Linux, so hiding a connection type would be warren overruling a setup it
	// cannot see. Naming the platform informs the choice; filtering would make it for you.
	title := fmt.Sprintf("Connect to %s", m.selInstance.Name)
	if p := m.selInstance.Platform; p != "" {
		title += " (" + p + ")"
	}
	m.list.Title = title + "  •  Esc=back"
	m.setListItems(items)
	m.list.Select(0)
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
	m.setListItems(items)
}

// connFavCandidateFor builds the favorite that would replay this connection, or nil when the
// current identity cannot describe it — only the sso-session flow can, because a favorite
// replays session → account → role → instance and a profile flow has no such path.
func (m *Model) connFavCandidateFor(instanceName, connType, sshUser string) *browser.Favorite {
	if m.selSession == nil || m.selAccount == nil || m.awsSess == nil || m.awsSess.RoleName == "" || instanceName == "" {
		return nil
	}
	return &browser.Favorite{
		Nickname: profileSlug(m.selAccount.Name, m.awsSess.RoleName) + "-" +
			profileSlug(instanceName, connType),
		StartURL:     m.selSession.StartURL,
		AccountID:    m.selAccount.ID,
		AccountName:  m.selAccount.Name,
		Role:         m.awsSess.RoleName,
		InstanceName: instanceName,
		ConnType:     connType,
		SSHUser:      sshUser,
	}
}

// noteConnFavCandidate remembers the connection that just started as something the tunnel
// manager can offer to star.
func (m *Model) noteConnFavCandidate(instanceName, connType, sshUser string) {
	m.connFavCandidate = m.connFavCandidateFor(instanceName, connType, sshUser)
}

// tunnelIdentity is the SSO session, account, and role behind the current credentials —
// stamped onto every tunnel warren starts so a Reconnect that outlives this run of warren
// can reassume the same identity instead of guessing. Same guard as connFavCandidateFor:
// empty for a profile-flow connection, which has no session to replay.
func (m *Model) tunnelIdentity() (startURL, accountID, accountName, role string) {
	if m.selSession == nil || m.selAccount == nil || m.awsSess == nil || m.awsSess.RoleName == "" {
		return "", "", "", ""
	}
	return m.selSession.StartURL, m.selAccount.ID, m.selAccount.Name, m.awsSess.RoleName
}

func (m *Model) buildMainList() {
	var items []list.Item
	for _, t := range m.manager.Active() {
		items = append(items, item{
			title: t.Label(),
			desc:  t.Hint(),
			value: fmt.Sprintf("%d", t.PID),
		})
	}
	// Star the connection that just started — offered here because this screen is where a
	// working connection lands, which is the moment the whole path is proven worth saving.
	if c := m.connFavCandidate; c != nil {
		if _, saved := browser.FindFavorite(*c); !saved {
			items = append(items, item{
				title: "☆ Favorite this connection",
				desc:  c.InstanceName + " (" + c.ConnType + ") — one Enter from launch next time, as " + c.Nickname,
				value: "favconn",
			})
		}
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
	m.setListItems(items)
}

// ---- styles for banner -----------------------------------------------------

var (
	styleBanner     = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("230")).Bold(true).PaddingLeft(1).PaddingRight(1)
	styleBannerDim  = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("189")).PaddingLeft(1).PaddingRight(1)
	styleBannerSep  = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("99")).PaddingLeft(1).PaddingRight(1)
	styleBannerFill = lipgloss.NewStyle().Background(lipgloss.Color("63"))
	styleBannerVer  = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("104")).PaddingRight(1)
	// The hint carries the same weight and brightness as the tool name. Dim was the obvious
	// choice and the wrong one: it read as decoration and went unnoticed, which defeats the
	// entire point of advertising a key nobody would otherwise guess.
	styleBannerHint = lipgloss.NewStyle().Background(lipgloss.Color("63")).Foreground(lipgloss.Color("230")).Bold(true).PaddingLeft(1).PaddingRight(1)
)

// ---- view ------------------------------------------------------------------

func (m *Model) View() string {
	// Size the list for whatever this screen looks like: the wordmark is on some screens and not
	// others, so the rows available to the list change as you move. Done here rather than at each
	// transition because there are a dozen of those and every one of them ends in a draw.
	if m.width > 0 {
		m.resizeList()
	}

	if m.screen == screenSetup {
		return m.banner() + m.splash() + m.setup.view(m.width)
	}
	if m.screen == screenBuildParams {
		return m.banner() + m.builder.view(m.width)
	}
	if m.screen == screenS3Upload {
		return m.s3UploadView()
	}
	if m.screen == screenProfileConfirm {
		return m.profileConfirmView()
	}
	if m.screen == screenAbout {
		m.resizeAbout()
		scrollable := m.aboutVP.TotalLineCount() > m.aboutVP.Height
		return m.banner() + m.aboutVP.View() + "\n" +
			aboutFooter(scrollable, m.aboutVP.AtBottom())
	}
	// Before the generic spinner: a pending sign-in *is* a loading state, but one whose
	// whole point is the URL and code being readable while it waits.
	if m.pendingLogin != nil {
		return m.loginView()
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
	return m.banner() + m.splash() + m.noticeLine() + m.list.View() + "\n" + m.footer()
}

// noticeLine renders the transient confirmation, or nothing when there is none.
func (m *Model) noticeLine() string {
	if m.notice == "" {
		return ""
	}
	return styleNotice.MarginLeft(2).Render("✓ "+m.notice) + "\n\n"
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

	// Fill the rest of the row with banner background, parking the "? help" hint at the right
	// edge when it fits.
	//
	// The hint lives here because the banner is the one row present on every screen and it already
	// ends in dead space — so discoverability costs nothing, where a footer line would cost a row
	// off the list on every screen. Without it "?" is a key nobody presses, which makes a screen
	// whose whole job is telling you the keys unreachable in practice.
	if visibleLen := lipgloss.Width(row); m.width > visibleLen {
		fill := m.width - visibleLen
		rendered := ""
		if hint := m.bannerHint(); hint != "" {
			rendered = styleBannerHint.Render(hint)
		}
		// Measure the *rendered* hint, not the string: the style pads a column either side,
		// so measuring "? help" undercounts by two and the row overflows into a second line —
		// the same failure the version guard above exists to avoid. Below that width the identity
		// on the left matters more, so the hint is dropped rather than crowding it.
		if hw := lipgloss.Width(rendered); rendered != "" && fill >= hw {
			row += styleBannerFill.Render(strings.Repeat(" ", fill-hw)) + rendered
		} else {
			row += styleBannerFill.Render(strings.Repeat(" ", fill))
		}
	}

	return row + "\n"
}

// bannerHint is the right-hand nudge in the banner: how you find out that "?" exists.
func (m *Model) bannerHint() string {
	switch {
	// Pointless on the help screen itself — "?" closes it, and the screen already says so.
	case m.screen == screenAbout:
		return ""
	// Text-entry screens have their own instructions and a "?" there is a literal character,
	// so advertising it as a shortcut would be a lie.
	case m.screen == screenSetup, m.screen == screenBuildParams:
		return ""
	default:
		return "? help"
	}
}

func (m *Model) bannerRow(withVersion bool) string {
	title := styleBanner.Render("▶ warren")
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
