package aws

import (
	"errors"
	ssooidctypes "github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treyperrone/warren/internal/testenv"
)

// The bug this guards: ConfigPath read os.Getenv("HOME") directly, and Windows normally leaves
// HOME unset in favour of USERPROFILE. That produced the *relative* path ".aws/config", so on
// Windows warren read and wrote config in whatever directory it was run from — never finding a
// real SSO session, and leaving a stray file behind on `warren setup`.
//
// Every test injected HOME, so the suite passed on the Windows runner while the shipped Windows
// binary could not find its own configuration. Hence an explicit absolute-path assertion.
func TestConfigPathIsAbsolute(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	got := ConfigPath()
	if !filepath.IsAbs(got) {
		t.Errorf("ConfigPath() = %q, which is relative — it would resolve against the working directory", got)
	}
	if !strings.HasPrefix(got, home) {
		t.Errorf("ConfigPath() = %q, want it under the home directory %q", got, home)
	}
	if filepath.Base(got) != "config" || filepath.Base(filepath.Dir(got)) != ".aws" {
		t.Errorf("ConfigPath() = %q, want it to end in .aws/config", got)
	}
}

func TestSSOCachePathIsAbsolute(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	got := ssoCacheDir()
	if !filepath.IsAbs(got) {
		t.Errorf("ssoCacheDir() = %q, which is relative", got)
	}
	// Must be exactly where the aws CLI looks, or the token cache is not shared and silent
	// renewal stops working even though both tools are signed in.
	if want := filepath.Join(home, ".aws", "sso", "cache"); got != want {
		t.Errorf("ssoCacheDir() = %q, want %q", got, want)
	}
}

// AWS_CONFIG_FILE is the documented override and the aws CLI honours it. Ignoring it meant warren
// read a different config from the CLI while sharing the same token cache.
func TestConfigPathHonoursAwsConfigFile(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	custom := filepath.Join(t.TempDir(), "elsewhere.cfg")
	t.Setenv("AWS_CONFIG_FILE", custom)

	if got := ConfigPath(); got != custom {
		t.Errorf("ConfigPath() = %q, want %q from AWS_CONFIG_FILE", got, custom)
	}
}

// The bug this guards: warren's own sentinel — set on a `warren shell`/`exec` child by
// internal/awsexec so *that child's* AWS calls cannot see an ambient [default] profile — is
// still just an AWS_CONFIG_FILE value, and ConfigPath honoured AWS_CONFIG_FILE unconditionally.
// Run `warren` again from inside the shell it just handed you (its ordinary, expected use), and
// it would inherit the sentinel and find no sso-sessions, offering first-run setup on a machine
// that is already configured. Confirmed live: exactly this happened on the first real machine
// this shipped to.
func TestConfigPathIgnoresItsOwnNeutralizationSentinel(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	sentinel, _ := NeutralizedProfilePaths()
	t.Setenv("AWS_CONFIG_FILE", sentinel)

	want := filepath.Join(home, ".aws", "config")
	if got := ConfigPath(); got != want {
		t.Errorf("ConfigPath() = %q with its own sentinel set, want the real default %q", got, want)
	}
}

// A value the user set for their own reasons — pointing warren and the aws CLI at a shared
// alternate config — must still be honoured. Only the exact sentinel value is special-cased.
func TestConfigPathStillHonoursARealOverride(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	custom := filepath.Join(t.TempDir(), "elsewhere.cfg")
	t.Setenv("AWS_CONFIG_FILE", custom)

	if got := ConfigPath(); got != custom {
		t.Errorf("ConfigPath() = %q, want the deliberate override %q — the sentinel check must not swallow a real one", got, custom)
	}
}

// An empty AWS_CONFIG_FILE must not be mistaken for a request to use "" as the path, which would
// make every read fail and every write land nowhere.
func TestEmptyAwsConfigFileFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("AWS_CONFIG_FILE", "")

	if got := ConfigPath(); got != filepath.Join(home, ".aws", "config") {
		t.Errorf("ConfigPath() = %q, want the default under %q", got, home)
	}
}

// The close-error bug CodeQL found: the file was closed with `defer f.Close()`, discarding the
// error. For a writable file Close is where buffered data reaches disk, so a failure there means
// the block never landed while the function reported success.
//
// A genuine Close failure is hard to force portably, so this asserts the observable contract
// instead: when AddSSOSession returns nil, the block is actually readable from the file.
func TestAddSSOSessionOnlyReportsSuccessWhenTheBlockIsOnDisk(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	cfg := SSOSessionConfig{
		Name:     "corp",
		StartURL: "https://corp.awsapps.com/start",
		Region:   "us-east-1",
	}
	if err := AddSSOSession(cfg); err != nil {
		t.Fatalf("AddSSOSession: %v", err)
	}

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatalf("reported success but the config file is unreadable: %v", err)
	}
	for _, want := range []string{"[sso-session corp]", cfg.StartURL, cfg.Region} {
		if !strings.Contains(string(data), want) {
			t.Errorf("reported success but %q is not in the file:\n%s", want, data)
		}
	}

	// And it must be parseable back, not merely present as text.
	sessions, _, err := ParseConfig()
	if err != nil {
		t.Fatalf("ParseConfig after append: %v", err)
	}
	var found bool
	for _, s := range sessions {
		if s.Name == "corp" {
			found = true
		}
	}
	if !found {
		t.Error("the appended block does not parse back as an sso-session")
	}
}

