package browser

// Headless reports whether launching a GUI browser on this machine cannot reach the user, so
// the sign-in should show the URL and device code instead of silently opening a window nobody
// is looking at.
//
// The SSH check runs on every OS, and it matters most on the graphical ones: `open` on a Mac
// being administered over SSH happily opens the page on the *console* — a signed-in browser
// window on a screen the user is not in front of, which is worse than doing nothing. On Linux
// the display variables are the ground truth; macOS and Windows sessions always have a display
// of their own.
//
// goos and getenv are parameters for the same reason detection injects its probes: every
// interesting case here is "an environment this test machine is not".
func Headless(goos string, getenv func(string) string) bool {
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return true
	}
	switch goos {
	case "darwin", "windows":
		return false
	}
	return getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == ""
}
