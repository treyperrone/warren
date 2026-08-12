// Package homedir resolves the user's home directory the way AWS's own tools do.
//
// It exists because reading os.Getenv("HOME") is wrong on Windows. Windows normally leaves HOME
// unset and uses USERPROFILE, so filepath.Join(os.Getenv("HOME"), ".aws", "config") produced the
// *relative* path ".aws/config" there — meaning warren read and wrote config in whatever directory
// it happened to be run from. It never found a real SSO session, `warren setup` left a stray file
// behind, and the SSO token cache landed somewhere the aws CLI would never look, which defeats the
// cache sharing that makes silent renewal work.
//
// Every test injected HOME, so the whole test suite passed on the Windows runner while the shipped
// Windows binary could not find its own configuration.
package homedir

import "os"

// Dir returns the user's home directory, or "" if it cannot be determined.
//
// os.UserHomeDir reads USERPROFILE on Windows and HOME everywhere else, which is what the aws CLI
// resolves too: botocore uses expanduser("~"), and on Windows that is USERPROFILE-based. Agreeing
// with the CLI matters because warren shares ~/.aws/config and ~/.aws/sso/cache with it.
//
// A "" return is left for callers to handle rather than substituted with a guess: a wrong home
// directory means reading the wrong config and writing files where nobody expects them, and
// failing to find something is easier to diagnose than silently using the wrong path.
func Dir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	// Last resort for an environment where UserHomeDir cannot answer — a container with no
	// USERPROFILE, say — but where HOME is still set.
	return os.Getenv("HOME")
}
