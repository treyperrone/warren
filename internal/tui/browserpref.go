package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
)

// loginAskGrace is how long after the inline picker appears that Enter is ignored. Long
// enough to swallow a key-repeat burst or a double-tap (~150ms apart), short enough that a
// human reading the new screen never notices it.
const loginAskGrace = 300 * time.Millisecond

// methodBrowserPref is the sentinel value of the "browser for SSO sign-in" row on the method
// screen. Like methodAddSession, it can never collide with a real method: sessions are matched
// by bare name and profiles carry a "profile:" prefix.
const methodBrowserPref = "= browser preference"

// Values for the non-browser rows on the browser screens. Browser rows carry a "b:" prefix
// plus the browser's name, so a browser literally named "system" cannot shadow a row.
const (
	browserValSystem = "system"
	browserValNone   = "none"
	browserValAsk    = "ask"
)

// Values for the remember screen shown after an inline (at-sign-in) pick.
const (
	rememberOnce    = "once"
	rememberSession = "session"
	rememberAlways  = "always"
)

// clearPrefix marks a settings row that deletes one session's override.
const clearPrefix = "clear:"

// browserPrefRow is the method-screen entry point. It lives on that screen rather than behind
// a config file because the moment someone wants it is the moment sign-in just opened in the
// wrong browser — which lands them back on exactly this screen to re-select the session.
func browserPrefRow() item {
	// "TUI sign-ins": since the CLI went device-code-only by default, this setting drives
	// the TUI and `warren login --browser`, and saying less than that would promise more.
	desc := "TUI sign-ins open in: " + browser.LoadPref().Describe()
	switch n := len(browser.SessionPrefs()); n {
	case 0:
	case 1:
		desc += " (1 session override)"
	default:
		desc += fmt.Sprintf(" (%d session overrides)", n)
	}
	return item{
		title: "⚙ Browser for SSO sign-in",
		desc:  desc,
		value: methodBrowserPref,
	}
}

// sessionLabelFor names an override by its [sso-session] block where one still points at the
// stored start URL, and by the URL itself otherwise — an override can outlive its block, and
// a row that cannot be identified cannot be confidently deleted.
func (m *Model) sessionLabelFor(startURL string) string {
	for _, s := range m.ssoSessions {
		if s.StartURL == startURL {
			return s.Name
		}
	}
	return startURL
}

func (m *Model) startBrowserPref() tea.Cmd {
	// Detected fresh on every visit, not cached at startup: installing a browser while
	// warren is open should not require restarting it to see the new option.
	m.browsers = browser.Detect()
	m.buildBrowserList()
	m.screen = screenBrowser
	return nil
}

// browserChoiceRows is the shared body of both browser screens: the settings one and the
// inline at-sign-in one. Detected browsers first — on the ask screen the top row is about
// to be Enter-mashed, and "the browser I actually use" is the likelier answer there than
// "system default".
func (m *Model) browserChoiceRows() []list.Item {
	var items []list.Item
	for _, b := range m.browsers {
		items = append(items, item{
			title: b.Name,
			desc:  browserRowDesc(b),
			value: "b:" + b.Name,
		})
	}
	return append(items,
		item{
			title: "System default browser",
			desc:  "whatever this OS opens links with",
			value: browserValSystem,
		},
		item{
			title: "No browser — device code only",
			desc:  "always show the URL and code to open anywhere; right for SSH and headless boxes",
			value: browserValNone,
		})
}

