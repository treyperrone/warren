package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

func TestProfileSlug(t *testing.T) {
	cases := map[[2]string]string{
		{"Corp Lab", "AdminRole"}:        "corp-lab-adminrole",
		{"prod (123)", "ReadOnly+Audit"}: "prod-123-readonly-audit",
		{"--weird--", "role"}:            "weird-role",
	}
	for in, want := range cases {
		if got := profileSlug(in[0], in[1]); got != want {
			t.Errorf("profileSlug(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// The save-profile row exists only in the sso-session flow: a named-profile flow has no
// session/account/role triple to write, and offering the row there would produce a broken
// block.
func TestSaveProfileRowOnlyInSessionFlow(t *testing.T) {
	m := modelWithSSOSession(t)
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "Admin"}

	m.buildActionList() // selSession nil: profile flow
	if _, ok := findItem(m, actionSaveProfile); ok {
		t.Error("save-profile row offered with no sso-session selected")
	}

	m.selSession = &m.ssoSessions[0]
	m.selAccount = &awsint.Account{ID: "123456789012", Name: "Corp Lab"}
	m.buildActionList()
	row, ok := findItem(m, actionSaveProfile)
	if !ok {
		t.Fatal("save-profile row missing in the session flow")
	}
	if !strings.Contains(row.desc, "corp-lab-admin") {
		t.Errorf("row desc = %q, want the generated profile name visible before selecting", row.desc)
	}
}

// The favorite round trip inside the TUI: star on the action screen, see it pinned first on
// the method screen, and Enter goes straight for the token with account+role staged.
func TestFavoriteStarPinSelect(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0]
	m.selAccount = &awsint.Account{ID: "123456789012", Name: "Corp Lab"}
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "AdminRole"}

	m.buildActionList()
	if _, ok := findItem(m, actionFavAdd); !ok {
		t.Fatal("no add-to-favorites row")
	}
	m.selectAction(actionFavAdd)
	if _, ok := findItem(m, actionFavRemove); !ok {
		t.Fatal("star did not flip to remove after adding")
	}

	m.selSession, m.selAccount = nil, nil
	m.buildMethodList()
	first, ok := m.list.Items()[0].(item)
	if !ok || !strings.HasPrefix(first.title, "★ Corp Lab / AdminRole") {
		t.Fatalf("first method row = %+v, want the pinned favorite", first)
	}
	// The nickname is searchable even though the row does not display it.
	if !strings.Contains(first.FilterValue(), "corp-lab-adminrole") {
		t.Errorf("favorite row not searchable by nickname: %q", first.FilterValue())
	}

	cmd := m.selectMethod(first.value)
	if cmd == nil || !m.loading {
		t.Fatal("selecting the favorite did not start a token fetch")
	}
	if m.selSession == nil || m.selAccount == nil || m.pendingFavRole != "AdminRole" {
		t.Fatalf("favorite staging: sess=%v acct=%v role=%q", m.selSession, m.selAccount, m.pendingFavRole)
	}

	// Token lands: the flow fetches role credentials directly, no account list.
	_, cmd = m.Update(msgToken{token: "tok"})
	if m.pendingFavRole != "" || cmd == nil {
		t.Fatalf("favorite role fetch not started (pending=%q)", m.pendingFavRole)
	}

	// Esc from the action screen after a favorite goes home, not to an empty account list.
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "AdminRole"}
	m.accounts = nil
	m.screen = screenAction
	m.goBack()
	if m.screen != screenMethod {
		t.Fatalf("goBack from favorite action screen = %v, want screenMethod", m.screen)
	}
}

// A favorite whose sso-session was deleted must explain itself, not guess.
func TestFavoriteWithDeadSessionErrors(t *testing.T) {
	m := modelWithSSOSession(t)
	if err := browser.AddFavorite(browser.Favorite{
		Nickname: "ghost", StartURL: "https://gone.awsapps.com/start", AccountID: "1", Role: "r",
	}); err != nil {
		t.Fatal(err)
	}
	m.buildMethodList()
	m.selectMethod("fav:0")
	if m.err == nil || !strings.Contains(m.err.Error(), "gone.awsapps.com") {
		t.Fatalf("err = %v, want the orphaned start URL named", m.err)
	}
}