// Writing into a directory that cannot be created must be an error, not a silent success.
func TestAddSSOSessionReportsAnUnwritableConfig(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Point the config at a path underneath a regular file, so creating its parent fails.
	// SetHome clears AWS_CONFIG_FILE, so it has to be set after.
	testenv.SetHome(t, dir)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(blocked, "config"))

	err := AddSSOSession(SSOSessionConfig{
		Name: "corp", StartURL: "https://corp.awsapps.com/start", Region: "us-east-1",
	})
	if err == nil {
		t.Error("reported success writing under a regular file")
	}
}

// The credential_process profile block: strictly appended (existing content byte-identical),
// collisions and unknown sessions rejected, and every value restricted to the allowlist —
// the line lands in a file every AWS tool parses AND in a command line the SDK executes.
func TestAddCredentialProcessProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", "")
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "# hand-written comment\n[sso-session corp]\nsso_start_url = https://corp.awsapps.com/start\nsso_region = us-east-1\n"
	cfg := filepath.Join(home, ".aws", "config")
	if err := os.WriteFile(cfg, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AddCredentialProcessProfile("corp-lab-admin", "corp", "123456789012", "AdminRole"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cfg)
	if !strings.HasPrefix(string(data), existing) {
		t.Error("append modified existing content")
	}
	want := "credential_process = warren creds --session corp --account 123456789012 --role AdminRole"
	if !strings.Contains(string(data), "[profile corp-lab-admin]") || !strings.Contains(string(data), want) {
		t.Errorf("appended block wrong:\n%s", data)
	}
	// The parser must see what the writer wrote.
	_, profiles, err := ParseConfig()
	if err != nil || len(profiles) != 1 || profiles[0].Name != "corp-lab-admin" {
		t.Fatalf("round trip: %v, %v", profiles, err)
	}

	if err := AddCredentialProcessProfile("corp-lab-admin", "corp", "1", "r"); err == nil {
		t.Error("duplicate profile accepted")
	}
	if err := AddCredentialProcessProfile("other", "nosuch", "1", "r"); err == nil {
		t.Error("unknown sso-session accepted")
	}
	if err := AddCredentialProcessProfile("bad name", "corp", "1", "r"); err == nil {
		t.Error("space in profile name accepted")
	}
	if err := AddCredentialProcessProfile("ok", "corp", "1", "Role Name; rm -rf /"); err == nil {
		t.Error("shell metacharacters in role accepted")
	}
}

// S3Entry names are the last path segment — what a directory-style listing shows — with
// prefixes keeping their trailing slash so they read as folders.
func TestS3EntryName(t *testing.T) {
	cases := []struct {
		e    S3Entry
		want string
	}{
		{S3Entry{Key: "tools/linux/", IsPrefix: true}, "linux/"},
		{S3Entry{Key: "tools/mimi.zip"}, "mimi.zip"},
		{S3Entry{Key: "top.txt"}, "top.txt"},
		{S3Entry{Key: "deep/", IsPrefix: true}, "deep/"},
	}
	for _, c := range cases {
		if got := c.e.Name(); got != c.want {
			t.Errorf("Name(%q) = %q, want %q", c.e.Key, got, c.want)
		}
	}
}

// A warren comment at the boundary between a REMOVED block and a KEPT one belongs to the
// kept block below it and must survive the removal.
func TestRemoveProfileKeepsNextBlocksComment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", "")
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "[profile doomed]\nregion = us-east-1\n\n# added by warren\n[profile survivor]\nregion = us-west-2\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProfileBlock("doomed"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".aws", "config"))
	if !strings.Contains(string(after), "# added by warren\n[profile survivor]") {
		t.Errorf("the surviving block lost its ownership comment:\n%s", after)
	}
	if strings.Contains(string(after), "doomed") {
		t.Errorf("doomed block survived:\n%s", after)
	}
}

// Empty values and "default" must never reach a credential_process line or a header.
func TestAddCredentialProcessProfileRejectsEmptyAndDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", "")
	for name, args := range map[string][4]string{
		"empty session": {"p", "", "1", "r"},
		"empty account": {"p", "s", "", "r"},
		"empty role":    {"p", "s", "1", ""},
		"default name":  {"default", "s", "1", "r"},
	} {
		if err := AddCredentialProcessProfile(args[0], args[1], args[2], args[3]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Only an auth rejection means the org session ended; a network blip must not teach warren
// a bogus session duration.
func TestIsSessionEnded(t *testing.T) {
	if isSessionEnded(errors.New("dial tcp: lookup oidc.us-east-1.amazonaws.com: no such host")) {
		t.Error("DNS failure counted as a session ending")
	}
	if isSessionEnded(errors.New("operation error SSO OIDC: CreateToken, https response error StatusCode: 429, ThrottlingException")) {
		t.Error("throttling counted as a session ending")
	}
	if !isSessionEnded(errors.New("operation error SSO OIDC: CreateToken, InvalidGrantException: invalid_grant")) {
		t.Error("InvalidGrantException not recognized")
	}
	if !isSessionEnded(&ssooidctypes.InvalidGrantException{}) {
		t.Error("typed InvalidGrantException not recognized")
	}
}

// Keys are attacker-adjacent: whatever their last segment holds, the local name must stay
// inside the download directory on every OS.
func TestSafeLocalName(t *testing.T) {
	cases := map[string]string{
		"docs/report.pdf":             "report.pdf",
		`docs/..\..\Startup\evil.bat`: ".._.._Startup_evil.bat",
		"a/..":                        "object",
		"weird/":                      "weird",
		"":                            "object",
		`back\slash.txt`:              "back_slash.txt",
	}
	for key, want := range cases {
		if got := safeLocalName(key); got != want {
			t.Errorf("safeLocalName(%q) = %q, want %q", key, got, want)
		}
	}
}
