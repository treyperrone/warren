package termwin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requireSh skips a test that runs a generated script. The scripts are /bin/sh, which Windows has
// no equivalent of — and it needs none: Choose reports no window on Windows, so Launch returns
// before writing a script at all. TestWindowsReportsNoWindow is what covers that contract.
func requireSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("generated scripts are /bin/sh; Windows never reaches the code that writes one")
	}
}

// noOutputCmd is a command that succeeds and prints nothing, for tests that assert on exactly what
// the script itself emits.
//
// Not /bin/true: that path does not exist on macOS, where true lives in /usr/bin — which cost a CI
// run to discover. /bin/sh is required by POSIX to be there, so the shell is the portable choice.
func noOutputCmd() *exec.Cmd {
	return exec.Command("/bin/sh", "-c", ":")
}

// fakeEnv builds an Env with a fixed platform, a fixed set of variables, and a fixed set of
// binaries that exist. Nothing here touches the real host, so the same cases run on every runner.
func fakeEnv(goos string, vars map[string]string, present ...string) Env {
	has := map[string]bool{}
	for _, p := range present {
		has[p] = true
	}
	return Env{
		GOOS:   goos,
		Getenv: func(k string) string { return vars[k] },
		LookPath: func(bin string) (string, error) {
			if has[bin] {
				return "/usr/bin/" + bin, nil
			}
			return "", errors.New("not found")
		},
	}
}

// The case that started this: warren over SSH to a headless box. There is no display, so no
// process there can make a window, and saying so is the whole point — a half-working spawn that
// silently does nothing would be worse than reporting it.
func TestNoWindowWithoutADisplay(t *testing.T) {
	e := fakeEnv("linux", map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22"},
		"xterm", "gnome-terminal", "tmux")

	if s := Choose(e); s.Available() {
		t.Errorf("Choose returned %q with no DISPLAY; an emulator being installed does not mean it can open anything", s.Name)
	}
}

func TestDisplayEnablesAnEmulator(t *testing.T) {
	e := fakeEnv("linux", map[string]string{"DISPLAY": ":0"}, "xterm")
	s := Choose(e)
	if !s.Available() {
		t.Fatal("Choose found no way to open a window with DISPLAY set and xterm present")
	}
	if s.Name != "xterm" {
		t.Errorf("chose %q, want xterm", s.Name)
	}
}

// Wayland-only sessions have no DISPLAY, and treating that as headless would disable the feature
// on a machine that plainly has a screen.
func TestWaylandCountsAsADisplay(t *testing.T) {
	e := fakeEnv("linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "kitty")
	if !Choose(e).Available() {
		t.Error("Choose found no way to open a window under Wayland")
	}
}

func TestDarwinUsesOpen(t *testing.T) {
	s := Choose(fakeEnv("darwin", nil, "open"))
	if !s.Available() {
		t.Fatal("Choose found no way to open a window on macOS")
	}
	joined := strings.Join(s.Argv, " ")
	for _, want := range []string{"open", "-a", "Terminal"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q does not contain %q", joined, want)
		}
	}
}

// macOS has a window server whether or not anyone is over SSH, but the emulator check for Linux
// keys off DISPLAY; darwin must not accidentally inherit that gate.
func TestDarwinDoesNotNeedDisplay(t *testing.T) {
	if !Choose(fakeEnv("darwin", map[string]string{"SSH_CONNECTION": "x"}, "open")).Available() {
		t.Error("darwin refused to open a window because DISPLAY was unset")
	}
}

func TestITermPreferredWhenRunningInIt(t *testing.T) {
	e := fakeEnv("darwin", map[string]string{"TERM_PROGRAM": "iTerm.app"}, "open")
	if got := Choose(e).Name; got != "iTerm" {
		t.Errorf("chose %q inside iTerm, want iTerm", got)
	}
}

// Inside tmux, a new tmux window *is* what "a new window" means — and it is the only kind
// available on a headless host, so it must win over the absence of a display.
func TestTmuxWinsWhenInsideTmux(t *testing.T) {
	e := fakeEnv("linux", map[string]string{"TMUX": "/tmp/tmux-1000/default,123,0"}, "tmux", "xterm")
	s := Choose(e)
	if s.Name != "tmux" {
		t.Fatalf("chose %q inside tmux, want tmux", s.Name)
	}
	if !strings.Contains(strings.Join(s.Argv, " "), "new-window") {
		t.Errorf("argv %v does not create a new window", s.Argv)
	}
}

// Detection cannot be exhaustive, so someone on an unrecognised setup must be able to state it —
// and their statement has to beat every guess this package would otherwise make.
func TestOverrideBeatsEverything(t *testing.T) {
	e := fakeEnv("darwin", map[string]string{
		Var:    "wezterm start --",
		"TMUX": "/tmp/tmux",
	}, "open", "tmux")

	s := Choose(e)
	if s.Name != Var {
		t.Fatalf("chose %q, want the %s override to win", s.Name, Var)
	}
	if got := strings.Join(s.Argv, " "); got != "wezterm start --" {
		t.Errorf("argv = %q, want the override split into words", got)
	}
}

// An override set to whitespace is a misconfiguration, not a request to run "" as a terminal.
func TestBlankOverrideIsIgnored(t *testing.T) {
	e := fakeEnv("darwin", map[string]string{Var: "   "}, "open")
	if got := Choose(e).Name; got != "Terminal" {
		t.Errorf("chose %q with a blank override, want detection to proceed", got)
	}
}

