package tui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	awsint "github.com/treyperrone/ssm-tool/internal/aws"
)

// A machine with no ~/.aws/config used to be a hard stop: ParseConfig returned an error and
// the program exited before drawing anything. Now it opens the form.
func TestNewOpensSetupWhenNothingConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m, err := New(context.Background())
	if err != nil {
		t.Fatalf("New with no config: %v", err)
	}
	if m.screen != screenSetup {
		t.Errorf("screen = %v, want screenSetup", m.screen)
	}
}

// A config file that exists but declares neither an sso-session nor a profile left an empty
// picker with nothing selectable — the same dead end by a different route.
func TestNewOpensSetupWhenConfigHasNothingUsable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"),
		[]byte("[default]\nregion = us-east-1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.screen != screenSetup {
		t.Errorf("screen = %v, want screenSetup", m.screen)
	}
}

// Placeholders are examples, not defaults. Falling back to them meant tabbing straight
// through produced a session named "my-sso" pointing at https://my-org.awsapps.com/start —
// a block that looks configured and can never authenticate.
func TestSetupFormDoesNotSubmitPlaceholders(t *testing.T) {
	f := newSetupForm()
	cfg := f.config()

	// Name and start URL have no plausible default, so they stay empty.
	if cfg.Name != "" || cfg.StartURL != "" {
		t.Errorf("untouched form produced name=%q url=%q, want both empty", cfg.Name, cfg.StartURL)
	}
	if err := awsint.ValidateSSOSession(cfg); err == nil {
		t.Error("an untouched form validated; name and start URL are required")
	}
}

// The region is a real prefilled value rather than a placeholder, so enter accepts it.
func TestSetupFormRegionDefaults(t *testing.T) {
	f := newSetupForm()

	if got := f.config().Region; got != defaultRegion {
		t.Errorf("default region = %q, want %q", got, defaultRegion)
	}
	// Prefilled, not a placeholder — the value must actually be in the input, or enter
	// would submit nothing.
	if got := f.inputs[fieldRegion].Value(); got != defaultRegion {
		t.Errorf("region input value = %q, want it prefilled with %q", got, defaultRegion)
	}

	f.inputs[fieldRegion].SetValue("eu-west-1")
	if got := f.config().Region; got != "eu-west-1" {
		t.Errorf("overridden region = %q, want eu-west-1", got)
	}
}

// Clearing the prefilled region is a deliberate act and must not silently resurrect the
// default — an empty region is an error, the same as it was before.
func TestSetupFormClearedRegionIsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.setup.inputs[fieldName].SetValue("crlab")
	m.setup.inputs[fieldStartURL].SetValue("https://crlab.awsapps.com/start")
	m.setup.inputs[fieldRegion].SetValue("")
	m.saveSetup()

	if m.setup.err == nil {
		t.Fatal("an empty region was accepted")
	}
	if !strings.Contains(m.setup.err.Error(), "region") {
		t.Errorf("error does not mention the region: %v", m.setup.err)
	}
}

// Saving with nothing typed must report which field is missing rather than writing anything.
func TestSaveSetupRequiresEveryField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.saveSetup()

	if m.setup.err == nil {
		t.Fatal("saving an empty form succeeded")
	}
	if !strings.Contains(m.setup.err.Error(), "required") {
		t.Errorf("error does not say what is missing: %v", m.setup.err)
	}
	if _, err := os.Stat(filepath.Join(home, ".aws", "config")); err == nil {
		t.Error("empty form still wrote ~/.aws/config")
	}
	if m.screen != screenSetup {
		t.Errorf("screen = %v, want to stay on screenSetup", m.screen)
	}

	// Filling only the name is still not enough.
	m.setup.inputs[fieldName].SetValue("crlab")
	m.saveSetup()
	if m.setup.err == nil {
		t.Error("saving with only a name succeeded")
	}
}

// Whitespace is not a value. " " in the name field must not create [sso-session  ].
func TestSaveSetupRejectsWhitespaceOnlyFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.setup.inputs[fieldName].SetValue("   ")
	m.setup.inputs[fieldStartURL].SetValue("https://e.awsapps.com/start")
	m.setup.inputs[fieldRegion].SetValue("us-east-1")
	m.saveSetup()

	if m.setup.err == nil {
		t.Error("a whitespace-only name was accepted")
	}
}

