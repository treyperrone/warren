package aws

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/treyperrone/warren/internal/testenv"
)

// stamp renders an RFC3339 timestamp offset from now, the way the SSO cache stores expiries.
func stamp(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

// fakeHome points HOME at a temp dir and returns the SSO cache path inside it, so cache
// tests never touch the real ~/.aws.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	cache := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	return cache
}

func writeCacheFile(t *testing.T, dir, name string, rec tokenRecord) {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLive(t *testing.T) {
	tests := []struct {
		name string
		ts   string
		want bool
	}{
		{"future", stamp(time.Hour), true},
		{"past", stamp(-time.Hour), false},
		{"inside skew window", stamp(30 * time.Second), false},
		{"unparseable", "not-a-timestamp", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		if got := live(tc.ts); got != tc.want {
			t.Errorf("%s: live(%q) = %v, want %v", tc.name, tc.ts, got, tc.want)
		}
	}
}

func TestParseConfigScopes(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := `#[profile commented-out]
#sso_start_url = https://ignore.example.com/start

[sso-session with-scopes]
sso_start_url = https://one.example.com/start
sso_region = us-east-1
sso_registration_scopes = sso:account:access, codewhisperer:completions

[sso-session no-scopes]
sso_start_url = https://two.example.com/start
sso_region = us-west-2

[profile real]
region = us-east-1
`
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}

	sessions, profiles, err := ParseConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(sessions), sessions)
	}

	// Commented-out blocks must not become profiles.
	if len(profiles) != 1 || profiles[0].Name != "real" {
		t.Errorf("got profiles %+v, want just [real]", profiles)
	}

	want := []string{"sso:account:access", "codewhisperer:completions"}
	got := sessions[0].scopes()
	if len(got) != len(want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("scopes[%d] = %q, want %q (whitespace should be trimmed)", i, got[i], want[i])
		}
	}

	// A session with no scopes configured must still register with the default, or AWS
	// issues no refresh token.
	if got := sessions[1].scopes(); len(got) != 1 || got[0] != "sso:account:access" {
		t.Errorf("default scopes = %v, want [sso:account:access]", got)
	}
}

func TestCanRefresh(t *testing.T) {
	full := tokenRecord{
		RefreshToken:          "rt",
		ClientID:              "cid",
		ClientSecret:          "secret",
		RegistrationExpiresAt: stamp(24 * time.Hour),
	}
	if !full.canRefresh() {
		t.Error("complete record should be refreshable")
	}

	var nilRec *tokenRecord
	if nilRec.canRefresh() {
		t.Error("nil record must not be refreshable")
	}

	// Each missing piece independently kills refresh.
	noRT := full
	noRT.RefreshToken = ""
	noID := full
	noID.ClientID = ""
	noSecret := full
	noSecret.ClientSecret = ""
	deadReg := full
	deadReg.RegistrationExpiresAt = stamp(-time.Minute)

	for name, rec := range map[string]tokenRecord{
		"no refresh token":     noRT,
		"no client id":         noID,
		"no client secret":     noSecret,
		"expired registration": deadReg,
		"absent registration":  {RefreshToken: "rt", ClientID: "cid", ClientSecret: "secret"},
	} {
		if rec.canRefresh() {
			t.Errorf("%s: should not be refreshable", name)
		}
	}
}

func TestCachedRecordPrefersLiveToken(t *testing.T) {
	cache := fakeHome(t)
	const url = "https://one.example.com/start"

	writeCacheFile(t, cache, "expired.json", tokenRecord{
		StartURL:              url,
		AccessToken:           "stale",
		ExpiresAt:             stamp(-time.Hour),
		RefreshToken:          "rt",
		ClientID:              "cid",
		ClientSecret:          "secret",
		RegistrationExpiresAt: stamp(24 * time.Hour),
	})
	writeCacheFile(t, cache, "fresh.json", tokenRecord{
		StartURL:    url,
		AccessToken: "good",
		ExpiresAt:   stamp(time.Hour),
	})

	rec := cachedRecord(url)
	if rec == nil {
		t.Fatal("got nil, want the live record")
	}
	if rec.AccessToken != "good" {
		t.Errorf("AccessToken = %q, want %q — a live token must win over a stale one", rec.AccessToken, "good")
	}
}

