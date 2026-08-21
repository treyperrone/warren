package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
)

func findItem(m *Model, value string) (item, bool) {
	for _, li := range m.list.Items() {
		if it, ok := li.(item); ok && it.value == value {
			return it, true
		}
	}
	return item{}, false
}

func TestMethodScreenOffersBrowserPreference(t *testing.T) {
	m := modelWithSSOSession(t)
	row, ok := findItem(m, methodBrowserPref)
	if !ok {
		t.Fatal("method screen has no browser-preference row")
	}
	// The row must say what is currently set, or the only way to learn the setting is to
	// open it — and with nothing saved, that is the ask-at-sign-in default.
	if !strings.Contains(row.desc, "ask at each sign-in") {
		t.Errorf("row desc = %q, want the current (default) setting named", row.desc)
	}
}

func TestBrowserScreenAlwaysHasSystemAndNone(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selectMethod(methodBrowserPref)

	if m.screen != screenBrowser {
		t.Fatalf("screen = %v, want screenBrowser", m.screen)
	}
	// Detection found whatever this machine has — possibly nothing, on CI — but the two
	// non-detected options exist unconditionally: no machine state can make the screen empty.
	if _, ok := findItem(m, browserValSystem); !ok {
		t.Error("no system-default row")
	}
	if _, ok := findItem(m, browserValNone); !ok {
		t.Error("no no-browser row")
	}
}

func TestSelectingNoneSavesAndReturns(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selectMethod(methodBrowserPref)
	m.selectBrowser(browserValNone)

	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want back on screenMethod", m.screen)
	}
	if got := browser.LoadPref(); got.Mode != browser.ModeNone {
		t.Errorf("saved mode = %q, want %q", got.Mode, browser.ModeNone)
	}
	if m.notice == "" {
		t.Error("no notice — saving a setting is invisible without one")
	}
	// The method row must reflect the new value immediately, not on next launch.
	row, ok := findItem(m, methodBrowserPref)
	if !ok {
		t.Fatal("browser-preference row missing after save")
	}
	if !strings.Contains(row.desc, "device code") {
		t.Errorf("row desc = %q, want it to describe no-browser mode", row.desc)
	}
}

func TestBrowserProfileScreenGoesBackToBrowserList(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selectMethod(methodBrowserPref)
	m.screen = screenBrowserProfile // as selectBrowser would after a multi-profile pick

	m.goBack()
	if m.screen != screenBrowser {
		t.Fatalf("screen after goBack = %v, want screenBrowser", m.screen)
	}
	m.goBack()
	if m.screen != screenMethod {
		t.Fatalf("screen after second goBack = %v, want screenMethod", m.screen)
	}
}

// The multi-profile path: picking the browser shows its profiles, picking a profile saves
// browser+profile together.
func TestSelectingBrowserWithProfilesThenProfile(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selectMethod(methodBrowserPref)

	m.browsers = []browser.Browser{{Name: "Google Chrome", Kind: browser.KindChromium}}
	m.browserProfiles = nil
	m.selBrowser = &m.browsers[0]
	m.browserProfiles = []browser.Profile{
		{Dir: "Default", Name: "Person 1"},
		{Dir: "Profile 1", Name: "Work"},
	}
	m.buildBrowserProfileList()
	m.screen = screenBrowserProfile

	m.selectBrowserProfile("Profile 1")

	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want screenMethod after saving", m.screen)
	}
	got := browser.LoadPref()
	want := browser.Pref{Mode: browser.ModeBrowser, Browser: "Google Chrome", ProfileDir: "Profile 1", ProfileName: "Work"}
	if got != want {
		t.Errorf("saved pref = %+v, want %+v", got, want)
	}
}

