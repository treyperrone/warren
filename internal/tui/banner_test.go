package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/buildinfo"
	"github.com/treyperrone/warren/internal/tunnel"
)

// The banner pads itself to the terminal width by hand, so an off-by-one in the
// version's reserved space either wraps the header onto a second row or leaves a
// ragged gap in the purple bar.
func TestBannerFillsExactlyOneRow(t *testing.T) {
	for _, width := range []int{40, 80, 120, 200} {
		m := &Model{width: width, manager: tunnel.NewManager(), lastInstance: "globogym-web-01"}

		row := strings.TrimSuffix(m.banner(), "\n")
		if strings.Contains(row, "\n") {
			t.Fatalf("width %d: banner spans multiple lines", width)
		}
		if got := lipgloss.Width(row); got != width {
			t.Errorf("width %d: banner rendered %d columns", width, got)
		}
	}
}

func TestBannerShowsVersion(t *testing.T) {
	m := &Model{width: 120, manager: tunnel.NewManager()}
	if v := buildinfo.Version(); !strings.Contains(m.banner(), v) {
		t.Errorf("banner does not show version %q", v)
	}
}

// The version sits immediately after the tool name. It was originally right-aligned, where
// it read as decoration and went unnoticed — placement is the whole point of this.
func TestBannerVersionFollowsToolName(t *testing.T) {
	m := &Model{width: 120, manager: tunnel.NewManager(), lastInstance: "globogym-web-01"}
	m.awsSess = &awsint.Session{Label: "some-account/SomeRole"}

	row := m.banner()
	name := strings.Index(row, "warren")
	version := strings.Index(row, buildinfo.Version())
	label := strings.Index(row, "some-account/SomeRole")

	if name < 0 || version < 0 || label < 0 {
		t.Fatalf("banner missing an expected part: %q", row)
	}
	if version < name {
		t.Errorf("version appears before the tool name: %q", row)
	}
	if version > label {
		t.Errorf("version appears after the account label, not next to the name: %q", row)
	}
}

// A terminal too narrow for both the labels and the version drops the version
// rather than letting it push the row over the edge.
func TestBannerDropsVersionWhenNarrow(t *testing.T) {
	m := &Model{width: 14, manager: tunnel.NewManager()}

	row := strings.TrimSuffix(m.banner(), "\n")
	if strings.Contains(row, buildinfo.Version()) {
		t.Errorf("version kept at width 14: %q", row)
	}
}
