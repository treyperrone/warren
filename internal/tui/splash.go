package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The wordmark, shown once on the opening screen and never again.
//
// It earns its rows at startup, when knowing what you just launched is the most useful thing on
// screen. It stops earning them the moment you are working, when every row of the account list
// matters more — so it is dismissed permanently after the first selection rather than being
// redrawn on the way back.
// Solid blocks rather than box-drawing: at this size the letters read as pixels, which is the
// point. Every row is the same width — see TestSplashArtRowsAreEqualWidth, because one row a
// character short renders as a visibly crooked wordmark and is easy to miss by eye.
var splashArt = []string{
	`█████ █████ █████ █████ █████ █████ █   █`,
	`█   █ █   █ █       █   █     █   █ ██  █`,
	`█████ █   █ █████   █   ████  █████ █ █ █`,
	`█     █   █     █   █   █     █  █  █  ██`,
	`█     █████ █████   █   █████ █   █ █   █`,
}

const splashTagline = "a side gate into your AWS accounts"

const (
	// Below these the wordmark costs more rows than the list can spare, so it is skipped
	// rather than shrinking the thing you came here to use. The width floor sits just above
	// the art itself so it is never clipped mid-letter.
	splashMinWidth  = 45
	splashMinHeight = 24
)

var (
	styleSplash    = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	styleSplashDim = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
)

// splashVisible reports whether the wordmark should be drawn right now.
func (m *Model) splashVisible() bool {
	// credsOnly renders to stderr on the way to running a command; a greeting is noise there.
	if m.splashDone || m.credsOnly {
		return false
	}
	if m.screen != screenMethod && m.screen != screenSetup {
		return false
	}
	return m.width >= splashMinWidth && m.height >= splashMinHeight
}

// splashRows is how many rows the wordmark occupies, so the list can be sized around it.
func (m *Model) splashRows() int {
	if !m.splashVisible() {
		return 0
	}
	return len(splashArt) + 2 // tagline and the blank line under it
}

// splash renders the wordmark, or "" when it should not be shown — so call sites can append it
// unconditionally.
func (m *Model) splash() string {
	if !m.splashVisible() {
		return ""
	}

	var b strings.Builder
	for _, line := range splashArt {
		b.WriteString(styleSplash.MarginLeft(2).Render(line) + "\n")
	}
	b.WriteString(styleSplashDim.MarginLeft(2).Render(splashTagline) + "\n\n")
	return b.String()
}

// resizeList sizes the list to whatever is left after the banner, the list's own chrome, and the
// wordmark while it is showing. Without accounting for the wordmark the opening list overflows
// by exactly its height.
func (m *Model) resizeList() {
	h := m.height - 5 - m.splashRows() // -1 banner, -4 list chrome
	if h < 3 {
		h = 3
	}
	m.list.SetSize(m.width, h)
}
