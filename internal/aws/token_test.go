package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/treyperrone/warren/internal/testenv"
)

// The trap this guards: LiveToken falls through to the device-auth flow, which prints a user
// code, opens a browser, and then polls for as long as the code is valid. Called from a
// background renewal inside the TUI that means a prompt painted behind the alt screen while the
// tool appears to hang. SilentToken must report that a login is needed and return immediately.
//
// With no token cache there is nothing to validate and nothing to refresh, so this also proves
// the silent path makes no network call before giving up.
func TestSilentTokenNeverStartsDeviceAuth(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	sess := SSOSessionConfig{
		Name:     "crlab",
		StartURL: "https://example.awsapps.com/start",
		Region:   "us-east-1",
	}

	token, err := SilentToken(context.Background(), sess)

	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
	if !errors.Is(err, ErrLoginRequired) {
		t.Errorf("err = %v, want ErrLoginRequired — anything else means it tried to log in", err)
	}
}

// A record with no refresh material cannot be renewed silently, whatever else it carries.
func TestCanRefreshRequiresEveryPart(t *testing.T) {
	full := tokenRecord{
		RefreshToken:          "rt",
		ClientID:              "cid",
		ClientSecret:          "secret",
		RegistrationExpiresAt: stamp(time.Hour),
	}
	if !full.canRefresh() {
		t.Fatal("a complete record cannot refresh")
	}

	tests := map[string]func(r *tokenRecord){
		"no refresh token":     func(r *tokenRecord) { r.RefreshToken = "" },
		"no client id":         func(r *tokenRecord) { r.ClientID = "" },
		"no client secret":     func(r *tokenRecord) { r.ClientSecret = "" },
		"dead registration":    func(r *tokenRecord) { r.RegistrationExpiresAt = stamp(-time.Hour) },
		"undated registration": func(r *tokenRecord) { r.RegistrationExpiresAt = "" },
	}
	for name, break_ := range tests {
		t.Run(name, func(t *testing.T) {
			rec := full
			break_(&rec)
			if rec.canRefresh() {
				t.Errorf("%s still reported as refreshable", name)
			}
		})
	}

	// A nil record is the ordinary "nothing cached" case and must answer, not panic.
	var nilRec *tokenRecord
	if nilRec.canRefresh() {
		t.Error("nil record reported as refreshable")
	}
}
