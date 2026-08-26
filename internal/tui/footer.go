package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The footer is the lazygit-style key strip under every list: the keys that work on THIS
// screen, in THIS state, phrased by what they do. It exists because a keybind nobody can see
// is a feature nobody has — "x removes a favorite" was real for a week before anyone found
// it, and the about screen (the "?" reference) is the wrong distance away for that: you have
// to already suspect a key exists to go looking for it.
//
// Contextual means the strip changes with the cursor, not just the screen: "enter download"
// on an object row becomes "enter open" on a folder and "enter upload" on the upload row,
// and while a search is being typed the strip says so — the keys really do mean something
// different then.

type keyHint struct{ key, label string }

var (
	styleFooterKey   = lipgloss.NewStyle().Foreground(lipgloss.Color("189")).Bold(true)
	styleFooterLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleFooterSep   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// footerHints is the ordered strip for the current screen and cursor: the primary action
// first, then anything specific to the highlighted row, then navigation, then the globals.
// Ordering is priority — footer drops from the END when the terminal is too narrow, so the
// keys most worth knowing survive longest.
func (m *Model) footerHints() []keyHint {
	// Typing a search: the list owns the keyboard and the usual keys mean other things.
	if m.list.SettingFilter() {
		return []keyHint{{"enter", "apply search"}, {"esc", "cancel search"}}
	}

	var hints []keyHint
	add := func(key, label string) { hints = append(hints, keyHint{key, label}) }

	sel, _ := m.list.SelectedItem().(item)

	switch m.screen {
	case screenMain:
		add("enter", "select")
		add("n", "new connection")
		add("p", "switch account")
	case screenS3Objects:
		switch {
		case sel.value == "upload":
			add("enter", "upload")
		case strings.HasPrefix(sel.value, "p:"):
			add("enter", "open")
		case strings.HasPrefix(sel.value, "o:"):
			add("enter", "download")
		default:
			add("enter", "select")
		}
	case screenConnType:
		add("enter", "connect")
	case screenFavoriteRemove:
		add("enter", "remove")
	case screenProfileConfirm:
		add("enter", "confirm")
	case screenMethod:
		if sel.value == methodRDPScreen {
			add("enter", "toggle")
		} else {
			add("enter", "select")
		}
	default:
		add("enter", "select")
	}

	// Row-specific: the favorite under the cursor can be deleted in place.
	if (m.screen == screenMethod || m.screen == screenFavorites) && strings.HasPrefix(sel.value, "fav:") {
		add("x", "remove favorite")
	}

	add("/", "search")

	// esc: clearing an applied search comes before going back, exactly as Update orders it.
	switch {
	case m.list.IsFiltered():
		add("esc", "clear search")
	case m.screen == screenS3Objects && m.s3Prefix != "":
		add("esc", "up a level")
	case m.screen == screenMain && m.awsSess == nil:
		// Nowhere to go back to without credentials; goBack stays put, so say nothing.
	case m.screen == screenMethod:
		// The first screen: there is no "back", and q is the way out.
	default:
		add("esc", "back")
	}

	add("q", "quit")
	return hints
}

// footer renders the strip on one row, fitted to the terminal. Hints are dropped from the
// end until it fits — a strip that wraps to a second row steals a list row and misaligns
// everything above it, which is worse than a shorter strip. The first hint always stays:
// a footer that says nothing is a blank row.
func (m *Model) footer() string {
	return renderFooter(m.footerHints(), m.width)
}

func renderFooter(hints []keyHint, width int) string {
	if len(hints) == 0 {
		return ""
	}
	if width <= 0 {
		width = 80 // no WindowSizeMsg yet
	}
	render := func(hs []keyHint) string {
		parts := make([]string, 0, len(hs))
		for _, h := range hs {
			parts = append(parts, styleFooterKey.Render(h.key)+" "+styleFooterLabel.Render(h.label))
		}
		return " " + strings.Join(parts, styleFooterSep.Render("  ·  "))
	}
	out := render(hints)
	for lipgloss.Width(out) > width && len(hints) > 1 {
		hints = hints[:len(hints)-1]
		out = render(hints)
	}
	return out
}
