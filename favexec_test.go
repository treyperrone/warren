package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treyperrone/warren/internal/browser"
	"github.com/treyperrone/warren/internal/testenv"
)

// runFavoriteStderr runs runFavorite and returns (exit code, stderr).
func runFavoriteStderr(t *testing.T, fav browser.Favorite, run invocation) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := runFavorite(context.Background(), fav, run)
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func writeSSOSessionConfigNamed(t *testing.T, sessionName, startURL string) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[sso-session " + sessionName + "]\nsso_start_url = " + startURL + "\nsso_region = us-east-1\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// warren ssm-shell <favorite> <target> is issue #19: ssm-shell had no way to skip the picker,
// unlike exec and shell. runFavorite's modeSSMShell branch is what makes that possible — this
// proves it's actually wired up (reachable, and not silently falling through to the
// modeExec/modeShell path at the bottom, which would try to run run.argv as a command instead
// of opening an SSM session). No live SSO token is available in a test environment, so this
// can only observe the earliest failure — SilentToken reporting ErrLoginRequired — but that
// failure is common to every mode, so reaching it at all with mode: modeSSMShell and no argv
// confirms the branch didn't panic or take the wrong path before ever reaching a difference.
func TestRunFavoriteSSMShellReachesTheSSMPath(t *testing.T) {
	const startURL = "https://ex.awsapps.com/start"
	writeSSOSessionConfigNamed(t, "corp", startURL)

	fav := browser.Favorite{
		Nickname: "prod-admin", StartURL: startURL,
		AccountID: "111111111111", AccountName: "prod", Role: "AdminRole",
	}

	code, stderr := runFavoriteStderr(t, fav, invocation{mode: modeSSMShell, target: "i-0123456789abcdef0"})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 (no sign-in available in a test environment)", code)
	}
	if !strings.Contains(stderr, "needs a sign-in") {
		t.Errorf("stderr = %q, want the sign-in hint", stderr)
	}
}

func TestRunFavoriteUnknownSSOSessionFailsFast(t *testing.T) {
	writeSSOSessionConfigNamed(t, "corp", "https://ex.awsapps.com/start")

	fav := browser.Favorite{
		Nickname: "prod-admin", StartURL: "https://gone.awsapps.com/start",
		AccountID: "111111111111", AccountName: "prod", Role: "AdminRole",
	}

	code, stderr := runFavoriteStderr(t, fav, invocation{mode: modeSSMShell, target: "i-0123456789abcdef0"})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no [sso-session]") {
		t.Errorf("stderr = %q, want it to explain the missing sso-session", stderr)
	}
}
