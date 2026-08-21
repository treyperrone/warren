package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
)

func TestParseLoginArgs(t *testing.T) {
	cases := []struct {
		args    []string
		want    loginInvocation
		wantErr bool
	}{
		{nil, loginInvocation{}, false},
		{[]string{"work"}, loginInvocation{session: "work"}, false},
		{[]string{"--status"}, loginInvocation{status: true}, false},
		{[]string{"-s"}, loginInvocation{status: true}, false},
		// The flag works on either side of the name: nobody remembers argument order for a
		// command they run twice a month.
		{[]string{"--status", "work"}, loginInvocation{session: "work", status: true}, false},
		{[]string{"work", "--status"}, loginInvocation{session: "work", status: true}, false},
		// A mistyped flag must be an error, not a session name that AWS rejects later.
		{[]string{"--stauts"}, loginInvocation{}, true},
		{[]string{"work", "personal"}, loginInvocation{}, true},
	}
	for _, c := range cases {
		got, err := parseLoginArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Errorf("parseLoginArgs(%v) error = %v, wantErr %v", c.args, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseLoginArgs(%v) = %+v, want %+v", c.args, got, c.want)
		}
	}
}

func TestLoginTargets(t *testing.T) {
	sessions := []awsint.SSOSessionConfig{{Name: "corp", StartURL: "https://corp.awsapps.com/start", Region: "us-east-1"}}
	profiles := []awsint.ProfileConfig{
		{Name: "tp24", SSOSession: "corp"},
		{Name: "legacy-lab", SSOStartURL: "https://old.awsapps.com/start", SSORegion: "us-west-2"},
		{Name: "ci-keys"},
		{Name: "orphan", SSOSession: "gone"},
	}

	got := loginTargets(sessions, profiles)
	if len(got) != 5 {
		t.Fatalf("got %d targets, want 5", len(got))
	}

	// tp24 signs in through corp — the tp24 dead-end repro is exactly this row working.
	if tp := got[1]; tp.sess == nil || tp.sess.Name != "corp" || tp.static() {
		t.Errorf("tp24 = %+v, want it resolved to the corp session", tp)
	}
	// Legacy inline SSO synthesizes a session from the profile itself.
	if lg := got[2]; lg.sess == nil || lg.sess.StartURL != "https://old.awsapps.com/start" || lg.sess.Region != "us-west-2" {
		t.Errorf("legacy = %+v, want a synthesized session", lg)
	} else if !strings.Contains(lg.describe(), "legacy SSO") {
		t.Errorf("legacy describe = %q", lg.describe())
	}
	// Static: nothing to sign in to, and the row says so.
	if ck := got[3]; !ck.static() || !strings.Contains(ck.describe(), "no sign-in") {
		t.Errorf("static = %+v describe=%q", ck, ck.describe())
	}
	// A profile naming a missing session is broken config, not a static profile.
	if or := got[4]; or.brokenRef != "gone" || or.static() {
		t.Errorf("orphan = %+v, want brokenRef=gone", or)
	}
}

func TestResolveLoginTarget(t *testing.T) {
	sessions := []awsint.SSOSessionConfig{{Name: "corp"}, {Name: "lab"}}
	profiles := []awsint.ProfileConfig{{Name: "ci-keys"}}
	targets := loginTargets(sessions, profiles)

	if _, err := resolveLoginTarget(nil, ""); err == nil || !strings.Contains(err.Error(), "warren setup") {
		t.Errorf("no targets: err = %v, want a pointer at setup", err)
	}
	// Exactly one identity needs no name — the common case must stay zero-argument.
	if tg, err := resolveLoginTarget(targets[:1], ""); err != nil || tg.name != "corp" {
		t.Errorf("single: got %v, %v", tg, err)
	}
	// Several identities and no name is a decision the tool must not guess; the error
	// carries every candidate so a headless user does not need a second round trip.
	if _, err := resolveLoginTarget(targets, ""); err == nil || !strings.Contains(err.Error(), "corp, lab, ci-keys") {
		t.Errorf("ambiguous: err = %v, want the candidates listed", err)
	}
	if tg, err := resolveLoginTarget(targets, "ci-keys"); err != nil || !tg.static() {
		t.Errorf("by profile name: got %v, %v", tg, err)
	}
	if _, err := resolveLoginTarget(targets, "prod"); err == nil || !strings.Contains(err.Error(), "corp, lab, ci-keys") {
		t.Errorf("unknown name: err = %v, want the candidates listed", err)
	}
}