// Connection favorites: the tunnel manager offers to star the connection that just started,
// and selecting the pinned row later replays it — creds, then instance resolution by Name
// tag, then the same start path.
func TestConnectionFavoriteStarAndReplay(t *testing.T) {
	m := modelWithSSOSession(t)
	m.selSession = &m.ssoSessions[0]
	m.selAccount = &awsint.Account{ID: "123456789012", Name: "Corp Lab"}
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "AdminRole"}

	// The connection lands: candidate noted, star row offered.
	m.noteConnFavCandidate("kali-box", "rdp", "")
	m.buildMainList()
	row, ok := findItem(m, "favconn")
	if !ok {
		t.Fatal("no favorite-this-connection row after a tunnel started")
	}
	if !strings.Contains(row.desc, "corp-lab-adminrole-kali-box-rdp") {
		t.Errorf("row desc = %q, want the nickname visible", row.desc)
	}
	m.handleMainSelect("favconn")
	if _, saved := browser.FindFavorite(*m.connFavCandidate); !saved {
		t.Fatal("star did not save the connection favorite")
	}
	// Saved: the row withdraws.
	m.buildMainList()
	if _, ok := findItem(m, "favconn"); ok {
		t.Error("star row still offered after saving")
	}

	// Replay: the pinned row stages the connection half alongside account and role.
	m.selSession, m.selAccount, m.awsSess = nil, nil, nil
	m.buildMethodList()
	first := m.list.Items()[0].(item)
	if !strings.HasPrefix(first.title, "★ Corp Lab / AdminRole") {
		t.Fatalf("first row = %q, want the connection favorite pinned", first.title)
	}
	m.selectMethod(first.value)
	if m.pendingFavConn == nil || m.pendingFavConn.InstanceName != "kali-box" {
		t.Fatalf("connection half not staged: %+v", m.pendingFavConn)
	}

	// Instance resolution: by Name TAG, against what is running.
	m.token = "tok"
	m.pendingFavRole = "" // creds step done for this test's purposes
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "AdminRole"}
	f := *m.pendingFavConn

	// Zero matches: land on the list with the reason, favorite consumed.
	m.Update(msgInstances{instances: []awsint.Instance{{ID: "i-1", Name: "other"}}})
	if m.screen != screenInstance || !strings.Contains(m.notice, "not running") {
		t.Errorf("zero-match: screen=%v notice=%q", m.screen, m.notice)
	}

	// Ambiguity: same landing, different reason.
	m.pendingFavConn = &f
	m.Update(msgInstances{instances: []awsint.Instance{{ID: "i-1", Name: "kali-box"}, {ID: "i-2", Name: "kali-box"}}})
	if !strings.Contains(m.notice, "2 running instances") {
		t.Errorf("ambiguous: notice=%q", m.notice)
	}

	// Unique match: the instance is selected and the RDP start path engages (loading, cmd).
	m.pendingFavConn = &f
	_, cmd := m.Update(msgInstances{instances: []awsint.Instance{{ID: "i-9", Name: "kali-box", Platform: "windows"}}})
	if m.selInstance == nil || m.selInstance.ID != "i-9" {
		t.Fatalf("unique match not selected: %+v", m.selInstance)
	}
	if cmd == nil || !m.loading {
		t.Error("RDP start did not engage on the unique match")
	}
	if m.pendingFavConn != nil {
		t.Error("connection half not consumed")
	}
}

