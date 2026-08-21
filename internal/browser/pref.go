package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/treyperrone/warren/internal/homedir"
)

// Pref is the saved answer to "where should SSO sign-in pages open". It lives in warren's own
// config file, never in ~/.aws/config — that file is shared with the aws CLI and every SDK on
// the machine and is read-only to warren beyond the append-only sso-session bootstrap.
type Pref struct {
	// Mode: "" and ModeSystem open the OS default browser (the behaviour before this feature
	// existed, so an empty config changes nothing). ModeBrowser opens the named browser and
	// profile. ModeNone never opens anything — the sign-in screen shows the URL and device
	// code and the user carries them wherever they like, which is the only mode that makes
	// sense from an SSH session or a box whose browser is signed in to the wrong world.
	Mode        string `json:"mode,omitempty"`
	Browser     string `json:"browser,omitempty"`      // matches Browser.Name from Detect
	ProfileDir  string `json:"profile_dir,omitempty"`  // --profile-directory / -P argument
	ProfileName string `json:"profile_name,omitempty"` // display only
}

const (
	ModeSystem  = "system"
	ModeBrowser = "browser"
	ModeNone    = "none"
	// ModeAsk shows the browser/profile picker inline every time a sign-in is actually
	// needed. The unset mode ("") behaves the same way — asking on the first real sign-in
	// is how anyone discovers the choice exists at all, where a settings row alone had to
	// be found by accident. They stay distinct values because "" also means "never chose",
	// which is what lets the ask screen offer to save an answer as the default.
	ModeAsk = "ask"
)

// prefKey holds the global preference; sessionsKey holds per-sso-session overrides, keyed by
// start URL. The start URL rather than the session name: it is the identity the sign-in
// actually authenticates against (the token cache is keyed by it too), so an override
// survives renaming the [sso-session] block, and two blocks pointing at the same Identity
// Center deliberately share one answer. Namespaced keys because the file is warren's one
// config file, not this feature's.
const (
	prefKey     = "sso_browser"
	sessionsKey = "sso_browser_sessions"
)

// PrefPath is warren's own config file, next to .warren_sessions.json for the same reason
// that file is where it is: state of warren's own invention stays out of ~/.aws/.
//
// "" when the home directory cannot be determined: joining onto an empty home would make
// the relative path ".warren_config.json", read from and written to whatever directory
// warren happened to start in — the exact bug internal/homedir documents for ~/.aws/config.
func PrefPath() string {
	home := homedir.Dir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".warren_config.json")
}

// LoadPref returns the saved preference. A missing or unreadable file is the zero Pref —
// system default — because sign-in must never be blocked by a config problem; the worst
// outcome of losing this file is the wrong browser opening, exactly as before the feature.
func LoadPref() Pref {
	doc, err := readConfigDoc()
	if err != nil {
		return Pref{}
	}
	var p Pref
	if raw, ok := doc[prefKey]; ok {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

// ResolvePrefFor returns the preference governing a sign-in to startURL: that session's
// override when one is saved, the global preference otherwise. scoped says which won, so a
// UI can tell "Chrome because you said so for this session" from "Chrome because that is
// the default".
func ResolvePrefFor(startURL string) (p Pref, scoped bool) {
	if startURL != "" {
		if sp, ok := SessionPrefs()[startURL]; ok {
			return sp, true
		}
	}
	return LoadPref(), false
}

// SessionPrefs returns every per-session override. Same degradation as LoadPref: an
// unreadable file is an empty map, never a blocked sign-in.
func SessionPrefs() map[string]Pref {
	doc, err := readConfigDoc()
	if err != nil {
		return nil
	}
	var m map[string]Pref
	if raw, ok := doc[sessionsKey]; ok {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// SavePrefFor saves an override for one session's start URL.
func SavePrefFor(startURL string, p Pref) error {
	if startURL == "" {
		return errors.New("cannot save a per-session preference without a start URL")
	}
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		m := map[string]Pref{}
		if raw, ok := doc[sessionsKey]; ok {
			_ = json.Unmarshal(raw, &m)
		}
		m[startURL] = p
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		doc[sessionsKey] = raw
		return nil
	})
}

// DeletePrefFor removes one session's override, so its sign-ins fall back to the global
// preference. Deleting an override that does not exist is a no-op, not an error.
func DeletePrefFor(startURL string) error {
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		m := map[string]Pref{}
		if raw, ok := doc[sessionsKey]; ok {
			_ = json.Unmarshal(raw, &m)
		}
		delete(m, startURL)
		if len(m) == 0 {
			delete(doc, sessionsKey)
			return nil
		}
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		doc[sessionsKey] = raw
		return nil
	})
}

