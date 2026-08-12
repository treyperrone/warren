package tui

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// bin has to be a real executable: wrapWithTmux runs `tmux new-session` for real and falls back
// silently when it fails, so a fake path would make the opt-in case look like the default one.
func headerCmd(t *testing.T, bin string) *exec.Cmd {
	t.Helper()
	m := newFormModel(t)
	// A real geometry matters: tmux new-session rejects -x 0 -y 0, and wrapWithTmux falls back
	// silently on any tmux failure, so a zero-sized model would fake a pass for the default case.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	inner := exec.Command(bin, "target", "{}")
	inner.Env = []string{"AWS_ACCESS_KEY_ID=AKIAX"}
	return m.wrapWithHeader(inner, "web-prod-01")
}

func isTmux(cmd *exec.Cmd) bool {
	return strings.Contains(cmd.Path, "tmux") || (len(cmd.Args) > 1 && cmd.Args[1] == "new-session") ||
		(len(cmd.Args) > 1 && cmd.Args[1] == "attach-session")
}

// tmux is what keeps the banner pinned, so it is used by default where available.
func TestSessionUsesTmuxByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no wrapper on windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv(TmuxVar, "")
	t.Setenv("TMUX", "")

	got := headerCmd(t, "/bin/sleep")
	if !isTmux(got) {
		t.Errorf("did not use tmux, so the banner will scroll away: %q", got.Args)
	}
}

// The command warren waits on must BE the session. The old version created it detached and made
// attach-session the foreground command, so the terminal was a client whose session it did not
// own — and destroying that session on exit took the terminal with it.
func TestTmuxRunsInForegroundOnAPrivateSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no wrapper on windows")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv(TmuxVar, "")
	t.Setenv("TMUX", "")

	got := headerCmd(t, "/bin/sleep")
	if containsExact(got.Args, "attach-session") {
		t.Error("still attaches to a session it does not own")
	}
	if containsExact(got.Args, "-d") {
		t.Error("created the session detached; it must run in the foreground")
	}
	if i := indexOf(got.Args, "-L"); i < 0 || i+1 >= len(got.Args) ||
		!strings.HasPrefix(got.Args[i+1], "warren-") {
		t.Errorf("not on a private socket, so it can disturb the user's own tmux: %q", got.Args)
	}
	if indexOf(got.Args, "new-session") < 0 {
		t.Errorf("no new-session in %q", got.Args)
	}
}

func TestSessionSkipsTmuxWhenDisabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no wrapper on windows")
	}
	t.Setenv("TMUX", "")
	t.Setenv(TmuxVar, "0")

	if got := headerCmd(t, "/bin/echo"); isTmux(got) {
		t.Errorf("%s=0 did not disable tmux: %q", TmuxVar, got.Args)
	}
}

// Inside tmux, attach-session exits 1 rather than nesting, which would leave the SSM session
// running in a detached session with the picker back on screen as if nothing had happened.
func TestSessionRefusesToNestInsideTmux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no wrapper on windows")
	}
	t.Setenv(TmuxVar, "")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1212078,5")

	if got := headerCmd(t, "/bin/echo"); isTmux(got) {
		t.Errorf("nested a tmux session inside an existing one: %q", got.Args)
	}
}

// Whatever the wrapper, the plugin and its arguments must survive: a session that loses its
// target or its credentials does not open.
func TestHeaderWrapperPreservesTheCommandAndEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no wrapper on windows")
	}
	t.Setenv(TmuxVar, "0")
	t.Setenv("TMUX", "")

	got := headerCmd(t, "/bin/echo")
	// The plugin path travels in _WARREN_BIN rather than argv: the script runs "$_WARREN_BIN" "$@",
	// so argv carries only its arguments. Search both rather than assuming which.
	joined := strings.Join(got.Args, "\x00") + "\x00" + strings.Join(got.Env, "\x00")
	for _, want := range []string{"/bin/echo", "target", "{}"} {
		if !strings.Contains(joined, want) {
			t.Errorf("wrapper dropped %q from args %q / env %q", want, got.Args, got.Env)
		}
	}
	if !strings.Contains(strings.Join(got.Env, "\n"), "AWS_ACCESS_KEY_ID=AKIAX") {
		t.Errorf("wrapper dropped the credentials: %q", got.Env)
	}
}
