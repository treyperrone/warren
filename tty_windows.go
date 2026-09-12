//go:build windows

package main

// canOpenTerminal reports whether bubbletea will be able to read raw keyboard input. Unlike
// unix, bubbletea's Windows input path (tty_windows.go in its own source) doesn't fail loudly
// when stdin isn't a console — term.IsTerminal comes back false and it just skips enabling
// raw/VT mode, so the program appears to hang waiting for input that will never arrive rather
// than erroring. stdinIsTTY's character-device check is the same signal bubbletea itself uses
// there, so it stays the best available preflight on this platform.
func canOpenTerminal() bool {
	return stdinIsTTY()
}
