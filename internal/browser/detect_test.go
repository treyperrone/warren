package browser

import (
	"errors"
	"path/filepath"
	"testing"
)

// fakeDeps builds a deps describing some other machine: files is the set of paths that exist,
// bins the set of PATH-resolvable names, env the environment. Detection must be drivable from
// a test on any OS or the Windows and macOS arms only ever run on someone else's machine.
func fakeDeps(goos, home string, env map[string]string, files map[string]bool, bins map[string]string) deps {
	return deps{
		goos:   goos,
		home:   home,
		getenv: func(k string) string { return env[k] },
		exists: func(p string) bool { return files[p] },
		lookPath: func(name string) (string, error) {
			if p, ok := bins[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
	}
}

func TestDetectDarwinFindsAppsAndSafariHasNoDataDir(t *testing.T) {
	d := fakeDeps("darwin", "/Users/trey", nil, map[string]bool{
		"/Applications/Google Chrome.app": true,
		"/Applications/Safari.app":        true,
	}, nil)

	got := detect(d)
	if len(got) != 2 {
		t.Fatalf("detect = %v, want Chrome and Safari", got)
	}
	chrome, safari := got[0], got[1]
	if chrome.Name != "Google Chrome" || chrome.Path != "Google Chrome" {
		t.Errorf("chrome = %+v; Path must be the app name open -a resolves", chrome)
	}
	if want := "/Users/trey/Library/Application Support/Google/Chrome"; chrome.DataDir != want {
		t.Errorf("chrome.DataDir = %q, want %q", chrome.DataDir, want)
	}
	// Safari has no enumerable profiles, and a non-empty DataDir would send the profile
	// scanner digging through a directory that holds no profiles.
	if safari.Name != "Safari" || safari.DataDir != "" {
		t.Errorf("safari = %+v, want empty DataDir", safari)
	}
}

func TestDetectDarwinFindsUserApplications(t *testing.T) {
	d := fakeDeps("darwin", "/Users/trey", nil, map[string]bool{
		"/Users/trey/Applications/Firefox.app": true,
	}, nil)
	got := detect(d)
	if len(got) != 1 || got[0].Name != "Firefox" {
		t.Fatalf("detect = %v, want Firefox from ~/Applications", got)
	}
}

func TestDetectLinuxUsesPathAndSkipsSafari(t *testing.T) {
	d := fakeDeps("linux", "/home/trey", nil, nil, map[string]string{
		// Chrome ships as google-chrome-stable on Debian; the first roster name misses and
		// the second must still find it.
		"google-chrome-stable": "/usr/bin/google-chrome-stable",
		"firefox":              "/usr/bin/firefox",
	})

	got := detect(d)
	if len(got) != 2 {
		t.Fatalf("detect = %v, want Chrome and Firefox", got)
	}
	if got[0].Path != "/usr/bin/google-chrome-stable" {
		t.Errorf("chrome resolved to %q, want the -stable binary", got[0].Path)
	}
	if want := "/home/trey/.mozilla/firefox"; got[1].DataDir != want {
		t.Errorf("firefox.DataDir = %q, want %q", got[1].DataDir, want)
	}
}

func TestDetectWindowsProbesEnvRootedPaths(t *testing.T) {
	env := map[string]string{
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"LOCALAPPDATA":      `C:\Users\trey\AppData\Local`,
		"APPDATA":           `C:\Users\trey\AppData\Roaming`,
	}
	edgeExe := filepath.Join(`C:\Program Files (x86)`, `Microsoft\Edge\Application\msedge.exe`)
	d := fakeDeps("windows", `C:\Users\trey`, env, map[string]bool{edgeExe: true}, nil)

	got := detect(d)
	if len(got) != 1 || got[0].Name != "Microsoft Edge" {
		t.Fatalf("detect = %v, want Edge alone", got)
	}
	if got[0].Path != edgeExe {
		t.Errorf("edge.Path = %q, want the probed exe path %q", got[0].Path, edgeExe)
	}
	if want := filepath.Join(`C:\Users\trey\AppData\Local`, `Microsoft\Edge\User Data`); got[0].DataDir != want {
		t.Errorf("edge.DataDir = %q, want %q", got[0].DataDir, want)
	}
}

// A machine with ProgramFiles unset (stripped-down CI, odd shells) must not turn "" into a
// relative path that accidentally exists — the env miss skips the candidate outright.
func TestDetectWindowsSkipsUnsetEnvRoots(t *testing.T) {
	d := fakeDeps("windows", `C:\Users\trey`, map[string]string{},
		map[string]bool{filepath.Join("", `Mozilla Firefox\firefox.exe`): true}, nil)
	if got := detect(d); len(got) != 0 {
		t.Fatalf("detect = %v, want nothing when every env root is unset", got)
	}
}

func TestDetectEmptyMachine(t *testing.T) {
	if got := detect(fakeDeps("linux", "/home/x", nil, nil, nil)); got != nil {
		t.Fatalf("detect on a bare machine = %v, want nil", got)
	}
}

// The BSDs lay browsers out like Linux, and an unknown GOOS must fall back to that layout
// rather than return no data dir and silently lose profile listing.
func TestDetectUnknownGOOSFallsBackToLinuxLayout(t *testing.T) {
	d := fakeDeps("freebsd", "/home/trey", nil, nil, map[string]string{
		"firefox": "/usr/local/bin/firefox",
	})
	got := detect(d)
	if len(got) != 1 {
		t.Fatalf("detect = %v, want Firefox", got)
	}
	if want := "/home/trey/.mozilla/firefox"; got[0].DataDir != want {
		t.Errorf("DataDir = %q, want the Linux layout %q", got[0].DataDir, want)
	}
}

// Stock Ubuntu ships Firefox as a snap: the PATH probe finds the shim, but the profiles live
// under ~/snap, so the data dir must be the first CANDIDATE THAT EXISTS, not the first listed.
func TestDetectLinuxPrefersExistingSnapDataDir(t *testing.T) {
	snapDir := "/home/trey/snap/firefox/common/.mozilla/firefox"
	d := fakeDeps("linux", "/home/trey", nil,
		map[string]bool{snapDir: true},
		map[string]string{"firefox": "/snap/bin/firefox"})

	got := detect(d)
	if len(got) != 1 {
		t.Fatalf("detect = %v, want Firefox", got)
	}
	if got[0].DataDir != snapDir {
		t.Errorf("DataDir = %q, want the existing snap dir %q", got[0].DataDir, snapDir)
	}
}

// With no candidate existing yet (installed, never launched), the classic path stands in
// rather than nothing — Profiles() returns empty either way, but "" would read as Safari's
// no-profiles-possible case.
func TestDetectLinuxFallsBackToFirstCandidate(t *testing.T) {
	d := fakeDeps("linux", "/home/trey", nil, nil,
		map[string]string{"firefox": "/usr/bin/firefox"})
	got := detect(d)
	if len(got) != 1 {
		t.Fatalf("detect = %v, want Firefox", got)
	}
	if want := "/home/trey/.mozilla/firefox"; got[0].DataDir != want {
		t.Errorf("DataDir = %q, want %q", got[0].DataDir, want)
	}
}