// The teletype picker, driven by scripted stdin. The conversation IS the interface, so the
// tests pin both the choices made and the prompts a user would have seen.
func TestPromptBrowserChoice(t *testing.T) {
	// A Chrome with two real profiles, built on disk so Profiles() has something to list.
	dataDir := t.TempDir()
	ls := `{"profile":{"info_cache":{"Default":{"name":"Person 1"},"Profile 1":{"name":"Work"}}}}`
	if err := os.WriteFile(filepath.Join(dataDir, "Local State"), []byte(ls), 0o600); err != nil {
		t.Fatal(err)
	}
	browsers := []browser.Browser{
		{Name: "Google Chrome", Kind: browser.KindChromium, DataDir: dataDir},
		{Name: "Safari", Kind: browser.KindSafari},
	}

	t.Run("browser then profile then session-scope", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("1\n2\n2\n"))
		var out strings.Builder
		pref, remember, err := promptBrowserChoice(in, &out, "corp", browsers)
		if err != nil {
			t.Fatal(err)
		}
		want := browser.Pref{Mode: browser.ModeBrowser, Browser: "Google Chrome", ProfileDir: "Profile 1", ProfileName: "Work"}
		if pref != want || remember != rememberForSession {
			t.Errorf("got %+v remember=%q, want %+v remember=%q", pref, remember, want, rememberForSession)
		}
		if !strings.Contains(out.String(), "Always for corp") {
			t.Errorf("remember menu does not name the session:\n%s", out.String())
		}
	})

	t.Run("empty input takes the first row everywhere", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("\n\n\n"))
		var out strings.Builder
		pref, remember, err := promptBrowserChoice(in, &out, "corp", browsers)
		if err != nil {
			t.Fatal(err)
		}
		if pref.Browser != "Google Chrome" || pref.ProfileDir != "Default" || remember != rememberJustOnce {
			t.Errorf("enter-through = %+v remember=%q, want first browser, first profile, just-once", pref, remember)
		}
	})

	t.Run("no-profile browser skips the profile menu", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("2\n1\n"))
		var out strings.Builder
		pref, _, err := promptBrowserChoice(in, &out, "corp", browsers)
		if err != nil {
			t.Fatal(err)
		}
		if pref.Browser != "Safari" || pref.ProfileDir != "" {
			t.Errorf("Safari pick = %+v, want no profile", pref)
		}
		if strings.Contains(out.String(), "Safari profile") {
			t.Error("a profile menu was shown for a browser that has none")
		}
	})

	t.Run("system and none rows exist past the browsers", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("4\n1\n"))
		var out strings.Builder
		pref, _, err := promptBrowserChoice(in, &out, "corp", browsers)
		if err != nil {
			t.Fatal(err)
		}
		if pref.Mode != browser.ModeNone {
			t.Errorf("row 4 = %+v, want the no-browser mode", pref)
		}
	})

	t.Run("garbage is re-asked, EOF aborts", func(t *testing.T) {
		in := bufio.NewReader(strings.NewReader("banana\n99\n3\n1\n"))
		var out strings.Builder
		pref, _, err := promptBrowserChoice(in, &out, "corp", browsers)
		if err != nil || pref.Mode != browser.ModeSystem {
			t.Errorf("after re-asks got %+v, %v; want the system row", pref, err)
		}
		if !strings.Contains(out.String(), "between 1 and") {
			t.Error("no re-ask message for out-of-range input")
		}

		if _, _, err := promptBrowserChoice(bufio.NewReader(strings.NewReader("")), io.Discard, "corp", browsers); err == nil {
			t.Error("EOF mid-conversation did not surface as an error")
		}
	})
}

