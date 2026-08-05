package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
)

// The bug this guards: FilterValue used to return only the title, so an account's ID and
// an instance's ID/IP — both visible on screen in the description — could not be searched.
// These run the real list.DefaultFilter (sahilm/fuzzy), the same path "/" uses.
func TestFilterValueSearchesDescription(t *testing.T) {
	accounts := []item{
		{title: "crlab-clients-globogym-compute", desc: "195170887130"},
		{title: "crlab-clients-aperture-data", desc: "402113345901"},
		{title: "crlab-infra-mgmt", desc: "070638634630"},
	}
	instances := []item{
		{title: "globogym-web-01", desc: "i-0abc123def456  10.20.1.15  t3.medium"},
		{title: "globogym-db-01", desc: "i-0999888777666  10.20.2.30  m5.large"},
	}

	tests := []struct {
		name  string
		items []item
		term  string
		want  string // expected title of the single match
	}{
		{"account by full ID", accounts, "195170887130", "crlab-clients-globogym-compute"},
		{"account by ID prefix", accounts, "0706", "crlab-infra-mgmt"},
		{"account by name", accounts, "aperture", "crlab-clients-aperture-data"},
		{"instance by ID", instances, "i-0999", "globogym-db-01"},
		{"instance by private IP", instances, "10.20.1.15", "globogym-web-01"},
		{"instance by type", instances, "m5.large", "globogym-db-01"},
		{"instance by name", instances, "web-01", "globogym-web-01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := make([]string, len(tt.items))
			for i, it := range tt.items {
				targets[i] = it.FilterValue()
			}

			ranks := list.DefaultFilter(tt.term, targets)
			if len(ranks) == 0 {
				t.Fatalf("%q matched nothing; want %q", tt.term, tt.want)
			}
			// DefaultFilter sorts best-first, so rank 0 is the intended hit.
			if got := tt.items[ranks[0].Index].title; got != tt.want {
				t.Errorf("%q best match = %q, want %q", tt.term, got, tt.want)
			}
		})
	}
}

// Descriptions are searchable, but they must not swallow the title: a term that names one
// item should not rank a different item first just because its description is longer.
func TestFilterValueIncludesTitle(t *testing.T) {
	it := item{title: "crlab-infra-mgmt", desc: "070638634630"}
	got := it.FilterValue()
	want := "crlab-infra-mgmt 070638634630"
	if got != want {
		t.Errorf("FilterValue() = %q, want %q", got, want)
	}
}
