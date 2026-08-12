package tui

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/treyperrone/warren/internal/awscli"
	"github.com/treyperrone/warren/internal/buildinfo"
	"github.com/treyperrone/warren/internal/plugin"
)

func aboutModel(t *testing.T) *Model {
	t.Helper()
	m := newFormModel(t)
	m.screen = screenMethod
	m.buildMethodList()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

func press(m *Model, key string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

// "?" has to work wherever you are: being several screens deep with no idea which key goes back is
// exactly when the reference is needed, and a main-menu-only entry is unreachable from there.
func TestAboutOpensFromEveryListScreen(t *testing.T) {
	for _, sc := range []screen{screenMethod, screenAccount, screenRole, screenInstance,
		screenConnType, screenMain, screenAction, screenBuildService, screenBuildTask} {
		m := aboutModel(t)
		m.screen = sc
		press(m, AboutKey)
		if m.screen != screenAbout {
			t.Errorf("screen %d: %q did not open the about screen", sc, AboutKey)
		}
	}
}

// esc must return to wherever "?" was pressed. Reachable from everywhere means there is no single
// screen it could sensibly go back to.
func TestAboutReturnsToTheScreenItWasOpenedFrom(t *testing.T) {
	for _, sc := range []screen{screenAccount, screenInstance, screenMain} {
		m := aboutModel(t)
		m.screen = sc
		press(m, AboutKey)
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.screen != sc {
			t.Errorf("opened from %d, esc landed on %d", sc, m.screen)
		}
	}
}

// It takes no action, so every key that means "done" should leave rather than only one.
func TestAboutIsLeftByAnyDismissKey(t *testing.T) {
	for _, key := range []string{"q", AboutKey} {
		m := aboutModel(t)
		m.screen = screenInstance
		press(m, AboutKey)
		press(m, key)
		if m.screen != screenInstance {
			t.Errorf("%q did not leave the about screen (on %d)", key, m.screen)
		}
	}
}

// "q" quits from the main screen, so it must not do that while the about screen is up — pressing a
// key to dismiss a reference screen should never end the program.
func TestAboutDoesNotQuitWhenOpenedFromTheMainScreen(t *testing.T) {
	m := aboutModel(t)
	m.screen = screenMain
	press(m, AboutKey)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Error("q on the about screen quit warren instead of going back")
		}
	}
	if m.screen != screenMain {
		t.Errorf("landed on %d rather than returning to the main screen", m.screen)
	}
}

// A "?" typed into the search box is a search term, not a shortcut.
func TestAboutDoesNotOpenWhileTyping(t *testing.T) {
	m := aboutModel(t)
	m.screen = screenInstance
	m.list.SetFilteringEnabled(true)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	press(m, AboutKey)
	if m.screen == screenAbout {
		t.Error("opened the about screen from a keystroke meant for the search box")
	}
}

// The whole point is answering "what am I running?" without a rebuild, so the facts have to be
// real rather than placeholders.
func TestAboutReportsTheRunningVersions(t *testing.T) {
	m := aboutModel(t)
	m.screen = screenAbout
	got := m.View()

	for _, want := range []string{
		plugin.Version(),
		runtime.GOOS + "/" + runtime.GOARCH,
		runtime.Version(),
		issuesURL,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("about screen does not mention %q", want)
		}
	}
}

// Every binding listed must exist, or the reference sends people to keys that do nothing. "?" and
// the modifier keys are checked by the tests above rather than here.
func TestAboutListsTheKeysThatActuallyExist(t *testing.T) {
	listed := map[string]bool{}
	for _, kv := range keyHelp {
		listed[kv[0]] = true
	}
	for _, want := range []string{"/", "enter", "esc", "n", "p", "q", "ctrl+c", "?"} {
		if !listed[want] {
			t.Errorf("keyHelp does not document %q", want)
		}
	}
	for _, kv := range keyHelp {
		if strings.TrimSpace(kv[1]) == "" {
			t.Errorf("key %q is listed with no description", kv[0])
		}
	}
}

// The banner is the only row on every screen, so it is where "?" gets advertised. Without this the
// help screen is unreachable in practice: nobody presses a key they were never told about.
func TestBannerAdvertisesTheHelpKey(t *testing.T) {
	for _, sc := range []screen{screenMethod, screenAccount, screenInstance, screenMain} {
		m := aboutModel(t)
		m.screen = sc
		if got := m.banner(); !strings.Contains(got, "? help") {
			t.Errorf("screen %d: banner does not mention the help key", sc)
		}
	}
}

