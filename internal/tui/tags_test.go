package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"

	awsint "github.com/treyperrone/postern/internal/aws"
)

// instanceListModel builds the instance picker over a fixed set of tagged instances, going
// through the real buildInstanceList so the test exercises what "/" actually searches.
func instanceListModel(t *testing.T) *Model {
	t.Helper()
	m := newFormModel(t)
	m.instances = []awsint.Instance{
		{
			ID: "i-0aaa", Name: "web-01", PrivateIP: "10.20.1.15", Type: "t3.medium",
			Tags: map[string]string{"Name": "web-01", "client": "globogym", "env": "production", "owner": "trey"},
		},
		{
			ID: "i-0bbb", Name: "web-02", PrivateIP: "10.20.1.16", Type: "t3.medium",
			Tags: map[string]string{"Name": "web-02", "client": "aperture", "env": "staging", "owner": "sam"},
		},
		{
			ID: "i-0ccc", Name: "db-01", PrivateIP: "10.20.2.30", Type: "m5.large",
			Tags: map[string]string{"Name": "db-01", "client": "globogym", "scenario": "ransomware"},
		},
	}
	m.buildInstanceList()
	return m
}

// bestMatch runs the same fuzzy filter "/" uses and returns the top-ranked row's title.
func bestMatch(t *testing.T, m *Model, term string) string {
	t.Helper()
	items := m.list.Items()
	targets := make([]string, len(items))
	for i, li := range items {
		it, ok := li.(item)
		if !ok {
			t.Fatalf("item %d is not an item", i)
		}
		targets[i] = it.FilterValue()
	}

	ranks := list.DefaultFilter(term, targets)
	if len(ranks) == 0 {
		t.Fatalf("%q matched nothing", term)
	}
	it, _ := items[ranks[0].Index].(item)
	return it.title
}

// The point of stage 0: tags DescribeInstances already returns are searchable, so an instance
// can be found by client, scenario, or owner — none of which appear in its name.
func TestInstanceSearchMatchesArbitraryTags(t *testing.T) {
	m := instanceListModel(t)

	tests := []struct {
		term string
		want string
	}{
		{"ransomware", "db-01"}, // a tag key nothing else carries
		{"owner=sam", "web-02"}, // key and value together
		{"aperture", "web-02"},  // a client that is not in any instance name
		{"10.20.2.30", "db-01"}, // the pre-existing description search still works
		{"i-0aaa", "web-01"},    // and so does instance ID
	}

	for _, tt := range tests {
		t.Run(tt.term, func(t *testing.T) {
			if got := bestMatch(t, m, tt.term); got != tt.want {
				t.Errorf("%q best match = %q, want %q", tt.term, got, tt.want)
			}
		})
	}
}

// Tags are searchable but must not be rendered: an instance can carry a dozen
// CloudFormation-managed tags, which would bury the ID, IP, and type actually on the row.
func TestInstanceRowsDoNotDisplayTags(t *testing.T) {
	m := instanceListModel(t)

	for _, li := range m.list.Items() {
		it, ok := li.(item)
		if !ok {
			t.Fatal("not an item")
		}
		if strings.Contains(it.desc, "client=") || strings.Contains(it.desc, "env=") {
			t.Errorf("row %q renders tags in its description: %q", it.title, it.desc)
		}
		// ...but they must be there to search.
		if it.search == "" {
			t.Errorf("row %q has no searchable tag text", it.title)
		}
	}
}

// An instance with no tags must still list, and must not gain a stray "=" in its search text.
func TestUntaggedInstanceStillLists(t *testing.T) {
	m := newFormModel(t)
	m.instances = []awsint.Instance{{ID: "i-0ddd", Name: "(no name)", PrivateIP: "10.0.0.9", Type: "t2.nano"}}
	m.buildInstanceList()

	if got := len(m.list.Items()); got != 1 {
		t.Fatalf("list has %d items, want 1", got)
	}
	it, _ := m.list.Items()[0].(item)
	if it.search != "" {
		t.Errorf("untagged instance has search text %q, want empty", it.search)
	}
	if strings.HasSuffix(it.FilterValue(), " ") {
		t.Errorf("FilterValue has a trailing space: %q", it.FilterValue())
	}
}

// The title advertises what "/" covers; if tags became searchable silently nobody would try.
func TestInstanceListTitleMentionsTags(t *testing.T) {
	m := instanceListModel(t)
	if !strings.Contains(m.list.Title, "tag") {
		t.Errorf("instance list title does not mention tags: %q", m.list.Title)
	}
}