func TestLoginViewShowsURLAndCode(t *testing.T) {
	m := modelWithSSOSession(t)
	m.loading = true
	m.pendingLogin = &awsint.PendingLogin{
		VerificationURL: "https://device.sso.us-east-1.amazonaws.com/?user_code=WXYZ-9876",
		UserCode:        "WXYZ-9876",
	}
	m.loginNote = "opening your default browser"

	out := m.View()
	// The URL and code are the content of this screen: they used to be printed to stderr
	// behind the alt screen, which is the bug the screen exists to fix.
	if !strings.Contains(out, m.pendingLogin.VerificationURL) {
		t.Error("login view does not show the verification URL")
	}
	if !strings.Contains(out, "WXYZ-9876") {
		t.Error("login view does not show the user code")
	}
	if !strings.Contains(out, "opening your default browser") {
		t.Error("login view does not show what happened to the browser")
	}
}

// The pref row is the LAST row of the method list, so the shared list's cursor arrives on
// the browser screen at an index the shorter list may not have. bubbles clamps the page on
// SetItems but not the cursor, so without an explicit Select(0) the screen rendered with no
// row highlighted and Enter dead — on exactly the machines (zero detected browsers) the
// no-browser option exists for.
func TestBrowserScreensResetCursor(t *testing.T) {
	m := modelWithSSOSession(t)
	m.list.Select(len(m.list.Items()) - 1) // sitting on the pref row, as a user would be
	m.selectMethod(methodBrowserPref)

	if _, ok := m.list.SelectedItem().(item); !ok {
		t.Fatalf("browser screen has no selected row (index %d of %d items) — Enter would do nothing",
			m.list.Index(), len(m.list.Items()))
	}
	if m.list.Index() != 0 {
		t.Errorf("browser screen opens on index %d, want 0", m.list.Index())
	}

	// Same on the second hop, into a list that can also be shorter than the cursor.
	m.list.Select(len(m.list.Items()) - 1)
	m.browsers = []browser.Browser{{Name: "Google Chrome", Kind: browser.KindChromium}}
	m.selBrowser = &m.browsers[0]
	m.browserProfiles = []browser.Profile{{Dir: "Default", Name: "Person 1"}, {Dir: "Profile 1", Name: "Work"}}
	m.buildBrowserProfileList()
	if _, ok := m.list.SelectedItem().(item); !ok || m.list.Index() != 0 {
		t.Errorf("profile screen opens on index %d with selection %v, want index 0 with a selected row",
			m.list.Index(), m.list.SelectedItem())
	}
}

// A Chromium profile can legitimately display under its directory name (no name recorded in
// Local State, unreadable Preferences); that must not get it labelled a Firefox profile —
// the label is keyed on the browser's Kind, not on the name coinciding with the directory.
func TestProfileDescKeyedOnKindNotNameShape(t *testing.T) {
	m := modelWithSSOSession(t)
	m.browsers = []browser.Browser{{Name: "Google Chrome", Kind: browser.KindChromium}}
	m.selBrowser = &m.browsers[0]
	m.browserProfiles = []browser.Profile{{Dir: "Profile 1", Name: "Profile 1"}}
	m.buildBrowserProfileList()

	row, ok := findItem(m, "Profile 1")
	if !ok {
		t.Fatal("profile row missing")
	}
	if strings.Contains(row.desc, "Firefox") {
		t.Errorf("Chrome profile described as %q", row.desc)
	}

	m.browsers[0] = browser.Browser{Name: "Firefox", Kind: browser.KindFirefox}
	m.browserProfiles = []browser.Profile{{Dir: "Work", Name: "Work"}}
	m.buildBrowserProfileList()
	row, _ = findItem(m, "Work")
	if !strings.Contains(row.desc, "Firefox") {
		t.Errorf("Firefox profile described as %q, want the Firefox wording", row.desc)
	}
}

