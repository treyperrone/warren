package awscli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeCLI puts an `aws` on PATH that prints what a real one would, so the parser is exercised
// against actual observed output rather than an invented shape.
func fakeCLI(t *testing.T, versionOutput string, toStderr bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub is not portable to windows")
	}
	dir := t.TempDir()
	redirect := ""
	if toStderr {
		redirect = " >&2"
	}
	script := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(versionOutput) + redirect + "\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	reset()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// reset clears the memoised result so each test observes its own fake.
func reset() { once = sync.Once{}; cached = Info{} }

func TestParsesV2Version(t *testing.T) {
	fakeCLI(t, "aws-cli/2.36.14 Python/3.14.6 Linux/6.12 exe/x86_64.debian.13", false)

	got := Detect()
	if !got.Found() {
		t.Fatal("did not find the aws on PATH")
	}
	if got.Version != "2.36.14" || got.Major != 2 {
		t.Errorf("parsed %q major %d, want 2.36.14 / 2", got.Version, got.Major)
	}
	if got.Display() != "2.36.14" {
		t.Errorf("Display() = %q", got.Display())
	}
}

// Some v1 builds write --version to stderr rather than stdout, which is why CombinedOutput is
// used. Reading only stdout would report v1 as "installed, version unknown".
func TestParsesV1VersionFromStderr(t *testing.T) {
	fakeCLI(t, "aws-cli/1.29.80 Python/3.11.4 Linux/5.15 botocore/1.31.80", true)

	got := Detect()
	if got.Version != "1.29.80" || got.Major != 1 {
		t.Errorf("parsed %q major %d, want 1.29.80 / 1", got.Version, got.Major)
	}
}

// On PATH but reporting something unrecognised is still a usable CLI, so it must not be reported
// as missing — that would hide the feature from someone who can actually use it.
func TestUnparseableVersionStillCountsAsInstalled(t *testing.T) {
	fakeCLI(t, "some wrapper script v9", false)

	got := Detect()
	if !got.Found() {
		t.Error("treated an unrecognised version string as the CLI being absent")
	}
	if got.Version != "" {
		t.Errorf("invented a version %q", got.Version)
	}
	if !strings.Contains(got.Display(), "installed") {
		t.Errorf("Display() = %q, should still say it is installed", got.Display())
	}
}

func TestReportsMissingWhenNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	reset()

	got := Detect()
	if got.Found() {
		t.Error("found an aws CLI on an empty PATH")
	}
	if got.Display() != "not installed" {
		t.Errorf("Display() = %q, want %q", got.Display(), "not installed")
	}
}

// The message is what a first-time user sees instead of "executable file not found in $PATH", so
// it has to say what still works and where to get the missing piece.
func TestMissingErrorExplainsItself(t *testing.T) {
	msg := MissingError()
	for _, want := range []string{"aws CLI", InstallURL, "SSM", "without it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
}

// Detect runs a subprocess, and the about screen redraws on every keystroke — so it must be
// memoised rather than spawning a process per frame.
func TestDetectRunsTheCLIAtMostOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub is not portable to windows")
	}
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho x >> " + counter + "\necho 'aws-cli/2.0.0 Python/3.11'\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	reset()

	for i := 0; i < 5; i++ {
		Detect()
	}

	b, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("stub never ran: %v", err)
	}
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Errorf("ran `aws --version` %d times, want 1", n)
	}
}
