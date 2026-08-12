// Package awsexec runs a command with a selected AWS session's credentials in its
// environment.
//
// This is the answer to a constraint, not a design preference: a child process cannot modify
// its parent shell's environment, so warren can never simply "log your shell in". It can
// hand credentials to one process it starts. `exec` makes that process your command; `shell`
// makes it your shell. Either way the credentials live and die with that process — they are
// never written to disk and never enter your shell history.
package awsexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// SessionLabelVar names the selected session inside the child process. It is not read by the
// AWS SDK — it exists so a shell prompt can show which account it is pointed at, since an
// authenticated shell that looks identical to an unauthenticated one is how mistakes happen.
const SessionLabelVar = "WARREN_SESSION"

// Env builds the child environment: the parent's, minus every AWS variable this tool sets,
// plus the session's own.
//
// Stripping first is the point. Appending alone would leave a stale AWS_PROFILE from the
// user's shell sitting alongside the injected keys, and which one wins then depends on the
// SDK's provider order rather than on what was selected here. Removing them makes the child
// unambiguous.
// credOverride, when given, replaces the session's own credential variables — used to point the
// child at the local credential endpoint instead of handing it a copy that cannot be updated. The
// region and the session label are supplied either way.
func Env(parent []string, sess *awsint.Session, credOverride ...string) []string {
	strip := make(map[string]bool, len(awsint.EnvKeys()))
	for _, k := range awsint.EnvKeys() {
		strip[k] = true
	}
	strip[SessionLabelVar] = true

	out := make([]string, 0, len(parent)+len(awsint.EnvKeys())+1)
	for _, kv := range parent {
		// An entry without "=" is not a variable assignment; pass it through untouched
		// rather than guessing at it.
		name, _, ok := strings.Cut(kv, "=")
		if ok && strip[name] {
			continue
		}
		out = append(out, kv)
	}

	if len(credOverride) > 0 {
		out = append(out, credOverride...)
	} else {
		out = append(out, sess.CredentialEnv()...)
	}
	out = append(out, sess.RegionEnv()...)

	if sess.Label != "" {
		out = append(out, SessionLabelVar+"="+sess.Label)
	}
	return out
}

// runnerScript runs the user's command line and then explains what happened.
//
// Two problems it solves, both consequences of being launched from a full-screen TUI:
//
// The output would otherwise be invisible. bubbletea leaves the alt screen to run a command and
// re-enters it immediately afterwards, so anything the command printed is hidden the moment the
// TUI comes back — output appeared to flash past and vanish. Waiting for a keypress *inside this
// process* is the fix, because by the time the parent regains control the screen is already gone.
//
// And "no output" reads identically to "it silently failed". A CLI is right to say nothing when a
// list is empty, but `aws s3 ls` against an account with no buckets then looks exactly like a
// broken command. Reporting it explicitly costs nothing and removes the ambiguity.
//
// The command's own output is teed to a file purely to learn whether there was any; it still goes
// straight to the terminal as it is produced, so streaming commands keep streaming. The exit
// status travels through a second file because a POSIX pipeline reports the status of its last
// stage, which here is tee.
const runnerScript = `
printf '\033[38;5;99m$ \033[0m%s\n' "$_WARREN_CMD"

captured=""
if tmpdir=$(mktemp -d 2>/dev/null); then
	captured="$tmpdir/out"
	{ eval "$_WARREN_CMD"; echo $? > "$tmpdir/status"; } 2>&1 | tee "$captured"
	status=$(cat "$tmpdir/status" 2>/dev/null)
	bytes=$(wc -c < "$captured" 2>/dev/null | tr -d ' \n')
else
	# No temp dir: still run, but claim nothing about emptiness we cannot measure.
	eval "$_WARREN_CMD" 2>&1
	status=$?
	bytes=1
fi

[ -n "$status" ] || status=1
[ -n "$bytes" ] || bytes=1

# AWS's own message is passed through untouched above, and it is usually specific enough to act
# on. So say what the status was and then stay quiet: a guess at the cause competes with an
# accurate message already on screen, and is wrong often enough to mislead. The two hints below
# are the exceptions, and both add something AWS cannot say rather than restating it.
if [ "$status" -eq 130 ]; then
	printf '\033[38;5;240mStopped.\033[0m\n'
elif [ "$status" -ne 0 ]; then
	printf '\033[38;5;203mCommand failed (exit %s).\033[0m\n' "$status"
	if [ -n "$captured" ] && grep -q 'not authorized to perform this operation' "$captured" 2>/dev/null; then
		# AWS deliberately does not reveal org structure in a denial, so an SCP block arrives as
		# a bare AccessDenied naming no action — indistinguishable from a role gap by message
		# alone. Hence "often", not "is".
		printf '\033[38;5;240m%s\033[0m\n' \
			"Denied without naming an action — often a policy above this account (an SCP) rather than this role."
	elif [ -n "$captured" ] && grep -qE 'ExpiredToken|InvalidClientTokenId|token included in the request is expired' "$captured" 2>/dev/null; then
		# What to do about it is specific to this tool, so AWS could not have told you.
		printf '\033[38;5;240m%s\033[0m\n' \
			"These credentials are no longer valid — press esc to go back and pick the role again."
	fi
elif [ "$bytes" -eq 0 ]; then
	printf '\033[38;5;240m%s\033[0m\n' \
		"No output. The command succeeded, so this account simply has none of what you asked for."
fi

[ -n "$tmpdir" ] && rm -rf "$tmpdir"

if [ -t 0 ]; then
	printf '\033[38;5;240mPress enter to return to warren.\033[0m'
	read -r _ 2>/dev/null || true
	printf '\n'
fi

exit "$status"
`