// The inline ask: sign-in needed with nothing saved → picker → remember question → the
// device authorization starts with the one-shot choice recorded.
func TestInlineAskFlowOnce(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0]
	m.loading = true

	m.Update(msgLoginAsk{})
	m.loginAskShownAt = time.Time{} // backdate past the double-enter grace window
	if m.screen != screenLoginBrowser {
		t.Fatalf("screen = %v, want screenLoginBrowser", m.screen)
	}
	if m.loading {
		t.Fatal("still loading — the picker's keys would be dead")
	}

	m.selectLoginBrowser(browserValNone)
	if m.screen != screenLoginRemember {
		t.Fatalf("screen = %v, want the remember question", m.screen)
	}

	cmd := m.selectLoginRemember(rememberOnce)
	if cmd == nil {
		t.Fatal("no command returned — the device authorization never starts")
	}
	if !m.loading {
		t.Error("not loading while the authorization starts")
	}
	if m.loginChoice == nil || m.loginChoice.Mode != browser.ModeNone {
		t.Errorf("one-shot choice = %+v, want mode none", m.loginChoice)
	}
	// "Just this once" must not have saved anything: the next sign-in asks again.
	if saved := browser.LoadPref(); saved.Mode != "" {
		t.Errorf("saved pref = %+v after 'just this once'", saved)
	}
	// The one-shot choice dies with the token, so the NEXT sign-in starts clean.
	m.Update(msgToken{token: "tok"})
	if m.loginChoice != nil {
		t.Error("one-shot choice survived the token")
	}
}

// "Always" saves the choice as the default, which is the only way asking stops.
func TestInlineAskFlowAlwaysSaves(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0]

	m.Update(msgLoginAsk{})
	m.loginAskShownAt = time.Time{} // backdate past the double-enter grace window
	m.selectLoginBrowser(browserValSystem)
	if cmd := m.selectLoginRemember(rememberAlways); cmd == nil {
		t.Fatal("no command returned after 'always'")
	}
	if saved := browser.LoadPref(); saved.Mode != browser.ModeSystem {
		t.Errorf("saved pref = %+v, want the system default persisted", saved)
	}
}

// Backing all the way out of the ask abandons the sign-in cleanly.
func TestInlineAskEscapesToMethodScreen(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0]
	m.Update(msgLoginAsk{})

	m.goBack()
	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want screenMethod after abandoning the ask", m.screen)
	}
}

// The settings screen offers "Ask at each sign-in" as a saveable mode, so the inline picker
// can be opted back into after a default was saved.
func TestSettingsOfferAskMode(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selectMethod(methodBrowserPref)
	if _, ok := findItem(m, browserValAsk); !ok {
		t.Fatal("settings browser screen has no ask row")
	}
	m.selectBrowser(browserValAsk)
	if saved := browser.LoadPref(); saved.Mode != browser.ModeAsk {
		t.Errorf("saved mode = %q, want %q", saved.Mode, browser.ModeAsk)
	}
}

// The inline picker can replace the method list within a millisecond of the Enter that
// selected the session — a cold-cache token miss never touches the network. A double-tapped
// or repeating Enter must not silently pick the top browser off a screen nobody saw.
func TestInlineAskSwallowsImmediateEnter(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0]
	m.Update(msgLoginAsk{}) // stamps loginAskShownAt = now

	if cmd := m.selectLoginBrowser(browserValNone); cmd != nil || m.screen != screenLoginBrowser {
		t.Fatalf("Enter within the grace window advanced the screen (screen=%v)", m.screen)
	}
	// After the window, the same press works.
	m.loginAskShownAt = time.Now().Add(-2 * loginAskGrace)
	m.selectLoginBrowser(browserValNone)
	if m.screen != screenLoginRemember {
		t.Fatalf("Enter after the grace window did not advance (screen=%v)", m.screen)
	}
}

