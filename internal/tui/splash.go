package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The wordmark, shown on the screens you rest on rather than the ones you pass through.
//
// It earns its rows at the top of the flow and on the tunnel manager, where there is little to
// list and knowing what you are looking at is worth something. It stops earning them on the
// working lists — accounts, roles, instances — where every row matters more. So this is derived
// from the current screen, not dismissed once: back out to the top and it is there again.
// The wordmark: "WARREN" set in Press Start 2P at 8px and packed two pixel-rows to a character
// cell using half blocks.
//
// It is traced from a real font rather than drawn by hand, and that is the point. Several
// hand-built attempts all read as "blocky text scaled up" for one reason: their corners were square.
// A genuine 8-bit face cuts the corner pixel — ▄█▀▀▀█▄ rather than ████████ — and that chamfer,
// not stroke weight, is what makes it read as 8-bit. The same goes for A's chamfered apex and N's
// diagonal, which a staircase of rectangles does not reproduce.
//
// The W is the one exception: it is drawn, not traced. Press Start 2P's W runs its centre stem
// the full height of the glyph, so at this size it reads as three equal verticals — closer to a
// Ш than a W — and that was confirmed to be the real glyph rather than a rendering artefact, since
// the same trace at 16px is an exact 2x of it. Faithfulness lost to legibility here.
//
// It is also 9 columns wide where every other glyph is 7, because a W needs four strokes and 7
// columns cannot fit four 2px strokes with gaps between them. That is the whole reason the font
// resorts to a centre stem. Widening only this glyph breaks the uniform grid, which is why
// splashMinWidth is 52 rather than 50.
//
// Its base is filled rather than left as separate strokes meeting at a point. With gaps flanking
// the centre peak the row rendered as ██▄ █ ▄██ and read as dotted or broken at this size, so the
// four strokes merge into one base and the peak rises a half-cell above it.
//
// What it does keep is the conventions of the traced letters: 2px stems, chamfered corners, and
// feet on the outer strokes — so it sits with A, R, E and N rather than looking bolted on.
//
// Press Start 2P is OFL-licensed and itself derived from the 1980s Namco arcade lettering. Only
// these glyph shapes are reproduced; no font file ships with warren. To regenerate: render the
// text at 8px, threshold above ~110, trim blank rows and columns, then pack row pairs into
// █ ▀ ▄ and space.
//
// A terminal cell is about twice as tall as it is wide, so one cell per pixel renders squashed;
// packing two pixel-rows into one cell keeps the pixels roughly square.
//
// Every row is the same width — see TestSplashArtRowsAreEqualWidth, because one row a character
// short renders as a visibly crooked wordmark and is easy to miss by eye.
var splashArt = []string{
	`██     ██  ▄█▀█▄  ██▀▀▀█▄ ██▀▀▀█▄ ██▀▀▀▀▀ ██▄  ██`,
	`██     ██ ██   ██ ██  ▄██ ██  ▄██ ██▄▄▄▄  ██▀█▄██`,
	`██▄▄█▄▄██ ██▀▀▀██ ██▀██▄  ██▀██▄  ██      ██  ▀██`,
	` ▀▀   ▀▀  ▀▀   ▀▀ ▀▀  ▀▀▀ ▀▀  ▀▀▀ ▀▀▀▀▀▀▀ ▀▀   ▀▀`,
}

const splashTagline = "a way into every one of your AWS accounts"

const (
	// Below these the wordmark costs more rows than the list can spare, or would be clipped
	// mid-letter, so it is skipped rather than shrinking the thing you came here to use. The
	// banner still names the tool, so nothing is lost but decoration.
	splashMinWidth = 52 // 49 of art plus the 2-column left margin, plus 1
	// 20, not 24. A standard terminal is exactly 24 rows, so at 24 anything that consumes a
	// single row — most commonly tmux's status bar — pushed the wordmark below the threshold
	// and it vanished with no indication why. The wordmark needs 6 rows and the rest of the
	// screen needs 8, so 20 still leaves the list 9 rows to work with.
	splashMinHeight = 20
)

var (
	styleSplash    = lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	styleSplashDim = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
)

// splashScreens are the screens the wordmark appears on: the two you start from, and the tunnel
// manager you return to after a session.
func splashScreens(s screen) bool {
	return s == screenMethod || s == screenSetup || s == screenMain
}

// splashVisible reports whether the wordmark should be drawn right now.
func (m *Model) splashVisible() bool {
	// credsOnly renders to stderr on the way to running a command; a greeting is noise there.
	if m.credsOnly || !splashScreens(m.screen) {
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
