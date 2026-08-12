//go:build !windows

package tunnel

import (
	"os"
	"syscall"
)

// aliveByPID reports whether a process with this pid is still running.
//
// Signal 0 is the POSIX existence probe: the kernel performs its permission checks and reports
// whether the process exists, without delivering anything to it.
//
// This used to pass a nil signal — proc.Signal(os.Signal(nil)) — and os.Process.Signal rejects
// that outright with "os: unsupported signal type", a non-nil error. So the check answered false
// for every live tunnel: Manager.Active pruned each one the moment it was asked, no tunnel ever
// appeared on the manager screen, the banner never showed a count, and the plugin processes were
// left running with nothing tracking them.
func aliveByPID(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix FindProcess never fails, so the signal is what actually answers the question.
	return proc.Signal(syscall.Signal(0)) == nil
}
