//go:build !windows

package main

import "os"

// canOpenTerminal reports whether bubbletea will be able to acquire the controlling terminal it
// needs for raw-mode input. This is a different question from stdinIsTTY's "can a human answer
// a prompt on fd 0": /dev/null is itself a character device — the default stdin cron and many
// service managers give a job unless told otherwise — so a script redirected that way still
// passes stdinIsTTY's check, yet bubbletea (tty_unix.go in its own source) opens /dev/tty
// directly and fails with "could not open a new TTY: open /dev/tty: no such device or address".
// Trying the same open here, before ever building the picker, is what lets that come back as a
// warren-authored message instead.
func canOpenTerminal() bool {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
