package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// bubbles/list's own keymap binds both "q" and "esc" straight to tea.Quit — independent of
// warren's own handling. Before requestQuit existed, an unhandled q on any screen but
// screenMain fell through to that raw binding and quit instantly, no confirmation. This
// checks that door stays shut: q on an ordinary picker screen must land on the confirm
// screen, never quit directly.
func TestQAsksBeforeQuittingFromAnOrdinaryScreen(t *testing.T) {
	m := modelWithSSOSession(t)
	m.screen = screenAccount

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if isQuit(t, cmd) {
		t.Fatal("q quit directly from screenAccount instead of asking first")
	}
	if m.screen != screenQuitConfirm {
		t.Fatalf("screen = %v, want screenQuitConfirm", m.screen)
	}
	if m.quitReturn != screenAccount {
		t.Errorf("quitReturn = %v, want screenAccount", m.quitReturn)
	}
}

// "Keep going" must restore the exact screen quitting was requested from, and must not have
// touched the underlying screen's own list — quitList is a separate widget precisely so
// asking never costs the user their place.
func TestQuitKeepGoingRestoresScreenAndList(t *testing.T) {
	m := modelWithSSOSession(t)
	m.buildMethodList()
	before := m.list.Items()

	cmd := m.requestQuit()
	if cmd != nil {
		t.Fatal("requestQuit itself must not quit")
	}
	if m.screen != screenQuitConfirm {
		t.Fatalf("screen = %v, want screenQuitConfirm", m.screen)
	}

	m.quitList.Select(0) // "Keep going" is the first, default row
	if got := m.selectQuitConfirm(); got != nil {
		t.Error("keep going returned a non-nil command")
	}
	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want screenMethod restored", m.screen)
	}
	if len(m.list.Items()) != len(before) {
		t.Errorf("underlying list changed: had %d items, now %d", len(before), len(m.list.Items()))
	}
}

// Picking "Quit" on the confirm screen is the one path that actually ends the program.
func TestQuitConfirmQuits(t *testing.T) {
	m := modelWithSSOSession(t)
	m.requestQuit()
	m.quitList.Select(1) // "Quit"

	cmd := m.selectQuitConfirm()
	if !isQuit(t, cmd) {
		t.Error("selecting Quit on the confirm screen did not quit")
	}
}

// ctrl+c stays an instant force-quit, unchanged — a raw-mode child restoring the terminal
// must always have a keybind that works no matter what screen or state warren is in.
func TestCtrlCStillQuitsInstantly(t *testing.T) {
	m := modelWithSSOSession(t)
	m.screen = screenAccount

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !isQuit(t, cmd) {
		t.Error("ctrl+c did not quit instantly")
	}
	if m.screen == screenQuitConfirm {
		t.Error("ctrl+c should not route through the confirm screen")
	}
}
