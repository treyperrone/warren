package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
)

// loginInvocation is `warren login` as parsed: which session, whether to only report, and
// whether to skip every browser and run a pure device-code sign-in.
type loginInvocation struct {
	session string
	status  bool
	code    bool
	browser bool
}

// parseLoginArgs accepts `warren login [--status] [session]` with the flag on either side of
// the name. Anything else is an error now rather than a surprise later — a mistyped flag
// must not be read as a session name and produce an AWS error several seconds on.
func parseLoginArgs(args []string) (loginInvocation, error) {
	var inv loginInvocation
	for _, a := range args {
		switch {
		case a == "--status" || a == "-s":
			inv.status = true
		case a == "--code" || a == "--no-browser":
			inv.code = true
		case a == "--browser" || a == "-b":
			inv.browser = true
		case len(a) > 0 && a[0] == '-':
			return inv, fmt.Errorf("unknown flag %q — login takes --status, --code, --browser and an optional session name", a)
		case inv.session != "":
			return inv, fmt.Errorf("login takes one session name, got %q and %q", inv.session, a)
		default:
			inv.session = a
		}
	}
	if inv.code && inv.browser {
		return inv, errors.New("--code and --browser contradict each other — pick one")
	}
	return inv, nil
}

// loginTarget is one identity `warren login` can act on: an sso-session (device auth), an
// SSO-backed or legacy profile (device auth against its underlying session), or a static
// profile (nothing to sign in to — its credentials are validated instead).
type loginTarget struct {
	name string
	// sess is what device auth runs against; nil for a static profile, and nil for the one
	// broken shape — a profile naming an sso-session that does not exist — which brokenRef
	// records so the error can say what to fix.
	sess      *awsint.SSOSessionConfig
	isProfile bool
	brokenRef string // the missing sso-session name, when the profile points at nothing
	// covers counts the other identities collapsed into this row — the profiles that
	// authenticate through the same start URL. One sign-in serves them all, so menus and
	// status listings show one row carrying the count instead of a hundred rows carrying
	// one fact. Set by collapseTargets; zero on the full list.
	covers int
}

// static reports whether this target has nothing device auth could do — validation is the
// only meaningful action.
func (t loginTarget) static() bool { return t.sess == nil && t.brokenRef == "" && t.isProfile }

// describe is the menu annotation: what the row is and what picking it will do.
func (t loginTarget) describe() string {
	base := t.describeKind()
	if t.covers > 0 {
		base += fmt.Sprintf(" — covers %d profiles", t.covers)
	}
	return base
}

func (t loginTarget) describeKind() string {
	switch {
	case !t.isProfile:
		return "sso-session — " + t.sess.StartURL
	case t.brokenRef != "":
		return "profile — BROKEN: names sso-session " + t.brokenRef + ", which does not exist"
	case t.static():
		return "profile — static keys or assume-role; no sign-in, credentials are validated"
	case t.sess.Name == t.name:
		return "profile — legacy SSO (" + t.sess.StartURL + ")"
	default:
		return "profile — signs in via sso-session " + t.sess.Name
	}
}

// loginTargets is everything the TUI's method screen lists, in the same order: sessions
// first, then named profiles. That parity is the point — a menu that hides what the picker
// shows reads as a different tool.
func loginTargets(sessions []awsint.SSOSessionConfig, profiles []awsint.ProfileConfig) []loginTarget {
	var targets []loginTarget
	for i := range sessions {
		targets = append(targets, loginTarget{name: sessions[i].Name, sess: &sessions[i]})
	}
	for _, p := range profiles {
		t := loginTarget{name: p.Name, isProfile: true}
		if sess, ssoBacked := p.LoginSession(sessions); ssoBacked {
			if sess == nil {
				t.brokenRef = p.SSOSession
			}
			t.sess = sess
		}
		targets = append(targets, t)
	}
	return targets
}

// resolveLoginTarget picks the identity to act on. One configured target needs no name;
// several do, because signing in is an identity decision no tool should guess at. Sessions
// shadow a like-named profile — the session is the thing that signs in. Every failure lists
// what IS available: the caller is likely a headless box or a script, where "run it again
// to see the list" is a round trip that was avoidable.
func resolveLoginTarget(targets []loginTarget, name string) (loginTarget, error) {
	if len(targets) == 0 {
		return loginTarget{}, errors.New("no sso-session or profile configured — run `warren setup` first")
	}
	if name == "" {
		if len(targets) == 1 {
			return targets[0], nil
		}
		return loginTarget{}, fmt.Errorf("several identities are configured — name one: %s", targetNames(targets))
	}
	for _, t := range targets {
		if t.name == name {
			return t, nil
		}
	}
	return loginTarget{}, fmt.Errorf("nothing named %q — configured: %s", name, targetNames(targets))
}

