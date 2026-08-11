package awsexec

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	awsint "github.com/treyperrone/postern/internal/aws"
)

func envMap(t *testing.T, pairs []string) map[string]string {
	t.Helper()
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// The bug this guards: appending the session's variables without stripping the parent's would
// leave the user's own AWS_PROFILE in the child alongside the injected keys, and which one
// wins would depend on the consuming SDK's provider order rather than on what was picked.
func TestEnvStripsInheritedAWSVariables(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"AWS_PROFILE=someone-elses-profile",
		"AWS_ACCESS_KEY_ID=STALE",
		"AWS_SECRET_ACCESS_KEY=stale",
		"AWS_SESSION_TOKEN=stale",
		"AWS_REGION=ap-southeast-2",
		"AWS_DEFAULT_REGION=ap-southeast-2",
		"HOME=/home/trey",
	}
	sess := &awsint.Session{
		AccessKeyID:     "AKIANEW",
		SecretAccessKey: "new",
		SessionToken:    "newtoken",
		Region:          "us-east-1",
		Label:           "crlab (123456789012)/AdminRole",
	}

	got := envMap(t, Env(parent, sess))

	if got["AWS_PROFILE"] != "" {
		t.Errorf("AWS_PROFILE = %q, want it stripped", got["AWS_PROFILE"])
	}
	for k, want := range map[string]string{
		"AWS_ACCESS_KEY_ID":  "AKIANEW",
		"AWS_REGION":         "us-east-1",
		"AWS_DEFAULT_REGION": "us-east-1",
		"PATH":               "/usr/bin",
		"HOME":               "/home/trey",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// The label is how a prompt can show which account the shell is pointed at.
	if got[SessionLabelVar] != sess.Label {
		t.Errorf("%s = %q, want %q", SessionLabelVar, got[SessionLabelVar], sess.Label)
	}
}

// A stale label from an outer postern shell must not survive into a nested one, or the prompt
// names the wrong account.
func TestEnvReplacesStaleSessionLabel(t *testing.T) {
	parent := []string{SessionLabelVar + "=old-account/OldRole"}
	sess := &awsint.Session{AccessKeyID: "AKIA", SecretAccessKey: "s", Label: "new-account/NewRole"}

	got := envMap(t, Env(parent, sess))
	if got[SessionLabelVar] != "new-account/NewRole" {
		t.Errorf("%s = %q, want the new label", SessionLabelVar, got[SessionLabelVar])
	}
	if n := strings.Count(strings.Join(Env(parent, sess), "\n"), SessionLabelVar+"="); n != 1 {
		t.Errorf("%s appears %d times, want exactly 1", SessionLabelVar, n)
	}
}

// Entries without "=" are not assignments; mangling them would corrupt the child's
// environment for reasons unrelated to AWS.
func TestEnvPassesThroughMalformedEntries(t *testing.T) {
	got := Env([]string{"NOT_AN_ASSIGNMENT"}, &awsint.Session{})
	for _, kv := range got {
		if kv == "NOT_AN_ASSIGNMENT" {
			return
		}
	}
	t.Errorf("malformed entry was dropped from %v", got)
}

func TestShellArgvHonoursShellEnv(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	if got := ShellArgv(); len(got) != 1 || got[0] != "/usr/bin/fish" {
		t.Errorf("ShellArgv() = %v, want [/usr/bin/fish]", got)
	}
}

func TestShellArgvFallsBackWithoutShellEnv(t *testing.T) {
	t.Setenv("SHELL", "")
	got := ShellArgv()
	if len(got) != 1 {
		t.Fatalf("ShellArgv() = %v, want one element", got)
	}
	want := "/bin/sh"
	if runtime.GOOS == "windows" {
		want = "cmd.exe"
	}
	if got[0] != want {
		t.Errorf("ShellArgv() = %v, want [%s]", got, want)
	}
}

func TestCommandLineRejectsBlankInput(t *testing.T) {
	for _, line := range []string{"", "   ", "\t\n"} {
		if _, err := CommandLine(&awsint.Session{}, line); err == nil {
			t.Errorf("CommandLine(%q) returned nil error", line)
		}
	}
}

// The line runs through a shell, so pipes and redirection work the way they would if typed —
// which is what makes the built command editable into something useful.
func TestCommandLineRunsThroughAShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available")
	}
	cmd, err := CommandLine(&awsint.Session{Region: "us-east-1"}, "echo hello | tr a-z A-Z")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(out.String(), "HELLO") {
		t.Errorf("output %q does not contain the piped result", out.String())
	}
	// The command is echoed so what ran is visible and stays in scrollback.
	if !strings.Contains(out.String(), "echo hello | tr a-z A-Z") {
		t.Errorf("output %q does not echo the command", out.String())
	}
}

