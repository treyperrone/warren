package tunnel

import (
	"strings"
	"testing"
)

// The .rdp document format is what Windows App parses; a wrong key silently opens the
// connection dialog empty, which reads as "warren did nothing".
func TestRDPFileContents(t *testing.T) {
	doc := rdpFileContents(13389, "kali", false)
	if !strings.Contains(doc, "full address:s:localhost:13389\n") {
		t.Errorf("missing address line:\n%s", doc)
	}
	if !strings.Contains(doc, "username:s:kali\n") {
		t.Errorf("missing username line:\n%s", doc)
	}
	// Windowed, or the remote desktop swallows the local one on every connect.
	for _, want := range []string{"screen mode id:i:1\n", "smart sizing:i:1\n", "desktopwidth:i:", "desktopheight:i:"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q:\n%s", want, doc)
		}
	}
	// No user: the key must be absent entirely, not present-and-empty, or the client
	// pre-fills a blank username instead of asking.
	if doc := rdpFileContents(1, "", false); strings.Contains(doc, "username") {
		t.Errorf("empty username emitted:\n%s", doc)
	}
}

// The full-screen variant is mode 2 with no fixed geometry — the display decides.
func TestRDPFileContentsFullscreen(t *testing.T) {
	doc := rdpFileContents(13389, "", true)
	if !strings.Contains(doc, "screen mode id:i:2\n") {
		t.Errorf("not full screen:\n%s", doc)
	}
	for _, stray := range []string{"desktopwidth", "smart sizing"} {
		if strings.Contains(doc, stray) {
			t.Errorf("full screen carries %s:\n%s", stray, doc)
		}
	}
}
