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
	"runtime"
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

// onPath reports whether dir is one of the directories in PATH.
//
// Compared with os.SameFile rather than by string, because a string comparison is wrong in more
// ways than it looks: Windows paths are case-insensitive, so C:\Users\me\go\bin and
// c:\users\me\go\bin are one directory that compares unequal, and Windows also hands out 8.3
// short names (RUNNER~1) for the same path. os.SameFile asks the filesystem whether two paths are
// the same directory, which also covers symlinks and hardlinks for free.
func onPath(dir string) bool {
	want, err := os.Stat(dir)
	if err != nil {
		return false
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		got, err := os.Stat(entry)
		if err != nil {
			continue // a PATH entry that does not exist cannot be this directory
		}
		if os.SameFile(want, got) {
			return true
		}
	}
	return false
}

// shellAdvice returns the export line and the command to reload, matched to $SHELL. The
// syntax differs enough between fish and the POSIX shells that a single generic line would
// be wrong for someone.
func shellAdvice(dir string) (line, reload string) {
	return adviceFor(runtime.GOOS, os.Getenv("SHELL"), dir)
}

// adviceFor is shellAdvice with the platform and shell passed in rather than read from the
// environment, so both branches can be tested from any host. Reading runtime.GOOS directly meant
// the Windows advice was only ever exercised on a Windows runner and the POSIX advice only on
// Unix — half the function untested on any single run, which is how the Windows branch shipped
// telling people to run `export PATH=...`.
func adviceFor(goos, shell, dir string) (line, reload string) {
	// Windows first: $SHELL is normally unset there, so keying off it alone falls through to the
	// POSIX default and gives advice a Windows user cannot run. SetEnvironmentVariable at User
	// scope is the persistent equivalent, and unlike setx it does not silently truncate a long
	// PATH at 1024 characters.
	if goos == "windows" {
		return fmt.Sprintf(`[Environment]::SetEnvironmentVariable("Path", $env:Path + ";%s", "User")`, dir),
			"(then open a new terminal)"
	}

	// Quote the directory, since a home directory can contain spaces.
	switch filepath.Base(shell) {
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
