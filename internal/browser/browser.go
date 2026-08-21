// Package browser finds the browsers installed on this machine, the profiles inside them, and
// opens URLs in a chosen browser+profile — so an SSO sign-in can land in the work profile that
// is already signed in to Identity Center instead of whatever the OS default happens to be.
//
// Detection is by looking, not by asking: there is no cross-platform API for "installed
// browsers", so this probes the well-known install locations per OS. A browser installed
// somewhere exotic is simply not offered, which is fine — the system-default and no-browser
// modes remain, so nothing is unreachable, just unlisted.
package browser

import (
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"

	"github.com/treyperrone/warren/internal/homedir"
)

// Kind is the launch-and-profile dialect a browser speaks. Everything Chromium-derived shares
// one dialect (--profile-directory, Local State), Firefox has its own (-P, profiles.ini), and
// Safari has no profile selection a process can ask for at all.
type Kind string

const (
	KindChromium Kind = "chromium"
	KindFirefox  Kind = "firefox"
	KindSafari   Kind = "safari"
)

// Browser is one detected installation.
type Browser struct {
	Name string // display name, also what a saved preference matches on
	Kind Kind
	// Path is what gets executed, and its shape is per-OS: on macOS it is the app name handed
	// to `open -a` (which resolves the bundle wherever it lives), on Windows an absolute .exe
	// path, on Linux a binary resolved from PATH.
	Path string
	// DataDir is where this browser keeps its profiles; "" when profiles cannot be enumerated
	// (Safari). Recorded at detection time so profile listing needs no second search.
	DataDir string
}

// Profile is one profile inside a browser.
type Profile struct {
	// Dir is the value the browser's command line wants: the --profile-directory for a
	// Chromium browser ("Default", "Profile 1", ...), the profile *name* for Firefox's -P.
	Dir string
	// Name is what a human calls it ("Work", "jane@corp.com"). Falls back to Dir when the
	// browser recorded no friendlier name.
	Name string
}

// spec describes where one browser lives on each OS. The zero value of any field means "not
// available on that OS" — Safari has no linuxBins, and that is its Linux story.
type spec struct {
	name      string
	kind      Kind
	macApp    string              // app name under /Applications or ~/Applications
	linuxBins []string            // PATH names, first hit wins (chrome ships under two)
	winPaths  []winPath           // exe candidates, first that exists wins
	dataDir   map[string][]string // GOOS -> profile root candidates; ~-relative unless winPath-style env prefix
}

// winPath is an exe or data location rooted at an environment variable, because nothing on
// Windows is reliably at a literal path: Program Files moves with locale and architecture,
// and per-user installs live under LOCALAPPDATA.
type winPath struct {
	env string
	sub string
}