// Past favInlineMax the method screen shows one collapsed row instead of a bookmark pile,
// and the dedicated screen carries the full set plus the removal flow.
func TestFavoritesCollapseBeyondInlineMax(t *testing.T) {
	m := modelWithSSOSession(t)
	url := m.ssoSessions[0].StartURL
	for i := 0; i < favInlineMax+2; i++ {
		if err := browser.AddFavorite(browser.Favorite{
			Nickname: fmt.Sprintf("fav-%d", i), StartURL: url,
			AccountID: fmt.Sprintf("%012d", i), AccountName: fmt.Sprintf("Acct %d", i), Role: "Admin",
		}); err != nil {
			t.Fatal(err)
		}
	}

	m.buildMethodList()
	if _, ok := findItem(m, "fav:0"); ok {
		t.Error("favorites still inline past the cap")
	}
	row, ok := findItem(m, methodFavList)
	if !ok || !strings.Contains(row.title, fmt.Sprintf("(%d)", favInlineMax+2)) {
		t.Fatalf("collapsed row = %+v", row)
	}

	m.selectMethod(methodFavList)
	if m.screen != screenFavorites {
		t.Fatalf("screen = %v", m.screen)
	}
	if _, ok := findItem(m, "fav:0"); !ok {
		t.Error("favorites screen missing the bookmarks")
	}

	// Remove flow: delete one, the list shrinks in place.
	m.buildFavoriteRemoveList()
	m.screen = screenFavoriteRemove
	m.selectFavoriteRemove("favdel:0")
	if len(browser.Favorites()) != favInlineMax+1 {
		t.Errorf("favorite not removed: %d left", len(browser.Favorites()))
	}
	if _, ok := findItem(m, fmt.Sprintf("favdel:%d", favInlineMax)); !ok {
		t.Error("remove list did not rebuild in place")
	}
}

// Profile removal: preview screen carries the exact block, "keep" is the safe first row,
// and removal reloads what the picker knows.
func TestProfileRemoveFlow(t *testing.T) {
	m := modelWithSSOSession(t)
	if err := awsint.AddCredentialProcessProfile("bad-one", "corp", "111111111111", "Admin"); err != nil {
		t.Fatal(err)
	}
	sessions, profiles, _ := awsint.ParseConfig()
	m.ssoSessions, m.profiles = sessions, profiles

	m.buildMethodList()
	if _, ok := findItem(m, methodRemoveProfil); !ok {
		t.Fatal("no remove-profile row despite a profile existing")
	}
	m.selectMethod(methodRemoveProfil)
	if m.screen != screenProfileRemove {
		t.Fatalf("screen = %v", m.screen)
	}

	m.selectProfileRemove("bad-one")
	if m.screen != screenProfileConfirm || !strings.Contains(m.profileRemoveBlock, "[profile bad-one]") {
		t.Fatalf("confirm: screen=%v block=%q", m.screen, m.profileRemoveBlock)
	}
	if first := m.list.Items()[0].(item); first.value != "keep" {
		t.Errorf("first confirm row = %+v — Enter-through must be safe", first)
	}

	// Keep: nothing changes.
	m.selectProfileConfirm("keep")
	if _, _, err := awsint.ParseConfig(); err != nil {
		t.Fatal(err)
	}
	if len(m.profiles) != 1 {
		t.Error("keep removed something")
	}

	// Remove: the block goes, the picker reloads, and with zero profiles left the
	// remove-profile row withdraws from the method screen.
	m.screen = screenProfileConfirm
	m.selectProfileConfirm("remove")
	if len(m.profiles) != 0 {
		t.Errorf("profiles after removal: %+v", m.profiles)
	}
	if m.screen != screenMethod {
		t.Fatalf("screen = %v, want method with nothing left to remove", m.screen)
	}
	if _, ok := findItem(m, methodRemoveProfil); ok {
		t.Error("remove-profile row offered with no profiles")
	}
}

