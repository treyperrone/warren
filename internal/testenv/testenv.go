// Package testenv holds helpers shared by tests in more than one package.
//
// Not a _test.go file because several packages need the same helper, and nothing outside a test
// imports this, so it never reaches the shipped binary.
package testenv

import "testing"

// SetHome points the user's home directory at dir for the duration of the test.
//
// Sets both HOME and USERPROFILE. warren resolves the home directory with os.UserHomeDir so that
// it agrees with the aws CLI, and on Windows that reads USERPROFILE and ignores HOME entirely.
// Setting only HOME would leave Windows tests resolving the *real* profile — so a test that writes
// a config file would append to the developer's actual ~/.aws/config, and a test asserting a file
// is absent would read whatever they happen to have.
func SetHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	// Cleared so a value inherited from the real environment cannot redirect the config path out
	// from under a test that has just pointed HOME at a scratch directory.
	t.Setenv("AWS_CONFIG_FILE", "")
}
