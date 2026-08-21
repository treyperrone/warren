package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The sequence bytes ARE the protocol: a wrong prefix or terminator is silently ignored by
// every terminal, which looks exactly like "OSC 52 unsupported" and debugs like weather.
func TestOSC52Sequence(t *testing.T) {
	const url = "https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-1234"
	b64 := base64.StdEncoding.EncodeToString([]byte(url))

	plain := string(osc52Sequence(url, false))
	if plain != "\x1b]52;c;"+b64+"\x07" {
		t.Errorf("plain sequence = %q", plain)
	}

	// Inside tmux the same payload rides the DCS passthrough wrapper, or tmux consumes it.
	wrapped := string(osc52Sequence(url, true))
	if !strings.HasPrefix(wrapped, "\x1bPtmux;\x1b\x1b]52;c;") || !strings.HasSuffix(wrapped, "\x07\x1b\\") {
		t.Errorf("tmux-wrapped sequence = %q", wrapped)
	}
	if !strings.Contains(wrapped, b64) {
		t.Error("tmux-wrapped sequence lost the payload")
	}
}