// The remember screen scopes "always" to the session being signed in to, which is the whole
// point for anyone whose work and personal SSO sessions belong in different browsers.
func TestInlineAskRememberForSession(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0] // "corp", https://corp.awsapps.com/start

	m.Update(msgLoginAsk{})
	m.loginAskShownAt = time.Time{} // backdate past the double-enter grace window
	m.selectLoginBrowser(browserValNone)

	// The scoped row names the session, so the choice reads as what it does.
	found := false
	for _, li := range m.list.Items() {
		if it, ok := li.(item); ok && it.value == rememberSession {
			found = true
			if !strings.Contains(it.title, "corp") {
				t.Errorf("scoped row title = %q, want the session named", it.title)
			}
		}
	}
	if !found {
		t.Fatal("remember screen has no per-session row")
	}

	if cmd := m.selectLoginRemember(rememberSession); cmd == nil {
		t.Fatal("no command after scoped save — the sign-in never starts")
	}
	p, scoped := browser.ResolvePrefFor(m.selSession.StartURL)
	if !scoped || p.Mode != browser.ModeNone {
		t.Errorf("scoped pref = %+v scoped=%v, want the choice saved for this session", p, scoped)
	}
	// Global stays unset: other sessions keep asking.
	if g := browser.LoadPref(); g.Mode != "" {
		t.Errorf("global pref = %+v after a scoped save", g)
	}
}

// The settings screen shows each override as a row that clears it in place.
func TestSettingsListAndClearOverride(t *testing.T) {
	m := modelWithSSOSession(t)
	url := m.ssoSessions[0].StartURL
	if err := browser.SavePrefFor(url, browser.Pref{Mode: browser.ModeNone}); err != nil {
		t.Fatal(err)
	}

	// The ⚙ row advertises that overrides exist.
	m.buildMethodList()
	row, _ := findItem(m, methodBrowserPref)
	if !strings.Contains(row.desc, "1 session override") {
		t.Errorf("⚙ desc = %q, want the override count", row.desc)
	}

	m.selectMethod(methodBrowserPref)
	clearVal := clearPrefix + url
	or, ok := findItem(m, clearVal)
	if !ok {
		t.Fatal("settings screen has no row for the override")
	}
	if !strings.Contains(or.title, "corp") {
		t.Errorf("override row = %q, want the session named", or.title)
	}

	m.selectBrowser(clearVal)
	if _, scoped := browser.ResolvePrefFor(url); scoped {
		t.Error("override survived clearing")
	}
	if _, ok := findItem(m, clearVal); ok {
		t.Error("cleared override row still listed")
	}
	if m.screen != screenBrowser {
		t.Errorf("screen = %v, want to stay on the settings list after clearing", m.screen)
	}
}

// The tp24 dead-end: an SSO-backed profile whose session expired used to error with
// "login session has expired, please reauthenticate" — telling the user to do by hand what
// the tool exists to do. The sign-in detour must route through the normal flow and come
// back for the profile's credentials.
func TestProfileLoginDetourRoutesAndRetries(t *testing.T) {
	m := modelWithSSOSession(t)
	m.profiles = []awsint.ProfileConfig{{Name: "tp24", SSOSession: "corp"}}
	sess := awsint.SSOSessionConfig{Name: "corp", StartURL: "https://corp.awsapps.com/start", Region: "us-east-1"}

	_, cmd := m.Update(msgProfileLoginNeeded{profile: m.profiles[0], sess: sess})
	if m.selSession == nil || m.selSession.Name != "corp" || m.pendingProfile != "tp24" {
		t.Fatalf("detour state: selSession=%v pendingProfile=%q", m.selSession, m.pendingProfile)
	}
	if cmd == nil {
		t.Fatal("no token fetch started for the detour")
	}

	// Token lands → the flow must go back for the PROFILE, not to the account list.
	_, cmd = m.Update(msgToken{token: "tok"})
	if m.pendingProfile != "" {
		t.Error("pendingProfile not consumed by the token")
	}
	if m.selSession != nil {
		t.Error("selSession still set — goBack would treat a profile flow as a session flow")
	}
	if cmd == nil {
		t.Fatal("no profile retry command after the token")
	}

	// A failed sign-in ends the detour instead of arming a stale retry.
	m.Update(msgProfileLoginNeeded{profile: m.profiles[0], sess: sess})
	m.Update(msgToken{err: errors.New("denied")})
	if m.pendingProfile != "" || m.selSession != nil {
		t.Error("failed sign-in left the profile detour armed")
	}
}
