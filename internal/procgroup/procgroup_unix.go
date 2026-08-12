//go:build !windows

package procgroup

import "syscall"

// Detached puts the child in a process group of its own.
//
// Setpgid with a zero Pgid means "make this process the leader of a new group", which is what takes
// it out of the foreground group the terminal signals on ctrl-c.
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
