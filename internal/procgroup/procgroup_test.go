//go:build !windows

package procgroup

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// The bug this exists for: ctrl-c does not signal the process you are looking at, it sends SIGINT to
// the whole foreground process group. Backgrounded port forwards inherited warren's group, so ctrl-c
// killed every tunnel — while `q` left them running, exactly as the screen promises. Two different
// outcomes from two ways of quitting, one of them undocumented.
//
// This asserts the property that fixes it, rather than sending a real SIGINT: signalling our own
// group would take the test binary down with it, and the delivery semantics are POSIX's to
// guarantee, not warren's. What is warren's choice is which group the child lands in.
func TestDetachedChildLeavesTheParentsProcessGroup(t *testing.T) {
	own, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	// A plain child, for contrast: this is what every tunnel used to be.
	plain := exec.Command("sleep", "30")
	if err := plain.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Process.Kill(); _, _ = plain.Process.Wait() })

	detached := exec.Command("sleep", "30")
	detached.SysProcAttr = Detached()
	if err := detached.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = detached.Process.Kill(); _, _ = detached.Process.Wait() })

	plainPG, err := syscall.Getpgid(plain.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if plainPG != own {
		t.Fatalf("premise wrong: a plain child is in group %d, not the parent's %d", plainPG, own)
	}

	detachedPG, err := syscall.Getpgid(detached.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if detachedPG == own {
		t.Errorf("detached child is still in the parent's process group %d, so ctrl-c would kill it", own)
	}
}

// A detached child must still be killable, because that is how warren ends a tunnel on request —
// Tunnel.Kill signals the pid directly rather than the group.
func TestDetachedChildIsStillKillableByPID(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = Detached()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing a detached child by pid: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Errorf("waiting on a killed detached child: %v", err)
	}
}
