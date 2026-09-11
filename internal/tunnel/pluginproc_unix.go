//go:build !windows

package tunnel

import (
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
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "session-manager-plugin")
}
