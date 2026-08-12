package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/credserver"
)

const (
	// credRefreshLead is how long before expiry to renew. Role credentials last an hour, so
	// this renews roughly every fifty minutes — early enough that nothing in the TUI ever
	// meets an expired credential, late enough not to churn API calls.
	credRefreshLead = 10 * time.Minute

	// credCheckEvery is how often expiry is checked. Cheap: it compares a timestamp and
	// almost always does nothing. A laptop that suspends for hours wakes to a check within
	// this interval rather than a dead session.
	credCheckEvery = 30 * time.Second
)

type msgCredTick time.Time

type msgCredsRefreshed struct {
	sess  *awsint.Session
	token string
	err   error
}

// credTick schedules the next expiry check. It re-arms itself for as long as the program runs,
// which is what "keeps refreshing while the TUI is open" amounts to.
func credTick() tea.Cmd {
	return tea.Tick(credCheckEvery, func(t time.Time) tea.Msg { return msgCredTick(t) })
}

// needsCredRefresh reports whether the current credentials are close enough to expiry to renew.
//
// A zero expiry means the credentials do not expire — a profile backed by long-lived IAM keys —
// and must not be renewed on a timer. Credentials already being renewed are left alone so a slow
// call cannot stack up requests behind itself.
func (m *Model) needsCredRefresh(now time.Time) bool {
	if m.awsSess == nil || m.refreshingCreds || m.credsOnly {
		return false
	}
	if m.awsSess.Expires.IsZero() {
		return false
	}
	return m.awsSess.Expires.Sub(now) < credRefreshLead
}

// renewal carries everything a renewal needs, captured by value so it can run on a goroutine
// without reading Model fields the event loop may be changing — View reads the session on every
// frame to draw the banner.
type renewal struct {
	ssoCfg      *awsint.SSOSessionConfig
	profile     string
	accountID   string
	accountName string
	role        string
}

func (m *Model) renewal() renewal {
	r := renewal{ssoCfg: m.selSession}
	if m.awsSess != nil {
		r.profile = m.awsSess.ProfileName
		r.accountID = m.awsSess.AccountID
		r.accountName = m.awsSess.AccountName
		r.role = m.awsSess.RoleName
	}
	return r
}

// run renews the credentials, returning the SSO token alongside them when one was used.
//
// Shared by the event-loop tick and the credential endpoint's own goroutine, so there is one
// definition of what renewal means rather than two that can drift.
func (r renewal) run(ctx context.Context) (*awsint.Session, string, error) {
	// A profile resolves through the SDK's own chain, which covers static keys, SSO-backed and
	// assume-role profiles alike.
	if r.profile != "" {
		fresh, err := awsint.ProfileSession(ctx, r.profile)
		return fresh, "", err
	}

	if r.ssoCfg == nil || r.accountID == "" || r.role == "" {
		return nil, "", errors.New("cannot renew: no account and role recorded")
	}

	// SilentToken, never LiveToken: the SSO token may have expired too, and renewing it is fine,
	// but falling through to device auth from here would open a browser behind the alt screen and
	// block. ErrLoginRequired comes back instead and is reported.
	token, err := awsint.SilentToken(ctx, *r.ssoCfg)
	if err != nil {
		return nil, "", err
	}

	fresh, err := awsint.GetRoleCredentials(ctx, *r.ssoCfg, token, r.accountID, r.role)
	if err != nil {
		return nil, "", err
	}
	fresh.AccountID = r.accountID
	fresh.BuildLabel(r.accountName, r.role)
	return fresh, token, nil
}

func (m *Model) refreshCreds() tea.Cmd {
	r := m.renewal()
	ctx := m.ctx
	m.refreshingCreds = true

	return func() tea.Msg {
		fresh, token, err := r.run(ctx)
		return msgCredsRefreshed{sess: fresh, token: token, err: err}
	}
}

// applyCredRefresh records the outcome. Applied on the main goroutine, so nothing else is reading
// the session while it is replaced.
//
// A failure is deliberately not raised into the error view: this runs on a timer the user did not
// ask for, and hijacking the screen over it would interrupt whatever they were doing. The old
// credentials are kept — they may still have minutes left — and the reason is shown alongside the
// identity, where it is visible without being in the way.
func (m *Model) applyCredRefresh(msg msgCredsRefreshed) {
	m.refreshingCreds = false

	if msg.err != nil || msg.sess == nil {
		m.credRefreshErr = msg.err
		if msg.err == nil {
			m.credRefreshErr = errors.New("renewal returned no credentials")
		}
		return
	}

	m.credRefreshErr = nil
	m.awsSess = msg.sess
	if msg.token != "" {
		m.token = msg.token
	}
}

// credRefreshNote describes the renewal state for the header, or "" when there is nothing to say.
func (m *Model) credRefreshNote() string {
	switch {
	case m.refreshingCreds:
		return "renewing"
	case errors.Is(m.credRefreshErr, awsint.ErrLoginRequired):
		// The one case with a real instruction attached: no amount of waiting fixes it, and
		// re-selecting the session is what runs the browser flow.
		return "sign-in needed — re-select the SSO session"
	case m.credRefreshErr != nil:
		return "renewal failed"
	}
	return ""
}

// ---- credential endpoint ---------------------------------------------------

// credentialEnv starts the loopback credential endpoint if it is not already running, points it at
// the current session, and returns the variables a child needs to use it.
//
// This replaces handing the child a copy of the keys. A copy cannot be updated once the process has
// started, which is why a shell opened from warren used to stop working after an hour; the child
// asking an endpoint each time has no such limit.
func (m *Model) credentialEnv() ([]string, error) {
	if m.awsSess == nil {
		return nil, errors.New("no credentials selected")
	}
	if m.credSrv == nil {
		srv, err := credserver.Start()
		if err != nil {
			return nil, err
		}
		m.credSrv = srv
	}
	m.credSrv.Set(m.awsSess)
	return m.credSrv.Env(), nil
}

// beginCredRefresh keeps the endpoint's credentials current while a child process holds the
// terminal — the one window where the event loop is blocked and its own tick cannot fire, and
// therefore exactly when a long-lived shell would otherwise cross its expiry.
func (m *Model) beginCredRefresh() {
	if m.credSrv == nil || m.credRefreshStop != nil {
		return
	}
	// Captured on this goroutine; the renewal must not read Model fields concurrently.
	r := m.renewal()
	ctx, cancel := context.WithCancel(m.ctx)
	m.credRefreshStop = cancel

	go m.credSrv.KeepFresh(ctx, credCheckEvery, credRefreshLead,
		func(ctx context.Context) (*awsint.Session, error) {
			fresh, _, err := r.run(ctx)
			return fresh, err
		})
}

// endCredRefresh stops that renewal and adopts anything it produced, so the header reflects
// renewals that happened while the screen was someone else's.
func (m *Model) endCredRefresh() {
	if m.credRefreshStop != nil {
		m.credRefreshStop()
		m.credRefreshStop = nil
	}
	if m.credSrv != nil {
		if latest := m.credSrv.Session(); latest != nil {
			m.awsSess = latest
		}
	}
}

// CredentialEndpoint hands the endpoint to a caller outside the TUI — `warren exec` and
// `warren shell`, which run their command after the picker has quit. The returned stop function
// halts renewal and closes the listener.
func (m *Model) CredentialEndpoint() (env []string, stop func(), err error) {
	env, err = m.credentialEnv()
	if err != nil {
		return nil, func() {}, err
	}
	m.beginCredRefresh()
	return env, func() {
		m.endCredRefresh()
		if m.credSrv != nil {
			_ = m.credSrv.Close()
		}
	}, nil
}
