package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/treyperrone/warren/internal/testenv"
)

// Windowed is the default and the fallback: a missing key, an unknown value, or no file at all
// must all mean "do not take over the display".
func TestRDPScreenDefaultsToWindowed(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)

	if got := LoadRDPScreen(); got != RDPWindowed {
		t.Fatalf("no file: %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, ".warren_config.json"), []byte(`{"rdp_screen":"huge"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadRDPScreen(); got != RDPWindowed {
		t.Fatalf("unknown value: %q", got)
	}
}

// Saving flips the setting and leaves every other key alone.
func TestRDPScreenRoundTripPreservesNeighbours(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	path := filepath.Join(home, ".warren_config.json")
	if err := os.WriteFile(path, []byte(`{"sso_browser":"chrome","other_tool":{"keep":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveRDPScreen(RDPWindowed.Toggle()); err != nil {
		t.Fatal(err)
	}
	if got := LoadRDPScreen(); got != RDPFullscreen {
		t.Fatalf("after toggle: %q", got)
	}
	if err := SaveRDPScreen(LoadRDPScreen().Toggle()); err != nil {
		t.Fatal(err)
	}
	if got := LoadRDPScreen(); got != RDPWindowed {
		t.Fatalf("after second toggle: %q", got)
	}

	raw, _ := os.ReadFile(path)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["sso_browser"]) != `"chrome"` {
		t.Errorf("sso_browser = %s", doc["sso_browser"])
	}
	var other struct{ Keep bool }
	if err := json.Unmarshal(doc["other_tool"], &other); err != nil || !other.Keep {
		t.Errorf("other_tool = %s (%v)", doc["other_tool"], err)
	}
}
