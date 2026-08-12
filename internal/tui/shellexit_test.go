package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
)

func mainScreenModel(t *testing.T) *Model {
	t.Helper()
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab/AdminRole", AccessKeyID: "AKIA"}
	m.buildMainList()
	m.screen = screenMain
	return m
}

// The bug this guards: a single ESC on the tunnel manager used to end the program. That is the
// screen an interactive SSM shell returned to, and a terminal being restored by a raw-mode child
// emits escape sequences — read as a keypress, one of those ended the tool, which looked like
// "exiting the remote shell killed my terminal". The README always documented esc as going back.
func TestEscOnMainScreenDoesNotQuit(t *testing.T) {
	m := mainScreenModel(t)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if isQuit(t, cmd) {
		t.Fatal("esc on the tunnel manager still quits the program")
	}
	if m.screen != screenAction {
		t.Errorf("screen = %v, want screenAction — esc should go back", m.screen)
	}
}

// q is the advertised way out and stays that way; the list title says so.
func TestQOnMainScreenStillQuits(t *testing.T) {
	m := mainScreenModel(t)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if !isQuit(t, cmd) {
		t.Error("q no longer quits from the tunnel manager")
	}
}

// With nothing authenticated there is nowhere to go back to, and quitting would be worse than
// staying put.
func TestEscOnMainScreenWithoutCredentialsStaysPut(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = nil
	m.buildMainList()
	m.screen = screenMain

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if isQuit(t, cmd) {
		t.Error("esc quit the program when there was nowhere to go back to")
	}
	if m.screen != screenMain {
		t.Errorf("screen = %v, want to stay on screenMain", m.screen)
	}
}

// Exiting an interactive shell should land somewhere useful. A foreground shell registers no
// tunnel, so the tunnel manager would have nothing new on it; the instance list is both more
// useful and free of any single-key quit.
func TestShellExitReturnsToTheInstanceList(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab/AdminRole", AccessKeyID: "AKIA"}
	m.instances = []awsint.Instance{
		{ID: "i-0aaa", Name: "web-01"},
		{ID: "i-0bbb", Name: "db-01"},
	}

	m.Update(msgShellDone{})

	if m.screen != screenInstance {
		t.Errorf("screen = %v, want screenInstance", m.screen)
	}
	if got := len(m.list.Items()); got != 2 {
		t.Errorf("instance list has %d rows, want 2", got)
	}
}

// Whatever it lands on must survive the keystrokes a restored terminal can fabricate.
//
// Only escape and enter are checked. A stray "q" does quit — that is the bubbles list default
// (its Quit binding is "q"/"esc") and the README advertises it — but a terminal handing control
// back does not emit a bare "q", whereas it emits escape sequences by the byte.
func TestScreenAfterShellExitSurvivesTerminalNoise(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}
	m.instances = []awsint.Instance{{ID: "i-0aaa", Name: "web-01"}}
	m.Update(msgShellDone{})

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyEnter},
	} {
		m.screen = screenInstance
		if _, cmd := m.Update(key); isQuit(t, cmd) {
			t.Errorf("%v quit the program right after a shell exited", key)
		}
	}
}

// With no instances cached there is no list to return to, so the manager is the fallback — and it
// must still not be quittable by a stray escape.
func TestShellExitFallsBackToManagerSafely(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}
	m.instances = nil

	m.Update(msgShellDone{})
	if m.screen != screenMain {
		t.Fatalf("screen = %v, want screenMain as the fallback", m.screen)
	}

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); isQuit(t, cmd) {
		t.Error("esc quit from the fallback screen")
	}
}
