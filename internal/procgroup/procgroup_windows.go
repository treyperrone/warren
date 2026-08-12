//go:build windows

package procgroup

import "syscall"

// Detached puts the child in a process group of its own.
//
// Windows has no Setpgid; the equivalent is CREATE_NEW_PROCESS_GROUP, which stops the child
// receiving the console's ctrl-c and ctrl-break events.
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
