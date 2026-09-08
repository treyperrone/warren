package tunnel

import (
	"path/filepath"
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

// Windows App labels a connection by the .rdp file's base name — the one place a friendly
// name lands, since the format has no display-name key — so the file is named for the
// instance, with the port kept for uniqueness.
func TestRDPFileNamedForInstance(t *testing.T) {
	path, err := writeRDPFile(13389, "kali", "web-01", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(path); got != "web-01-13389.rdp" {
		t.Errorf("file name = %q, want web-01-13389.rdp", got)
	}
}

// No name, or a name that was all punctuation: fall back to the address so the file is still
// well-formed and unique.
func TestRDPFileFallsBackWithoutAName(t *testing.T) {
	for _, name := range []string{"", "///"} {
		path, err := writeRDPFile(13389, "", name, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := filepath.Base(path); got != "localhost-13389.rdp" {
			t.Errorf("name %q: file = %q, want localhost-13389.rdp", name, got)
		}
	}
}

func TestRDPFileSlug(t *testing.T) {
	cases := map[string]string{
		"web-01":          "web-01",
		"Corp Prod / RDP": "Corp-Prod-RDP",
		"  spaced  ":      "spaced",
		"tag:with:colons": "tag-with-colons",
		"emoji🔥host":      "emoji-host",
		"":                "",
		"...":             "...", // dots are kept — a name that is only dots is odd but harmless
	}
	for in, want := range cases {
		if got := rdpFileSlug(in); got != want {
			t.Errorf("rdpFileSlug(%q) = %q, want %q", in, got, want)
		}
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
