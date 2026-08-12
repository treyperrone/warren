// Package termwin opens a command in a new terminal window, where the platform has a way to make
// one, and says so when it does not.
//
// The distinction matters more than it sounds. A window is created by a terminal emulator, which
// needs a display server to draw into; over SSH to a headless host there is none, so no process
// running there can produce a window at all. That is not a limitation worth hiding behind a
// half-working feature — Launch reports it, and the caller runs the command in place instead.
//
// Inside tmux the "window" is a tmux window, which is the only kind that exists on a headless host
// and is indistinguishable from the real thing for this purpose.
package termwin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Var lets the user name the command that opens a window, overriding detection. The command is run
// with the path to a script as its final argument, so anything that ends in "run this file" works:
//
//	WARREN_TERMINAL="wezterm start --"
//	WARREN_TERMINAL="kitty --hold"
//
// It exists because terminal detection cannot be exhaustive and being wrong is worse than being
// absent: someone on a setup we do not recognise can state it once rather than lose the feature.
const Var = "WARREN_TERMINAL"

// scriptTTL is how long a launch script is left on disk before being swept.
//
// The script cannot delete itself: `rm "$0"` on the first line relies on the shell having already
// buffered the whole file, which holds for small scripts and stops holding at exactly the size a
// session token pushes us to. So it is removed on a timer instead, and any script a crash left
// behind is swept by the next launch.
const scriptTTL = 2 * time.Minute

// Env is the environment Launch inspects, injected so the strategy choice is testable on any host.
type Env struct {
	GOOS     string
	Getenv   func(string) string
	LookPath func(string) (string, error)
}

// OSEnv is the real environment.
func OSEnv() Env {
	return Env{GOOS: runtimeGOOS, Getenv: os.Getenv, LookPath: exec.LookPath}
}

// Strategy is how a window will be opened: argv with the script path appended last.
type Strategy struct {
	// Name is for the message shown to the user and for tests. Empty means no window is possible.
	Name string
	Argv []string
}

// Available reports whether a window can be opened at all.
func (s Strategy) Available() bool { return s.Name != "" && len(s.Argv) > 0 }

// linuxTerminals are tried in order. Each entry's flag is the one that means "run this and nothing
// else"; they differ enough between emulators that a single flag would not do.
//
// -e is deprecated in gnome-terminal in favour of `--`, and x-terminal-emulator is Debian's
// alternatives symlink, which is why both a generic and specific entries appear.
var linuxTerminals = []struct {
	bin  string
	args []string
}{
	{"x-terminal-emulator", []string{"-e"}},
	{"gnome-terminal", []string{"--"}},
	{"konsole", []string{"-e"}},
	{"xfce4-terminal", []string{"-x"}},
	{"alacritty", []string{"-e"}},
	{"kitty", []string{}},
	{"wezterm", []string{"start", "--"}},
	{"xterm", []string{"-e"}},
}

// Choose picks how to open a window, or returns an unavailable Strategy.
//
// Order is deliberate. An explicit override wins over everything, because someone who set it knows
// their setup better than this list does. tmux comes next: if the user is already inside tmux, a new
// tmux window is what "a new window" means to them, and it works whether or not a display exists.
// Only then does a real emulator get considered, and only when there is a display to put it on.
func Choose(e Env) Strategy {
	if v := strings.TrimSpace(e.Getenv(Var)); v != "" {
		// Split on spaces rather than parsing a shell line: the value is an argv prefix, and
		// pretending to support quoting here would be a worse lie than not supporting it.
		return Strategy{Name: Var, Argv: strings.Fields(v)}
	}

	if e.Getenv("TMUX") != "" {
		if bin, err := e.LookPath("tmux"); err == nil {
			return Strategy{Name: "tmux", Argv: []string{bin, "new-window"}}
		}
	}

	switch e.GOOS {
	case "darwin":
		// `open -a <app> <file>` makes the app open the file in a new window, which for a
		// terminal means running it. osascript could do more but needs Automation permission,
		// and a permission prompt on first connect is a bad trade for a window.
		app := "Terminal"
		if strings.Contains(strings.ToLower(e.Getenv("TERM_PROGRAM")), "iterm") {
			app = "iTerm"
		}
		if bin, err := e.LookPath("open"); err == nil {
			return Strategy{Name: app, Argv: []string{bin, "-a", app}}
		}
	case "linux", "freebsd", "openbsd", "netbsd":
		// No display, no window. Checked before looking for an emulator, because the emulator
		// being installed says nothing about whether it can open anything.
		if e.Getenv("DISPLAY") == "" && e.Getenv("WAYLAND_DISPLAY") == "" {
			return Strategy{}
		}
		for _, t := range linuxTerminals {
			if bin, err := e.LookPath(t.bin); err == nil {
				return Strategy{Name: t.bin, Argv: append([]string{bin}, t.args...)}
			}
		}
	}
	return Strategy{}
}

