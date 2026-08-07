package pathhint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary lives in a temp directory that is not on PATH, which is the same
// situation as a fresh `go install` — so a hint is expected by default.
func TestHintWhenNotOnPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", "/bin/zsh")

	got := Hint()
	if got == "" {
		t.Fatal("no hint for a binary that is not on PATH")
	}
	dir, ok := execDir()
	if !ok {
		t.Fatal("could not resolve exec dir")
	}
	if !strings.Contains(got, dir) {
		t.Errorf("hint does not name the install directory %q:\n%s", dir, got)
	}
	if !strings.Contains(got, ".zshrc") {
		t.Errorf("zsh hint does not mention ~/.zshrc:\n%s", got)
	}
}

// Silence is the whole point once the directory is on PATH — otherwise this becomes a nag
// that every user sees forever.
func TestNoHintWhenOnPath(t *testing.T) {
	dir, ok := execDir()
	if !ok {
		t.Fatal("could not resolve exec dir")
	}
	t.Setenv("PATH", "/usr/bin:"+dir+":/bin")

	if got := Hint(); got != "" {
		t.Errorf("hint shown for a binary already on PATH:\n%s", got)
	}
}

// A PATH entry that reaches the same directory by a different name is still on PATH. A
// false hint telling someone to add a directory they already have is worse than none.
func TestNoHintWhenPathEntryIsASymlink(t *testing.T) {
	dir, ok := execDir()
	if !ok {
		t.Fatal("could not resolve exec dir")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	t.Setenv("PATH", "/usr/bin:"+link)
	if got := Hint(); got != "" {
		t.Errorf("hint shown when PATH reaches the directory via a symlink:\n%s", got)
	}
}

// Relative and empty PATH entries are common ("" from a trailing colon, "." from older
// setups) and must not crash or produce a spurious match.
func TestOddPathEntriesAreHarmless(t *testing.T) {
	t.Setenv("PATH", ":.::/usr/bin:")
	t.Setenv("SHELL", "/bin/bash")

	if got := Hint(); got == "" {
		t.Error("odd PATH entries suppressed a hint that should have been shown")
	}
}

func TestShellAdvice(t *testing.T) {
	tests := []struct {
		shell    string
		wantLine string
		wantRun  string
	}{
		{"/bin/zsh", ".zshrc", "exec zsh"},
		{"/usr/bin/fish", "fish_add_path", "exec fish"},
		{"/bin/bash", ".bash_profile", "exec bash -l"},
		{"/bin/ksh", "export PATH=", "startup file"},
		{"", "export PATH=", "startup file"},
	}

	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			t.Setenv("SHELL", tt.shell)
			line, reload := shellAdvice("/home/someone/go/bin")

			if !strings.Contains(line, tt.wantLine) {
				t.Errorf("line = %q, want it to contain %q", line, tt.wantLine)
			}
			if !strings.Contains(reload, tt.wantRun) {
				t.Errorf("reload = %q, want it to contain %q", reload, tt.wantRun)
			}
			if !strings.Contains(line, "/home/someone/go/bin") {
				t.Errorf("line = %q, want it to name the directory", line)
			}
		})
	}
}
