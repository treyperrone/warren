package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// A session that opens in another window is otherwise indistinguishable from nothing happening:
// the TUI is never handed the terminal, so without a confirmation the keypress looks ignored.
func TestWindowedShellConfirmsWhereItWent(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{
		{ID: "i-0abc", Name: "kali-01", Platform: "linux", PlatformDetails: "Linux/UNIX"},
	}
	m.buildInstanceList()

	m.Update(msgShellWindowed{where: "Terminal", name: "kali-01"})

	if m.notice == "" {
		t.Fatal("no confirmation after a session opened in another window")
	}
	for _, want := range []string{"kali-01", "Terminal"} {
		if !strings.Contains(m.notice, want) {
			t.Errorf("notice %q does not mention %q", m.notice, want)
		}
	}
	if !strings.Contains(m.View(), m.notice) {
		t.Error("the notice is set but not rendered")
	}
}

// Landing back on the instance list is what makes opening a second session one keypress, which is
// the entire reason for using a window.
func TestWindowedShellReturnsToTheInstanceList(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{{ID: "i-0abc", Name: "kali-01"}}
	m.buildInstanceList()
	m.screen = screenConnType

	m.Update(msgShellWindowed{where: "tmux", name: "kali-01"})

	if m.screen != screenInstance {
		t.Errorf("screen = %v after opening a window, want the instance list", m.screen)
	}
}

// With no instances loaded there is no list to return to, so it must not land on an empty screen.
func TestWindowedShellFallsBackToTheMainScreen(t *testing.T) {
	m := newFormModel(t)
	m.instances = nil
	m.screen = screenConnType

	m.Update(msgShellWindowed{where: "Terminal", name: "kali-01"})

	if m.screen != screenMain {
		t.Errorf("screen = %v with no instances, want the main screen", m.screen)
	}
}

// A stale confirmation left on screen reads as applying to whatever the user does next.
func TestNoticeIsClearedByTheNextKeypress(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{{ID: "i-0abc", Name: "kali-01"}}
	m.buildInstanceList()
	m.Update(msgShellWindowed{where: "Terminal", name: "kali-01"})

	if m.notice == "" {
		t.Fatal("precondition failed: no notice to clear")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})

	if m.notice != "" {
		t.Errorf("notice %q survived a keypress", m.notice)
	}
}

// Unlike an error, a confirmation must not swallow the key that dismisses it — the key was aimed
// at the list, and eating it would make navigation feel like it dropped an input.
func TestClearingANoticeDoesNotSwallowTheKey(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{
		{ID: "i-0aaa", Name: "aaa-01"},
		{ID: "i-0bbb", Name: "bbb-02"},
	}
	m.buildInstanceList()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(msgShellWindowed{where: "Terminal", name: "aaa-01"})

	before := m.list.Index()
	m.Update(tea.KeyMsg{Type: tea.KeyDown})

	if m.list.Index() == before {
		t.Error("the keypress that cleared the notice was consumed instead of moving the cursor")
	}
}

// The notice is transient chrome, so it must not push the list off a short terminal permanently —
// it occupies rows only while it is set.
func TestNoticeTakesNoRoomWhenUnset(t *testing.T) {
	m := newFormModel(t)
	if got := m.noticeLine(); got != "" {
		t.Errorf("noticeLine() = %q with no notice, want empty", got)
	}
}