func TestSetupFormFocusWraps(t *testing.T) {
	f := newSetupForm()

	f.focusOn(numFields) // off the end
	if f.focus != 0 {
		t.Errorf("focus after wrapping forward = %d, want 0", f.focus)
	}
	f.focusOn(-1) // off the start
	if f.focus != numFields-1 {
		t.Errorf("focus after wrapping back = %d, want %d", f.focus, numFields-1)
	}

	// Exactly one input holds focus at a time, or two cursors blink at once.
	focused := 0
	for i := range f.inputs {
		if f.inputs[i].Focused() {
			focused++
		}
	}
	if focused != 1 {
		t.Errorf("%d inputs focused, want exactly 1", focused)
	}
}

func TestSetupViewShowsFieldsAndKeys(t *testing.T) {
	f := newSetupForm()
	got := f.view(100)

	for _, want := range []string{"session name", "SSO start URL", "SSO region", "enter save", "esc quit"} {
		if !strings.Contains(got, want) {
			t.Errorf("setup view missing %q:\n%s", want, got)
		}
	}
}

// Opened from the method screen there is a screen behind the form, so esc must not kill
// the program the way it does on first run.
func TestSetupViewOffersCancelWhenReachedFromMethod(t *testing.T) {
	f := newSetupForm()
	f.cancelable = true
	got := f.view(100)

	if !strings.Contains(got, "esc cancel") {
		t.Errorf("cancelable form does not offer cancel:\n%s", got)
	}
	if strings.Contains(got, "esc quit") {
		t.Errorf("cancelable form still offers quit:\n%s", got)
	}
}

// Adding a second session (a prod range alongside the lab one) must not drop the first —
// the failure mode of appending to the in-memory slice incorrectly, or of replacing it.
func TestSaveSetupKeepsExistingSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	existing := "[sso-session lab]\nsso_start_url = https://lab.awsapps.com/start\nsso_region = us-east-1\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The method screen comes first even with a single session.
	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want screenMethod", m.screen)
	}

	m.StartSetup()
	m.setup.inputs[fieldName].SetValue("prod")
	m.setup.inputs[fieldStartURL].SetValue("https://prod.awsapps.com/start")
	m.setup.inputs[fieldRegion].SetValue("us-west-2")
	m.saveSetup()

	if m.setup.err != nil {
		t.Fatalf("saveSetup: %v", m.setup.err)
	}
	if len(m.ssoSessions) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(m.ssoSessions), m.ssoSessions)
	}
	names := []string{m.ssoSessions[0].Name, m.ssoSessions[1].Name}
	sort.Strings(names)
	if names[0] != "lab" || names[1] != "prod" {
		t.Errorf("sessions = %v, want [lab prod]", names)
	}
	// The new one is selected — it is what the user came to use.
	if m.selSession == nil || m.selSession.Name != "prod" {
		t.Errorf("selSession = %+v, want prod", m.selSession)
	}
	// And it must point into the live slice, not a stale copy.
	if m.selSession != &m.ssoSessions[0] && m.selSession != &m.ssoSessions[1] {
		t.Error("selSession does not point into m.ssoSessions")
	}
}

// The method screen is where "+ Add SSO session" lives, so a single-session user must land
// on it at startup rather than being dropped straight into the account list.
func TestMethodScreenIsFirstWithOneSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"),
		[]byte("[sso-session lab]\nsso_start_url = https://lab.awsapps.com/start\nsso_region = us-east-1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if m.screen != screenMethod {
		t.Fatalf("startup screen = %v, want screenMethod", m.screen)
	}

	// Both the session itself and the add entry are on screen from the first frame.
	var sawSession, sawAdd bool
	for _, li := range m.list.Items() {
		it, ok := li.(item)
		if !ok {
			continue
		}
		switch it.value {
		case "lab":
			sawSession = true
		case methodAddSession:
			sawAdd = true
		}
	}
	if !sawSession {
		t.Error("method list is missing the configured session")
	}
	if !sawAdd {
		t.Error("method list has no add-session entry")
	}

	// And esc from the account list still returns here.
	m.screen = screenAccount
	m.goBack()
	if m.screen != screenMethod {
		t.Errorf("screen after esc from accounts = %v, want screenMethod", m.screen)
	}
}

// The sentinel must not be mistakable for a real session name. ValidateSSOSession rejects
// spaces in names, so a session can never be called this.
func TestAddSessionSentinelCannotBeASessionName(t *testing.T) {
	if err := awsint.ValidateSSOSession(awsint.SSOSessionConfig{
		Name: methodAddSession, StartURL: "https://e.awsapps.com/start", Region: "us-east-1",
	}); err == nil {
		t.Error("a session could legally be named the same as the add-session sentinel")
	}
}
