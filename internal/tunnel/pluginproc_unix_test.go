//go:build !windows

package tunnel

import (
	"runtime"
	"testing"
)

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

// A minimal container base image commonly has no procps (ps not on PATH). Without a fallback,
// load() would drop every restored tunnel unconditionally on such a host, with no diagnostic —
// isPluginProcess must fall back to /proc, the same place ps reads this from on Linux.
func TestIsPluginProcessFallsBackToProcWithoutPS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the /proc fallback only applies on Linux")
	}
	// Both helpers resolve and copy their binaries via the real PATH — done here, before it's
	// emptied below, so what's left unresolved is only isPluginProcess's own "ps" lookup.
	pluginPID := pluginSleeper(t)
	otherPID := sleeper(t)
	t.Setenv("PATH", t.TempDir()) // a PATH with no ps on it at all

	if !isPluginProcess(pluginPID) {
		t.Error("isPluginProcess = false for the plugin with ps unavailable — the /proc fallback did not fire")
	}
	if isPluginProcess(otherPID) {
		t.Error("isPluginProcess = true for an unrelated process with ps unavailable")
	}
}

func TestIsPluginProcessFalseForAPidThatDoesNotExist(t *testing.T) {
	// Not a guarantee no process ever has this pid, but true is not plausible: the manager's
	// own tests exit long before any real system reissues it, so this is stable in practice.
	if isPluginProcess(999999) {
		t.Error("isPluginProcess = true for a pid nothing is running under")
	}
}
