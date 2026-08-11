package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/postern/internal/aws"
)

// isQuit reports whether cmd is the quit command, by running it and inspecting the message.
//
// Only call this where quitting is the expected outcome. There is no way to identify tea.Quit
// without running the command, and the alternative on this path — fetchInstances — calls AWS
// and dereferences a session that a test has no reason to have populated.
func isQuit(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// `exec` and `shell` need an account and a role and nothing after that — there is no instance
// to pick for an API call — so the flow must stop the moment credentials exist.
func TestCredsModeQuitsOnceCredentialsExist(t *testing.T) {
	m := newFormModel(t)
	m.StartCredsMode()

	_, cmd := m.Update(msgCredsReady{})
	if !isQuit(t, cmd) {
		t.Error("creds-only mode did not quit when credentials became ready")
	}
	// Quitting instead of fetching, so nothing should be pending.
	if m.loading {
		t.Error("creds-only mode started a background fetch it will never use")
	}
}

// The normal picker stops on the action screen instead, because connecting to a host and
// calling the API diverge at this point and an API call has no instance to pick.
func TestNormalModeStopsOnActionScreen(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab (1) /AdminRole", AccessKeyID: "AKIA"}

	_, cmd := m.Update(msgCredsReady{})

	if m.screen != screenAction {
		t.Errorf("screen = %v, want screenAction", m.screen)
	}
	if isQuit(t, cmd) {
		t.Error("normal mode quit instead of offering the action list")
	}
	// Nothing is fetched yet — the choice is the user's.
	if m.loading {
		t.Error("loading was set before the user chose an action")
	}
	if len(m.list.Items()) == 0 {
		t.Error("action list is empty")
	}
}

// Browsing instances must still be the default, so the flow the tool is mostly used for costs
// one keystroke and no thought.
func TestBrowseInstancesIsTheDefaultAction(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}
	m.Update(msgCredsReady{})

	sel, ok := m.list.SelectedItem().(item)
	if !ok {
		t.Fatal("no selected item")
	}
	if sel.value != actionInstances {
		t.Errorf("default action = %q, want %q", sel.value, actionInstances)
	}
}

// The action screen is the only in-TUI route to the credentials, so it has to advertise them.
func TestActionListOffersTheCLI(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}
	m.Update(msgCredsReady{})

	for _, li := range m.list.Items() {
		if it, ok := li.(item); ok && it.value == actionCLI {
			return
		}
	}
	t.Error("action list has no entry for running AWS CLI commands")
}

// Choosing to browse starts the fetch, and the spinner has to be showing by the time Update
// returns or the UI looks frozen. The command itself is not run here — it would call AWS.
func TestSelectingBrowseStartsTheInstanceFetch(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}

	if cmd := m.selectAction(actionInstances); cmd == nil {
		t.Fatal("selecting browse returned no command")
	}
	if !m.loading {
		t.Error("loading was not set before the instance fetch")
	}
}

// A profile-backed session resolves through a different path than SSO, and must reach the same
// stopping point — otherwise `postern exec` works for SSO sessions and hangs for profiles.
func TestCredsModeQuitsForProfileSession(t *testing.T) {
	m := newFormModel(t)
	m.StartCredsMode()

	want := &awsint.Session{
		AccessKeyID: "AKIA", SecretAccessKey: "s", Region: "us-west-2",
		ProfileName: "crlab", Label: "profile:crlab",
	}
	_, cmd := m.Update(msgProfileReady{sess: want})

	if !isQuit(t, cmd) {
		t.Error("creds-only mode did not quit for a profile session")
	}
	if m.Session() != want {
		t.Errorf("Session() = %+v, want the resolved profile session", m.Session())
	}
}

// Backing out of the action screen has three different right answers, because a profile has no
// account or role step and a single-role account skips the role screen.
func TestActionScreenGoesBackToWhereCredentialsCameFrom(t *testing.T) {
	sso := &awsint.SSOSessionConfig{Name: "crlab", StartURL: "https://x.awsapps.com/start", Region: "us-east-1"}

	tests := []struct {
		name  string
		setup func(m *Model)
		want  screen
	}{
		{
			name:  "profile has no account or role step",
			setup: func(m *Model) { m.selSession = nil },
			want:  screenMethod,
		},
		{
			name: "several roles means the role screen was shown",
			setup: func(m *Model) {
				m.selSession = sso
				m.selAccount = &awsint.Account{ID: "1", Name: "one"}
				m.roles = []string{"AdminRole", "ReadOnly"}
			},
			want: screenRole,
		},
		{
			name: "a single role skips straight past the role screen",
			setup: func(m *Model) {
				m.selSession = sso
				m.selAccount = &awsint.Account{ID: "1", Name: "one"}
				m.roles = []string{"AdminRole"}
				m.accounts = []awsint.Account{{ID: "1", Name: "one"}}
			},
			want: screenAccount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newFormModel(t)
			tt.setup(m)
			m.screen = screenAction
			m.goBack()

			if m.screen != tt.want {
				t.Errorf("screen = %v, want %v", m.screen, tt.want)
			}
		})
	}
}

// The instance list is reached from the action screen, so that is where esc belongs — and it is
// the only route back to "Run AWS CLI commands" without starting over.
func TestInstanceListGoesBackToActionScreen(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab", AccessKeyID: "AKIA"}
	m.screen = screenInstance
	m.goBack()

	if m.screen != screenAction {
		t.Errorf("screen = %v, want screenAction", m.screen)
	}
}

// Time left is the part of the summary worth showing; it decides whether starting a task is
// worth it. An unknown expiry must simply be omitted rather than rendered as a zero.
func TestCredSummaryShowsRemainingTime(t *testing.T) {
	m := newFormModel(t)

	m.awsSess = &awsint.Session{Label: "crlab/AdminRole", Expires: time.Now().Add(52 * time.Minute)}
	if got := m.credSummary(); !strings.Contains(got, "crlab/AdminRole") || !strings.Contains(got, "expires in") {
		t.Errorf("credSummary() = %q, want the label and the time left", got)
	}

	m.awsSess = &awsint.Session{Label: "profile:static-keys"}
	if got := m.credSummary(); strings.Contains(got, "expires") {
		t.Errorf("credSummary() = %q, want no expiry for credentials that do not expire", got)
	}
}

// Quitting before choosing is an ordinary cancellation. main relies on a nil Session to tell
// that apart from success, and must not run the wrapped command.
func TestSessionIsNilBeforeAnythingIsChosen(t *testing.T) {
	m := newFormModel(t)
	m.StartCredsMode()

	if got := m.Session(); got != nil {
		t.Errorf("Session() = %+v before any selection, want nil", got)
	}
}
