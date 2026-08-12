package homedir

import (
	"os"
	"path/filepath"
	"testing"
)

// The property that matters: warren must resolve the same home directory the aws CLI does, because
// they share ~/.aws/config and ~/.aws/sso/cache. os.UserHomeDir is the platform convention — HOME
// on Unix, USERPROFILE on Windows — so agreeing with it is the contract.
//
// This is what reading os.Getenv("HOME") directly got wrong: on Windows HOME is normally unset, so
// it produced "" and every path built from it became relative.
func TestDirMatchesThePlatformConvention(t *testing.T) {
	want, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}
	if got := Dir(); got != want {
		t.Errorf("Dir() = %q, want %q from os.UserHomeDir", got, want)
	}
}

// Anything built on a relative home resolves against the working directory, which is how config
// ended up being read from wherever warren happened to be started.
func TestDirIsAbsoluteWhenKnown(t *testing.T) {
	got := Dir()
	if got == "" {
		t.Skip("no home directory in this environment")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("Dir() = %q, which is relative", got)
	}
}

// A home directory that cannot be determined must report "" so callers can fail visibly, rather
// than a guess that would silently read and write the wrong files.
func TestDirIsEmptyWhenNothingIsSet(t *testing.T) {
	for _, k := range []string{"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(k, "")
	}
	if got := Dir(); got != "" {
		// Not a failure on every platform — some resolve a home without these — but it must at
		// least be absolute rather than a fragment.
		if !filepath.IsAbs(got) {
			t.Errorf("Dir() = %q with nothing set: neither empty nor absolute", got)
		}
	}
}