// SavePref writes the global preference, replacing only its own key.
func SavePref(p Pref) error {
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		doc[prefKey] = raw
		return nil
	})
}

// mutateConfigDoc is the one writer of warren's config file: read the whole document as raw
// JSON members, let f edit exactly the keys it owns, re-encode around everything else. Keys
// this build has never heard of — a newer build's settings, say — survive every write
// instead of being the price of changing a browser.
func mutateConfigDoc(f func(doc map[string]json.RawMessage) error) error {
	path := PrefPath()
	if path == "" {
		return errors.New("cannot determine the home directory, so there is nowhere to save the preference")
	}
	doc, err := readConfigDoc()
	if err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	if err := f(doc); err != nil {
		return err
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// Temp file + rename, not truncate-in-place: this function's whole contract is that a
	// save never costs the file its other keys, and a WriteFile interrupted mid-write leaves
	// a half-document that poisons every later read AND save (readConfigDoc refuses corrupt
	// JSON). Rename is atomic on the same filesystem, so the file is always either the old
	// document or the new one. 0600 like the session file: nothing here is secret today, but
	// a config file that may grow fields later is cheaper to keep private from the start.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func readConfigDoc() (map[string]json.RawMessage, error) {
	if PrefPath() == "" {
		// No home means no file; the zero preference is the honest answer.
		return nil, nil
	}
	data, err := os.ReadFile(PrefPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", PrefPath(), err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", PrefPath(), err)
	}
	return doc, nil
}

// Describe renders the preference for the settings row, so the picker shows what is set
// before anyone opens it.
func (p Pref) Describe() string {
	switch p.Mode {
	case ModeNone:
		return "never — show the device code to open anywhere"
	case ModeBrowser:
		if p.ProfileName != "" {
			return p.Browser + " — " + p.ProfileName
		}
		return p.Browser
	case ModeSystem:
		return "system default browser"
	default:
		return "ask at each sign-in"
	}
}

// ShouldAsk reports whether a sign-in should show the inline browser picker: nothing saved
// yet, or the saved answer is to always ask. Suppressed wholesale in environments where no
// browser could open anyway — over SSH, on a displayless box, or under WARREN_NO_BROWSER —
// because asking which browser to use when the answer cannot matter is pure friction; those
// sign-ins go straight to the device-code screen.
func ShouldAsk(p Pref) bool {
	if p.Mode != "" && p.Mode != ModeAsk {
		return false
	}
	if v := os.Getenv("WARREN_NO_BROWSER"); v != "" && v != "0" {
		return false
	}
	return !Headless(runtime.GOOS, os.Getenv)
}

// OpenForLogin opens url according to the preference and returns a one-line note for the
// sign-in screen saying what actually happened. It never returns an error: the URL and code
// are on screen regardless, so every failure here downgrades to information — sign-in
// continues either way, which is the whole reason the device-code flow was chosen over a
// loopback redirect.
func OpenForLogin(p Pref, url string) string {
	if v := os.Getenv("WARREN_NO_BROWSER"); v != "" && v != "0" {
		return "WARREN_NO_BROWSER is set — open the URL on any device"
	}
	if p.Mode == ModeNone {
		return "browser opening is off — open the URL on any device"
	}
	if Headless(runtime.GOOS, os.Getenv) {
		return "no local display — open the URL on any device"
	}

	if p.Mode == ModeBrowser {
		for _, b := range Detect() {
			if b.Name == p.Browser {
				if err := Open(b, p.ProfileDir, url); err != nil {
					return fmt.Sprintf("could not open %s (%v) — use the URL below", p.Browser, err)
				}
				return "opening " + p.Describe()
			}
		}
		// Saved browser gone — uninstalled, or the preference travelled to another machine.
		// The system default is the least surprising stand-in, and the note says why.
		if err := OpenSystem(url); err != nil {
			return fmt.Sprintf("%s is no longer installed and the default browser failed (%v)", p.Browser, err)
		}
		return p.Browser + " is no longer installed — opening the default browser"
	}

	if err := OpenSystem(url); err != nil {
		return fmt.Sprintf("could not open a browser (%v) — use the URL below", err)
	}
	return "opening your default browser"
}
