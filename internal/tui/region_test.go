package tui

import (
	"context"
	"strings"
	"testing"

	awsint "github.com/treyperrone/postern/internal/aws"
)

func newFormModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.screen != screenSetup {
		t.Fatalf("screen = %v, want screenSetup", m.screen)
	}
	return m
}

func TestRegionPickerOpensOnCurrentValue(t *testing.T) {
	m := newFormModel(t)
	m.setup.inputs[fieldRegion].SetValue("eu-west-2")
	m.openRegionPicker()

	if m.screen != screenRegion {
		t.Fatalf("screen = %v, want screenRegion", m.screen)
	}
	if len(m.list.Items()) != len(awsint.SSORegions) {
		t.Errorf("picker has %d items, want %d", len(m.list.Items()), len(awsint.SSORegions))
	}

	// Opening on the value already in the field saves scrolling to find it.
	sel, ok := m.list.SelectedItem().(item)
	if !ok {
		t.Fatal("no selected item")
	}
	if sel.value != "eu-west-2" {
		t.Errorf("picker opened on %q, want the current value eu-west-2", sel.value)
	}
}

func TestSelectRegionWritesBackToForm(t *testing.T) {
	m := newFormModel(t)
	m.openRegionPicker()
	m.selectRegion("ap-southeast-2")

	if m.screen != screenSetup {
		t.Errorf("screen = %v, want to return to screenSetup", m.screen)
	}
	if got := m.setup.inputs[fieldRegion].Value(); got != "ap-southeast-2" {
		t.Errorf("region field = %q, want ap-southeast-2", got)
	}
	if got := m.setup.config().Region; got != "ap-southeast-2" {
		t.Errorf("config region = %q, want ap-southeast-2", got)
	}
	// Focus lands on the field that changed, so the change is visible.
	if m.setup.focus != fieldRegion {
		t.Errorf("focus = %d, want the region field (%d)", m.setup.focus, fieldRegion)
	}
}

// Backing out must not alter the field — the picker is a shortcut, not a commitment.
func TestRegionPickerEscapeLeavesFieldAlone(t *testing.T) {
	m := newFormModel(t)
	m.setup.inputs[fieldRegion].SetValue("eu-central-1")
	m.openRegionPicker()
	m.goBack()

	if m.screen != screenSetup {
		t.Errorf("screen = %v, want screenSetup", m.screen)
	}
	if got := m.setup.inputs[fieldRegion].Value(); got != "eu-central-1" {
		t.Errorf("region field = %q, want it unchanged at eu-central-1", got)
	}
}

// The list is a convenience, not a gate. A region AWS adds after this snapshot was vendored
// must still be usable, or the tool breaks for a new region until it is rebuilt.
func TestUnlistedRegionIsStillAccepted(t *testing.T) {
	const future = "xx-moon-1"
	for _, r := range awsint.SSORegions {
		if r == future {
			t.Fatalf("%s is unexpectedly in the vendored list", future)
		}
	}

	m := newFormModel(t)
	m.setup.inputs[fieldName].SetValue("moonbase")
	m.setup.inputs[fieldStartURL].SetValue("https://moonbase.awsapps.com/start")
	m.setup.inputs[fieldRegion].SetValue(future)
	m.saveSetup()

	if m.setup.err != nil {
		t.Fatalf("a region outside the vendored list was rejected: %v", m.setup.err)
	}
	if m.selSession == nil || m.selSession.Region != future {
		t.Errorf("saved session = %+v, want region %s", m.selSession, future)
	}
}

func TestSetupViewMentionsRegionPicker(t *testing.T) {
	f := newSetupForm()
	if got := f.view(100); !strings.Contains(got, "ctrl+r") {
		t.Errorf("setup view does not advertise the region picker:\n%s", got)
	}
}

// The vendored list must contain the default, or the picker opens on nothing.
func TestDefaultRegionIsInTheList(t *testing.T) {
	for _, r := range awsint.SSORegions {
		if r == defaultRegion {
			return
		}
	}
	t.Errorf("default region %q is not in SSORegions", defaultRegion)
}

func TestGovCloudRegionsArePresentAndLabelled(t *testing.T) {
	want := map[string]bool{"us-gov-east-1": false, "us-gov-west-1": false}
	for _, r := range awsint.SSORegions {
		if _, ok := want[r]; ok {
			want[r] = true
		}
	}
	for r, found := range want {
		if !found {
			t.Errorf("%s missing from SSORegions", r)
		}
	}

	// GovCloud needs separate credentials and a separate account structure, so it must not
	// look like just another us- region in the list.
	m := newFormModel(t)
	m.openRegionPicker()
	for _, li := range m.list.Items() {
		it, ok := li.(item)
		if !ok || !strings.HasPrefix(it.value, "us-gov-") {
			continue
		}
		if !strings.Contains(it.desc, "GovCloud") {
			t.Errorf("%s is not labelled GovCloud (desc = %q)", it.value, it.desc)
		}
	}
}

// The vendored list is sorted; the picker renders it in order, so an unsorted entry would
// show up in the wrong place.
func TestSSORegionsAreSorted(t *testing.T) {
	for i := 1; i < len(awsint.SSORegions); i++ {
		if awsint.SSORegions[i-1] >= awsint.SSORegions[i] {
			t.Errorf("out of order or duplicated: %q then %q",
				awsint.SSORegions[i-1], awsint.SSORegions[i])
		}
	}
}
