package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyperrone/warren/internal/testenv"
)

// modelWithSSOSession builds a model that has a real [sso-session] to select, which newFormModel
// deliberately does not — it starts from an empty config to exercise first-run setup.
func modelWithSSOSession(t *testing.T) *Model {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[sso-session corp]\nsso_start_url = https://corp.awsapps.com/start\nsso_region = us-east-1\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want screenMethod with a session configured", m.screen)
	}
	m.buildMethodList()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// Pressing Enter twice on an SSO session used to start two device authorizations: two browser tabs
// and two user codes, so the code on screen belonged to a different authorization than the one the
// browser was asking about. They visibly did not match and neither could be confirmed.
//
// m.loading was set in nineteen places and read only by View, so nothing stopped the second one.
func TestSecondKeypressCannotStartASecondRequest(t *testing.T) {
	m := modelWithSSOSession(t)

	// Select the SSO session, which begins the token fetch.
	_, first := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if first == nil {
		t.Fatal("selecting an sso session started no work")
	}
	if !m.loading {
		t.Fatal("selecting an sso session did not mark the model as loading")
	}

	// A second Enter must do nothing at all.
	_, second := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if second != nil {
		t.Error("a second Enter while loading started more work; that is a second device authorization")
	}
}

// Every other key is inert too — one of them selecting something else mid-fetch would leave the
// arriving response applying to a screen the user has already left.
func TestKeysAreInertWhileLoading(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyRunes, Runes: []rune("n")},
		{Type: tea.KeyRunes, Runes: []rune("/")},
		{Type: tea.KeyRunes, Runes: []rune("?")},
	} {
		m := newFormModel(t)
		m.screen = screenMain
		m.buildMainList()
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m.loading = true

		before := m.screen
		if _, cmd := m.Update(key); cmd != nil {
			t.Errorf("key %v produced work while loading", key)
		}
		if m.screen != before {
			t.Errorf("key %v changed screen while loading: %d -> %d", key, before, m.screen)
		}
	}
}

// ctrl+c has to keep working, or a slow device authorization — which polls until the code expires —
// would leave no way out but killing the terminal.
func TestCtrlCStillQuitsWhileLoading(t *testing.T) {
	m := newFormModel(t)
	m.screen = screenMethod
	m.buildMethodList()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.loading = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c did nothing while loading")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Error("ctrl+c did not quit while loading")
	}
}