// x on a highlighted inline favorite deletes it — the fix for "I have a bad favorite and
// no TUI way to remove it" when the count is under the collapse threshold and the manage
// screen therefore does not exist.
func TestInlineFavoriteRemovedWithX(t *testing.T) {
	m := modelWithSSOSession(t)
	if err := browser.AddFavorite(browser.Favorite{
		Nickname: "linux-box-rdp", StartURL: m.ssoSessions[0].StartURL,
		AccountID: "111111111111", AccountName: "Lab", Role: "Admin",
		InstanceName: "linux-box", ConnType: "rdp",
	}); err != nil {
		t.Fatal(err)
	}
	m.buildMethodList()
	row, ok := findItem(m, "fav:0")
	if !ok {
		t.Fatal("favorite not inline")
	}
	if !strings.Contains(row.desc, "x removes") {
		t.Errorf("row desc = %q — the key must advertise itself", row.desc)
	}

	m.list.Select(0) // cursor on the favorite
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if len(browser.Favorites()) != 0 {
		t.Fatal("x did not remove the favorite")
	}
	if _, ok := findItem(m, "fav:0"); ok {
		t.Error("row still rendered after removal")
	}
	if !strings.Contains(m.notice, "linux-box-rdp") {
		t.Errorf("notice = %q, want the removed nickname named", m.notice)
	}

	// x anywhere else must do nothing destructive: on a session row it is just a key.
	m.list.Select(0)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.err != nil {
		t.Errorf("x on a non-favorite row errored: %v", m.err)
	}
}

// RDP toward a Linux box: the conn-type row warns in place, and a favorite replay pauses on
// that screen instead of silently tunnelling to a port nothing listens on. Enter still
// proceeds — xrdp exists — and unknown platforms stay silent so the warning keeps meaning.
func TestRDPLinuxMismatchWarns(t *testing.T) {
	m := modelWithSSOSession(t)
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "Admin"}
	m.selInstance = &awsint.Instance{ID: "i-1", Name: "kali", Platform: "linux"}
	m.buildConnTypeList()
	rdp, _ := findItem(m, "rdp")
	if !strings.Contains(rdp.desc, "Linux box") {
		t.Errorf("rdp row = %q, want the mismatch named", rdp.desc)
	}

	m.selInstance = &awsint.Instance{ID: "i-2", Name: "mystery", Platform: ""}
	m.buildConnTypeList()
	if rdp, _ := findItem(m, "rdp"); strings.Contains(rdp.desc, "Linux") {
		t.Errorf("unknown platform warned anyway: %q", rdp.desc)
	}

	// Favorite replay: unique match, RDP wanted, Linux reported → pause on the screen.
	m.pendingFavConn = &browser.Favorite{InstanceName: "kali", ConnType: "rdp"}
	_, cmd := m.Update(msgInstances{instances: []awsint.Instance{{ID: "i-1", Name: "kali", Platform: "linux"}}})
	if cmd != nil || m.screen != screenConnType {
		t.Fatalf("paused wrong: cmd=%v screen=%v", cmd, m.screen)
	}
	if !strings.Contains(m.notice, "paused") {
		t.Errorf("notice = %q", m.notice)
	}
	// A Windows box replays straight through.
	m.pendingFavConn = &browser.Favorite{InstanceName: "winbox", ConnType: "rdp"}
	_, cmd = m.Update(msgInstances{instances: []awsint.Instance{{ID: "i-3", Name: "winbox", Platform: "windows"}}})
	if cmd == nil {
		t.Error("windows RDP favorite did not auto-connect")
	}
}

// Proceeding past the Linux-RDP warning is the "don't show this again": the warning and the
// favorite pause both stand down for that account+name afterwards, and only for it.
func TestRDPLinuxAckSuppressesWarning(t *testing.T) {
	m := modelWithSSOSession(t)
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "Admin", AccountID: "111111111111"}
	m.selInstance = &awsint.Instance{ID: "i-1", Name: "kali", Platform: "linux"}

	// Proceeding records the acknowledgment (startRDP fires async; the ack is synchronous).
	m.selectConnType("rdp")
	m.loading = false
	if !browser.RDPLinuxAcked("111111111111", "kali") {
		t.Fatal("proceeding did not record the acknowledgment")
	}

	// The row warning stands down for this box…
	m.buildConnTypeList()
	if rdp, _ := findItem(m, "rdp"); strings.Contains(rdp.desc, "Linux box") {
		t.Errorf("warning shown after ack: %q", rdp.desc)
	}
	// …but not for its neighbours.
	m.selInstance = &awsint.Instance{ID: "i-2", Name: "other-linux", Platform: "linux"}
	m.buildConnTypeList()
	if rdp, _ := findItem(m, "rdp"); !strings.Contains(rdp.desc, "Linux box") {
		t.Error("ack leaked to a different instance")
	}

	// The favorite replay no longer pauses on the acked box.
	m.pendingFavConn = &browser.Favorite{InstanceName: "kali", ConnType: "rdp"}
	_, cmd := m.Update(msgInstances{instances: []awsint.Instance{{ID: "i-1", Name: "kali", Platform: "linux"}}})
	if cmd == nil {
		t.Error("acked RDP favorite still paused")
	}
}

