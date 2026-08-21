package browser

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// OpenSystem hands a URL to the OS default handler — the behaviour warren always had, kept as
// its own entry point because it is both the default preference and the fallback for every
// way a specific-browser launch can go wrong.
//
// Start, not Run: the browser owns its own lifetime, and a browser that exits non-zero after
// showing the page is not a sign-in failure. Same for Open below.
func OpenSystem(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}

// Open opens url in a specific browser, under a specific profile when one is given.
func Open(b Browser, profile, url string) error {
	name, args := openArgs(runtime.GOOS, b, profile, url)
	if name == "" {
		return fmt.Errorf("%s cannot be launched on this platform", b.Name)
	}
	return exec.Command(name, args...).Start()
}

// openArgs builds the launch command. Pure so the exact argv for every OS × browser × profile
// combination is pinned by tests — the strings below are the entire feature, and a wrong flag
// fails only on someone else's machine.
func openArgs(goos string, b Browser, profile, url string) (string, []string) {
	// A Firefox profile is passed as the bare token after -P, so a name starting with "-"
	// would be read as another flag by Firefox's own parser. Dropping the profile and
	// launching plain is the safe degradation — profile names are read from profiles.ini,
	// which the user controls, but "their own file" is no reason to build a broken argv.
	// Chromium is immune: its profile rides inside a single --profile-directory=<dir> token.
	if b.Kind == KindFirefox && strings.HasPrefix(profile, "-") {
		profile = ""
	}
	switch goos {
	case "darwin":
		// Everything goes through open(1): it resolves the app by name wherever the bundle
		// lives, and detaches properly from this process.
		switch {
		case b.Kind == KindChromium && profile != "":
			// -n forces a new process: `open -a` with --args only applies the arguments when
			// it *launches* the app, so against a running Chrome the profile flag would be
			// silently dropped and the URL would open in whichever profile was frontmost —
			// the exact wrong-profile problem this feature exists to fix. Chromium's second
			// process notices the running instance, hands the URL to it under the named
			// profile, and exits.
			return "open", []string{"-na", b.Path, "--args", "--profile-directory=" + profile, url}
		case b.Kind == KindFirefox && profile != "":
			// Same -n reasoning. Firefox remoting is per-profile — on macOS since Firefox 73
			// (unified remoting, Mozilla bug 1565597; 67 only covered Windows/Linux) — so -P
			// against a running instance of that profile opens a tab there instead of erroring.
			return "open", []string{"-na", b.Path, "--args", "-P", profile, url}
		default:
			return "open", []string{"-a", b.Path, url}
		}
	case "windows":
		fallthrough
	default:
		// b.Path is directly executable here (an .exe path or a PATH-resolved binary).
		switch {
		case b.Kind == KindChromium && profile != "":
			return b.Path, []string{"--profile-directory=" + profile, url}
		case b.Kind == KindFirefox && profile != "":
			return b.Path, []string{"-P", profile, url}
		case b.Kind == KindSafari:
			// Safari exists only on macOS; reaching this arm means a stale saved preference
			// from another machine. Let the caller fall back rather than exec nothing.
			return "", nil
		default:
			return b.Path, []string{url}
		}
	}
}
