package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/postern/internal/aws"
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

// The wordmark is a greeting. Once you are working it stops earning its rows, and it must not
// come back when you navigate back to the opening screen.
func TestSplashIsDismissedForGoodAfterTheFirstScreen(t *testing.T) {
	m := openingModel(t)
	if !m.splashVisible() {
		t.Fatal("wordmark not visible to begin with")
	}

	// Move past the opening screen; the draw is what retires it.
	m.screen = screenAccount
	m.buildAccountList()
	m.View()

	if !m.splashDone {
		t.Error("wordmark was not retired after leaving the opening screen")
	}

	// ...and going back does not bring it back.
	m.screen = screenMethod
	m.buildMethodList()
	if m.splashVisible() {
		t.Error("wordmark returned after navigating back")
	}
	if strings.Contains(m.View(), splashTagline) {
		t.Error("wordmark redrawn after navigating back")
	}
}

// The list has to be sized around the wordmark, or the opening screen overflows by exactly its
// height and the bottom rows are pushed off.
func TestListIsSizedAroundTheSplash(t *testing.T) {
	m := openingModel(t)
	withSplash := m.list.Height()

	m.splashDone = true
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
	if want >= splashMinWidth {
		t.Errorf("art is %d wide but is drawn down to %d columns", want, splashMinWidth)
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
