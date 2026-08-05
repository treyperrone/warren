package aws

// End-to-end SSO checks against a REAL Identity Center instance. Skipped unless
// SSM_TOOL_E2E=1, because they need network, a browser (on whatever machine can reach one),
// and a human to approve the device code.
//
//	SSM_TOOL_E2E=1 go test ./internal/aws/ -run TestE2E -v -count=1 -timeout 15m
//
// Optional: SSM_TOOL_SSO=<session-name> to pick a specific [sso-session]; defaults to the
// first one in ~/.aws/config.
//
// These use your real HOME. They only ever write ssm-tool-*.json in the SSO cache, never a
// file the AWS CLI owns.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func redact(s string) string {
	if s == "" {
		return "(absent)"
	}
	if len(s) <= 8 {
		return "(present, short)"
	}
	return fmt.Sprintf("(present, %d chars, ...%s)", len(s), s[len(s)-4:])
}

// ourCachePath is where writeRecord puts things, recomputed so the test can age a token.
func ourCachePath(startURL string) string {
	return filepath.Join(ssoCacheDir(), fmt.Sprintf("ssm-tool-%x.json", hashString(startURL)))
}

func TestE2E(t *testing.T) {
	if os.Getenv("SSM_TOOL_E2E") == "" {
		t.Skip("set SSM_TOOL_E2E=1 to run (needs a real Identity Center instance + browser)")
	}

	sessions, profiles, err := ParseConfig()
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	t.Logf("parsed %d sso-session block(s), %d profile(s)", len(sessions), len(profiles))
	if len(sessions) == 0 {
		t.Fatal("no [sso-session] blocks in ~/.aws/config")
	}

	sess := sessions[0]
	if want := os.Getenv("SSM_TOOL_SSO"); want != "" {
		found := false
		for _, s := range sessions {
			if s.Name == want {
				sess, found = s, true
			}
		}
		if !found {
			t.Fatalf("no sso-session named %q", want)
		}
	}
	t.Logf("session   : %s", sess.Name)
	t.Logf("start url : %s", sess.StartURL)
	t.Logf("region    : %s", sess.Region)
	t.Logf("scopes    : %v  (parsed from sso_registration_scopes)", sess.scopes())

	ctx := context.Background()

	t.Run("login", func(t *testing.T) {
		if pre := cachedRecord(sess.StartURL); pre != nil {
			t.Logf("a cached record already exists — this may not exercise the browser flow")
		} else {
			t.Log("no cached record: expect a device-code prompt on stderr below")
			t.Log("open the URL on any machine with a browser and enter the code")
		}

		start := time.Now()
		token, err := LiveToken(ctx, sess)
		if err != nil {
			t.Fatalf("LiveToken: %v", err)
		}
		t.Logf("got a token in %s: %s", time.Since(start).Round(time.Second), redact(token))

		rec := cachedRecord(sess.StartURL)
		if rec == nil {
			t.Fatal("token acquired but nothing readable in the cache")
		}
		t.Log("cached record:")
		t.Logf("  accessToken           %s", redact(rec.AccessToken))
		t.Logf("  refreshToken          %s", redact(rec.RefreshToken))
		t.Logf("  clientId              %s", redact(rec.ClientID))
		t.Logf("  clientSecret          %s", redact(rec.ClientSecret))
		t.Logf("  expiresAt             %s", rec.ExpiresAt)
		t.Logf("  registrationExpiresAt %s", rec.RegistrationExpiresAt)
		t.Logf("  region                %s", rec.Region)

		// The entire point of the change. If this is empty, scoped registration didn't
		// take and we're still on re-prompt-every-time behavior.
		if rec.RefreshToken == "" {
			t.Error("no refreshToken — silent renewal will NOT work")
			t.Error("if stderr showed 'scoped registration failed', that instance rejected the scopes")
		}
		if !rec.canRefresh() {
			t.Error("record is not refreshable (missing refresh token, client registration, or registration expired)")
		}

		accounts, err := ListAccounts(ctx, sess, token)
		if err != nil {
			t.Fatalf("ListAccounts: %v", err)
		}
		t.Logf("token works: %d account(s) visible", len(accounts))
		for i, a := range accounts {
			if i >= 5 {
				t.Logf("  ... and %d more", len(accounts)-5)
				break
			}
			t.Logf("  %s  %s", a.ID, a.Name)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		path := ourCachePath(sess.StartURL)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("no ssm-tool cache file yet (%v) — run the login subtest first", err)
		}
		var rec tokenRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		if !rec.canRefresh() {
			t.Skip("cached record has no refresh material; nothing to test")
		}

		oldToken, oldExpiry := rec.AccessToken, rec.ExpiresAt

		// Age the access token so LiveToken must renew. This is the deterministic version
		// of waiting ~8h for a natural expiry. The refresh token is left untouched.
		rec.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		aged, _ := json.Marshal(&rec)
		if err := os.WriteFile(path, aged, 0600); err != nil {
			t.Fatalf("aging the cached token: %v", err)
		}
		t.Logf("forced expiresAt to %s (was %s)", rec.ExpiresAt, oldExpiry)

		start := time.Now()
		token, err := LiveToken(ctx, sess)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("LiveToken after forced expiry: %v", err)
		}

		// Device auth cannot finish this fast — it sleeps at least one poll interval and
		// needs a human. A quick return means the refresh_token grant did the work.
		if elapsed > 15*time.Second {
			t.Errorf("took %s — too slow to have been a silent refresh; did it fall back to device auth?", elapsed.Round(time.Second))
		} else {
			t.Logf("renewed in %s with no browser", elapsed.Round(time.Millisecond))
		}

		after := cachedRecord(sess.StartURL)
		if after == nil {
			t.Fatal("no cached record after refresh")
		}
		if !live(after.ExpiresAt) {
			t.Errorf("expiresAt still not in the future: %s", after.ExpiresAt)
		}
		if after.RefreshToken == "" {
			t.Error("refreshToken lost during refresh — the next renewal would need a browser")
		}
		if token == oldToken {
			t.Log("note: same access token returned (AWS may reuse an unexpired one)")
		} else {
			t.Log("access token was replaced")
		}
		t.Logf("new expiresAt %s", after.ExpiresAt)

		if _, err := ListAccounts(ctx, sess, token); err != nil {
			t.Errorf("refreshed token rejected by the API: %v", err)
		} else {
			t.Log("refreshed token works against the SSO API")
		}
	})
}
