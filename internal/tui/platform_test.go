package tui

import (
	"strings"
	"testing"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// platformListModel builds the instance picker over a mixed-platform set, through the real
// buildInstanceList so the row text and the search text are the ones the picker actually uses.
func platformListModel(t *testing.T) *Model {
	t.Helper()
	m := newFormModel(t)
	m.instances = []awsint.Instance{
		{
			ID: "i-0win", Name: "goad-dc01", PrivateIP: "10.71.191.132", Type: "t3.medium",
			Platform: "windows", PlatformDetails: "Windows",
			Tags: map[string]string{"Name": "goad-dc01"},
		},
		{
			ID: "i-0lin", Name: "kali-01", PrivateIP: "10.71.191.44", Type: "t3.small",
			Platform: "linux", PlatformDetails: "Linux/UNIX",
			Tags: map[string]string{"Name": "kali-01"},
		},
		{
			ID: "i-0rh", Name: "app-01", PrivateIP: "10.71.191.55", Type: "m5.large",
			Platform: "linux", PlatformDetails: "Red Hat Enterprise Linux",
			Tags: map[string]string{"Name": "app-01"},
		},
	}
	m.buildInstanceList()
	return m
}

// rowFor returns the rendered description line for the instance with the given title.
func rowFor(t *testing.T, m *Model, title string) string {
	t.Helper()
	for _, li := range m.list.Items() {
		it, ok := li.(item)
		if ok && it.title == title {
			return it.desc
		}
	}
	t.Fatalf("no row titled %q", title)
	return ""
}

// The gap this closes: a row showed id, IP, and type, none of which say whether the host will
// answer an RDP client. Finding out meant opening a tunnel and waiting for it to not work.
func TestInstanceRowNamesThePlatform(t *testing.T) {
	m := platformListModel(t)

	if got := rowFor(t, m, "goad-dc01"); !strings.Contains(got, "windows") {
		t.Errorf("windows row %q does not say so", got)
	}
	if got := rowFor(t, m, "kali-01"); !strings.Contains(got, "linux") {
		t.Errorf("linux row %q does not say so", got)
	}
	// The id must survive alongside it — it is what gets copied into `warren ssm-shell`.
	if got := rowFor(t, m, "goad-dc01"); !strings.Contains(got, "i-0win") {
		t.Errorf("row %q lost the instance id", got)
	}
}

// Fixed-width badges are what keep the id column aligned; a ragged left edge is the reason to
// prefer a badge over appending the platform to the end of the row.
func TestInstanceRowsAlignAfterTheBadge(t *testing.T) {
	m := platformListModel(t)

	offsets := map[int]string{}
	for _, title := range []string{"goad-dc01", "kali-01", "app-01"} {
		row := rowFor(t, m, title)
		idx := strings.Index(row, "i-")
		if idx < 0 {
			t.Fatalf("row %q has no instance id", row)
		}
		offsets[idx] = row
	}
	if len(offsets) != 1 {
		t.Errorf("instance ids start at differing columns, so the list does not line up: %v", offsets)
	}
}

// "/windows" is the query that answers "what can I RDP to", so PlatformDetails has to be in the
// search text even though the long values are never displayed.
func TestInstanceSearchMatchesPlatform(t *testing.T) {
	m := platformListModel(t)

	if got := bestMatch(t, m, "windows"); got != "goad-dc01" {
		t.Errorf("searching \"windows\" ranked %q first, want goad-dc01", got)
	}
	// The distinguishing part of a longer PlatformDetails must be searchable too — all three
	// hosts would otherwise be indistinguishable to a search for the distro.
	if got := bestMatch(t, m, "red hat"); got != "app-01" {
		t.Errorf("searching \"red hat\" ranked %q first, want app-01", got)
	}
}

// The connection screen is where platform changes the decision, so it says which one — but it
// must keep offering everything. Windows Server ships an optional OpenSSH server and xrdp exists
// for Linux, so removing a row would be warren overruling a setup it cannot see.
func TestConnTypeScreenNamesPlatformWithoutRemovingOptions(t *testing.T) {
	m := platformListModel(t)
	m.selInstance = &awsint.Instance{
		ID: "i-0lin", Name: "kali-01", Platform: "linux", PlatformDetails: "Linux/UNIX",
	}
	m.buildConnTypeList()

	if !strings.Contains(m.list.Title, "linux") {
		t.Errorf("connection screen title %q does not name the platform", m.list.Title)
	}

	var values []string
	for _, li := range m.list.Items() {
		if it, ok := li.(item); ok {
			values = append(values, it.value)
		}
	}
	// RDP in particular: it is the one a platform filter would have removed from a Linux host.
	for _, want := range []string{"shell", "ssh", "rdp"} {
		var found bool
		for _, v := range values {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Errorf("connection type %q is missing for a linux host; platform must inform, not filter", want)
		}
	}
}

// An unknown platform must not render as a blank gap that reads like a bug.
func TestUnknownPlatformStillRendersARow(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{
		{ID: "i-0unk", Name: "mystery", PrivateIP: "10.0.0.1", Type: "t3.nano"},
	}
	m.buildInstanceList()

	if got := rowFor(t, m, "mystery"); !strings.Contains(got, "unknown") {
		t.Errorf("row for an unknown platform = %q, want it to say unknown", got)
	}
}
