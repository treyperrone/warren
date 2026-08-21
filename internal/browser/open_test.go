package browser

import (
	"reflect"
	"testing"
)

// The argv strings below ARE the feature: a wrong flag here fails only on someone else's
// machine, in a browser this CI does not have, so every OS × kind × profile combination is
// pinned exactly.
func TestOpenArgs(t *testing.T) {
	const url = "https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-1234"
	chromeMac := Browser{Name: "Google Chrome", Kind: KindChromium, Path: "Google Chrome"}
	firefoxMac := Browser{Name: "Firefox", Kind: KindFirefox, Path: "Firefox"}
	safari := Browser{Name: "Safari", Kind: KindSafari, Path: "Safari"}
	chromeLinux := Browser{Name: "Google Chrome", Kind: KindChromium, Path: "/usr/bin/google-chrome"}
	firefoxWin := Browser{Name: "Firefox", Kind: KindFirefox, Path: `C:\Program Files\Mozilla Firefox\firefox.exe`}
	edgeWin := Browser{Name: "Microsoft Edge", Kind: KindChromium, Path: `C:\PF\msedge.exe`}

	cases := []struct {
		name     string
		goos     string
		b        Browser
		profile  string
		wantCmd  string
		wantArgs []string
	}{
		// -na, not -a: `open -a` only applies --args when it launches the app, so against a
		// running Chrome the profile flag is dropped and the URL lands in the frontmost
		// profile — the exact wrong-profile problem the feature exists to fix.
		{"mac chrome with profile", "darwin", chromeMac, "Profile 1",
			"open", []string{"-na", "Google Chrome", "--args", "--profile-directory=Profile 1", url}},
		{"mac chrome no profile", "darwin", chromeMac, "",
			"open", []string{"-a", "Google Chrome", url}},
		{"mac firefox with profile", "darwin", firefoxMac, "Work",
			"open", []string{"-na", "Firefox", "--args", "-P", "Work", url}},
		{"mac safari", "darwin", safari, "",
			"open", []string{"-a", "Safari", url}},
		{"linux chrome with profile", "linux", chromeLinux, "Default",
			"/usr/bin/google-chrome", []string{"--profile-directory=Default", url}},
		{"linux chrome no profile", "linux", chromeLinux, "",
			"/usr/bin/google-chrome", []string{url}},
		{"windows edge with profile", "windows", edgeWin, "Profile 2",
			`C:\PF\msedge.exe`, []string{"--profile-directory=Profile 2", url}},
		{"windows firefox with profile", "windows", firefoxWin, "Work",
			`C:\Program Files\Mozilla Firefox\firefox.exe`, []string{"-P", "Work", url}},
		// A Safari preference that travelled to a non-Mac must refuse to build a command,
		// so OpenForLogin can fall back instead of exec-ing an empty string.
		{"safari off mac", "linux", safari, "", "", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, args := openArgs(c.goos, c.b, c.profile, url)
			if cmd != c.wantCmd || !reflect.DeepEqual(args, c.wantArgs) {
				t.Errorf("openArgs(%s) = %q %v\nwant %q %v", c.name, cmd, args, c.wantCmd, c.wantArgs)
			}
		})
	}
}

func TestHeadless(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		// SSH beats everything, on every OS: `open` on a Mac administered over SSH opens
		// the page on the console — a signed-in browser on a screen the user is not at.
		{"ssh into mac", "darwin", map[string]string{"SSH_CONNECTION": "10.0.0.1 50000 10.0.0.2 22"}, true},
		{"ssh tty only", "linux", map[string]string{"SSH_TTY": "/dev/pts/0", "DISPLAY": ":0"}, true},
		{"mac desktop", "darwin", nil, false},
		{"windows desktop", "windows", nil, false},
		{"linux with x11", "linux", map[string]string{"DISPLAY": ":0"}, false},
		{"linux with wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-1"}, false},
		{"linux console", "linux", nil, true},
		{"freebsd console", "freebsd", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Headless(c.goos, env(c.env)); got != c.want {
				t.Errorf("Headless(%s, %v) = %v, want %v", c.goos, c.env, got, c.want)
			}
		})
	}
}

// A Firefox profile named like a flag would be read as one by Firefox's parser (-P takes the
// next bare token); the launch degrades to profile-less rather than building a broken argv.
func TestOpenArgsRefusesFlagShapedFirefoxProfile(t *testing.T) {
	ff := Browser{Name: "Firefox", Kind: KindFirefox, Path: "/usr/bin/firefox"}
	cmd, args := openArgs("linux", ff, "-headless", "https://example.test")
	if cmd != "/usr/bin/firefox" || len(args) != 1 || args[0] != "https://example.test" {
		t.Fatalf("openArgs with flag-shaped profile = %q %v, want a plain profile-less launch", cmd, args)
	}
}
