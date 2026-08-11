package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	awsint "github.com/treyperrone/postern/internal/aws"
)

func sessionExpiringIn(d time.Duration) *awsint.Session {
	return &awsint.Session{
		AccessKeyID: "AKIA", SecretAccessKey: "s", SessionToken: "t",
		Region: "us-east-1", AccountID: "195170887130", AccountName: "crlab-globogym",
		RoleName: "AdminRole", Label: "crlab-globogym (195170887130)/AdminRole",
		Expires: time.Now().Add(d),
	}
}

func TestNeedsCredRefresh(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		setup func(m *Model)
		want  bool
	}{
		{
			name:  "no credentials yet",
			setup: func(m *Model) {},
			want:  false,
		},
		{
			// Role credentials last an hour; there is nothing to do most of that time.
			name:  "plenty of time left",
			setup: func(m *Model) { m.awsSess = sessionExpiringIn(50 * time.Minute) },
			want:  false,
		},
		{
			name:  "inside the renewal window",
			setup: func(m *Model) { m.awsSess = sessionExpiringIn(5 * time.Minute) },
			want:  true,
		},
		{
			// Suspending a laptop for hours wakes to this: renew rather than sit expired.
			name:  "already expired",
			setup: func(m *Model) { m.awsSess = sessionExpiringIn(-time.Hour) },
			want:  true,
		},
		{
			// A profile backed by long-lived IAM keys has no expiry, so a timer must not
			// renew it — there is nothing to renew and it would call AWS forever.
			name:  "credentials that never expire",
			setup: func(m *Model) { m.awsSess = &awsint.Session{AccessKeyID: "AKIA", ProfileName: "static"} },
			want:  false,
		},
		{
			// A slow renewal must not stack requests up behind itself.
			name: "already renewing",
			setup: func(m *Model) {
				m.awsSess = sessionExpiringIn(time.Minute)
				m.refreshingCreds = true
			},
			want: false,
		},
		{
			// exec and shell hand credentials straight to a command and exit; there is no
			// TUI left to keep anything fresh for.
			name: "creds-only mode",
			setup: func(m *Model) {
				m.awsSess = sessionExpiringIn(time.Minute)
				m.credsOnly = true
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newFormModel(t)
			tt.setup(m)
			if got := m.needsCredRefresh(now); got != tt.want {
				t.Errorf("needsCredRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The tick has to re-arm every time. Returning nil on the quiet path would stop the clock after
// the first check and nothing would renew for the rest of the session.
func TestCredTickAlwaysReArms(t *testing.T) {
	for _, tt := range []struct {
		name string
		sess *awsint.Session
	}{
		{"nothing due", sessionExpiringIn(50 * time.Minute)},
		{"renewal due", sessionExpiringIn(time.Minute)},
		{"no credentials", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newFormModel(t)
			m.awsSess = tt.sess

			_, cmd := m.Update(msgCredTick(time.Now()))
			if cmd == nil {
				t.Error("tick did not schedule another check; renewal would stop here")
			}
		})
	}
}

func TestTickStartsARenewalWhenDue(t *testing.T) {
	m := newFormModel(t)
	m.selSession = &awsint.SSOSessionConfig{Name: "crlab", StartURL: "https://x.awsapps.com/start", Region: "us-east-1"}
	m.awsSess = sessionExpiringIn(2 * time.Minute)

	m.Update(msgCredTick(time.Now()))

	if !m.refreshingCreds {
		t.Error("a due renewal was not started")
	}
}

func TestSuccessfulRenewalReplacesTheSession(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = sessionExpiringIn(time.Minute)
	m.refreshingCreds = true
	m.credRefreshErr = errors.New("previous failure")

	fresh := sessionExpiringIn(time.Hour)
	fresh.AccessKeyID = "AKIAFRESH"
	m.applyCredRefresh(msgCredsRefreshed{sess: fresh, token: "new-sso-token"})

	if m.awsSess.AccessKeyID != "AKIAFRESH" {
		t.Errorf("access key = %q, want the renewed one", m.awsSess.AccessKeyID)
	}
	if m.token != "new-sso-token" {
		t.Errorf("SSO token = %q, want it updated alongside the credentials", m.token)
	}
	if m.refreshingCreds {
		t.Error("still marked as renewing")
	}
	if m.credRefreshErr != nil {
		t.Errorf("stale error survived a success: %v", m.credRefreshErr)
	}
}

// A failed renewal must not throw away credentials that may still have minutes left on them, and
// must not hijack the screen — it runs on a timer the user did not ask for.
func TestFailedRenewalKeepsTheOldCredentials(t *testing.T) {
	m := newFormModel(t)
	old := sessionExpiringIn(5 * time.Minute)
	m.awsSess = old
	m.refreshingCreds = true

	m.applyCredRefresh(msgCredsRefreshed{err: errors.New("network is down")})

	if m.awsSess != old {
		t.Error("old credentials were discarded on a failed renewal")
	}
	if m.err != nil {
		t.Errorf("a background failure was raised into the error view: %v", m.err)
	}
	if m.credRefreshErr == nil {
		t.Error("failure was not recorded")
	}
	if m.refreshingCreds {
		t.Error("still marked as renewing after a failure")
	}
}

// Needing a browser is the one failure with an instruction attached, and it has to be
// distinguishable from a transient one.
func TestCredRefreshNote(t *testing.T) {
	m := newFormModel(t)

	if got := m.credRefreshNote(); got != "" {
		t.Errorf("note = %q, want silence when all is well", got)
	}

	m.refreshingCreds = true
	if got := m.credRefreshNote(); got != "renewing" {
		t.Errorf("note = %q, want renewing", got)
	}

	m.refreshingCreds = false
	m.credRefreshErr = awsint.ErrLoginRequired
	if got := m.credRefreshNote(); got == "" || !strings.Contains(got, "sign-in") {
		t.Errorf("note = %q, want it to say a sign-in is needed", got)
	}

	m.credRefreshErr = errors.New("timeout")
	if got := m.credRefreshNote(); got != "renewal failed" {
		t.Errorf("note = %q, want a plain failure note", got)
	}
}

// The header is where renewal reports itself, so the note has to reach it.
func TestCredSummaryCarriesTheRenewalNote(t *testing.T) {
	m := newFormModel(t)
	m.awsSess = sessionExpiringIn(30 * time.Minute)
	m.credRefreshErr = awsint.ErrLoginRequired

	if got := m.credSummary(); !strings.Contains(got, "sign-in") {
		t.Errorf("credSummary() = %q, want the renewal note included", got)
	}
}
