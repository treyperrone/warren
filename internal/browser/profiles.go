package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Profiles lists the profiles inside b, in a stable, human-sensible order. An empty result
// means "no profile to choose" — Safari always, a Chromium browser whose data dir was never
// created — and the caller should launch the browser plain rather than treat it as an error.
func (b Browser) Profiles() []Profile {
	switch b.Kind {
	case KindChromium:
		return chromiumProfiles(b.DataDir)
	case KindFirefox:
		return firefoxProfiles(b.DataDir)
	}
	return nil
}

// ---- Chromium ---------------------------------------------------------------

// localState is the slice of Chrome's "Local State" file this package reads. info_cache is
// the browser's own registry of every profile directory and its display metadata — the same
// source its profile menu draws from, which is exactly the list a person expects to see here.
type localState struct {
	Profile struct {
		InfoCache map[string]struct {
			Name string `json:"name"`
			// GaiaName and UserName describe the signed-in account. Shown alongside the
			// profile name because "Person 1 — jane@corp.com" answers "which one is the
			// work profile?" where a bare "Person 1" does not.
			GaiaName string `json:"gaia_name"`
			UserName string `json:"user_name"`
		} `json:"info_cache"`
	} `json:"profile"`
}

func chromiumProfiles(dataDir string) []Profile {
	if dataDir == "" {
		return nil
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "Local State")); err == nil {
		if profs := parseChromiumLocalState(data); len(profs) > 0 {
			return profs
		}
	}
	// No readable Local State — a very old install, or a browser mid-first-run. The profile
	// directories themselves are still on disk, so fall back to naming what exists rather
	// than reporting nothing.
	return scanChromiumDirs(dataDir)
}

// parseChromiumLocalState pulls the profile list out of a Local State document. Exposed to
// tests as a pure function over bytes so every shape Chrome ships can be pinned down without
// building a fake browser install.
func parseChromiumLocalState(data []byte) []Profile {
	var ls localState
	if json.Unmarshal(data, &ls) != nil {
		return nil
	}
	profs := make([]Profile, 0, len(ls.Profile.InfoCache))
	for dir, info := range ls.Profile.InfoCache {
		name := info.Name
		if name == "" {
			name = dir
		}
		// The account, when known and not already the visible name: it is the field that
		// distinguishes two profiles a person named identically.
		if acct := firstNonEmpty(info.UserName, info.GaiaName); acct != "" && acct != name {
			name += " — " + acct
		}
		profs = append(profs, Profile{Dir: dir, Name: name})
	}
	sortChromiumProfiles(profs)
	return profs
}

// sortChromiumProfiles orders Default first, then Profile N numerically, then anything else
// lexically. info_cache is a JSON object, so map iteration would otherwise shuffle the list
// on every visit — and a settings list that reorders itself between visits reads as broken.
func sortChromiumProfiles(profs []Profile) {
	rank := func(p Profile) (int, int) {
		if p.Dir == "Default" {
			return 0, 0
		}
		if n, ok := strings.CutPrefix(p.Dir, "Profile "); ok {
			if i, err := strconv.Atoi(n); err == nil {
				return 1, i
			}
		}
		return 2, 0
	}
	sort.Slice(profs, func(i, j int) bool {
		ri, ni := rank(profs[i])
		rj, nj := rank(profs[j])
		if ri != rj {
			return ri < rj
		}
		if ni != nj {
			return ni < nj
		}
		return profs[i].Dir < profs[j].Dir
	})
}

// scanChromiumDirs is the Local State fallback: list the directories Chrome uses for
// profiles, taking each one's display name from its Preferences file when readable.
func scanChromiumDirs(dataDir string) []Profile {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	var profs []Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := e.Name()
		if dir != "Default" && !strings.HasPrefix(dir, "Profile ") {
			continue
		}
		name := dir
		if data, err := os.ReadFile(filepath.Join(dataDir, dir, "Preferences")); err == nil {
			var prefs struct {
				Profile struct {
					Name string `json:"name"`
				} `json:"profile"`
			}
			if json.Unmarshal(data, &prefs) == nil && prefs.Profile.Name != "" {
				name = prefs.Profile.Name
			}
		}
		profs = append(profs, Profile{Dir: dir, Name: name})
	}
	sortChromiumProfiles(profs)
	return profs
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- Firefox ----------------------------------------------------------------

// firefoxProfiles reads profiles.ini. Firefox profiles are addressed by *name* on the command
// line (-P Work), not by directory, so Dir carries the name — the launch side knows the
// difference by Kind.
func firefoxProfiles(dataDir string) []Profile {
	if dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "profiles.ini"))
	if err != nil {
		return nil
	}
	return parseFirefoxProfilesINI(data)
}

// parseFirefoxProfilesINI extracts the [ProfileN] sections. Only Name matters here: it is
// both the display string and the -P argument. Install/lock sections are skipped, and a
// profile with no name cannot be selected with -P, so it is not offered.
//
// Hand-rolled for the same reason ParseConfig in internal/aws is: the format is a dozen
// lines of key=value under bracketed headers, and a dependency would be bigger than the code.
func parseFirefoxProfilesINI(data []byte) []Profile {
	var profs []Profile
	inProfile := false
	var name string

	flush := func() {
		if inProfile && name != "" {
			profs = append(profs, Profile{Dir: name, Name: name})
		}
		name = ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			header := line[1 : len(line)-1]
			// [Profile0], [Profile1], ... — but not [ProfileN]-lookalikes such as [Install...].
			rest, ok := strings.CutPrefix(header, "Profile")
			_, err := strconv.Atoi(rest)
			inProfile = ok && err == nil
			continue
		}
		if !inProfile {
			continue
		}
		if v, ok := strings.CutPrefix(line, "Name="); ok {
			name = v
		}
	}
	flush()
	return profs
}