// Suppressed where it would mislead: "?" closes the help screen, and on the text-entry screens it
// is a literal character rather than a shortcut.
func TestBannerHintIsSuppressedWhereItWouldMislead(t *testing.T) {
	for _, sc := range []screen{screenAbout, screenSetup, screenBuildParams} {
		m := aboutModel(t)
		m.screen = sc
		if got := m.banner(); strings.Contains(got, "? help") {
			t.Errorf("screen %d: banner advertises a key that does not apply there", sc)
		}
	}
}

// The banner must occupy exactly one row of exactly the terminal width. Appending the hint without
// measuring its rendered width (the style pads a column either side) overflows into a second line,
// which shifts the whole view down and leaves a stale row behind.
func TestBannerFitsTheTerminalWidthExactly(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 120, 200} {
		for _, sc := range []screen{screenMethod, screenMain, screenAbout} {
			m := aboutModel(t)
			m.screen = sc
			m.Update(tea.WindowSizeMsg{Width: width, Height: 40})

			got := strings.TrimSuffix(m.banner(), "\n")
			if strings.Contains(got, "\n") {
				t.Errorf("width %d screen %d: banner wrapped onto a second line", width, sc)
				continue
			}
			if w := lipgloss.Width(got); w != width {
				t.Errorf("width %d screen %d: banner rendered %d columns", width, sc, w)
			}
		}
	}
}

// The hint has to carry the same visual weight as the tool name. It was dim first and read as
// decoration, which defeats advertising a key nobody would otherwise guess. Asserted on the style
// rather than the rendered output because lipgloss strips colour when stdout is not a terminal, so
// a test on escape sequences would pass vacuously here.
func TestHelpHintIsAsProminentAsTheToolName(t *testing.T) {
	if !styleBannerHint.GetBold() {
		t.Error("help hint is not bold; it reads as decoration next to the bold tool name")
	}
	if got, want := styleBannerHint.GetForeground(), styleBanner.GetForeground(); got != want {
		t.Errorf("help hint foreground is %v, want %v to match the tool name", got, want)
	}
	if got, want := styleBannerHint.GetBackground(), styleBanner.GetBackground(); got != want {
		t.Errorf("help hint background is %v, want %v so it sits on the banner", got, want)
	}
}

// Which pieces ship inside warren and which come from the machine is not guessable, and it changes
// what you would do about a problem: the plugin version does not move when you upgrade the plugin
// on your system, and the banner pins or scrolls depending on whether tmux is installed.
func TestAboutSeparatesBundledFromHostProvided(t *testing.T) {
	m := aboutModel(t)
	m.screen = screenAbout
	got := m.View()

	bundled := strings.Index(got, "bundled")
	host := strings.Index(got, "found on this machine")
	if bundled < 0 || host < 0 {
		t.Fatalf("about screen does not group bundled vs host-provided components:\n%s", got)
	}
	if bundled > host {
		t.Error("host-provided section comes before the bundled one; bundled is the more surprising fact")
	}

	// The plugin must sit under "bundled", the CLI and tmux under "found on this machine".
	plug := strings.Index(got, "session-manager-plugin")
	if !(plug > bundled && plug < host) {
		t.Error("session-manager-plugin is not listed as bundled")
	}
	for _, name := range []string{"aws cli", "tmux"} {
		if i := strings.Index(got, name); i < host {
			t.Errorf("%q is not listed under host-provided components", name)
		}
	}
}

// "Why is my banner not pinned?" should be answerable from this screen rather than by asking.
func TestTmuxStatusExplainsWhyTheBannerBehavesAsItDoes(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tests := []struct {
		name, tmuxVar, tmuxEnv, want string
	}{
		{"in use", "", "", "pinning"},
		{"turned off", "0", "", "disabled by " + TmuxVar},
		{"already inside tmux", "", "/tmp/tmux-1000/default,1,0", "inside an existing tmux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(TmuxVar, tt.tmuxVar)
			t.Setenv("TMUX", tt.tmuxEnv)
			if got := tmuxStatus(); !strings.Contains(got, tt.want) {
				t.Errorf("tmuxStatus() = %q, want it to mention %q", got, tt.want)
			}
		})
	}
}

// On a machine with more than one aws, which one warren runs is only knowable from the path.
func TestAboutShowsWhereTheAwsCliWasFound(t *testing.T) {
	info := awscli.Detect()
	got := awsCLIStatus()
	if info.Found() && !strings.Contains(got, info.Path) {
		t.Errorf("status %q omits the resolved path %q", got, info.Path)
	}
	if !info.Found() && !strings.Contains(got, "only the AWS CLI features") {
		t.Errorf("status %q does not say what still works without it", got)
	}
}