// CommandLine builds a command from a shell command line rather than an argv, so a string the
// user can edit behaves the way it would if typed: pipes, quoting, and redirection all work.
// The alternative — parsing the line into an argv ourselves — means reimplementing shell
// word-splitting, and getting it subtly wrong on exactly the quoting a real command needs.
//
// The line is echoed before it runs, so what executed is on screen above its output. It is
// passed through the environment rather than interpolated into the script, which keeps a quote or
// a `$` in the command from being re-expanded or from breaking the script around it.
//
// This is for the interactive builder. `exec` uses Run instead and stays silent, because there
// stdout belongs to the caller's pipeline and a friendly footer would be corruption.
func CommandLine(sess *awsint.Session, line string, credOverride ...string) (*exec.Cmd, error) {
	if strings.TrimSpace(line) == "" {
		return nil, errors.New("no command given")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// No echo, footer, or pause: cmd.exe already echoes unless told otherwise, and its
		// quoting rules do not survive the same treatment.
		cmd = exec.Command("cmd.exe", "/C", line)
	} else {
		cmd = exec.Command("/bin/sh", "-c", runnerScript)
	}

	cmd.Env = append(Env(os.Environ(), sess, credOverride...), "_WARREN_CMD="+line)
	return cmd, nil
}

// ShellArgv is the command for an interactive shell, honouring $SHELL.
func ShellArgv() []string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return []string{sh}
	}
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe"}
	}
	return []string{"/bin/sh"}
}

// Command builds argv as a command with the session's credentials injected, leaving stdio
// unset so the caller can decide. tea.ExecProcess wires stdio to the terminal itself, which
// is how the TUI hands a credentialed shell to the user and gets control back afterwards.
func Command(sess *awsint.Session, argv []string, credOverride ...string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, errors.New("no command given")
	}

	// Resolved up front so a missing binary is reported as such, rather than surfacing later
	// as an opaque failure from whatever ran it.
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}

	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = Env(os.Environ(), sess, credOverride...)
	return cmd, nil
}

// Run runs argv with the session's credentials injected, wired to the real terminal, and
// returns the command's own exit code.
//
// The exit code is propagated rather than collapsed to 0/1 so that `warren exec -- aws ...`
// can be used in a script or a && chain and behave like the command it wrapped.
func Run(sess *awsint.Session, argv []string, credOverride ...string) (int, error) {
	cmd, err := Command(sess, argv, credOverride...)
	if err != nil {
		if len(argv) == 0 {
			return 1, err
		}
		// 127 is the shell's convention for "command not found".
		return 127, err
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// The command ran and failed on its own terms — that is its exit code to report, not
		// an warren error to wrap.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
