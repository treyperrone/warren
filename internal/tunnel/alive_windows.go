//go:build windows

package tunnel

import "os"

// aliveByPID reports whether a process with this pid is still running.
//
// Signal 0 is not available on Windows, but os.FindProcess there is not the no-op it is on Unix:
// it opens a real process handle and fails once the pid is gone, so the lookup itself is the
// answer.
//
// Imperfect, and worth knowing where: a process that has exited but whose handle is still open
// elsewhere can remain openable, so this can report a tunnel as alive slightly longer than it is.
// That is a stale row on the manager screen, which is a far smaller problem than the previous
// behaviour of reporting every live tunnel as dead.
func aliveByPID(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = proc.Release()
	return true
}
