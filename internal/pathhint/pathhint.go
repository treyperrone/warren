// Package pathhint tells the user how to put warren on their PATH.
//
// `go install` has no post-install hook — a module cannot print anything once the toolchain
// has copied the binary into GOPATH/bin — and that directory is not on PATH by default. The
// result is `zsh: command not found: warren` immediately after a successful install. The
// only place left to say something useful is the binary itself, at runtime.
package pathhint

import (
	"fmt"
	"os"
	"path/filepath"
)

// Hint returns advice on adding this binary's directory to PATH, or "" if it is already
// there — or if the answer cannot be determined, since a wrong hint is worse than none.
func Hint() string {
	dir, ok := execDir()
	if !ok || onPath(dir) {
		return ""
	}

	name := filepath.Base(os.Args[0])
	if name == "." || name == string(filepath.Separator) {
		name = "warren"
	}

	line, reload := shellAdvice(dir)
	return fmt.Sprintf("\n%s is not on your PATH — run it as `%s` from anywhere with:\n\n    %s\n    %s\n",
		name, name, line, reload)
}

// execDir is the resolved directory holding the running binary. Symlinks are followed so
// that a binary linked into a directory that *is* on PATH does not produce a false hint.
func execDir() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir, err := filepath.Abs(filepath.Dir(exe))
	if err != nil {
		return "", false
	}
	return dir, true
}

func onPath(dir string) bool {
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		abs, err := filepath.Abs(entry)
		if err != nil {
			continue
		}
		// Resolve the PATH entry too: ~/go/bin and a symlinked /usr/local/go/bin can be the
		// same directory by different names.
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if abs == dir {
			return true
		}
	}
	return false
}

// shellAdvice returns the export line and the command to reload, matched to $SHELL. The
// syntax differs enough between fish and the POSIX shells that a single generic line would
// be wrong for someone.
func shellAdvice(dir string) (line, reload string) {
	// Quote the directory, since a home directory can contain spaces.
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return fmt.Sprintf("echo 'export PATH=\"$PATH:%s\"' >> ~/.zshrc", dir), "exec zsh"
	case "fish":
		return fmt.Sprintf("fish_add_path %q", dir), "exec fish"
	case "bash":
		// macOS bash reads ~/.bash_profile for login shells and most Linux distributions
		// source ~/.bashrc; ~/.bash_profile is the safer single answer on both.
		return fmt.Sprintf("echo 'export PATH=\"$PATH:%s\"' >> ~/.bash_profile", dir), "exec bash -l"
	default:
		return fmt.Sprintf("export PATH=\"$PATH:%s\"", dir), "(add that line to your shell's startup file)"
	}
}