func TestDescribeChoice(t *testing.T) {
	chrome := browser.Pref{Mode: browser.ModeBrowser, Browser: "Brave", ProfileName: "cyberrange-lab"}
	cases := []struct {
		asked, scoped bool
		p             browser.Pref
		want          string
	}{
		// The repro that motivated this line: a saved override opening Brave read as a bug.
		{false, true, chrome, "using the saved override for corp: Brave — cyberrange-lab"},
		{true, false, chrome, "using Brave — cyberrange-lab"},
		{false, false, browser.Pref{Mode: browser.ModeSystem}, "using the saved default: system default browser"},
		// Unset/ask with nobody to ask: OpenForLogin's own note says what happens; adding a
		// second line here would just repeat it.
		{false, false, browser.Pref{}, ""},
		{false, false, browser.Pref{Mode: browser.ModeAsk}, ""},
	}
	for _, c := range cases {
		if got := describeChoice(c.asked, c.scoped, "corp", c.p); got != c.want {
			t.Errorf("describeChoice(asked=%v scoped=%v %+v) = %q, want %q", c.asked, c.scoped, c.p, got, c.want)
		}
	}
}

func TestParseLoginArgsCodeFlag(t *testing.T) {
	for _, flag := range []string{"--code", "--no-browser"} {
		inv, err := parseLoginArgs([]string{flag, "work"})
		if err != nil || !inv.code || inv.session != "work" {
			t.Errorf("parseLoginArgs(%s work) = %+v, %v", flag, inv, err)
		}
	}
}

// The bare-invocation identity menu: numbered, annotated, Enter takes the first row.
func TestPromptTargetChoice(t *testing.T) {
	targets := loginTargets(
		[]awsint.SSOSessionConfig{{Name: "corp", StartURL: "https://corp.awsapps.com/start"}},
		[]awsint.ProfileConfig{{Name: "tp24", SSOSession: "corp"}, {Name: "ci-keys"}},
	)

	in := bufio.NewReader(strings.NewReader("2\n"))
	var out strings.Builder
	got, err := promptTargetChoice(in, &out, targets)
	if err != nil || got.name != "tp24" {
		t.Fatalf("choice 2 = %v, %v", got, err)
	}
	menu := out.String()
	// The annotations are the difference between rows that sign in, validate, or point at
	// broken config — the menu must carry them.
	if !strings.Contains(menu, "https://corp.awsapps.com/start") ||
		!strings.Contains(menu, "signs in via sso-session corp") ||
		!strings.Contains(menu, "no sign-in") {
		t.Errorf("menu is missing annotations:\n%s", menu)
	}

	if got, err := promptTargetChoice(bufio.NewReader(strings.NewReader("\n")), io.Discard, targets); err != nil || got.name != "corp" {
		t.Errorf("enter-through = %v, %v; want the first identity", got, err)
	}
	if _, err := promptTargetChoice(bufio.NewReader(strings.NewReader("")), io.Discard, targets); err == nil {
		t.Error("EOF did not surface as an error")
	}
}

// The hundred-profile box: identities sharing a start URL are ONE fact and one action, and
// the set views must say it once — while explicit names keep resolving individually.
func TestCollapseTargets(t *testing.T) {
	sessions := []awsint.SSOSessionConfig{{Name: "crlab", StartURL: "https://crlab.awsapps.com/start"}}
	profiles := []awsint.ProfileConfig{
		{Name: "crlab-1", SSOSession: "crlab"},
		{Name: "crlab-2", SSOSession: "crlab"},
		{Name: "crlab-3", SSOSession: "crlab"},
		{Name: "old-a", SSOStartURL: "https://old.awsapps.com/start", SSORegion: "us-east-1"},
		{Name: "old-b", SSOStartURL: "https://old.awsapps.com/start", SSORegion: "us-east-1"},
		{Name: "ci-keys"},
		{Name: "orphan", SSOSession: "gone"},
	}
	full := loginTargets(sessions, profiles)
	got := collapseTargets(full)

	// crlab(+3), old-a(+1), ci-keys, orphan — 8 rows down to 4.
	if len(got) != 4 {
		t.Fatalf("collapsed to %d rows: %+v", len(got), got)
	}
	if got[0].name != "crlab" || got[0].covers != 3 {
		t.Errorf("session group = %+v, want crlab covering 3", got[0])
	}
	if !strings.Contains(got[0].describe(), "covers 3 profiles") {
		t.Errorf("describe = %q, want the count carried", got[0].describe())
	}
	// A legacy URL shared only by profiles is represented by the first of them.
	if got[1].name != "old-a" || got[1].covers != 1 {
		t.Errorf("legacy group = %+v, want old-a covering 1", got[1])
	}
	// Statics and broken rows never collapse — each is its own fact.
	if got[2].name != "ci-keys" || got[3].name != "orphan" || got[2].covers != 0 {
		t.Errorf("singles = %+v, %+v", got[2], got[3])
	}
	// Explicit names still resolve on the FULL list.
	if tg, err := resolveLoginTarget(full, "crlab-2"); err != nil || tg.sess.Name != "crlab" {
		t.Errorf("resolve crlab-2 on full list = %v, %v", tg, err)
	}
}

