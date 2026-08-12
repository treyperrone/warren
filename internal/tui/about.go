package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/treyperrone/warren/internal/awscli"
	"github.com/treyperrone/warren/internal/buildinfo"
	"github.com/treyperrone/warren/internal/plugin"
	"github.com/treyperrone/warren/internal/termwin"
)

const (
	repoURL   = "https://github.com/treyperrone/warren"
	issuesURL = repoURL + "/issues"

	// AboutKey opens this screen. Bound on every screen rather than offered as a menu entry on
	// one of them: the moment you want the issue link is the moment you are stuck, and being
	// stuck three screens deep is exactly when a main-menu-only entry is out of reach.
	AboutKey = "?"
)

// keyHelp is the keybinding reference. On screen as well as in the README, because the README is
// not in front of you when you have forgotten which key goes back.
var keyHelp = [][2]string{
	{"/", "search the current list — fuzzy, matches everything on the row"},
	{"enter", "select"},
	{"esc", "clear an active search, otherwise go back a screen"},
	{"n", "new connection (main screen)"},
	{"p", "switch account or role (main screen)"},
	{"ctrl+e", "edit the built command (command builder)"},
	{"?", "this screen"},
	{"q", "quit — active tunnels keep running"},
	{"ctrl+c", "quit"},
}

var (
	styleAboutHead = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	styleAboutKey  = lipgloss.NewStyle().Foreground(lipgloss.Color("189")).Bold(true)
	styleAboutURL  = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Underline(true)
)

// aboutSection is a group of labelled facts under a heading.
type aboutSection struct {
	head string
	rows [][2]string
}

// aboutFacts is the environment detail a bug report needs, in the order it is worth reading.
//
// Split into "bundled" and "found on this machine" because which is which is not guessable and
// changes what you would do about a problem. The plugin version does not move when you upgrade the
// plugin on your system — it ships inside warren — and the banner pins or scrolls depending on
// whether tmux happens to be installed. Both of those have already caused confusion; a parenthetical
// would be easy to skim past, so the grouping carries it instead.
//
// Shown as plain rows rather than behind a pre-filled new-issue URL: warren is often run over SSH on
// a headless box where nothing can open a browser, and a URL-encoded issue body is several hundred
// characters that wrap into noise. Rows can be read off the screen and typed into a report anywhere.
func aboutFacts() []aboutSection {
	return []aboutSection{
		{"about", [][2]string{
			{"warren", buildinfo.Version()},
			{"platform", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)},
			{"go", runtime.Version()},
		}},
		{"bundled — ships inside warren", [][2]string{
			// Named in full rather than as "plugin": this is the string AWS's own docs, packages
			// and support use, so it is what someone would search for or be asked about.
			{"session-manager-plugin", plugin.Version() + "  (built from source)"},
		}},
		{"found on this machine — not bundled", [][2]string{
			{"aws cli", awsCLIStatus()},
			{"tmux", tmuxStatus()},
			{"new windows", windowStatus()},
		}},
	}
}

// awsCLIStatus reports the CLI and where it was found. The path matters: on a machine with more
// than one `aws` it is the only way to know which one warren will actually run.
func awsCLIStatus() string {
	info := awscli.Detect()
	if !info.Found() {
		return info.Display() + "  (only the AWS CLI features need it)"
	}
	return info.Display() + "  " + info.Path
}

// windowStatus answers "why did my session not open in a new window?", which is not guessable:
// it depends on whether there is a display server to draw one on, and over SSH to a headless host
// there is none — so no process running there can make a window at all. Naming the mechanism that
// will be used, or why none can be, turns a silent difference in behaviour into a stated one.
func windowStatus() string {
	s := termwin.Choose(termwin.OSEnv())
	if !s.Available() {
		return "unavailable  (no display here — sessions open in this terminal)"
	}
	return s.Name + "  (sessions open in a new window)"
}

// tmuxStatus answers "why is the banner not pinned?" without anyone having to ask, since that
// depends on a program warren does not ship and on an environment variable.
func tmuxStatus() string {
	path, err := exec.LookPath("tmux")
	switch {
	case err != nil:
		return "not installed  (the session banner will scroll instead of pinning)"
	case os.Getenv(TmuxVar) == "0":
		return path + "  (disabled by " + TmuxVar + "=0)"
	case os.Getenv("TMUX") != "":
		return path + "  (not used inside an existing tmux session)"
	default:
		return path + "  (pinning the session banner)"
	}
}

// aboutContent renders the version, keybindings, and where to report a problem. The caller is
// responsible for fitting it to the terminal — it can be taller than the screen.
func (m *Model) aboutContent() string {
	var b strings.Builder

	sections := aboutFacts()

	// Two column widths, not one. The fact labels are long ("session-manager-plugin") and the key
	// names are short ("/", "esc"); padding the keys to the same column pushed their descriptions
	// most of the way across the screen for no reason. Each width is derived from its own longest
	// label, so a longer entry cannot silently break the alignment.
	widest := func(rows [][2]string, min int) int {
		w := min
		for _, kv := range rows {
			if len(kv[0]) > w {
				w = len(kv[0])
			}
		}
		return w
	}
	factWidth := 9
	for _, sec := range sections {
		factWidth = widest(sec.rows, factWidth)
	}
	keyWidth := widest(keyHelp, 6)

	row := func(width int) func(string, string) {
		return func(k, v string) {
			b.WriteString("  " + styleAboutKey.Width(width).Render(k) + "  " + v + "\n")
		}
	}
	factRow, keyRow := row(factWidth), row(keyWidth)
	// One blank line before a heading, none after. Five headings each costing two blank rows was
	// most of the reason this screen did not fit a 24-row terminal.
	head := func(s string) {
		b.WriteString("\n" + styleAboutHead.MarginLeft(2).Render(s) + "\n")
	}

	for _, sec := range sections {
		head(sec.head)
		for _, kv := range sec.rows {
			factRow(kv[0], kv[1])
		}
	}

	head("keys")
	for _, kv := range keyHelp {
		keyRow(kv[0], kv[1])
	}

	head("report a problem")
	b.WriteString("  " + styleAboutURL.Render(issuesURL) + "\n")
	b.WriteString(styleDim.MarginLeft(2).Render("include the rows above — they are what the first reply would ask for") + "\n")

	return b.String()
}

// aboutFooter is the always-visible last row: how to leave, and whether there is more to see.
//
// Outside the scrolled body on purpose. It used to be the final line of the content, which meant
// that on a short terminal the one instruction telling you how to get out was the first thing to
// scroll off the top.
func aboutFooter(scrollable bool, atBottom bool) string {
	parts := []string{"esc back"}
	if scrollable {
		hint := "↑↓ / wheel to scroll"
		if !atBottom {
			hint = "↑↓ / wheel to scroll — more below"
		}
		parts = append(parts, hint)
	}
	return styleDim.MarginLeft(2).Render(strings.Join(parts, "   •   "))
}

// resizeAbout fits the scrolled body between the banner and the footer, and refreshes its content.
//
// Content is rebuilt here rather than once on open because it is not static: the credential label
// in the banner and the detected tool statuses can change while the screen is up.
func (m *Model) resizeAbout() {
	h := m.height - 2 // -1 banner, -1 footer
	if h < 3 {
		h = 3
	}
	w := m.width
	if w < 20 {
		w = 80 // no WindowSizeMsg yet
	}
	m.aboutVP.Width = w
	m.aboutVP.Height = h
	m.aboutVP.SetContent(m.aboutContent())
}