// openAbout presses "?" at a given terminal size, the way a user reaches this screen.
func openAbout(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newFormModel(t)
	m.screen = screenMethod
	m.buildMethodList()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(AboutKey)})
	return m
}

// The content is taller than a standard terminal. Rendering it whole made the terminal scroll, and
// what scrolled off the top was the warren version — the single most useful line on the screen.
func TestAboutNeverRendersTallerThanTheTerminal(t *testing.T) {
	for _, h := range []int{15, 20, 24, 30, 40, 60} {
		m := openAbout(t, 100, h)
		rows := len(strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n"))
		if rows > h {
			t.Errorf("height %d: rendered %d rows, so the top scrolls out of reach", h, rows)
		}
	}
}

// Opening it must start at the top, whatever a previous visit left the scroll position at.
func TestAboutStartsAtTheTopShowingTheVersion(t *testing.T) {
	for _, h := range []int{20, 24, 40} {
		m := openAbout(t, 100, h)
		if !strings.Contains(m.View(), buildinfo.Version()) {
			t.Errorf("height %d: warren version is not visible on opening", h)
		}
	}

	// Scroll down, leave, come back: still at the top.
	m := openAbout(t, 100, 24)
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(AboutKey)})
	if !strings.Contains(m.View(), buildinfo.Version()) {
		t.Error("reopening kept the previous scroll position instead of returning to the top")
	}
}

// How to leave has to stay on screen. As the last line of the scrolled content it was the first
// thing to disappear on a short terminal.
func TestAboutFooterStaysVisibleAtEveryHeight(t *testing.T) {
	for _, h := range []int{15, 24, 40} {
		m := openAbout(t, 100, h)
		if !strings.Contains(m.View(), "esc back") {
			t.Errorf("height %d: no way out shown", h)
		}
	}
}

// A short terminal must say there is more, or the rest of the screen is invisible.
func TestAboutSaysWhenThereIsMoreBelow(t *testing.T) {
	short := openAbout(t, 100, 24).View()
	if !strings.Contains(short, "more below") {
		t.Error("short terminal does not indicate the content continues")
	}
	tall := openAbout(t, 100, 60).View()
	if strings.Contains(tall, "more below") {
		t.Error("tall terminal claims there is more below when everything fits")
	}
}

func TestAboutScrolls(t *testing.T) {
	m := openAbout(t, 100, 24)
	before := m.View()
	for i := 0; i < 8; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if after := m.View(); after == before {
		t.Error("arrow keys did not scroll the about screen")
	}
	// The keys section is below the fold at this height; scrolling must reach it.
	m2 := openAbout(t, 100, 24)
	for i := 0; i < 40; i++ {
		m2.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if !strings.Contains(m2.View(), issuesURL) {
		t.Error("could not scroll far enough to reach the issues link")
	}
}

// The wheel is what people reach for, and the viewport supports it — but only if mouse reporting is
// on, which warren does not enable globally because it would take click-drag text selection away
// from the terminal everywhere. So it is switched on for this screen and off again on the way out.
func TestAboutEnablesTheMouseWheelOnlyWhileItIsOpen(t *testing.T) {
	m := newFormModel(t)
	m.screen = screenMethod
	m.buildMethodList()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(AboutKey)})
	if cmd == nil {
		t.Fatal("opening the about screen issued no command; the wheel will not work")
	}
	// The message types are unexported, so compare against what the exported command produces.
	if got := cmd(); got != tea.EnableMouseCellMotion() {
		t.Errorf("opening did not enable mouse reporting, got %T", got)
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("leaving issued no command; mouse reporting would stay on and block text selection")
	}
	if got := cmd(); got != tea.DisableMouse() {
		t.Errorf("leaving did not disable mouse reporting, got %T", got)
	}
}

func TestAboutScrollsOnTheWheel(t *testing.T) {
	m := openAbout(t, 100, 24)
	before := m.View()
	for i := 0; i < 5; i++ {
		m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	}
	if m.View() == before {
		t.Error("wheel-down did not scroll the about screen")
	}
}

// A wheel event anywhere else must be inert rather than leaking into a list.
func TestWheelIsIgnoredOutsideTheAboutScreen(t *testing.T) {
	m := newFormModel(t)
	m.screen = screenInstance
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	before := m.screen
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.screen != before {
		t.Errorf("a wheel event changed screen from %d to %d", before, m.screen)
	}
}
