package aws

import (
	"reflect"
	"testing"
)

func TestTagPairsIsSorted(t *testing.T) {
	i := Instance{Tags: map[string]string{
		"owner":  "trey",
		"env":    "prod",
		"client": "globogym",
		"Name":   "globogym-web-01",
	}}

	// Sorted, because map iteration order is random and the result is folded into search
	// text: an unstable order would make the same instance's search value differ per run.
	want := []string{"Name=globogym-web-01", "client=globogym", "env=prod", "owner=trey"}
	if got := i.TagPairs(); !reflect.DeepEqual(got, want) {
		t.Errorf("TagPairs() = %v, want %v", got, want)
	}
}

func TestTagPairsWithNoTags(t *testing.T) {
	// An instance with no tags at all is normal, and must not produce a stray "=" entry.
	for name, i := range map[string]Instance{
		"nil map":   {},
		"empty map": {Tags: map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := i.TagPairs(); len(got) != 0 {
				t.Errorf("TagPairs() = %v, want empty", got)
			}
		})
	}
}

// A tag with a value nobody set is still worth matching on its key.
func TestTagPairsKeepsEmptyValues(t *testing.T) {
	i := Instance{Tags: map[string]string{"scheduled-stop": ""}}
	want := []string{"scheduled-stop="}
	if got := i.TagPairs(); !reflect.DeepEqual(got, want) {
		t.Errorf("TagPairs() = %v, want %v", got, want)
	}
}