// Windows has no /bin/sh script path and is handled by the caller, so it must report no window
// rather than emit something unrunnable.
func TestWindowsReportsNoWindow(t *testing.T) {
	if Choose(fakeEnv("windows", nil, "open", "xterm")).Available() {
		t.Error("Choose claimed it could open a window on windows")
	}
}

// The plugin's arguments are JSON containing $, ", and \, all of which a double-quoted shell
// string would re-expand. Getting this wrong corrupts the one argument the session depends on.
func TestShellQuoteSurvivesJSON(t *testing.T) {
	requireSh(t)
	payload := `{"SessionId":"s-1","TokenValue":"a$b\"c\\d","Stream":"wss://x?y=1&z=2"}`
	script := shellQuote(payload)

	out, err := exec.Command("/bin/sh", "-c", "printf %s "+script).Output()
	if err != nil {
		t.Fatalf("sh could not run the quoted word: %v", err)
	}
	if string(out) != payload {
		t.Errorf("round trip changed the payload:\n got %s\nwant %s", out, payload)
	}
}

// A single quote is the one character single-quoting cannot contain, and the plugin's argv does
// carry apostrophes in practice.
func TestShellQuoteHandlesASingleQuote(t *testing.T) {
	requireSh(t)
	in := `it's a "test" $HOME`
	out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(in)).Output()
	if err != nil {
		t.Fatalf("sh could not run the quoted word: %v", err)
	}
	if string(out) != in {
		t.Errorf("round trip changed the value:\n got %s\nwant %s", out, in)
	}
}

func TestScriptRunsTheCommandWithItsEnvironment(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `printf '%s|%s' "$WARREN_SESSION" "$1"`, "sh", "hello world")
	cmd.Env = []string{"WARREN_SESSION=acct/Role", "PATH=" + os.Getenv("PATH")}

	// No title: the title is an escape sequence written to stdout, which would otherwise be
	// mixed into what this test is reading. TestScriptSetsTheWindowTitle covers it separately.
	path, err := writeScript(dir, cmd, "")
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("/bin/sh", path).Output()
	if err != nil {
		t.Fatalf("running the generated script: %v", err)
	}
	if got := string(out); got != "acct/Role|hello world" {
		t.Errorf("script produced %q; the environment or the argv did not survive", got)
	}
}

// With several sessions open at once, the window title is the only thing distinguishing them —
// which is the point of opening them in separate windows in the first place.
func TestScriptSetsTheWindowTitle(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	cmd := noOutputCmd()
	cmd.Env = []string{}

	path, err := writeScript(dir, cmd, "goad-dc01")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", path).Output()
	if err != nil {
		t.Fatalf("running the generated script: %v", err)
	}
	// OSC 0 — set window title — terminated by BEL.
	if want := "\x1b]0;goad-dc01\a"; string(out) != want {
		t.Errorf("script emitted %q, want the title sequence %q", out, want)
	}
}

// exec, so closing the session closes the window rather than leaving a shell the user did not ask
// for sitting on top of a dead session.
func TestScriptExecsRatherThanReturning(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/echo", "hi")
	cmd.Env = []string{}

	path, err := writeScript(dir, cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "exec ") {
		t.Errorf("script does not exec:\n%s", data)
	}
}

// The script holds a session token, so its mode is part of the contract, not an incidental.
func TestScriptIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not meaningful on Windows; ACLs govern access there")
	}
	dir := t.TempDir()
	cmd := exec.Command("/bin/echo", "hi")
	cmd.Env = []string{"AWS_SESSION_TOKEN=secret"}

	path, err := writeScript(dir, cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("launch script mode is %04o; it carries a session token and must not be group or world readable", perm)
	}
}

// A run that dies before its timer fires leaves a token on disk. The next launch has to clear it,
// or they accumulate for as long as the cache directory survives.
func TestSweepRemovesStaleScriptsButKeepsFreshOnes(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, "session-stale.sh")
	fresh := filepath.Join(dir, "session-fresh.sh")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * scriptTTL)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	sweep(dir)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("sweep left a stale launch script, and it holds a session token")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("sweep removed a script that a window may not have started reading yet")
	}
}

// Launch's contract: false with a nil error means "this environment cannot", which is the caller's
// cue to run in place. It must not be reported as a failure.
func TestLaunchReportsNoWindowWithoutError(t *testing.T) {
	e := fakeEnv("linux", nil) // no display, no terminals
	ok, err := Launch(e, exec.Command("/bin/echo", "hi"), "x")
	if err != nil {
		t.Errorf("Launch errored where it should simply report no window: %v", err)
	}
	if ok {
		t.Error("Launch claimed it opened a window with no display and no emulator")
	}
}

// The script path has to be the last argument, because that is the contract every entry in the
// table and the override are written against.
func TestScriptPathGoesLast(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/echo", "hi")
	cmd.Env = []string{}
	path, err := writeScript(dir, cmd, "")
	if err != nil {
		t.Fatal(err)
	}

	s := Choose(fakeEnv("darwin", nil, "open"))
	argv := append(append([]string{}, s.Argv...), path)
	if argv[len(argv)-1] != path {
		t.Errorf("script path is not last in %v", argv)
	}
}
