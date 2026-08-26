package browser

import "encoding/json"

// RDPScreen is how RDP clients warren launches present the remote desktop.
type RDPScreen string

const (
	// RDPWindowed opens a resizable window sized for a laptop, the remote desktop scaling to
	// fit it. The default: a tunnel to one box should not swallow the local desktop.
	RDPWindowed RDPScreen = "windowed"
	// RDPFullscreen hands the client the whole display, as Windows App does when told nothing.
	RDPFullscreen RDPScreen = "fullscreen"
)

// rdpScreenKey is the ~/.warren_config.json key; the value is one of the constants above.
const rdpScreenKey = "rdp_screen"

// LoadRDPScreen reads the preference, windowed when unset or unrecognised — an unknown
// value is a typo in a hand-edited file, not a request for full screen.
func LoadRDPScreen() RDPScreen {
	doc, err := readConfigDoc()
	if err != nil {
		return RDPWindowed
	}
	var s RDPScreen
	if raw, ok := doc[rdpScreenKey]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	if s == RDPFullscreen {
		return RDPFullscreen
	}
	return RDPWindowed
}

// SaveRDPScreen stores the preference; the other keys in the file are untouched.
func SaveRDPScreen(s RDPScreen) error {
	return mutateConfigDoc(func(doc map[string]json.RawMessage) error {
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		doc[rdpScreenKey] = raw
		return nil
	})
}

// Toggle returns the other setting: the TUI row flips between the two on Enter.
func (s RDPScreen) Toggle() RDPScreen {
	if s == RDPFullscreen {
		return RDPWindowed
	}
	return RDPFullscreen
}

// Describe is the human phrasing used on the settings row.
func (s RDPScreen) Describe() string {
	if s == RDPFullscreen {
		return "full screen"
	}
	return "a window"
}