// collapseTargets folds every identity that authenticates through the same start URL into
// one row. A hundred crlab-* profiles backed by one sso-session are ONE fact ("sign in to
// crlab") and one action; a menu or status listing that says it a hundred times buries the
// statics and broken rows that actually differ. A real session row wins representative
// duty over a profile; a legacy URL shared only by profiles is represented by the first.
// Name-based resolution stays on the FULL list — `warren login crlab-7` keeps working.
func collapseTargets(targets []loginTarget) []loginTarget {
	byURL := map[string]int{}
	var out []loginTarget
	for _, t := range targets {
		if t.sess == nil {
			// Static and broken rows never collapse: each is its own fact.
			out = append(out, t)
			continue
		}
		i, ok := byURL[t.sess.StartURL]
		if !ok {
			byURL[t.sess.StartURL] = len(out)
			out = append(out, t)
			continue
		}
		if !t.isProfile && out[i].isProfile {
			// The session arrives after a profile already claimed its URL: the session is
			// the better name for the group, and the displaced profile joins the count.
			t.covers = out[i].covers + 1
			out[i] = t
			continue
		}
		out[i].covers++
	}
	return out
}

func targetNames(targets []loginTarget) string {
	out := ""
	for i, t := range targets {
		if i > 0 {
			out += ", "
		}
		out += t.name
	}
	return out
}