// specs is the roster, in the order the picker shows it. Chrome and Edge first because they
// are what the feature was asked for; Safari last because it can only ever be "open the URL".
var specs = []spec{
	{
		name: "Google Chrome", kind: KindChromium, macApp: "Google Chrome",
		linuxBins: []string{"google-chrome", "google-chrome-stable"},
		winPaths: []winPath{
			{"ProgramFiles", `Google\Chrome\Application\chrome.exe`},
			{"ProgramFiles(x86)", `Google\Chrome\Application\chrome.exe`},
			{"LOCALAPPDATA", `Google\Chrome\Application\chrome.exe`},
		},
		dataDir: map[string][]string{
			"darwin":  {"Library/Application Support/Google/Chrome"},
			"linux":   {".config/google-chrome", ".var/app/com.google.Chrome/config/google-chrome"},
			"windows": {`%LOCALAPPDATA%\Google\Chrome\User Data`},
		},
	},
	{
		name: "Microsoft Edge", kind: KindChromium, macApp: "Microsoft Edge",
		linuxBins: []string{"microsoft-edge", "microsoft-edge-stable"},
		winPaths: []winPath{
			{"ProgramFiles(x86)", `Microsoft\Edge\Application\msedge.exe`},
			{"ProgramFiles", `Microsoft\Edge\Application\msedge.exe`},
		},
		dataDir: map[string][]string{
			"darwin":  {"Library/Application Support/Microsoft Edge"},
			"linux":   {".config/microsoft-edge", ".var/app/com.microsoft.Edge/config/microsoft-edge"},
			"windows": {`%LOCALAPPDATA%\Microsoft\Edge\User Data`},
		},
	},
	{
		name: "Brave", kind: KindChromium, macApp: "Brave Browser",
		linuxBins: []string{"brave-browser", "brave"},
		winPaths: []winPath{
			{"ProgramFiles", `BraveSoftware\Brave-Browser\Application\brave.exe`},
			{"LOCALAPPDATA", `BraveSoftware\Brave-Browser\Application\brave.exe`},
		},
		dataDir: map[string][]string{
			"darwin":  {"Library/Application Support/BraveSoftware/Brave-Browser"},
			"linux":   {".config/BraveSoftware/Brave-Browser", ".var/app/com.brave.Browser/config/BraveSoftware/Brave-Browser"},
			"windows": {`%LOCALAPPDATA%\BraveSoftware\Brave-Browser\User Data`},
		},
	},
	{
		name: "Chromium", kind: KindChromium, macApp: "Chromium",
		linuxBins: []string{"chromium", "chromium-browser"},
		winPaths: []winPath{
			{"LOCALAPPDATA", `Chromium\Application\chrome.exe`},
		},
		dataDir: map[string][]string{
			"darwin": {"Library/Application Support/Chromium"},
			// Snap first after the classic path: Ubuntu has shipped Chromium as a snap for
			// years, so the classic dir existing at all usually means a migration leftover.
			"linux":   {".config/chromium", "snap/chromium/common/chromium", ".var/app/org.chromium.Chromium/config/chromium"},
			"windows": {`%LOCALAPPDATA%\Chromium\User Data`},
		},
	},
	{
		name: "Firefox", kind: KindFirefox, macApp: "Firefox",
		linuxBins: []string{"firefox"},
		winPaths: []winPath{
			{"ProgramFiles", `Mozilla Firefox\firefox.exe`},
			{"ProgramFiles(x86)", `Mozilla Firefox\firefox.exe`},
		},
		dataDir: map[string][]string{
			// These hold profiles.ini; the profiles themselves are wherever it points.
			"darwin": {"Library/Application Support/Firefox"},
			// Stock Ubuntu ships Firefox as a snap since 22.04, whose profiles live under
			// ~/snap; without that candidate the PATH probe finds the snap shim, the classic
			// dir reads empty, and the picker silently shows no profiles on the most common
			// Linux desktop. Flatpak is the same story under ~/.var.
			"linux":   {".mozilla/firefox", "snap/firefox/common/.mozilla/firefox", ".var/app/org.mozilla.firefox/.mozilla/firefox"},
			"windows": {`%APPDATA%\Mozilla\Firefox`},
		},
	},
	{
		// Safari: detected so it can be *chosen*, but it has no CLI profile selection —
		// profiles exist in its UI since macOS 14, and nothing a launching process passes
		// can pick one. Offering it with that limit beats hiding the browser people use.
		name: "Safari", kind: KindSafari, macApp: "Safari",
	},
}

// deps are the probes detection runs on, injected so tests can describe any OS from any OS.
// The shipped Windows binary once read the wrong home because every test injected HOME and
// nothing exercised the real resolution (see internal/homedir); keeping the seam explicit is
// how this package avoids growing the same blind spot.
type deps struct {
	goos     string
	home     string
	getenv   func(string) string
	exists   func(string) bool
	lookPath func(string) (string, error)
}

