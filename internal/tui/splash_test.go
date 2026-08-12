package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// openingModel is a model sitting on the opening screen with a roomy terminal.
func openingModel(t *testing.T) *Model {
	t.Helper()
	m := newFormModel(t)
	m.screen = screenMethod
	m.buildMethodList()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

func TestSplashShowsOnTheOpeningScreen(t *testing.T) {
	m := openingModel(t)

	view := m.View()
	for _, line := range splashArt {
		if !strings.Contains(view, line) {
			t.Errorf("wordmark row missing from the opening screen:\n%s", view)
			break
		}
	}
	if !strings.Contains(view, splashTagline) {
		t.Error("tagline missing — the wordmark alone does not say what the tool is")
	}
}

// The wordmark belongs on the screens you rest on, not the ones you pass through — and it has to
// come back when you return to one, which is what backing out after a session does.
func TestSplashFollowsTheScreenRatherThanBeingDismissed(t *testing.T) {
	m := openingModel(t)
	if !m.splashVisible() {
		t.Fatal("wordmark not visible on the opening screen")
	}

	// Working lists: every row counts, so it gets out of the way.
	for _, sc := range []screen{screenAccount, screenRole, screenInstance, screenAction} {
		m.screen = sc
		if m.splashVisible() {
			t.Errorf("wordmark shown on screen %v, where rows are scarce", sc)
		}
	}

	// The tunnel manager is where an SSM session returns you, and it is a resting screen.
	m.screen = screenMain
	m.buildMainList()
	if !m.splashVisible() {
		t.Error("wordmark missing from the tunnel manager")
	}

	// ...and backing all the way out brings it back.
	m.screen = screenMethod
	m.buildMethodList()
	if !m.splashVisible() {
		t.Error("wordmark did not return to the opening screen")
	}
	if !strings.Contains(m.View(), splashTagline) {
		t.Error("wordmark not redrawn on the opening screen")
	}
}

// The list has to be sized around the wordmark, or the opening screen overflows by exactly its
// height and the bottom rows are pushed off.
func TestListIsSizedAroundTheSplash(t *testing.T) {
	m := openingModel(t)
	withSplash := m.list.Height()

	// A working list has no wordmark, so it gets those rows back.
	m.screen = screenAccount
	m.resizeList()
	withoutSplash := m.list.Height()

	if got := withoutSplash - withSplash; got != len(splashArt)+2 {
		t.Errorf("list grew by %d rows when the wordmark went away, want %d",
			got, len(splashArt)+2)
	}
}

// On a short or narrow terminal the wordmark would cost more rows than the list can spare.
func TestSplashIsSkippedOnSmallTerminals(t *testing.T) {
	for _, tt := range []struct {
		name string
		w, h int
	}{
		{"too short", 100, 12},
		{"too narrow", 30, 40},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newFormModel(t)
			m.screen = screenMethod
			m.buildMethodList()
			m.Update(tea.WindowSizeMsg{Width: tt.w, Height: tt.h})

			if m.splashVisible() {
				t.Errorf("wordmark shown at %dx%d", tt.w, tt.h)
			}
			// The list must still get usable rows rather than a negative height.
			if m.list.Height() < 3 {
				t.Errorf("list height = %d, want at least 3", m.list.Height())
			}
		})
	}
}

// exec and shell render the picker to stderr on the way to running a command; a greeting is
// noise on that path.
func TestSplashIsSkippedInCredsOnlyMode(t *testing.T) {
	m := openingModel(t)
	m.StartCredsMode()

	if m.splashVisible() {
		t.Error("wordmark shown in creds-only mode")
	}
}

// It also greets on first run, which is the screen a new user actually lands on.
func TestSplashShowsOnTheSetupScreen(t *testing.T) {
	m := newFormModel(t) // no ~/.aws/config, so this opens on the setup form
	if m.screen != screenSetup {
		t.Fatalf("screen = %v, want screenSetup", m.screen)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	if !strings.Contains(m.View(), splashTagline) {
		t.Error("wordmark missing from the first-run screen")
	}
}

// Guards the art itself: rows of differing width render as a ragged wordmark.
func TestSplashArtRowsAreEqualWidth(t *testing.T) {
	want := len([]rune(splashArt[0]))
	for i, row := range splashArt {
		if got := len([]rune(row)); got != want {
			t.Errorf("row %d is %d runes wide, want %d", i, got, want)
		}
	}
	// The margin counts. Comparing the bare art width against splashMinWidth let art of 49 sit
	// under a threshold of 50, where the 2-column MarginLeft in splash() pushed it to 51 and it
	// clipped. Whatever the art is, the terminal has to fit art + margin.
	const margin = 2
	if want+margin > splashMinWidth {
		t.Errorf("art is %d wide plus a %d-column margin, but is drawn down to %d columns",
			want, margin, splashMinWidth)
	}
}

// Sanity: a model that never received a WindowSizeMsg must not draw the wordmark into an unknown
// viewport, and must not panic.
func TestSplashWithoutAWindowSize(t *testing.T) {
	m := newFormModel(t)
	m.screen = screenMethod
	m.buildMethodList()
	m.awsSess = &awsint.Session{Label: "x"}

	if m.splashVisible() {
		t.Error("wordmark shown before any window size was known")
	}
	_ = m.View() // must not panic
}