// runLogin is `warren login`: sign in to one sso-session from a plain terminal — no TUI, so
// it works from scripts and from inside sessions that cannot host an alt screen. --status
// reports token liveness without ever starting a device authorization, which is what a
// script wants to know before it decides to bother a human.
func runLogin(ctx context.Context, args []string) int {
	inv, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	sessions, profiles, err := awsint.ParseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	targets := loginTargets(sessions, profiles)
	// Everything that presents identities as a set works on the collapsed view; only
	// resolution by explicit name uses the full list.
	collapsed := collapseTargets(targets)

	// `warren login --status` with several identities and no name reports all of them: that
	// is the "which of my worlds needs attention" view, and a script that wants exactly one
	// answer names the identity. Collapsed, this is also one liveness check per start URL
	// instead of one per profile — on a box with a hundred profiles that is the difference
	// between a listing and a hang.
	if inv.status && inv.session == "" && len(collapsed) > 1 {
		exit := 0
		for _, t := range collapsed {
			if code := reportTargetStatus(ctx, t); code != 0 {
				exit = 1
			}
		}
		return exit
	}

	// Bare `warren login` with several identities: on a terminal that is a picker, not an
	// error — seeing what is configured was the request. Scripts (non-TTY) keep the hard
	// exit 2, because a menu nobody can answer is a hang.
	var target loginTarget
	if inv.session == "" && len(collapsed) > 1 && !inv.status && stdinIsTTY() {
		target, err = promptTargetChoice(bufio.NewReader(os.Stdin), os.Stderr, collapsed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
	} else {
		// No name given: resolve against the collapsed view, so one session with a hundred
		// profiles through it counts as the one identity it is. An explicit name resolves
		// against the full list — `warren login crlab-7` names a real thing.
		pool := targets
		if inv.session == "" {
			pool = collapsed
		}
		target, err = resolveLoginTarget(pool, inv.session)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		// Say which identity is about to be used when the user never named one — a bare
		// `warren login` that silently proceeds into the only identity reads as warren
		// deciding for them, even when there was nothing to decide.
		if inv.session == "" && !inv.status {
			fmt.Fprintf(os.Stderr, "[sso] %s (%s) — the only one configured\n", target.name, target.describe())
		}
	}

	if target.brokenRef != "" {
		fmt.Fprintf(os.Stderr, "profile %s names sso-session %q, which is not in ~/.aws/config — fix the profile before signing in\n",
			target.name, target.brokenRef)
		return 1
	}

	if inv.status {
		return reportTargetStatus(ctx, target)
	}

	// A static profile has nothing to device-auth: validating that its credentials resolve
	// is the whole service, and the honest output for "log in to my keys profile".
	if target.static() {
		return checkProfile(ctx, target.name)
	}

	sess := target.sess
	if target.isProfile {
		// Signing in on behalf of a profile: name the detour, or the session's URL and
		// code appear to belong to the wrong identity.
		fmt.Fprintf(os.Stderr, "[sso] %s signs in via %s\n", target.name, sess.Name)
	}

	// Silent first, so `warren login` in a loop or a shell profile is free when the token
	// is live; the device flow only runs when it is genuinely the last resort.
	if _, err := awsint.SilentToken(ctx, *sess); err == nil {
		fmt.Fprintf(os.Stderr, "%s: already signed in — token cached and shared with the aws CLI\n", sess.Name)
		return 0
	} else if !errors.Is(err, awsint.ErrLoginRequired) {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// CLI default is a pure device-code sign-in: print the URL and code, hand the URL to
	// the local clipboard, get out of the way — no browser launch, no questions. Opening a
	// browser is opt-in here, either by an explicitly saved preference (that answer was
	// given once, on purpose) or by --browser for this run. The pickery experience lives in
	// the TUI; ask-mode therefore means "device code" on the CLI unless --browser asks for
	// the teletype picker.
	pref, scoped := browser.ResolvePrefFor(sess.StartURL)
	asked := false
	deviceCodeDefault := false
	skippedOverride := ""
	switch loginOpenPlan(inv, pref, stdinIsTTY()) {
	case planCode:
		// Say when a saved answer exists and is deliberately not used, or the difference
		// between CLI and TUI behaviour reads as the override being broken.
		if !inv.code && scoped && (pref.Mode == browser.ModeSystem || pref.Mode == browser.ModeBrowser) {
			skippedOverride = pref.Describe()
		}
		pref, scoped = browser.Pref{Mode: browser.ModeNone}, false
		deviceCodeDefault = !inv.code // the hint below is for the default, not the flag
	case planPicker:
		chosen, remember, err := promptBrowserChoice(bufio.NewReader(os.Stdin), os.Stderr, sess.Name, browser.Detect())
		if err != nil {
			// EOF or unreadable stdin mid-prompt: fall back rather than fail — the URL and
			// code below are still everything a sign-in truly needs.
			fmt.Fprintf(os.Stderr, "[sso] picker aborted (%v); using the default browser\n", err)
			pref, scoped = browser.Pref{Mode: browser.ModeSystem}, false
		} else {
			switch remember {
			case rememberForSession:
				if err := browser.SavePrefFor(sess.StartURL, chosen); err != nil {
					fmt.Fprintf(os.Stderr, "[sso] could not save the default: %v\n", err)
				}
			case rememberGlobally:
				if err := browser.SavePref(chosen); err != nil {
					fmt.Fprintf(os.Stderr, "[sso] could not save the default: %v\n", err)
				}
			}
			pref = chosen
			asked = true
		}
	case planSystem:
		pref, scoped = browser.Pref{Mode: browser.ModeSystem}, false
	case planPref:
		// pref stands as resolved.
	}

	pending, err := awsint.StartLogin(ctx, *sess)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "\n[sso] Signing in to %s\n", sess.StartURL)
	if pending.RegistrationWarning != "" {
		fmt.Fprintf(os.Stderr, "[sso] %s\n", pending.RegistrationWarning)
	}
	fmt.Fprintf(os.Stderr, "[sso] If no browser opens, visit: %s\n", pending.VerificationURL)
	fmt.Fprintf(os.Stderr, "[sso] User code: %s\n", pending.UserCode)
	// Name WHY this browser, not just which: a saved override opening an unexpected browser
	// reads as a bug precisely when the reader has forgotten they saved it.
	switch {
	case deviceCodeDefault:
		// Not OpenForLogin's "browser opening is off" — nothing is off, this is the CLI's
		// normal mode, and the line teaches the escape hatch.
		if skippedOverride != "" {
			fmt.Fprintf(os.Stderr, "[sso] saved override for %s (%s) not used here — pass --browser to open it\n",
				sess.Name, skippedOverride)
		}
		fmt.Fprintf(os.Stderr, "[sso] device-code sign-in — pass --browser to open one automatically\n")
	case inv.code:
		fmt.Fprintf(os.Stderr, "[sso] device-code sign-in (--code)\n")
	default:
		if why := describeChoice(asked, scoped, sess.Name, pref); why != "" {
			fmt.Fprintf(os.Stderr, "[sso] %s\n", why)
		}
		fmt.Fprintf(os.Stderr, "[sso] %s\n", browser.OpenForLogin(pref, pending.VerificationURL))
	}

	// When no browser is opening HERE — by flag, by choice, or because this is a headless
	// or SSH session — hand the URL to the user's local clipboard through the terminal
	// (OSC 52). This is the remote-box flow: warren runs over SSH, the URL lands on the
	// laptop's clipboard, any local browser finishes the sign-in. "Sent", not "copied":
	// a terminal without OSC 52 ignores the sequence and nothing reports back.
	if pref.Mode == browser.ModeNone || browser.Headless(runtime.GOOS, os.Getenv) {
		if copyToLocalClipboard(pending.VerificationURL) {
			fmt.Fprintf(os.Stderr, "[sso] URL sent to your local clipboard (OSC 52; in tmux this needs allow-passthrough)\n")
		} else {
			fmt.Fprintf(os.Stderr, "[sso] copy the URL above into any browser\n")
		}
	}
	fmt.Fprintf(os.Stderr, "\n")

	if _, err := pending.Wait(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s: signed in — token cached and shared with the aws CLI\n", sess.Name)
	return 0
}

// promptTargetChoice is the numbered menu for "which identity" — shown instead of exit 2
// when a human is present to answer. Every row carries its describe() annotation because
// the rows are not interchangeable: some sign in, some validate, one kind is broken config.
func promptTargetChoice(in *bufio.Reader, out io.Writer, targets []loginTarget) (loginTarget, error) {
	fmt.Fprintf(out, "\nWhich identity?\n")
	for i, t := range targets {
		fmt.Fprintf(out, "  %d. %s — %s\n", i+1, t.name, t.describe())
	}
	n, err := promptNumber(in, out, len(targets))
	if err != nil {
		return loginTarget{}, fmt.Errorf("no identity chosen: %w", err)
	}
	return targets[n-1], nil
}

// openPlan is what the CLI does about a browser for one sign-in.
type openPlan int

const (
	planCode   openPlan = iota // device code only: print, OSC 52, no launch
	planPref                   // honor the resolved (saved) preference as-is
	planPicker                 // teletype picker, then honor the answer
	planSystem                 // open the system default browser
)

// loginOpenPlan is the whole CLI browser policy in one decidable place: the CLI NEVER opens
// a browser unless asked to. Default is device code — print, OSC 52, get out of the way —
// saved overrides included, because on the CLI even a saved answer opening windows unasked
// read as noise (the TUI is where overrides live). --browser opts in: the saved concrete
// answer when there is one, otherwise the picker on a TTY, the system default off one.
// --code forces device code and contradicts --browser at parse time.
func loginOpenPlan(inv loginInvocation, pref browser.Pref, tty bool) openPlan {
	switch {
	case inv.code || !inv.browser:
		return planCode
	case pref.Mode == browser.ModeSystem || pref.Mode == browser.ModeBrowser:
		return planPref
	case tty:
		return planPicker
	default:
		return planSystem
	}
}

// Remember answers from the teletype picker, mirroring the TUI's remember screen.
const (
	rememberJustOnce   = "once"
	rememberForSession = "session"
	rememberGlobally   = "always"
)

// describeChoice names the reason a browser is about to open, or "" when there is nothing
// more to say than what OpenForLogin's own note will.
func describeChoice(asked, scoped bool, sessName string, p browser.Pref) string {
	switch {
	case asked:
		return "using " + p.Describe()
	case scoped:
		return "using the saved override for " + sessName + ": " + p.Describe()
	case p.Mode != "" && p.Mode != browser.ModeAsk:
		return "using the saved default: " + p.Describe()
	}
	return ""
}

// stdinIsTTY reports whether a human can answer a prompt. Character-device is the portable
// test: pipes and redirections fail it on every platform, which is exactly the set of
// callers that must never block on a question.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// promptBrowserChoice is the TUI's ask flow as a numbered teletype menu: browser, then
// profile when there is a real choice, then how long to remember it. Reader and writer are
// parameters so tests can script the whole conversation.
func promptBrowserChoice(in *bufio.Reader, out io.Writer, sessName string, browsers []browser.Browser) (browser.Pref, string, error) {
	fmt.Fprintf(out, "\nSign-in needed for %s — where should it open?\n", sessName)
	for i, b := range browsers {
		fmt.Fprintf(out, "  %d. %s\n", i+1, b.Name)
	}
	fmt.Fprintf(out, "  %d. System default browser\n", len(browsers)+1)
	fmt.Fprintf(out, "  %d. No browser — show the device code only\n", len(browsers)+2)

	n, err := promptNumber(in, out, len(browsers)+2)
	if err != nil {
		return browser.Pref{}, "", err
	}

	var chosen browser.Pref
	switch {
	case n == len(browsers)+1:
		chosen = browser.Pref{Mode: browser.ModeSystem}
	case n == len(browsers)+2:
		chosen = browser.Pref{Mode: browser.ModeNone}
	default:
		b := browsers[n-1]
		chosen = browser.Pref{Mode: browser.ModeBrowser, Browser: b.Name}
		if profs := b.Profiles(); len(profs) == 1 {
			chosen.ProfileDir, chosen.ProfileName = profs[0].Dir, profs[0].Name
		} else if len(profs) > 1 {
			fmt.Fprintf(out, "\n%s profile:\n", b.Name)
			for i, p := range profs {
				fmt.Fprintf(out, "  %d. %s\n", i+1, p.Name)
			}
			pn, err := promptNumber(in, out, len(profs))
			if err != nil {
				return browser.Pref{}, "", err
			}
			chosen.ProfileDir, chosen.ProfileName = profs[pn-1].Dir, profs[pn-1].Name
		}
	}

	fmt.Fprintf(out, "\nRemember this?\n")
	fmt.Fprintf(out, "  1. Just this once\n")
	fmt.Fprintf(out, "  2. Always for %s\n", sessName)
	fmt.Fprintf(out, "  3. Always, for every session\n")
	rn, err := promptNumber(in, out, 3)
	if err != nil {
		return browser.Pref{}, "", err
	}
	remember := [...]string{rememberJustOnce, rememberForSession, rememberGlobally}[rn-1]
	return chosen, remember, nil
}

// promptNumber reads a 1-based menu answer, re-asking on anything out of range. Empty input
// means 1 — the first row of every menu here is the safest answer, and Enter-through should
// do the unsurprising thing.
func promptNumber(in *bufio.Reader, out io.Writer, max int) (int, error) {
	for {
		fmt.Fprintf(out, "Choice [1]: ")
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return 1, nil
		}
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= max {
			return n, nil
		}
		fmt.Fprintf(out, "enter a number between 1 and %d\n", max)
	}
}

// reportTargetStatus prints one identity's state on stdout. Exit semantics match what a
// script wants to branch on: 0 usable now, 1 a human (or a config fix) is needed.
func reportTargetStatus(ctx context.Context, t loginTarget) int {
	switch {
	case t.brokenRef != "":
		fmt.Printf("%s: broken — names sso-session %q, which does not exist\n", t.name, t.brokenRef)
		return 1
	case t.static():
		// Resolving through the SDK chain is the real test for a keys/assume-role profile:
		// there is no token to inspect, only credentials that work or do not.
		if _, err := awsint.ProfileSession(ctx, t.name); err != nil {
			fmt.Printf("%s: credentials failed to resolve: %v\n", t.name, err)
			return 1
		}
		fmt.Printf("%s: credentials resolve (no sign-in involved)\n", t.name)
		return 0
	}

	covers := ""
	if t.covers > 0 {
		covers = fmt.Sprintf(" — covers %d profiles", t.covers)
	}
	_, err := awsint.SilentToken(ctx, *t.sess)
	switch {
	case err == nil:
		fmt.Printf("%s: signed in%s\n", t.name, covers)
		return 0
	case errors.Is(err, awsint.ErrLoginRequired):
		fmt.Printf("%s: sign-in required%s (run: warren login %s)\n", t.name, covers, t.name)
		return 1
	default:
		fmt.Printf("%s: error: %v\n", t.name, err)
		return 1
	}
}

// checkProfile is `warren login <static profile>`: resolve its credentials and say so. The
// exit code is the answer; the wording says why no browser is coming.
func checkProfile(ctx context.Context, name string) int {
	sess, err := awsint.ProfileSession(ctx, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: credentials failed to resolve: %v\n", name, err)
		return 1
	}
	line := name + ": credentials resolve — nothing to sign in to for a keys/assume-role profile"
	if left := sess.ExpiresIn(time.Now()); left != "" {
		line += " (expire in " + left + ")"
	}
	fmt.Fprintf(os.Stderr, "%s\n", line)
	return 0
}