// join builds a path in the SIMULATED OS's dialect, not the host's. Detection is driven by
// d.goos so tests can describe any OS from any OS — but the host's filepath.Join broke that
// promise: simulating macOS on the Windows CI runner produced \-joined paths that matched
// nothing, which is exactly the class of only-fails-on-another-OS bug the injected deps
// exist to prevent. On a real machine d.goos is runtime.GOOS and this agrees with the host.
func (d deps) join(elem ...string) string {
	if d.goos == "windows" {
		return strings.Join(elem, `\`)
	}
	return path.Join(elem...)
}

func realDeps() deps {
	return deps{
		goos:     runtime.GOOS,
		home:     homedir.Dir(),
		getenv:   os.Getenv,
		exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		lookPath: exec.LookPath,
	}
}

// Detect returns the browsers installed on this machine, in roster order. An empty result is
// an ordinary answer (a server, a fresh box), not an error — the caller still has the
// system-default and no-browser options to offer.
func Detect() []Browser {
	return detect(realDeps())
}

func detect(d deps) []Browser {
	var found []Browser
	for _, s := range specs {
		b, ok := s.locate(d)
		if ok {
			found = append(found, b)
		}
	}
	return found
}

func (s spec) locate(d deps) (Browser, bool) {
	b := Browser{Name: s.name, Kind: s.kind, DataDir: s.resolveDataDir(d)}
	switch d.goos {
	case "darwin":
		if s.macApp == "" {
			return Browser{}, false
		}
		// /Applications first, ~/Applications second: both are where macOS puts apps, and
		// `open -a <name>` will find either — the probe only decides whether to list it.
		for _, root := range []string{"/Applications", d.join(d.home, "Applications")} {
			if d.exists(d.join(root, s.macApp+".app")) {
				b.Path = s.macApp
				return b, true
			}
		}
	case "windows":
		for _, w := range s.winPaths {
			root := d.getenv(w.env)
			if root == "" {
				continue
			}
			p := d.join(root, w.sub)
			if d.exists(p) {
				b.Path = p
				return b, true
			}
		}
	default:
		for _, bin := range s.linuxBins {
			if p, err := d.lookPath(bin); err == nil {
				b.Path = p
				return b, true
			}
		}
	}
	return Browser{}, false
}

// resolveDataDir expands the spec's per-OS profile root candidates and returns the first
// that exists on disk — which is how snap and flatpak installs are found: their PATH shims
// look like any binary, but their profiles live under ~/snap and ~/.var instead of the
// classic dot-directory. When none exists yet (browser installed, never launched) the first
// candidate stands in, so the answer is a sensible place rather than nothing.
func (s spec) resolveDataDir(d deps) string {
	candidates, ok := s.dataDir[d.goos]
	if !ok {
		// Linux covers every non-mac, non-windows GOOS: the BSDs lay browsers out the same way.
		candidates, ok = s.dataDir["linux"]
		if d.goos == "darwin" || d.goos == "windows" || !ok {
			return ""
		}
	}
	first := ""
	for _, raw := range candidates {
		p := s.expandDataDir(d, raw)
		if p == "" {
			continue
		}
		if first == "" {
			first = p
		}
		if d.exists(p) {
			return p
		}
	}
	return first
}

// expandDataDir turns one candidate into an absolute path. Windows entries are rooted at an
// environment variable spelled %LIKE_THIS%; everything else is home-relative.
func (s spec) expandDataDir(d deps, raw string) string {
	if len(raw) > 0 && raw[0] == '%' {
		end := 1
		for end < len(raw) && raw[end] != '%' {
			end++
		}
		root := d.getenv(raw[1:end])
		if root == "" {
			return ""
		}
		// The template reads `%VAR%\sub\path`; the separator after the variable belongs to
		// the template's readability, not to join, which supplies its own.
		return d.join(root, strings.TrimPrefix(raw[end+1:], `\`))
	}
	if d.home == "" {
		return ""
	}
	return d.join(d.home, raw)
}
