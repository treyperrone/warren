package aws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, ".aws", "config")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return home, path
}

// A machine that has never run `aws configure` has no config file. That is the ordinary
// first-run state, not a failure — the TUI offers to create one, which it cannot do if
// ParseConfig aborts first.
func TestParseConfigMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sessions, profiles, err := ParseConfig()
	if err != nil {
		t.Fatalf("missing config returned error: %v", err)
	}
	if len(sessions) != 0 || len(profiles) != 0 {
		t.Errorf("got %d sessions and %d profiles, want none", len(sessions), len(profiles))
	}
}

// The whole reason AddSSOSession appends instead of rewriting: ParseConfig discards
// comments, key order, and unmodelled keys, so a round-trip would quietly delete config
// belonging to the aws CLI, Terraform, and every SDK sharing the file.
func TestAddSSOSessionPreservesForeignConfig(t *testing.T) {
	original := `# hand-written, do not lose me
[profile prod]
region = eu-west-1
role_arn = arn:aws:iam::123456789012:role/Admin
source_profile = default
credential_process = /usr/local/bin/some-helper --json

[services my-services]
s3 =
  endpoint_url = http://localhost:4566
`
	_, path := writeConfig(t, original)

	err := AddSSOSession(SSOSessionConfig{
		Name:     "new-sso",
		StartURL: "https://example.awsapps.com/start",
		Region:   "us-east-1",
	})
	if err != nil {
		t.Fatalf("AddSSOSession: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	if !strings.HasPrefix(got, original) {
		t.Fatalf("original content was modified rather than appended to:\n%s", got)
	}
	for _, want := range []string{
		"[sso-session new-sso]",
		"sso_start_url = https://example.awsapps.com/start",
		"sso_region = us-east-1",
		// Without scopes AWS never issues a refresh token, so the default must be written
		// explicitly rather than left to chance.
		"sso_registration_scopes = sso:account:access",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("appended block missing %q:\n%s", want, got)
		}
	}

	// And the result must parse back cleanly, including the pre-existing profile.
	sessions, profiles, err := ParseConfig()
	if err != nil {
		t.Fatalf("re-parsing after append: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "new-sso" {
		t.Errorf("got sessions %+v, want one named new-sso", sessions)
	}
	if len(profiles) != 1 || profiles[0].Name != "prod" {
		t.Errorf("got profiles %+v, want the pre-existing prod", profiles)
	}
}

func TestAddSSOSessionBacksUpExistingConfig(t *testing.T) {
	original := "[profile prod]\nregion = eu-west-1\n"
	_, path := writeConfig(t, original)

	if err := AddSSOSession(SSOSessionConfig{
		Name: "s", StartURL: "https://e.awsapps.com/start", Region: "us-east-1",
	}); err != nil {
		t.Fatal(err)
	}

	backup, err := os.ReadFile(path + ".warren.bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the pre-append content %q", backup, original)
	}
}

func TestAddSSOSessionCreatesConfigWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := AddSSOSession(SSOSessionConfig{
		Name: "fresh", StartURL: "https://e.awsapps.com/start", Region: "us-west-2",
	}); err != nil {
		t.Fatalf("AddSSOSession on a bare home: %v", err)
	}

	sessions, _, err := ParseConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Region != "us-west-2" {
		t.Errorf("got %+v, want one session in us-west-2", sessions)
	}
	// No prior file means nothing to back up.
	if _, err := os.Stat(filepath.Join(home, ".aws", "config.warren.bak")); err == nil {
		t.Error("wrote a backup for a config that did not exist")
	}
}

// A duplicate name would produce two [sso-session x] headers; the parser would return both
// and the picker would show the same name twice.
func TestAddSSOSessionRejectsDuplicateName(t *testing.T) {
	writeConfig(t, "[sso-session taken]\nsso_start_url = https://a.awsapps.com/start\nsso_region = us-east-1\n")

	err := AddSSOSession(SSOSessionConfig{
		Name: "taken", StartURL: "https://b.awsapps.com/start", Region: "us-east-1",
	})
	if err == nil {
		t.Fatal("duplicate session name was accepted")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unhelpful duplicate error: %v", err)
	}
}

func TestValidateSSOSession(t *testing.T) {
	valid := SSOSessionConfig{
		Name: "ok", StartURL: "https://example.awsapps.com/start", Region: "us-east-1",
	}

	tests := []struct {
		name    string
		mutate  func(*SSOSessionConfig)
		wantErr bool
	}{
		{"valid", func(*SSOSessionConfig) {}, false},
		{"empty name", func(c *SSOSessionConfig) { c.Name = "" }, true},
		// The name lands inside [sso-session <name>], so these would corrupt the header.
		{"name with space", func(c *SSOSessionConfig) { c.Name = "my sso" }, true},
		{"name with bracket", func(c *SSOSessionConfig) { c.Name = "my]sso" }, true},
		{"empty start URL", func(c *SSOSessionConfig) { c.StartURL = "" }, true},
		{"http start URL", func(c *SSOSessionConfig) { c.StartURL = "http://example.com/start" }, true},
		{"bare host", func(c *SSOSessionConfig) { c.StartURL = "example.awsapps.com/start" }, true},
		{"empty region", func(c *SSOSessionConfig) { c.Region = "" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			err := ValidateSSOSession(cfg)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateSSOSession(%+v) = nil, want error", cfg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateSSOSession(%+v) = %v, want nil", cfg, err)
			}
		})
	}
}

// Validation must run before anything touches the file, or a rejected block still leaves a
// backup and a half-written config behind.
func TestAddSSOSessionInvalidWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := AddSSOSession(SSOSessionConfig{Name: "bad name", StartURL: "nope", Region: ""}); err == nil {
		t.Fatal("invalid config was accepted")
	}
	if _, err := os.Stat(filepath.Join(home, ".aws", "config")); err == nil {
		t.Error("invalid config still created ~/.aws/config")
	}
}

// The old check was ContainsAny(name, " \t[]"), which read as "reject whitespace or a bracket"
// but let every other control character through. A newline splits the [sso-session <name>]
// header across two lines and corrupts ~/.aws/config — the file shared with the aws CLI,
// Terraform and every SDK on the machine, which is why this file is only ever appended to.
func TestSSOSessionNameRejectsControlCharacters(t *testing.T) {
	valid := func(name string) error {
		return ValidateSSOSession(SSOSessionConfig{
			Name: name, StartURL: "https://x.awsapps.com/start", Region: "us-east-1",
		})
	}

	for _, name := range []string{
		"bad\nx",               // bare newline
		"bad\nsso_region=evil", // an injected key, no space to catch it
		"bad\rx", "bad\vx", "bad\fx",
		"bad name", "bad\tx", "bad[x]", "bad]x",
		"bad/x", "bad$x", "bad;x", "bad\x00x",
	} {
		if err := valid(name); err == nil {
			t.Errorf("accepted %q, which cannot go inside an [sso-session] header", name)
		}
	}

	for _, name := range []string{"prod", "my-sso", "my_sso", "corp.sso", "sso2024", "A-b_c.9"} {
		if err := valid(name); err != nil {
			t.Errorf("rejected %q, which is a reasonable session name: %v", name, err)
		}
	}
}
