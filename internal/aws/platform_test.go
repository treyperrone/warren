package aws

import (
	"strings"
	"testing"
)

// The picker previously showed no platform at all, so "is this a Windows box worth pointing an RDP
// client at?" could only be answered by connecting and finding out. DescribeInstances already
// carries the answer, so the whole feature rests on reading these two fields correctly.
func TestPlatformOf(t *testing.T) {
	cases := []struct {
		name     string
		platform string // the Platform field
		details  string // the PlatformDetails field
		want     string
	}{
		// What a real Windows instance returns — verified against a Server 2019 host.
		{"windows both fields", "windows", "Windows", "windows"},
		// AWS documents Platform as "windows"; nothing guarantees the casing survives an API
		// change, and a case-sensitive compare would silently reclassify every Windows host.
		{"windows odd casing", "Windows", "", "windows"},
		// Platform is omitted for non-Windows, so details is the only positive Linux signal.
		{"linux", "", "Linux/UNIX", "linux"},
		{"rhel", "", "Red Hat Enterprise Linux", "linux"},
		{"suse", "", "SUSE Linux", "linux"},
		// A licence-bearing Windows AMI names the extra product, not just the OS.
		{"windows with sql server", "windows", "Windows with SQL Server Standard", "windows"},
		// Details alone must still classify, in case Platform is ever absent on a Windows host.
		{"details only windows", "", "Windows", "windows"},
		// Neither field usable: say nothing rather than guess. A wrong "linux" on a Windows box
		// sends someone to ssh, which is the exact waste this field exists to prevent.
		{"nothing known", "", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := platformOf(c.platform, c.details); got != c.want {
				t.Errorf("platformOf(%q, %q) = %q, want %q", c.platform, c.details, got, c.want)
			}
		})
	}
}

// The badge is a column, so every value must occupy the same width or the id, IP, and type behind
// it ragged-edge down the list and stop being scannable.
func TestPlatformBadgeIsFixedWidth(t *testing.T) {
	widths := map[int][]string{}
	for _, p := range []string{"windows", "linux", ""} {
		b := Instance{Platform: p}.PlatformBadge()
		widths[len(b)] = append(widths[len(b)], b)
	}
	if len(widths) != 1 {
		t.Errorf("badges have differing widths, so the columns after them will not line up: %v", widths)
	}
}

func TestPlatformBadgeNamesThePlatform(t *testing.T) {
	if got := (Instance{Platform: "windows"}).PlatformBadge(); !strings.Contains(got, "windows") {
		t.Errorf("badge %q does not name the platform", got)
	}
	if got := (Instance{Platform: "linux"}).PlatformBadge(); !strings.Contains(got, "linux") {
		t.Errorf("badge %q does not name the platform", got)
	}
	// An unknown platform must read as unknown, not as a blank that looks like a rendering bug.
	if got := (Instance{}).PlatformBadge(); !strings.Contains(got, "unknown") {
		t.Errorf("badge for an unknown platform = %q, want it to say so", got)
	}
}
