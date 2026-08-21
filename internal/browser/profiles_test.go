package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// A Local State shaped like a real multi-profile Chrome: map order is JSON-object order,
// which Go randomises, so the assertion on sequence is also the assertion that sorting works.
const localStateJSON = `{
  "profile": {
    "info_cache": {
      "Profile 2": {"name": "Personal", "gaia_name": "Trey P"},
      "Default":   {"name": "Person 1", "user_name": "trey@corp.example"},
      "Profile 10": {"name": "Testing"},
      "Profile 1": {"name": "Work", "user_name": "trey@work.example"}
    },
    "last_used": "Profile 1"
  },
  "browser": {"enabled_labs_experiments": []}
}`

func TestParseChromiumLocalState(t *testing.T) {
	profs := parseChromiumLocalState([]byte(localStateJSON))

	wantDirs := []string{"Default", "Profile 1", "Profile 2", "Profile 10"}
	if len(profs) != len(wantDirs) {
		t.Fatalf("got %d profiles, want %d: %v", len(profs), len(wantDirs), profs)
	}
	for i, want := range wantDirs {
		if profs[i].Dir != want {
			// "Profile 10" after "Profile 2" is the point: lexical order would interleave
			// them and the list would not match the browser's own profile menu.
			t.Errorf("profs[%d].Dir = %q, want %q (numeric order)", i, profs[i].Dir, want)
		}
	}

	// The account is what distinguishes two profiles someone named the same, so it must
	// ride along with the display name.
	if want := "Work — trey@work.example"; profs[1].Name != want {
		t.Errorf("work profile name = %q, want %q", profs[1].Name, want)
	}
	// gaia_name backs up user_name when the latter is absent.
	if want := "Personal — Trey P"; profs[2].Name != want {
		t.Errorf("personal profile name = %q, want %q", profs[2].Name, want)
	}
	// No account info at all: the bare name, with no dangling separator.
	if want := "Testing"; profs[3].Name != want {
		t.Errorf("testing profile name = %q, want %q", profs[3].Name, want)
	}
}

func TestParseChromiumLocalStateUnusableInputs(t *testing.T) {
	for name, data := range map[string]string{
		"not json":       "wat",
		"empty":          "",
		"no info_cache":  `{"profile": {}}`,
		"empty profiles": `{"profile": {"info_cache": {}}}`,
	} {
		if got := parseChromiumLocalState([]byte(data)); len(got) != 0 {
			t.Errorf("%s: got %v, want none — the caller falls back to scanning directories", name, got)
		}
	}
}

// A nameless entry still lists under its directory name: an unnamed profile that exists is
// selectable, and hiding it would make the picker disagree with the browser.
func TestParseChromiumLocalStateNamelessProfile(t *testing.T) {
	profs := parseChromiumLocalState([]byte(`{"profile":{"info_cache":{"Profile 3":{}}}}`))
	if len(profs) != 1 || profs[0].Name != "Profile 3" {
		t.Fatalf("got %v, want the directory standing in as the name", profs)
	}
}

// The fallback for a missing Local State: profile directories on disk, named from each one's
// Preferences where readable — mirroring what the browser would rebuild the cache from.
func TestChromiumProfilesFallsBackToScanningDirs(t *testing.T) {
	dir := t.TempDir()
	mk := func(sub, prefs string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if prefs != "" {
			if err := os.WriteFile(filepath.Join(dir, sub, "Preferences"), []byte(prefs), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("Default", `{"profile":{"name":"Main"}}`)
	mk("Profile 1", "not json") // unreadable Preferences: the dir name stands in
	mk("Profile 2", "")
	mk("System Profile", "") // Chrome-internal, never something to sign in with — must be skipped? No: see below.
	mk("Crashpad", "")       // not a profile directory shape, skipped

	got := chromiumProfiles(dir)
	// "System Profile" does not match "Default"/"Profile N", so the shape filter drops it —
	// the same reason Crashpad and GrShaderCache never show up.
	want := []Profile{{"Default", "Main"}, {"Profile 1", "Profile 1"}, {"Profile 2", "Profile 2"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profile[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestChromiumProfilesMissingDataDir(t *testing.T) {
	if got := chromiumProfiles(filepath.Join(t.TempDir(), "never-created")); got != nil {
		t.Fatalf("got %v from a data dir that does not exist, want nil", got)
	}
	if got := chromiumProfiles(""); got != nil {
		t.Fatalf("got %v from an empty data dir, want nil", got)
	}
}

// Local State wins over the directory scan when both exist: it carries account names and is
// the browser's own source of truth.
func TestChromiumProfilesPrefersLocalState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Profile 9"), 0o755); err != nil {
		t.Fatal(err)
	}
	ls := `{"profile":{"info_cache":{"Default":{"name":"FromLocalState"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "Local State"), []byte(ls), 0o644); err != nil {
		t.Fatal(err)
	}
	got := chromiumProfiles(dir)
	if len(got) != 1 || got[0].Name != "FromLocalState" {
		t.Fatalf("got %v, want only the Local State entry", got)
	}
}

const profilesINI = `[Install4F96D1932A9F858E]
Default=Profiles/abcd1234.default-release
Locked=1

[Profile1]
Name=Work
IsRelative=1
Path=Profiles/efgh5678.work

[Profile0]
Name=default-release
IsRelative=1
Path=Profiles/abcd1234.default-release
Default=1

[General]
StartWithLastProfile=1
Version=2
`

func TestParseFirefoxProfilesINI(t *testing.T) {
	profs := parseFirefoxProfilesINI([]byte(profilesINI))
	// File order, not alphabetical: profiles.ini order is the order Firefox's own manager
	// shows, and Dir doubles as the -P argument so it must be the Name verbatim.
	want := []Profile{{"Work", "Work"}, {"default-release", "default-release"}}
	if len(profs) != len(want) {
		t.Fatalf("got %v, want %v", profs, want)
	}
	for i := range want {
		if profs[i] != want[i] {
			t.Errorf("profile[%d] = %v, want %v", i, profs[i], want[i])
		}
	}
}

func TestParseFirefoxProfilesINIIgnoresNonProfileSections(t *testing.T) {
	// [ProfileX] is not [ProfileN]: the numeric check is what keeps lookalike sections out.
	data := "[ProfileX]\nName=Nope\n\n[Profile0]\nName=Real\n"
	profs := parseFirefoxProfilesINI([]byte(data))
	if len(profs) != 1 || profs[0].Name != "Real" {
		t.Fatalf("got %v, want only the numeric section", profs)
	}
}

func TestFirefoxProfilesMissingINI(t *testing.T) {
	if got := firefoxProfiles(t.TempDir()); got != nil {
		t.Fatalf("got %v with no profiles.ini, want nil", got)
	}
}

func TestSafariHasNoProfiles(t *testing.T) {
	b := Browser{Name: "Safari", Kind: KindSafari}
	if got := b.Profiles(); got != nil {
		t.Fatalf("Safari.Profiles() = %v, want nil — there is no CLI profile selection to offer", got)
	}
}
