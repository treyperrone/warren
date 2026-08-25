package tunnel

import (
	"strings"
	"testing"
)

// The .rdp document format is what Windows App parses; a wrong key silently opens the
// connection dialog empty, which reads as "warren did nothing".
func TestRDPFileContents(t *testing.T) {
	doc := rdpFileContents(13389, "kali")
	if !strings.Contains(doc, "full address:s:localhost:13389\n") {
		t.Errorf("missing address line:\n%s", doc)
	}
	if !strings.Contains(doc, "username:s:kali\n") {
		t.Errorf("missing username line:\n%s", doc)
	}
	// No user: the key must be absent entirely, not present-and-empty, or the client
	// pre-fills a blank username instead of asking.
	if doc := rdpFileContents(1, ""); strings.Contains(doc, "username") {
		t.Errorf("empty username emitted:\n%s", doc)
	}
}