// The line is passed through the environment rather than interpolated into the script. If it
// were interpolated, a quote in the command could break the script around it and a `$` could be
// expanded a second time before the command ever saw it.
func TestCommandLineDoesNotDoubleExpand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available")
	}
	cmd, err := CommandLine(&awsint.Session{}, `echo '$HOME is safe'`)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(out.String(), "$HOME is safe") {
		t.Errorf("output %q — $HOME was expanded when it should have stayed literal", out.String())
	}
}

// runLine runs a command line through CommandLine and returns everything it printed plus the
// exit code. Stdin is left unset, so the script's `[ -t 0 ]` pause is skipped.
func runLine(t *testing.T, line string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available")
	}

	cmd, err := CommandLine(&awsint.Session{Region: "us-east-1"}, line)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out

	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), code
}

// The report this guards: a command that returns nothing looks exactly like one that silently
// failed. `aws s3 ls` on an account with no buckets is the case that prompted it.
func TestCommandLineReportsEmptyOutput(t *testing.T) {
	out, code := runLine(t, "true")

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "No output") {
		t.Errorf("empty result was not explained:\n%s", out)
	}
}

// ...and must not say that when there was output, or the message becomes noise to ignore.
func TestCommandLineStaysQuietWhenThereIsOutput(t *testing.T) {
	out, code := runLine(t, "echo one bucket")

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "one bucket") {
		t.Errorf("output missing:\n%s", out)
	}
	if strings.Contains(out, "No output") {
		t.Errorf("claimed there was no output when there was:\n%s", out)
	}
}

// Output written only to stderr still counts as output — a warning is something to read, not an
// empty result.
func TestCommandLineTreatsStderrAsOutput(t *testing.T) {
	out, _ := runLine(t, "echo a warning >&2")

	if !strings.Contains(out, "a warning") {
		t.Errorf("stderr was lost:\n%s", out)
	}
	if strings.Contains(out, "No output") {
		t.Errorf("stderr-only output was reported as empty:\n%s", out)
	}
}

// A failure names its exit code and points at the message, rather than being reported as an empty
// result — the two are indistinguishable on screen otherwise.
func TestCommandLineReportsFailure(t *testing.T) {
	out, code := runLine(t, "sh -c 'echo denied >&2; exit 3'")

	if code != 3 {
		t.Errorf("exit code = %d, want 3 — the command's own status must survive", code)
	}
	if !strings.Contains(out, "exit 3") {
		t.Errorf("failure not reported:\n%s", out)
	}
	if strings.Contains(out, "No output") {
		t.Errorf("a failure was described as an empty result:\n%s", out)
	}
}

// AWS's own message is the authoritative explanation, so it has to arrive byte for byte —
// error code, wording and all. Anything postern adds goes below it, never instead of it.
func TestCommandLineReportsAWSErrorVerbatim(t *testing.T) {
	const awsErr = "An error occurred (InvalidAccessKeyId) when calling the ListBuckets " +
		"operation: The AWS Access Key Id you provided does not exist in our records."

	out, code := runLine(t, "sh -c 'echo \""+awsErr+"\" >&2; exit 254'")

	if !strings.Contains(out, awsErr) {
		t.Errorf("AWS message was altered or lost:\n%s", out)
	}
	if code != 254 {
		t.Errorf("exit code = %d, want 254", code)
	}
}

// When AWS has already said precisely what was wrong, adding a guess competes with an accurate
// message. This one is neither a permission problem nor an expiry, so nothing may claim it is.
func TestCommandLineDoesNotGuessAtAClearError(t *testing.T) {
	out, _ := runLine(t, "sh -c 'echo \"An error occurred (InvalidAccessKeyId): nope\" >&2; exit 254'")

	for _, phrase := range []string{"permission", "SCP", "no longer valid", "expired"} {
		if strings.Contains(out, phrase) {
			t.Errorf("footer speculated about %q over a clear AWS message:\n%s", phrase, out)
		}
	}
}