// A session row arriving after a profile already claimed its URL takes over as the group's
// name — "sign in to crlab", never "sign in to crlab-1" when the real session exists.
func TestCollapseSessionWinsRepresentative(t *testing.T) {
	url := "https://crlab.awsapps.com/start"
	targets := []loginTarget{
		{name: "crlab-1", isProfile: true, sess: &awsint.SSOSessionConfig{Name: "crlab", StartURL: url}},
		{name: "crlab", sess: &awsint.SSOSessionConfig{Name: "crlab", StartURL: url}},
		{name: "crlab-2", isProfile: true, sess: &awsint.SSOSessionConfig{Name: "crlab", StartURL: url}},
	}
	got := collapseTargets(targets)
	if len(got) != 1 || got[0].name != "crlab" || got[0].covers != 2 {
		t.Fatalf("collapsed = %+v, want the session named crlab covering 2", got)
	}
}

// The CLI browser policy in one table: --code beats everything, --browser opts in (saved
// answer > picker-on-TTY > system default), a saved preference is honored, and the default
// — unset or ask-mode — is a pure device-code sign-in.
func TestLoginOpenPlan(t *testing.T) {
	chrome := browser.Pref{Mode: browser.ModeBrowser, Browser: "Google Chrome"}
	cases := []struct {
		name string
		inv  loginInvocation
		pref browser.Pref
		tty  bool
		want openPlan
	}{
		{"default unset", loginInvocation{}, browser.Pref{}, true, planCode},
		{"default ask-mode", loginInvocation{}, browser.Pref{Mode: browser.ModeAsk}, true, planCode},
		// The final policy: even a saved answer never opens a browser without --browser.
		{"saved browser still device-code", loginInvocation{}, chrome, true, planCode},
		{"saved system still device-code", loginInvocation{}, browser.Pref{Mode: browser.ModeSystem}, true, planCode},
		{"saved none device-code", loginInvocation{}, browser.Pref{Mode: browser.ModeNone}, true, planCode},
		{"--code beats saved browser", loginInvocation{code: true}, chrome, true, planCode},
		{"--browser with saved answer", loginInvocation{browser: true}, chrome, false, planPref},
		{"--browser with saved system", loginInvocation{browser: true}, browser.Pref{Mode: browser.ModeSystem}, false, planPref},
		{"--browser unset on tty asks", loginInvocation{browser: true}, browser.Pref{}, true, planPicker},
		{"--browser unset piped opens system", loginInvocation{browser: true}, browser.Pref{}, false, planSystem},
		{"--browser over saved none asks", loginInvocation{browser: true}, browser.Pref{Mode: browser.ModeNone}, true, planPicker},
	}
	for _, c := range cases {
		if got := loginOpenPlan(c.inv, c.pref, c.tty); got != c.want {
			t.Errorf("%s: plan = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseLoginArgsBrowserFlag(t *testing.T) {
	inv, err := parseLoginArgs([]string{"--browser", "work"})
	if err != nil || !inv.browser {
		t.Errorf("parseLoginArgs(--browser work) = %+v, %v", inv, err)
	}
	if _, err := parseLoginArgs([]string{"--code", "--browser"}); err == nil {
		t.Error("contradictory flags were accepted")
	}
}
