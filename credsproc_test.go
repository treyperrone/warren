package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/testenv"
)

// credsErrorMessage is what turns an AWS error into the line warren creds prints on stderr —
// deterministic and network-free, unlike runCreds itself.
func TestCredsErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"login required", awsint.ErrLoginRequired, "needs a sign-in — run: warren login corp"},
		{"timeout", context.DeadlineExceeded, "timed out reaching AWS"},
		{"other", errors.New("boom"), "warren creds: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := credsErrorMessage("corp", c.err)
			if !strings.Contains(got, c.want) {
				t.Errorf("credsErrorMessage(%v) = %q, want it to contain %q", c.err, got, c.want)
			}
		})
	}
}

// runCredsStderr runs runCreds and returns (exit code, stderr).
func runCredsStderr(t *testing.T, ctx context.Context, args []string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := runCreds(ctx, args)
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func writeSSOSessionConfig(t *testing.T, name string) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[sso-session " + name + "]\nsso_start_url = https://ex.awsapps.com/start\nsso_region = us-east-1\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The bug: an unbounded context let a stalled network call (a flaky VPN half-opening a
// connection, say) hang the whole warren creds invocation forever, despite the documented
// promise that it's "strictly non-interactive... never a hang" — that promise only covered the
// browser-prompt path. runCreds now bounds its own context, so even a caller that never sets a
// deadline gets one. Proven with an ALREADY-expired incoming context: SilentToken has no cached
// token to work with, so it fails locally without a real network call either way — what this
// actually guards is that runCreds returns promptly and with a sane message rather than passing
// a cancelled context deeper and panicking or blocking on it.
func TestRunCredsDoesNotHangOnAnExpiredContext(t *testing.T) {
	writeSSOSessionConfig(t, "corp")

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	done := make(chan struct{})
	var code int
	var stderr string
	go func() {
		code, stderr = runCredsStderr(t, expired, []string{"--session", "corp", "--account", "111111111111", "--role", "AdminRole"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runCreds did not return within 5s on an already-expired context")
	}

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stderr == "" {
		t.Error("stderr is empty — a script gets no indication of what went wrong")
	}
}

func TestRunCredsUnknownSessionFailsFast(t *testing.T) {
	writeSSOSessionConfig(t, "corp")

	code, stderr := runCredsStderr(t, context.Background(), []string{"--session", "nope", "--account", "1", "--role", "R"})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, `no sso-session named "nope"`) {
		t.Errorf("stderr = %q, want it to name the missing session", stderr)
	}
}
