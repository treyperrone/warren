package plugin

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The recorded tag is what tells a user, and AWS support, which plugin a session used. An empty or
// malformed value would make `warren help` claim something false, which is worse than silence.
func TestVersionIsARecordedReleaseTag(t *testing.T) {
	got := Version()
	if got == "" {
		t.Fatal("no plugin version recorded; scripts/build-plugin.sh writes internal/plugin/version.txt")
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`).MatchString(got) {
		t.Errorf("version %q is not an aws/session-manager-plugin release tag (e.g. 1.2.835.0)", got)
	}
	if strings.ContainsAny(got, " \t\n\r") {
		t.Errorf("version %q carries whitespace; it is interpolated into output", got)
	}
}

// A recorded version with no binaries beside it would be a lie, and the embed directives would
// have failed the build anyway — but this names the cause rather than leaving an embed error.
func TestRecordedVersionHasAssetsBesideIt(t *testing.T) {
	entries, err := os.ReadDir("assets")
	if err != nil {
		t.Fatalf("reading assets: %v", err)
	}
	var plugins int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "session-manager-plugin-") {
			plugins++
		}
	}
	if plugins == 0 {
		t.Error("version.txt records a version but assets/ holds no plugin binaries")
	}
}
