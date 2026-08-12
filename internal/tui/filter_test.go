package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// applySearch puts the list in the state "/term" plus enter leaves it in.
//
// SetFilterText is used rather than replaying keystrokes because filtering in bubbles is
// asynchronous — a keystroke returns a command that computes the matches and reports them back
// as a FilterMatchesMsg — and driving that from a test means also executing the cursor-blink
// commands batched alongside it, each of which sleeps for the blink interval. SetFilterText does
// the same work synchronously and lands in the same state. TestSlashKeyProducesTheSameState
// pins that equivalence, so these tests cannot drift from what the keyboard actually does.
func applySearch(t *testing.T, m *Model, term string) {
	t.Helper()
	m.list.SetFilterText(term)

	if !m.list.IsFiltered() {
		t.Fatalf("search %q was not applied; the test is not reproducing the scenario", term)
	}
}

// Ties applySearch to reality: typing "/globo" and pressing enter has to leave the list in the
// state the other tests set up directly.
func TestSlashKeyProducesTheSameState(t *testing.T) {
	typed := accountModel(t)
	typed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "globo" {
		typed.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	typed.Update(tea.KeyMsg{Type: tea.KeyEnter})

	direct := accountModel(t)
	direct.list.SetFilterText("globo")

	if typed.list.FilterState() != direct.list.FilterState() {
		t.Errorf("typed state = %v, SetFilterText state = %v",
			typed.list.FilterState(), direct.list.FilterState())
	}
	if typed.list.FilterValue() != direct.list.FilterValue() {
		t.Errorf("typed value = %q, SetFilterText value = %q",
			typed.list.FilterValue(), direct.list.FilterValue())
	}
}

func accountModel(t *testing.T) *Model {
	t.Helper()
	m := newFormModel(t)
	m.selSession = &awsint.SSOSessionConfig{Name: "crlab", Region: "us-east-1"}
	m.accounts = []awsint.Account{
		{ID: "195170887130", Name: "crlab-clients-globogym-compute"},
		{ID: "402113345901", Name: "crlab-clients-aperture-data"},
		{ID: "070638634630", Name: "crlab-infra-mgmt"},
	}
	// Set for the screens further down the flow, whose titles name them. In the real flow
	// nothing reaches those builders without a selection having been made.
	m.selAccount = &awsint.Account{ID: "195170887130", Name: "crlab-clients-globogym-compute"}
	m.selInstance = &awsint.Instance{ID: "i-0aaa", Name: "web-01"}
	m.buildAccountList()
	m.screen = screenAccount
	return m
}

// The bug this guards: one list widget is shared by every screen, and so was its filter.
// bubbles/list.SetItems re-applies the current term to whatever it is handed, so searching
// "globo" to find an account left that term filtering the *next* screen. The action screen came
// up with its rows hidden and read as broken, and the only way forward was to clear the search
// by hand.
func TestSearchDoesNotLeakIntoTheNextScreen(t *testing.T) {
	m := accountModel(t)
	applySearch(t, m, "globo")

	if got := len(m.list.VisibleItems()); got != 1 {
		t.Fatalf("search matched %d accounts, want 1", got)
	}

	// Arrive at the action screen the way the real flow does.
	m.awsSess = &awsint.Session{Label: "crlab/AdminRole", AccessKeyID: "AKIA"}
	m.Update(msgCredsReady{})

	if m.list.IsFiltered() {
		t.Errorf("the search survived into the action screen (term %q)", m.list.FilterValue())
	}
	if got, want := len(m.list.VisibleItems()), 3; got != want {
		t.Errorf("action screen shows %d rows, want all %d", got, want)
	}
}

// Every screen shares the widget, so every screen has the same exposure. Checked as a set rather
// than one representative, since a builder added later would otherwise reintroduce this quietly.
func TestSearchIsClearedByEveryScreenTransition(t *testing.T) {
	tests := []struct {
		name  string
		build func(m *Model)
	}{
		{"method", (*Model).buildMethodList},
		{"account", (*Model).buildAccountList},
		{"instance", (*Model).buildInstanceList},
		{"connection type", (*Model).buildConnTypeList},
		{"tunnel manager", (*Model).buildMainList},
		{"action", (*Model).buildActionList},
		{"builder service", (*Model).buildServiceList},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := accountModel(t)
			applySearch(t, m, "globo")

			tt.build(m)

			if m.list.IsFiltered() {
				t.Errorf("%s screen kept the search %q", tt.name, m.list.FilterValue())
			}
		})
	}
}

// The cursor is only reset when a search was actually active. Without a filter it stays put, so
// backing out of the connection-type screen returns you to the instance you had highlighted
// rather than to the top of the list.
func TestCursorIsKeptWhenNoSearchWasActive(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{
		{ID: "i-0aaa", Name: "web-01"},
		{ID: "i-0bbb", Name: "web-02"},
		{ID: "i-0ccc", Name: "db-01"},
	}
	m.buildInstanceList()
	m.list.Select(2)

	m.buildInstanceList() // as goBack does when returning to this screen

	if got := m.list.Index(); got != 2 {
		t.Errorf("cursor = %d, want it left at 2", got)
	}
}

// After a search is cleared the rows are renumbered, so a cursor from the filtered view would
// point at an unrelated entry. It has to go back to the top.
func TestCursorResetsAfterAClearedSearch(t *testing.T) {
	m := accountModel(t)
	m.list.Select(2)
	applySearch(t, m, "globo")

	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}
	m.Update(msgCredsReady{})

	if got := m.list.Index(); got != 0 {
		t.Errorf("cursor = %d, want 0 after the search was cleared", got)
	}
}