func TestCachedRecordFallsBackToRefreshable(t *testing.T) {
	cache := fakeHome(t)
	const url = "https://one.example.com/start"

	// Expired access token, but the refresh material is intact — the whole point of the
	// change is that this is still worth returning.
	writeCacheFile(t, cache, "expired.json", tokenRecord{
		StartURL:              url,
		AccessToken:           "stale",
		ExpiresAt:             stamp(-time.Hour),
		RefreshToken:          "rt",
		ClientID:              "cid",
		ClientSecret:          "secret",
		RegistrationExpiresAt: stamp(24 * time.Hour),
	})

	rec := cachedRecord(url)
	if rec == nil {
		t.Fatal("got nil; an expired-but-refreshable record must still be returned")
	}
	if !rec.canRefresh() {
		t.Error("returned record should be refreshable")
	}
}

func TestCachedRecordIgnoresOtherStartURLs(t *testing.T) {
	cache := fakeHome(t)

	writeCacheFile(t, cache, "other.json", tokenRecord{
		StartURL:    "https://someone-else.example.com/start",
		AccessToken: "not-ours",
		ExpiresAt:   stamp(time.Hour),
	})
	// Also drop in a non-JSON file and a malformed one; neither should panic or match.
	if err := os.WriteFile(filepath.Join(cache, "botched.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "README.txt"), []byte("ignore me"), 0600); err != nil {
		t.Fatal(err)
	}

	if rec := cachedRecord("https://one.example.com/start"); rec != nil {
		t.Errorf("got %+v, want nil for an unrelated start URL", rec)
	}
}

func TestCachedRecordNoCacheDir(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home) // no .aws/sso/cache at all
	if rec := cachedRecord("https://one.example.com/start"); rec != nil {
		t.Errorf("got %+v, want nil when the cache dir is absent", rec)
	}
}

func TestWriteRecordRoundTrip(t *testing.T) {
	fakeHome(t)
	const url = "https://one.example.com/start"

	in := &tokenRecord{
		StartURL:              url,
		Region:                "us-east-1",
		AccessToken:           "at",
		ExpiresAt:             stamp(time.Hour),
		RefreshToken:          "rt",
		ClientID:              "cid",
		ClientSecret:          "secret",
		RegistrationExpiresAt: stamp(24 * time.Hour),
	}
	writeRecord(in)

	out := cachedRecord(url)
	if out == nil {
		t.Fatal("wrote a record but read back nil")
	}
	// The refresh material surviving the round trip is what makes silent renewal work.
	if out.RefreshToken != "rt" || out.ClientID != "cid" || out.ClientSecret != "secret" {
		t.Errorf("refresh material lost: %+v", out)
	}
	if out.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", out.Region)
	}
	if !out.canRefresh() {
		t.Error("round-tripped record should be refreshable")
	}
}

func TestWriteRecordDoesNotClobberForeignCacheFiles(t *testing.T) {
	cache := fakeHome(t)
	const url = "https://one.example.com/start"

	// Stand in for a file the AWS CLI owns, including a field we do not model.
	cliFile := filepath.Join(cache, "0123456789abcdef.json")
	cliRaw := `{"startUrl":"` + url + `","accessToken":"cli-token","expiresAt":"` +
		stamp(time.Hour) + `","someFieldWeDoNotModel":"keep me"}`
	if err := os.WriteFile(cliFile, []byte(cliRaw), 0600); err != nil {
		t.Fatal(err)
	}

	writeRecord(&tokenRecord{StartURL: url, AccessToken: "ours", ExpiresAt: stamp(time.Hour)})

	after, err := os.ReadFile(cliFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != cliRaw {
		t.Errorf("the CLI's cache file was modified:\n before: %s\n after:  %s", cliRaw, after)
	}
}