// An SCP block arrives as a bare AccessDenied naming no action, because AWS will not reveal org
// structure — so it is indistinguishable from a role gap by message alone. This is the one place
// worth a hint, and it has to read as a possibility rather than a diagnosis.
func TestCommandLineHintsAtSCPOnAnUnnamedDenial(t *testing.T) {
	out, _ := runLine(t,
		"sh -c 'echo \"An error occurred (AccessDenied): You are not authorized to perform this operation.\" >&2; exit 254'")

	if !strings.Contains(out, "SCP") {
		t.Errorf("no hint for a denial that names no action:\n%s", out)
	}
	if !strings.Contains(out, "often") {
		t.Errorf("hint states the cause as fact; AWS does not tell us that:\n%s", out)
	}
}

// A denial that *does* name the action needs no hint — AWS already said which permission.
func TestCommandLineStaysQuietWhenTheActionIsNamed(t *testing.T) {
	out, _ := runLine(t,
		"sh -c 'echo \"AccessDenied: User ... is not authorized to perform: s3:ListAllMyBuckets\" >&2; exit 254'")

	if !strings.Contains(out, "s3:ListAllMyBuckets") {
		t.Errorf("AWS message lost:\n%s", out)
	}
	if strings.Contains(out, "SCP") {
		t.Errorf("guessed at an SCP when the action was named:\n%s", out)
	}
}

// Expired credentials are the one case where the useful next step is specific to this tool, so
// AWS could not have mentioned it.
func TestCommandLineHintsHowToRecoverExpiredCredentials(t *testing.T) {
	out, _ := runLine(t,
		"sh -c 'echo \"An error occurred (ExpiredToken): The security token has expired\" >&2; exit 255'")

	if !strings.Contains(out, "esc") {
		t.Errorf("no recovery hint for expired credentials:\n%s", out)
	}
}

// A command that produces nothing *and* fails is a failure, not an empty result.
func TestCommandLineFailureBeatsEmptiness(t *testing.T) {
	out, code := runLine(t, "false")

	if code == 0 {
		t.Error("exit code = 0, want non-zero")
	}
	if strings.Contains(out, "No output") {
		t.Errorf("silent failure was described as an empty result:\n%s", out)
	}
}

// Interrupting a streaming command is a normal way to stop it, so 130 must not be dressed up as a
// failure.
func TestCommandLineTreatsInterruptAsStopped(t *testing.T) {
	out, _ := runLine(t, "sh -c 'exit 130'")

	if !strings.Contains(out, "Stopped") {
		t.Errorf("interrupt not reported as stopped:\n%s", out)
	}
	if strings.Contains(out, "failed") {
		t.Errorf("interrupt reported as a failure:\n%s", out)
	}
}

func TestRunRejectsEmptyArgv(t *testing.T) {
	if _, err := Run(&awsint.Session{}, nil); err == nil {
		t.Error("Run with no command returned nil error")
	}
}

func TestRunReportsMissingCommand(t *testing.T) {
	// 127 is the shell convention for "command not found", which is what a caller chaining
	// this in a script will expect.
	code, err := Run(&awsint.Session{}, []string{"postern-no-such-binary-xyz"})
	if err == nil {
		t.Error("missing command returned nil error")
	}
	if code != 127 {
		t.Errorf("exit code = %d, want 127", code)
	}
}

// The wrapped command's exit code is its own to report: collapsing it to 0/1 would break
// `postern exec -- aws ... && next`.
func TestRunPropagatesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available")
	}
	code, err := Run(&awsint.Session{}, []string{"sh", "-c", "exit 3"})
	if err != nil {
		t.Fatalf("Run returned an error for a command that ran and failed: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestRunInjectsCredentialsIntoChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available")
	}
	sess := &awsint.Session{
		AccessKeyID:     "AKIACHILD",
		SecretAccessKey: "s",
		SessionToken:    "t",
		Region:          "eu-central-1",
	}
	// Exits non-zero unless the child really saw the injected values.
	code, err := Run(sess, []string{"sh", "-c",
		`test "$AWS_ACCESS_KEY_ID" = AKIACHILD && test "$AWS_REGION" = eu-central-1`})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("child did not see the injected credentials (exit %d)", code)
	}
}
