package main

import (
	"encoding/base64"
	"os"
)

// osc52Sequence builds the escape sequence that asks the terminal to place text on the
// LOCAL clipboard — the one on the machine whose screen the user is looking at, however
// many SSH hops away this process runs. That locality is the entire point: `warren login`
// on a remote box can hand its sign-in URL to the laptop it is being driven from, with no
// agent or forwarding involved, because the terminal emulator itself does the copying.
//
// Inside tmux the sequence must ride a passthrough wrapper (ESC P tmux; ... ESC \) or tmux
// eats it; whether tmux then forwards it depends on its allow-passthrough/set-clipboard
// settings, which is the user's configuration to make. Any literal ESC in the payload
// would need doubling inside the wrapper, but base64 never contains one.
func osc52Sequence(text string, tmux bool) []byte {
	payload := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if tmux {
		payload = "\x1bPtmux;\x1b" + payload + "\x1b\\"
	}
	return []byte(payload)
}

// copyToLocalClipboard emits the sequence at the controlling terminal and reports whether
// it was actually written somewhere a terminal could see. Emitted is not honoured — a
// terminal without OSC 52 support silently ignores it, and nothing reports back — so the
// caller's wording must promise no more than "sent".
func copyToLocalClipboard(text string) bool {
	seq := osc52Sequence(text, os.Getenv("TMUX") != "")

	// Stderr first: when it is the terminal, this interleaves correctly with the rest of
	// the sign-in output. /dev/tty is the fallback for redirected stderr; Windows has no
	// /dev/tty, and a redirected-everything Windows session just reports false.
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		_, err := os.Stderr.Write(seq)
		return err == nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	_, err = tty.Write(seq)
	return err == nil
}
