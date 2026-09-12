package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/tunnel"
)

func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// reauthReadyModel is a model that has fully resolved an SSO identity and could rebuild it:
// session, account and role are all known.
func reauthReadyModel(t *testing.T) *Model {
	t.Helper()
	m := mainScreenModel(t)
	m.selSession = &awsint.SSOSessionConfig{Name: "crlab", StartURL: "https://ex.awsapps.com/start", Region: "eu-west-2"}
	m.selAccount = &awsint.Account{ID: "111111111111", Name: "cr-lab"}
	m.selRole = "AdminRole"
	m.awsSess = &awsint.Session{Label: "cr-lab/AdminRole", RoleName: "AdminRole", AccessKeyID: "AKIA"}
	m.selInstance = &awsint.Instance{ID: "i-0aaa", Name: "win-01"}
	m.connType = tunnel.KindRDP
	return m
}

// An expired session that surfaces while listing instances should run the sign-in and come
// back to the instance list, not dead-end in the error view.
func TestExpiredSessionListingInstancesTriggersReauth(t *testing.T) {
	m := reauthReadyModel(t)

	_, cmd := m.Update(msgInstances{err: awsint.ErrLoginRequired})

	if m.err != nil {
		t.Fatalf("m.err = %v, want nil — the error should have become a re-auth", m.err)
	}
	if !m.loading {
		t.Error("m.loading = false, want true while the token is fetched")
	}
	if m.resume != resumeInstances {
		t.Errorf("m.resume = %v, want resumeInstances", m.resume)
	}
	if m.pendingFavRole != "AdminRole" {
		t.Errorf("m.pendingFavRole = %q, want AdminRole — so msgToken rebuilds the same identity", m.pendingFavRole)
	}
	if cmd == nil {
		t.Error("no command returned — nothing is fetching the token")
	}
}

// Once the token and fresh credentials land, the resume runs: back to the instance list.
func TestReauthResumeReturnsToInstanceList(t *testing.T) {
	m := reauthReadyModel(t)
	m.resume = resumeInstances

	_, cmd := m.Update(msgCredsReady{})

	if m.screen != screenInstance {
		t.Errorf("screen = %v, want screenInstance", m.screen)
	}
	if m.resume != resumeNone {
		t.Errorf("m.resume = %v, want resumeNone — it must be consumed", m.resume)
	}
	if cmd == nil {
		t.Error("no command returned — the instance list is not being fetched")
	}
}

// A resumed connection re-runs the tunnel instead of stopping at the action hub.
func TestReauthResumeReconnectsTheTunnel(t *testing.T) {
	m := reauthReadyModel(t)
	m.resume = resumeConnect

	_, cmd := m.Update(msgCredsReady{})

	if m.resume != resumeNone {
		t.Errorf("m.resume = %v, want resumeNone", m.resume)
	}
	if m.screen == screenAction {
		t.Error("landed on the action hub — a resumed connection should reconnect, not stop")
	}
	if cmd == nil {
		t.Error("no command returned — the tunnel is not being re-established")
	}
}

// Without a session to sign back into, an expired-session error stays an ordinary error.
func TestExpiredSessionWithoutContextStaysAnError(t *testing.T) {
	m := reauthReadyModel(t)
	m.selSession = nil

	m.Update(msgInstances{err: awsint.ErrLoginRequired})

	if m.err == nil {
		t.Error("m.err = nil, want the error shown — there is no session to re-auth")
	}
}

// A permission error is not an expired session: it must not loop into a browser.
func TestPermissionErrorDoesNotTriggerReauth(t *testing.T) {
	m := reauthReadyModel(t)

	m.Update(msgInstances{err: errors.New("describe instances: AccessDenied: not authorized")})

	if m.err == nil {
		t.Error("m.err = nil — a permission error should surface, not become a re-auth")
	}
	if m.resume != resumeNone {
		t.Errorf("m.resume = %v, want resumeNone", m.resume)
	}
}

// The passive path: the background renewal gave up (ErrLoginRequired on the header), and r
// runs the sign-in.
func TestRKeyReauthenticatesWhenRenewalGaveUp(t *testing.T) {
	m := reauthReadyModel(t)
	m.credRefreshErr = awsint.ErrLoginRequired
	m.buildActionList()
	m.screen = screenAction

	_, cmd := m.Update(keyRunes('r'))

	if cmd == nil {
		t.Fatal("r did nothing while a sign-in was needed")
	}
	if m.resume != resumeActionHub {
		t.Errorf("m.resume = %v, want resumeActionHub", m.resume)
	}
	if m.credRefreshErr != nil {
		t.Errorf("m.credRefreshErr = %v, want nil once re-auth starts", m.credRefreshErr)
	}
}

// r pressed while browsing instances must resume the instance list, not drop to the action
// hub — the whole point of pressing it there instead of backing out first.
func TestRKeyFromInstanceListResumesThere(t *testing.T) {
	m := reauthReadyModel(t)
	m.credRefreshErr = awsint.ErrLoginRequired
	m.instances = []awsint.Instance{{ID: "i-0aaa", Name: "win-01"}}
	m.buildInstanceList()
	m.screen = screenInstance

	_, cmd := m.Update(keyRunes('r'))

	if cmd == nil {
		t.Fatal("r did nothing while a sign-in was needed")
	}
	if m.resume != resumeInstances {
		t.Errorf("m.resume = %v, want resumeInstances", m.resume)
	}
}

// Same, for the S3 bucket/object screens.
func TestRKeyFromS3BucketsResumesThere(t *testing.T) {
	m := reauthReadyModel(t)
	m.credRefreshErr = awsint.ErrLoginRequired
	m.s3Buckets = []string{"b1"}
	m.buildS3BucketList()
	m.screen = screenS3Buckets

	_, cmd := m.Update(keyRunes('r'))

	if cmd == nil {
		t.Fatal("r did nothing while a sign-in was needed")
	}
	if m.resume != resumeS3Buckets {
		t.Errorf("m.resume = %v, want resumeS3Buckets", m.resume)
	}
}

// r is inert when there is nothing to re-authenticate — it is an ordinary key then.
func TestRKeyDoesNothingWithoutAPendingSignIn(t *testing.T) {
	m := reauthReadyModel(t)
	m.buildActionList()
	m.screen = screenAction

	m.Update(keyRunes('r'))

	if m.resume != resumeNone {
		t.Errorf("m.resume = %v, want resumeNone — r should not have started anything", m.resume)
	}
}