// The HIGH finding: any error must end every pending detour, or a stale pendingFavConn
// hijacks the NEXT credential flow into an automatic connection in whatever account the
// user moved on to.
func TestErrorClearsPendingDetours(t *testing.T) {
	m := modelWithSSOSession(t)
	m.pendingFavConn = &browser.Favorite{InstanceName: "kali", ConnType: "rdp"}
	m.pendingFavRole = "Admin"
	m.pendingProfile = "tp24"

	m.Update(msgError{errors.New("role was removed")})
	if m.pendingFavConn != nil || m.pendingFavRole != "" || m.pendingProfile != "" {
		t.Fatalf("pending state survived msgError: conn=%v role=%q profile=%q",
			m.pendingFavConn, m.pendingFavRole, m.pendingProfile)
	}
}

// A favorite skips the account and role screens, so it must also clear the lists a previous
// flow left behind — goBack from its action screen otherwise offered another account's
// roles under this account's title.
func TestFavoriteClearsStaleAccountAndRoleLists(t *testing.T) {
	m := modelWithSSOSession(t)
	m.accounts = []awsint.Account{{ID: "1", Name: "old"}}
	m.roles = []string{"XAdmin", "XReadOnly", "XDev"}
	if err := browser.AddFavorite(browser.Favorite{
		Nickname: "y-admin", StartURL: m.ssoSessions[0].StartURL,
		AccountID: "222222222222", AccountName: "Y", Role: "Admin",
	}); err != nil {
		t.Fatal(err)
	}
	m.buildMethodList()
	m.selectMethod("fav:0")
	if len(m.roles) != 0 || len(m.accounts) != 0 {
		t.Fatalf("stale lists survived the favorite: roles=%v accounts=%v", m.roles, m.accounts)
	}
	// And goBack from the action screen therefore goes home, not into a stale list.
	m.awsSess = &awsint.Session{Label: "x", AccessKeyID: "AKIA", RoleName: "Admin"}
	m.screen = screenAction
	m.goBack()
	if m.screen != screenMethod {
		t.Fatalf("goBack = %v, want screenMethod", m.screen)
	}
}

// The RDP presentation row flips in place: Enter toggles the stored setting, the row's title
// reflects the new value immediately, and the cursor stays on it.
func TestRDPScreenRowTogglesInPlace(t *testing.T) {
	m := modelWithSSOSession(t)
	m.buildMethodList()
	row, ok := findItem(m, methodRDPScreen)
	if !ok || !strings.Contains(row.title, "a window") {
		t.Fatalf("row = %+v, want the windowed default", row)
	}
	for i, li := range m.list.Items() {
		if it, ok := li.(item); ok && it.value == methodRDPScreen {
			m.list.Select(i)
		}
	}
	idx := m.list.Index()

	m.selectMethod(methodRDPScreen)
	if got := browser.LoadRDPScreen(); got != browser.RDPFullscreen {
		t.Fatalf("stored = %q after one toggle", got)
	}
	if row, _ = findItem(m, methodRDPScreen); !strings.Contains(row.title, "full screen") {
		t.Errorf("row did not redraw: %q", row.title)
	}
	if m.list.Index() != idx || m.screen != screenMethod {
		t.Errorf("cursor/screen moved: idx %d→%d, screen %v", idx, m.list.Index(), m.screen)
	}
	if !strings.Contains(m.notice, "full screen") {
		t.Errorf("notice = %q", m.notice)
	}

	m.selectMethod(methodRDPScreen)
	if got := browser.LoadRDPScreen(); got != browser.RDPWindowed {
		t.Errorf("stored = %q after two toggles", got)
	}
}
