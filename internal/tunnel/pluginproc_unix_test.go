//go:build !windows

package tunnel

import "testing"

// isPluginProcess is what load() uses to tell a genuine plugin process apart from an
// unrelated one that happens to have inherited its pid.
func TestIsPluginProcessTrueForThePlugin(t *testing.T) {
	if !isPluginProcess(pluginSleeper(t)) {
		t.Error("isPluginProcess = false for a process named session-manager-plugin-*")
	}
}

func TestIsPluginProcessFalseForAnUnrelatedProcess(t *testing.T) {
	if isPluginProcess(sleeper(t)) {
		t.Error("isPluginProcess = true for a plain sleep process")
	}
}

func TestIsPluginProcessFalseForAPidThatDoesNotExist(t *testing.T) {
	// Not a guarantee no process ever has this pid, but true is not plausible: the manager's
	// own tests exit long before any real system reissues it, so this is stable in practice.
	if isPluginProcess(999999) {
		t.Error("isPluginProcess = true for a pid nothing is running under")
	}
}
