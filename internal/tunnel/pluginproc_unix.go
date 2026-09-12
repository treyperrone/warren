//go:build !windows

package tunnel

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// isPluginProcess reports whether pid is actually running the session-manager-plugin, not
// just some process — which is all aliveByPID's kill(pid, 0) probe can tell apart. PIDs get
// reused, most commonly across a reboot: a tunnel entry persisted to disk survives to the
// next warren launch, its PID now belongs to a completely unrelated process, kill(pid, 0)
// reports it alive all the same, and warren would hand an RDP client a port with nothing
// behind it — which spins until the client's own timeout rather than failing outright.
//
// ps reports the full command line, which for the plugin is its path as extracted by the
// plugin package ("…/session-manager-plugin-<hash>") regardless of how warren was launched,
// so a substring match is reliable without hardcoding that path here.
func isPluginProcess(pid int) bool {
	if out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output(); err == nil {
		return strings.Contains(string(out), "session-manager-plugin")
	}
	// ps itself may simply not be installed — procps is not part of every minimal container
	// base image. Rather than that failing closed and dropping every restored tunnel on
	// load() with no diagnostic, fall back to /proc, the same place ps reads this from on
	// Linux: present whenever ps would have worked anyway, so this only ever helps.
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline"); err == nil {
		return strings.Contains(string(data), "session-manager-plugin")
	}
	return false
}