// Launch runs cmd in a new terminal window. The bool reports whether a window was opened; false
// with a nil error means this environment cannot open one, which is the caller's cue to run the
// command in place rather than an error to report.
func Launch(e Env, cmd *exec.Cmd, title string) (bool, error) {
	s := Choose(e)
	if !s.Available() {
		return false, nil
	}

	dir, err := scriptDir()
	if err != nil {
		return false, err
	}
	sweep(dir)

	path, err := writeScript(dir, cmd, title)
	if err != nil {
		return false, err
	}

	argv := append(append([]string{}, s.Argv...), path)
	// tmux takes a window name, which is what makes several sessions distinguishable in the
	// status line. Inserted before the script path, which must stay last.
	if s.Name == "tmux" && title != "" {
		argv = append(argv[:len(argv)-1], "-n", title, path)
	}

	open := exec.Command(argv[0], argv[1:]...)
	// The launcher exits immediately, having handed off to the terminal; its own stdio must not
	// be wired to warren's, or its output would land on top of the TUI.
	open.Stdout, open.Stderr, open.Stdin = nil, nil, nil
	if err := open.Start(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("opening a window with %s: %w", s.Name, err)
	}
	// Reaped rather than left a zombie; the wait is for the launcher, not the session.
	go func() { _ = open.Wait() }()

	// The script carries a session token, so it does not sit around waiting to be swept by a
	// later run that may never come.
	go func() {
		time.Sleep(scriptTTL)
		_ = os.Remove(path)
	}()

	return true, nil
}

// writeScript renders cmd as a shell script for the terminal to run.
//
// A script rather than a command line because every emulator disagrees about how to accept one, and
// because the plugin's arguments contain JSON — quoting that through two layers of someone else's
// parsing is how it breaks on the argument that matters.
//
// It contains a session token, which is why it is written 0600 into the user's own cache directory
// and removed on a timer. That token is already visible in `ps` for the running plugin, so this
// does not create an exposure that did not exist; it does extend it to the filesystem briefly,
// which is the cost of the feature.
func writeScript(dir string, cmd *exec.Cmd, title string) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Written by warren to open a session in this window. Safe to delete.\n")
	if title != "" {
		fmt.Fprintf(&b, "printf '\\033]0;%s\\007'\n", title)
	}
	for _, kv := range cmd.Env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s\n", name, shellQuote(value))
	}
	// exec so the window's shell is replaced: closing the session closes the window, instead of
	// dropping the user into a shell they did not ask for.
	b.WriteString("exec")
	for _, a := range cmd.Args {
		b.WriteString(" " + shellQuote(a))
	}
	b.WriteString("\n")

	f, err := os.CreateTemp(dir, "session-*.sh")
	if err != nil {
		return "", fmt.Errorf("creating a launch script: %w", err)
	}
	name := f.Name()
	if err := f.Chmod(0o700); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("setting launch script permissions: %w", err)
	}
	_, writeErr := f.WriteString(b.String())
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(name)
		if writeErr != nil {
			return "", fmt.Errorf("writing a launch script: %w", writeErr)
		}
		return "", fmt.Errorf("writing a launch script: %w", closeErr)
	}
	return name, nil
}

// shellQuote renders s as a single-quoted shell word.
//
// Single quotes because nothing inside them is special to the shell — the plugin's arguments are
// JSON full of $, ", and \, all of which a double-quoted string would re-expand. An embedded single
// quote is closed, escaped, and reopened, which is the only way out of single quoting.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func scriptDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating a cache directory for launch scripts: %w", err)
	}
	dir := filepath.Join(base, "warren", "launch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	return dir, nil
}

// sweep removes launch scripts left behind by a run that died before its timer fired. Errors are
// ignored: a script that cannot be removed is not a reason to refuse to open a window.
func sweep(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > scriptTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
