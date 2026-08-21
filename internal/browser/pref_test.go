package browser

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// isolateHome points every home-derived path at a temp dir. Both variables are set because
// homedir resolves through os.UserHomeDir, which reads USERPROFILE on Windows and HOME
// everywhere else — setting only HOME would leave the Windows runner writing to the real
// profile, which is the exact blindness internal/homedir documents.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

func TestLoadPrefMissingFileIsSystemDefault(t *testing.T) {
	isolateHome(t)
	p := LoadPref()
	if p != (Pref{}) {
		t.Fatalf("LoadPref with no file = %+v, want zero", p)
	}
	// The unset state IS a behaviour: ask at each sign-in. Saying so on the settings row is
	// how the default explains itself.
	if p.Describe() != "ask at each sign-in" {
		t.Errorf("zero pref describes as %q", p.Describe())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	isolateHome(t)
	want := Pref{Mode: ModeBrowser, Browser: "Google Chrome", ProfileDir: "Profile 1", ProfileName: "Work"}
	if err := SavePref(want); err != nil {
		t.Fatal(err)
	}
	if got := LoadPref(); got != want {
		t.Fatalf("round trip: got %+v, want %+v", got, want)
	}
}

// The config file is warren's only config file, not this feature's: a save must not eat keys
// written by a newer build or another feature.
func TestSavePrefPreservesUnknownKeys(t *testing.T) {
	isolateHome(t)
	seed := `{"future_feature": {"setting": true}, "sso_browser": {"mode": "none"}}`
	if err := os.WriteFile(PrefPath(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SavePref(Pref{Mode: ModeSystem}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(PrefPath())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("saved file is not JSON: %v\n%s", err, data)
	}
	if _, ok := doc["future_feature"]; !ok {
		t.Errorf("future_feature was dropped by a browser-pref save:\n%s", data)
	}
	if got := LoadPref(); got.Mode != ModeSystem {
		t.Errorf("mode = %q, want %q", got.Mode, ModeSystem)
	}
}

// A corrupt config must fail the save loudly rather than silently replacing whatever the
// file held; LoadPref meanwhile degrades to the default because sign-in must never be
// blocked by a config problem.
func TestCorruptConfigFile(t *testing.T) {
	isolateHome(t)
	if err := os.WriteFile(PrefPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := LoadPref(); p != (Pref{}) {
		t.Errorf("LoadPref on corrupt file = %+v, want zero", p)
	}
	if err := SavePref(Pref{Mode: ModeNone}); err == nil {
		t.Error("SavePref silently overwrote a corrupt config file")
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		p    Pref
		want string
	}{
		{Pref{}, "ask at each sign-in"},
		{Pref{Mode: ModeAsk}, "ask at each sign-in"},
		{Pref{Mode: ModeSystem}, "system default browser"},
		{Pref{Mode: ModeNone}, "never — show the device code to open anywhere"},
		{Pref{Mode: ModeBrowser, Browser: "Safari"}, "Safari"},
		{Pref{Mode: ModeBrowser, Browser: "Google Chrome", ProfileName: "Work"}, "Google Chrome — Work"},
	}
	for _, c := range cases {
		if got := c.p.Describe(); got != c.want {
			t.Errorf("Describe(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// The suppression paths must return before any Detect or exec: these are exactly the
// environments (SSH boxes, CI) where launching a browser is wrong, so the test doubles as
// proof no browser is touched.
func TestOpenForLoginSuppressionNotes(t *testing.T) {
	t.Setenv("WARREN_NO_BROWSER", "1")
	if note := OpenForLogin(Pref{}, "https://example.test"); !strings.Contains(note, "WARREN_NO_BROWSER") {
		t.Errorf("note = %q, want it to name the variable that suppressed the browser", note)
	}
	// "0" documents itself as off — the common convention, and cheaper honoured than explained.
	t.Setenv("WARREN_NO_BROWSER", "0")
	t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 22")
	if note := OpenForLogin(Pref{Mode: ModeNone}, "https://example.test"); !strings.Contains(note, "browser opening is off") {
		t.Errorf("note = %q, want the mode-none explanation", note)
	}
	t.Setenv("WARREN_NO_BROWSER", "")
	if note := OpenForLogin(Pref{}, "https://example.test"); !strings.Contains(note, "open the URL") {
		t.Errorf("note = %q, want the headless/SSH fallback wording", note)
	}
}

// No resolvable home must not degrade to the relative path ".warren_config.json" — that
// reads and writes wherever warren was started, the exact bug internal/homedir documents.
func TestPrefWithNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if p := PrefPath(); p != "" {
		t.Fatalf("PrefPath with no home = %q, want empty", p)
	}
	if p := LoadPref(); p != (Pref{}) {
		t.Errorf("LoadPref with no home = %+v, want zero", p)
	}
	if err := SavePref(Pref{Mode: ModeNone}); err == nil {
		t.Error("SavePref with no home succeeded — it wrote a relative path somewhere")
	}
}

// The save must be atomic: always the old document or the new one on disk, never a torn
// half-write that poisons every later read and save. The rename also cleans up the temp.
func TestSavePrefLeavesNoTempFile(t *testing.T) {
	isolateHome(t)
	if err := SavePref(Pref{Mode: ModeSystem}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(PrefPath() + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind after save: %v", err)
	}
	if got := LoadPref(); got.Mode != ModeSystem {
		t.Errorf("mode = %q after atomic save", got.Mode)
	}
}

// ShouldAsk gates the inline at-sign-in picker: ask by default and under ModeAsk, never once
// a real default is saved, and never where no browser could open anyway.
func TestShouldAsk(t *testing.T) {
	// A display and no SSH markers, so the environment itself never suppresses the ask on
	// the Linux CI runner.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("WARREN_NO_BROWSER", "")

	if !ShouldAsk(Pref{}) {
		t.Error("unset pref must ask — that IS the out-of-the-box behaviour")
	}
	if !ShouldAsk(Pref{Mode: ModeAsk}) {
		t.Error("ModeAsk must ask")
	}
	for _, mode := range []string{ModeSystem, ModeNone, ModeBrowser} {
		if ShouldAsk(Pref{Mode: mode}) {
			t.Errorf("mode %q must not ask — a saved answer is the whole point of saving", mode)
		}
	}

	t.Setenv("WARREN_NO_BROWSER", "1")
	if ShouldAsk(Pref{}) {
		t.Error("WARREN_NO_BROWSER must suppress the ask — nothing it could pick would open")
	}
	t.Setenv("WARREN_NO_BROWSER", "")
	t.Setenv("SSH_CONNECTION", "10.0.0.1 1 10.0.0.2 22")
	if ShouldAsk(Pref{}) {
		t.Error("an SSH session must suppress the ask")
	}
}

// Per-session overrides: the session's answer wins, everything else falls back to the
// global preference, and deleting the override restores the fallback.
func TestSessionPrefResolution(t *testing.T) {
	isolateHome(t)
	const work = "https://work.awsapps.com/start"
	const personal = "https://me.awsapps.com/start"

	if err := SavePref(Pref{Mode: ModeSystem}); err != nil {
		t.Fatal(err)
	}
	if err := SavePrefFor(work, Pref{Mode: ModeBrowser, Browser: "Google Chrome", ProfileDir: "Profile 1", ProfileName: "Work"}); err != nil {
		t.Fatal(err)
	}

	if p, scoped := ResolvePrefFor(work); !scoped || p.Browser != "Google Chrome" {
		t.Errorf("work session resolved to %+v scoped=%v, want its own override", p, scoped)
	}
	if p, scoped := ResolvePrefFor(personal); scoped || p.Mode != ModeSystem {
		t.Errorf("personal session resolved to %+v scoped=%v, want the global fallback", p, scoped)
	}
	// The global answer must be untouched by the scoped save.
	if g := LoadPref(); g.Mode != ModeSystem {
		t.Errorf("global pref = %+v after a scoped save", g)
	}

	if err := DeletePrefFor(work); err != nil {
		t.Fatal(err)
	}
	if p, scoped := ResolvePrefFor(work); scoped || p.Mode != ModeSystem {
		t.Errorf("after delete, work resolved to %+v scoped=%v, want the global fallback", p, scoped)
	}
	// Deleting the last override removes the whole key rather than leaving "{}" residue.
	if m := SessionPrefs(); len(m) != 0 {
		t.Errorf("overrides after delete = %v, want none", m)
	}
	// Deleting what does not exist is a no-op, not an error.
	if err := DeletePrefFor("https://never.example/start"); err != nil {
		t.Fatal(err)
	}
}

// Scoped saves go through the same unknown-key-preserving writer as everything else.
func TestSessionPrefSavePreservesUnknownKeys(t *testing.T) {
	isolateHome(t)
	seed := `{"future_feature": {"setting": true}}`
	if err := os.WriteFile(PrefPath(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SavePrefFor("https://work.awsapps.com/start", Pref{Mode: ModeNone}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(PrefPath())
	if !strings.Contains(string(data), "future_feature") {
		t.Errorf("scoped save dropped an unknown key:\n%s", data)
	}
}
