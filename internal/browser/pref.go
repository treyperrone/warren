package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

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

// errSkipWrite lets a mutator report "nothing changed" so mutateConfigDoc leaves the file
// untouched instead of rewriting identical bytes.
var errSkipWrite = errors.New("skip write")

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
		if errors.Is(err, errSkipWrite) {
			return nil
		}
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
	// A UNIQUE temp name per write: this file now has writers in separate processes (the
	// TUI, `warren creds` under some SDK, favexec's renewal), and two of them sharing one
	// ".tmp" path can interleave write/rename and install a torn document. Unique temps
	// make every rename atomic-or-lost; losing one whole update (last writer wins) is
	// acceptable for preferences and observations, a spliced file is not.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".warren_config-*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, werr := tmp.Write(append(out, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		if werr == nil {
			werr = cerr
		}
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
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

// ---- session observations ------------------------------------------------------------------

// SessionObservation is what warren has LEARNED about one Identity Center session, since AWS
// discloses none of it: when the current sign-in happened, and how long the org allowed the
// previous one to live before silent renewal stopped working. The learned duration is the
// only path to printing "session hard-expires in ~4h" at sign-in time — there is no grant
// field and no non-admin API that says it.
//
// This lives in warren's config file because that file is warren's memory, not because a
// session observation is a browser preference; if the file grows more tenants, the store
// deserves its own package.
type SessionObservation struct {
	SignedInAt            time.Time `json:"signed_in_at,omitempty"`
	LearnedSessionSeconds int64     `json:"learned_session_seconds,omitempty"`
}

// LearnedDuration is the observed org session length, zero when nothing has been learned yet.
func (o SessionObservation) LearnedDuration() time.Duration {
	return time.Duration(o.LearnedSessionSeconds) * time.Second
}

const obsKey = "sso_session_observations"

func loadObservations(doc map[string]json.RawMessage) map[string]SessionObservation {
	m := map[string]SessionObservation{}
	if raw, ok := doc[obsKey]; ok {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// LoadObservation returns what is known about one start URL's sessions.
func LoadObservation(startURL string) (SessionObservation, bool) {
	doc, err := readConfigDoc()
	if err != nil {
		return SessionObservation{}, false
	}
	o, ok := loadObservations(doc)[startURL]
	return o, ok
}

// RecordSignIn stamps the moment a device authorization succeeded. The learned duration from
// previous sessions survives — it is the whole point of keeping it.
func RecordSignIn(startURL string, at time.Time) error {
	return mutateObservation(startURL, func(o *SessionObservation) { o.SignedInAt = at })
}

// RecordSessionEnd marks the moment silent renewal stopped working — the one observable
// signal that the org's session ceiling was hit — and learns the session length from it.
// The sign-in stamp is consumed: later failures of the already-dead session must not
// re-learn ever-longer durations from the same start point.
func RecordSessionEnd(startURL string, at time.Time) error {
	return mutateObservation(startURL, func(o *SessionObservation) {
		if o.SignedInAt.IsZero() || !at.After(o.SignedInAt) {
			return
		}
		o.LearnedSessionSeconds = int64(at.Sub(o.SignedInAt) / time.Second)
		o.SignedInAt = time.Time{}
	})
}

func mutateObservation(startURL string, f func(*SessionObservation)) error {
	if startURL == "" {
		return nil
	}
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		m := loadObservations(doc)
		o := m[startURL]
		before := o
		f(&o)
		if o == before {
			// A no-op mutation (RecordSessionEnd with no sign-in stamp — every failed
			// refresh after a session dies) must not stamp empty entries or churn the
			// file with rewrites that change nothing.
			return errSkipWrite
		}
		m[startURL] = o
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		doc[obsKey] = raw
		return nil
	})
}

// ---- favorites -------------------------------------------------------------------------------

// Favorite is one bookmarked account+role, pinned to the top of the picker and addressable
// by nickname from the CLI. It exists because a 300-account Identity Center has maybe five
// destinations a person actually lives in — the picker is for finding, favorites are for
// returning. Keyed by start URL like everything else in this file, so renaming the
// [sso-session] block orphans nothing.
type Favorite struct {
	Nickname    string `json:"nickname,omitempty"`
	StartURL    string `json:"start_url"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name,omitempty"`
	Role        string `json:"role"`
	// The optional connection half: with these set, selecting the favorite carries on past
	// credentials to an actual session — resolve the instance, start the tunnel, open the
	// client. InstanceName is the Name TAG, never the instance id: in a range that repaves,
	// ids are corpses within the week while names are policy, so a favorite that stored
	// i-0abc... would quietly rot. Resolution happens at launch, against what is running.
	InstanceName string `json:"instance_name,omitempty"`
	ConnType     string `json:"conn_type,omitempty"` // "shell", "ssh" or "rdp"
	SSHUser      string `json:"ssh_user,omitempty"`
}