func (m *Model) buildBrowserList() {
	items := m.browserChoiceRows()
	// Settings-only: make "ask every time" a saveable answer, so the inline picker is a
	// mode you can opt back into after saving a default, not just the unset state.
	items = append(items, item{
		title: "Ask at each sign-in",
		desc:  "show this picker whenever a sign-in is needed",
		value: browserValAsk,
	})
	// Per-session overrides, each clearable in place. Listed by session name where the
	// start URL still matches a configured block, because the URL is the stored key but the
	// name is what a person recognises. Sorted: map iteration would shuffle the rows on
	// every visit, and a settings list that reorders itself reads as broken.
	overrides := browser.SessionPrefs()
	urls := make([]string, 0, len(overrides))
	for url := range overrides {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	for _, url := range urls {
		items = append(items, item{
			title: "✕ " + m.sessionLabelFor(url) + " override: " + overrides[url].Describe(),
			desc:  "this session ignores the default; enter removes the override",
			value: clearPrefix + url,
		})
	}

	m.list.Title = "Where should SSO sign-in open?"
	m.list.SetStatusBarItemName("option", "options")
	m.setListItems(items)
	// The entry row is by construction the LAST row of the method list, so the shared list's
	// cursor arrives here at an index this shorter list may not have — bubbles clamps the
	// page on SetItems but not the cursor, leaving no row selected and Enter dead. Same
	// pattern as the action and builder screens: entering a screen selects its first row.
	m.list.Select(0)
}

// browserRowDesc says what selecting the row will lead to — a profile choice or not — so the
// two-step flow is not a surprise on the second screen.
func browserRowDesc(b browser.Browser) string {
	if b.Kind == browser.KindSafari {
		return "no profile selection — Safari decides"
	}
	switch n := len(b.Profiles()); n {
	case 0:
		return "no profiles found"
	case 1:
		return "1 profile"
	default:
		return fmt.Sprintf("%d profiles", n)
	}
}

func (m *Model) selectBrowser(val string) tea.Cmd {
	switch val {
	case browserValSystem:
		return m.savePref(browser.Pref{Mode: browser.ModeSystem})
	case browserValNone:
		return m.savePref(browser.Pref{Mode: browser.ModeNone})
	case browserValAsk:
		return m.savePref(browser.Pref{Mode: browser.ModeAsk})
	}
	if url, ok := strings.CutPrefix(val, clearPrefix); ok {
		if err := browser.DeletePrefFor(url); err != nil {
			m.err = err
			return nil
		}
		m.notice = m.sessionLabelFor(url) + " now follows the default sign-in browser"
		m.buildBrowserList() // the cleared row disappears in place
		return nil
	}

	b, profs, multi := m.resolveBrowserRow(val)
	if b == nil {
		return nil
	}
	if !multi {
		return m.savePref(prefFor(*b, profs))
	}
	m.buildBrowserProfileList()
	m.screen = screenBrowserProfile
	return nil
}

// resolveBrowserRow maps a "b:<name>" row back to its browser and decides whether a profile
// screen is needed, setting up selBrowser/browserProfiles when it is. Shared by the settings
// flow and the at-sign-in flow, which differ only in what they do with the finished choice.
func (m *Model) resolveBrowserRow(val string) (b *browser.Browser, profs []browser.Profile, multi bool) {
	name := strings.TrimPrefix(val, "b:")
	for i := range m.browsers {
		if m.browsers[i].Name != name {
			continue
		}
		profs = m.browsers[i].Profiles()
		if len(profs) > 1 {
			m.selBrowser = &m.browsers[i]
			m.browserProfiles = profs
			return &m.browsers[i], profs, true
		}
		return &m.browsers[i], profs, false
	}
	return nil, nil, false
}

// prefFor is the finished choice for a browser with zero or one profiles. One profile is no
// choice; showing a one-row picker would be a screen that only ever has one answer.
func prefFor(b browser.Browser, profs []browser.Profile) browser.Pref {
	p := browser.Pref{Mode: browser.ModeBrowser, Browser: b.Name}
	if len(profs) == 1 {
		p.ProfileDir = profs[0].Dir
		p.ProfileName = profs[0].Name
	}
	return p
}

func (m *Model) buildBrowserProfileList() {
	var items []list.Item
	for _, p := range m.browserProfiles {
		desc := p.Dir
		if m.selBrowser.Kind == browser.KindFirefox {
			// Firefox profiles are addressed by name, so the "directory" is the name again
			// and repeating it reads like a rendering bug. Keyed on the Kind, not on
			// Dir == Name: a Chromium profile with no recorded display name falls back to
			// its directory name too, and must not be labelled a Firefox profile for it.
			desc = "Firefox profile"
		}
		items = append(items, item{title: p.Name, desc: desc, value: p.Dir})
	}
	m.list.Title = "Select a " + m.selBrowser.Name + " profile"
	m.list.SetStatusBarItemName("profile", "profiles")
	m.setListItems(items)
	// See buildBrowserList: the cursor from the previous, longer list must not arrive out of
	// range here.
	m.list.Select(0)
}

func (m *Model) selectBrowserProfile(val string) tea.Cmd {
	for _, p := range m.browserProfiles {
		if p.Dir == val {
			return m.savePref(browser.Pref{
				Mode: browser.ModeBrowser, Browser: m.selBrowser.Name,
				ProfileDir: p.Dir, ProfileName: p.Name,
			})
		}
	}
	return nil
}

// savePref persists the choice and lands back on the method screen, whose settings row now
// shows the new value — that visible change is the confirmation, the notice just names it.
func (m *Model) savePref(p browser.Pref) tea.Cmd {
	if err := browser.SavePref(p); err != nil {
		m.err = err
		return nil
	}
	m.notice = "TUI sign-ins will open in: " + p.Describe() + " (warren login stays device-code unless --browser)"
	m.screen = screenMethod
	m.buildMethodList()
	return nil
}

// ---- the at-sign-in picker -----------------------------------------------------------------

// msgLoginAsk arrives when a sign-in is needed and the preference says to ask: the inline
// picker runs BEFORE the device authorization starts, so nobody chooses a browser while a
// ten-minute code quietly burns down.
type msgLoginAsk struct{}

func (m *Model) startLoginAsk() {
	m.loading = false
	m.browsers = browser.Detect()
	m.buildLoginBrowserList()
	m.screen = screenLoginBrowser
	m.loginAskShownAt = time.Now()
}

func (m *Model) buildLoginBrowserList() {
	// The same rows as the settings screen minus "Ask at each sign-in" — this IS the ask.
	m.list.Title = "Sign-in needed — open it where?"
	m.list.SetStatusBarItemName("option", "options")
	m.setListItems(m.browserChoiceRows())
	m.list.Select(0)
}

func (m *Model) selectLoginBrowser(val string) tea.Cmd {
	// A cold-cache SilentToken failure is local-only, so this screen can replace the method
	// list within a millisecond of the Enter that selected the session — faster than a
	// double-tap or the keyboard's own repeat. Without this, the second Enter lands here and
	// silently picks the top browser: the picker "never appeared" and the wrong browser
	// opens, which is exactly the bug class the loading guard in Update exists for. Enter
	// inside the grace window is dropped; the picker is still on screen, so the user just
	// presses it again — deliberately, this time.
	if time.Since(m.loginAskShownAt) < loginAskGrace {
		return nil
	}
	switch val {
	case browserValSystem:
		return m.askRemember(browser.Pref{Mode: browser.ModeSystem})
	case browserValNone:
		return m.askRemember(browser.Pref{Mode: browser.ModeNone})
	}
	b, profs, multi := m.resolveBrowserRow(val)
	if b == nil {
		return nil
	}
	if !multi {
		return m.askRemember(prefFor(*b, profs))
	}
	m.buildBrowserProfileList()
	m.screen = screenLoginProfile
	return nil
}

func (m *Model) selectLoginProfile(val string) tea.Cmd {
	for _, p := range m.browserProfiles {
		if p.Dir == val {
			return m.askRemember(browser.Pref{
				Mode: browser.ModeBrowser, Browser: m.selBrowser.Name,
				ProfileDir: p.Dir, ProfileName: p.Name,
			})
		}
	}
	return nil
}

// askRemember interposes one question between the pick and the sign-in. It exists because
// the three sensible policies — ask every time, remember for this identity, remember for
// everything — are each right for someone, and the moment of a concrete choice is the one
// time the question costs nothing to answer. The per-session row sits above the global one
// because it is the likelier intent for anyone running more than one SSO session: work
// signs in through the work profile, personal through something else, and a global answer
// would make whichever was chosen second fight the first.
func (m *Model) askRemember(choice browser.Pref) tea.Cmd {
	m.loginChoiceDraft = choice
	m.list.Title = "Use " + choice.Describe() + "…"
	m.list.SetStatusBarItemName("option", "options")
	m.setListItems([]list.Item{
		item{title: "Just this once", desc: "ask again at the next sign-in", value: rememberOnce},
		item{
			title: "Always for " + m.selSession.Name,
			desc:  "save for this SSO session — used by the TUI and `warren login --browser`",
			value: rememberSession,
		},
		item{
			title: "Always, for every session",
			desc:  "save globally (TUI and `warren login --browser`); change it later under ⚙",
			value: rememberAlways,
		},
	})
	m.list.Select(0)
	m.screen = screenLoginRemember
	return nil
}

func (m *Model) selectLoginRemember(val string) tea.Cmd {
	// A failed save must not block the sign-in the user is mid-way through: report it, keep
	// the choice for this run, and carry on.
	switch val {
	case rememberSession:
		if err := browser.SavePrefFor(m.selSession.StartURL, m.loginChoiceDraft); err != nil {
			m.notice = "could not save the default: " + err.Error()
		}
	case rememberAlways:
		if err := browser.SavePref(m.loginChoiceDraft); err != nil {
			m.notice = "could not save the default: " + err.Error()
		}
	}
	choice := m.loginChoiceDraft
	m.loginChoice = &choice
	m.loading = true
	return m.startLogin()
}

// startLogin begins the device authorization once the browser question is settled — the
// second half of what fetchToken does when no ask is needed.
func (m *Model) startLogin() tea.Cmd {
	sess := *m.selSession
	ctx := m.ctx
	return func() tea.Msg {
		pending, err := awsint.StartLogin(ctx, sess)
		if err != nil {
			return msgToken{err: err}
		}
		return msgLoginPending{pending: pending}
	}
}

// ---- the sign-in screen ------------------------------------------------------

// msgLoginPending arrives when SilentToken found nothing to renew and a device authorization
// has been started: the URL and code exist, and nobody has approved them yet.
type msgLoginPending struct {
	pending *awsint.PendingLogin
}

// waitForLogin blocks on the user approving the sign-in, then reports like any token fetch.
// ctx is the cancellable login context, not m.ctx — cancellation is what lets ctrl+c actually
// end the poll (see Model.loginCancel).
func (m *Model) waitForLogin(ctx context.Context, p *awsint.PendingLogin) tea.Cmd {
	return func() tea.Msg {
		token, err := p.Wait(ctx)
		return msgToken{token: token, err: err}
	}
}

// loginView is what shows while the device authorization waits for approval. The URL and
// code are the content — they used to be printed to stderr *behind* this very screen, so a
// user whose browser failed to open saw an eternal spinner and no way to finish signing in.
func (m *Model) loginView() string {
	var b strings.Builder
	b.WriteString(m.banner())
	b.WriteString("\n")
	b.WriteString(styleTitle.MarginLeft(2).Render("Sign in to AWS SSO"))
	b.WriteString("\n\n")
	b.WriteString("  Visit:  " + m.pendingLogin.VerificationURL + "\n")
	b.WriteString("  Code:   " + styleTitle.Render(m.pendingLogin.UserCode) + "\n\n")
	if m.loginNote != "" {
		b.WriteString(styleDim.MarginLeft(2).Render(m.loginNote) + "\n\n")
	}
	b.WriteString(fmt.Sprintf("  %s waiting for approval… (ctrl+c quits)\n", m.spin.View()))
	return b.String()
}
