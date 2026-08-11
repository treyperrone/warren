package tui

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/postern/internal/aws"
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

// refreshCreds renews the credentials for the account and role already in use.
//
// Everything it needs is read here, on the main goroutine, and passed into the closure by value.
// Reading m.awsSess from inside the command instead would race with View, which reads it on every
// frame to draw the banner.
func (m *Model) refreshCreds() tea.Cmd {
	sess := m.awsSess
	var (
		ctx         = m.ctx
		profile     = sess.ProfileName
		accountID   = sess.AccountID
		accountName = sess.AccountName
		role        = sess.RoleName
		ssoCfg      = m.selSession
	)

	m.refreshingCreds = true

	return func() tea.Msg {
		// A profile resolves through the SDK's own chain, which covers static keys, SSO-backed
		// and assume-role profiles alike.
		if profile != "" {
			fresh, err := awsint.ProfileSession(ctx, profile)
			return msgCredsRefreshed{sess: fresh, err: err}
		}

		if ssoCfg == nil || accountID == "" || role == "" {
			return msgCredsRefreshed{err: errors.New("cannot renew: no account and role recorded")}
		}

		// SilentToken, never LiveToken: the SSO token may have expired too, and renewing it is
		// fine, but falling through to device auth from here would open a browser behind the
		// alt screen and block. ErrLoginRequired comes back instead and is reported.
		token, err := awsint.SilentToken(ctx, *ssoCfg)
		if err != nil {
			return msgCredsRefreshed{err: err}
		}

		fresh, err := awsint.GetRoleCredentials(ctx, *ssoCfg, token, accountID, role)
		if err != nil {
			return msgCredsRefreshed{err: err}
		}
		fresh.AccountID = accountID
		fresh.BuildLabel(accountName, role)
		return msgCredsRefreshed{sess: fresh, token: token}
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