// Connects reports whether this favorite carries a connection, or stops at credentials.
func (f Favorite) Connects() bool { return f.ConnType != "" }

// Same reports whether two favorites name the same destination — account, role, and the
// connection half. Two favorites on the same role but different instances (or the same
// instance over SSH and RDP) are different destinations; nickname and the account display
// name are decoration.
func (f Favorite) Same(o Favorite) bool {
	return f.StartURL == o.StartURL && f.AccountID == o.AccountID && f.Role == o.Role &&
		f.InstanceName == o.InstanceName && f.ConnType == o.ConnType && f.SSHUser == o.SSHUser
}

// UniqueNickname returns base, or base with a numeric suffix appended, so it does not collide
// with an existing favorite for a different destination. Two destinations differing only in
// case or punctuation (an account or instance named "Web-01" vs "web 01", say) slug to the same
// base — harmless for the picker, which matches by full destination, but ambiguous for `warren
// exec/shell <nickname>`, which matches by nickname alone. Re-favoriting the SAME destination
// deliberately keeps the same base: AddFavorite's own Same(dest) check updates that entry in
// place rather than creating a second one, so this must not disambiguate away from it.
func UniqueNickname(base string, dest Favorite) string {
	existing := Favorites()
	name := base
	for i := 2; ; i++ {
		collision := false
		for _, f := range existing {
			if f.Nickname == name && !f.Same(dest) {
				collision = true
				break
			}
		}
		if !collision {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

const favKey = "favorites"

// Favorites returns the saved bookmarks in saved order — the order is the user's, made by
// when they starred things, and reordering it for them would break spatial memory.
func Favorites() []Favorite {
	doc, err := readConfigDoc()
	if err != nil {
		return nil
	}
	var favs []Favorite
	if raw, ok := doc[favKey]; ok {
		_ = json.Unmarshal(raw, &favs)
	}
	return favs
}

// FindFavorite locates a saved bookmark equal to probe (Same semantics: the whole
// destination, connection half included — a creds-only probe only matches a creds-only
// favorite).
func FindFavorite(probe Favorite) (Favorite, bool) {
	for _, f := range Favorites() {
		if f.Same(probe) {
			return f, true
		}
	}
	return Favorite{}, false
}

// AddFavorite saves a bookmark; starring the same destination twice updates the existing
// entry in place (a rename) rather than growing a duplicate row.
func AddFavorite(f Favorite) error {
	return mutateFavorites(func(favs []Favorite) []Favorite {
		for i := range favs {
			if favs[i].Same(f) {
				favs[i] = f
				return favs
			}
		}
		return append(favs, f)
	})
}

// RemoveFavorite deletes the bookmark for f's destination; removing what is not there is a
// no-op, not an error.
func RemoveFavorite(f Favorite) error {
	return mutateFavorites(func(favs []Favorite) []Favorite {
		out := favs[:0]
		for _, x := range favs {
			if !x.Same(f) {
				out = append(out, x)
			}
		}
		return out
	})
}

func mutateFavorites(f func([]Favorite) []Favorite) error {
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		var favs []Favorite
		if raw, ok := doc[favKey]; ok {
			_ = json.Unmarshal(raw, &favs)
		}
		favs = f(favs)
		if len(favs) == 0 {
			delete(doc, favKey)
			return nil
		}
		raw, err := json.Marshal(favs)
		if err != nil {
			return err
		}
		doc[favKey] = raw
		return nil
	})
}

// ---- RDP-on-Linux acknowledgments ------------------------------------------------------------

// rdpAckKey identifies one acknowledged Linux-but-RDP box: account + Name tag, never the
// instance id, for the same repave reason favorites use names. Once the user proceeds past
// the warning on a box, they have declared "this one runs xrdp" — showing the same warning
// again would be warren forgetting on purpose.
const rdpAcksKey = "rdp_linux_acks"

func rdpAckKey(accountID, instanceName string) string { return accountID + "/" + instanceName }

// RDPLinuxAcked reports whether this box's Linux-but-RDP warning has been accepted before.
func RDPLinuxAcked(accountID, instanceName string) bool {
	doc, err := readConfigDoc()
	if err != nil {
		return false
	}
	var acks map[string]bool
	if raw, ok := doc[rdpAcksKey]; ok {
		_ = json.Unmarshal(raw, &acks)
	}
	return acks[rdpAckKey(accountID, instanceName)]
}

// AckRDPLinux records that the user knowingly RDPs into this Linux-reported box.
func AckRDPLinux(accountID, instanceName string) error {
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		acks := map[string]bool{}
		if raw, ok := doc[rdpAcksKey]; ok {
			_ = json.Unmarshal(raw, &acks)
		}
		acks[rdpAckKey(accountID, instanceName)] = true
		raw, err := json.Marshal(acks)
		if err != nil {
			return err
		}
		doc[rdpAcksKey] = raw
		return nil
	})
}
