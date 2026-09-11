//go:build windows

package tunnel

import (
	"os/exec"
	"strconv"
	"strings"
)

// isPluginProcess reports whether pid is actually running the session-manager-plugin. See
// the unix implementation for why this matters: without it, a PID reused across a reboot
// reads as a live tunnel pointing at nothing.
//
// tasklist gives an image name, which the plugin's extracted filename
// ("session-manager-plugin-<hash>.exe") would satisfy, but truncates in ways that vary by
// Windows version; Get-Process's Path is the full extracted location and is what plugin.Path
// actually writes, so it is what gets matched here.
func isPluginProcess(pid int) bool {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-Process -Id "+strconv.Itoa(pid)+" -ErrorAction SilentlyContinue).Path").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "session-manager-plugin")
}
