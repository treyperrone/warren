package tui

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/tunnel"
)

// liveHelperPID starts a throwaway process and returns its pid — alive enough for
// Manager.Active, and safe to Kill (it is not the test runner).
func liveHelperPID(t *testing.T) int {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 > NUL")
	} else {
		cmd = exec.Command("sleep", "30")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

func tunnelManagerModel(t *testing.T, kind tunnel.Kind) *Model {
	t.Helper()
	m := mainScreenModel(t)
	m.manager.Add(&tunnel.Tunnel{
		PID:          liveHelperPID(t),
		Kind:         kind,
		LocalPort:    13389,
		InstanceName: "win-01",
	})
	m.buildMainList()
	m.list.Select(0)
	return m
}

// Enter on an active tunnel used to kill it outright. Now it opens a menu, and the tunnel is
// left alone until an explicit choice is made.
func TestEnterOnTunnelOpensMenuWithoutKilling(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.screen != screenSessionActions {
		t.Fatalf("screen = %v, want screenSessionActions", m.screen)
	}
	if len(m.manager.Active()) != 1 {
		t.Error("the tunnel was killed by opening its menu")
	}
}

// Reconnect leads the menu for an RDP tunnel — that is the whole reason it exists.
func TestRDPTunnelMenuLeadsWithReconnect(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	items := m.list.Items()
	if len(items) == 0 {
		t.Fatal("menu has no rows")
	}
	if first := items[0].(item); first.value != "reconnect" {
		t.Errorf("first row = %q, want reconnect", first.value)
	}
	if last := items[len(items)-1].(item); last.value != "disconnect" {
		t.Errorf("last row = %q, want disconnect", last.value)
	}
}

// An SSH tunnel is a bare port forward warren does not launch a client for, so there is
// nothing to reconnect — the menu is just disconnect.
func TestSSHTunnelMenuHasNoReconnect(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindSSH)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	for _, it := range m.list.Items() {
		if it.(item).value == "reconnect" {
			t.Error("SSH tunnel menu offered reconnect")
		}
	}
}

// Reconnect re-opens the client and returns to the manager with the tunnel intact.
func TestReconnectKeepsTheTunnel(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // open menu, cursor on "reconnect"

	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick it

	if m.screen != screenMain {
		t.Errorf("screen = %v, want screenMain", m.screen)
	}
	if len(m.manager.Active()) != 1 {
		t.Error("reconnect dropped the tunnel")
	}
	if m.notice == "" {
		t.Error("no notice after reconnect — the user is told nothing happened")
	}
}

// Disconnect is the explicit, spelled-out way to end a tunnel.
func TestDisconnectEndsTheTunnel(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // menu, tunnel now staged

	m.selectSessionAction("disconnect")

	if m.screen != screenMain {
		t.Errorf("screen = %v, want screenMain", m.screen)
	}
	if len(m.manager.Active()) != 0 {
		t.Error("disconnect did not end the tunnel")
	}
}

// The manager was a connect-only dead end: a tunnel that outlived a re-auth had nowhere to
// be seen. The action screen now carries a way in when tunnels are live.
func TestActionScreenLinksToLiveTunnels(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)
	m.buildActionList()

	var row *item
	for _, it := range m.list.Items() {
		if i := it.(item); i.value == actionTunnels {
			row = &i
			break
		}
	}
	if row == nil {
		t.Fatal("no 'Active tunnels' row on the action screen while a tunnel is live")
	}
	if !strings.Contains(row.title, "(1)") {
		t.Errorf("row title %q does not show the count", row.title)
	}

	m.screen = screenAction
	m.selectAction(actionTunnels)
	if m.screen != screenMain {
		t.Errorf("screen = %v, want screenMain after picking Active tunnels", m.screen)
	}
}

// With no live tunnel the row is absent — it must not be dead weight on the common path.
func TestActionScreenHasNoTunnelRowWhenNoneActive(t *testing.T) {
	m := mainScreenModel(t)
	m.buildActionList()
	for _, it := range m.list.Items() {
		if it.(item).value == actionTunnels {
			t.Error("'Active tunnels' row shown with no tunnel active")
		}
	}
}

// After a re-auth, a tunnel that survived the timeout is the thing most worth landing on.
func TestReauthWithLiveTunnelLandsOnManager(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)
	m.selSession = &awsint.SSOSessionConfig{Name: "crlab", StartURL: "https://ex.awsapps.com/start", Region: "eu-west-2"}
	m.selAccount = &awsint.Account{ID: "111111111111", Name: "cr-lab"}
	m.selRole = "AdminRole"
	m.resume = resumeInstances

	m.Update(msgCredsReady{})

	if m.screen != screenMain {
		t.Errorf("screen = %v, want screenMain — the live tunnel should be in view", m.screen)
	}
}

// restoredTunnelModel is a manager with one RDP tunnel marked Restored — as if it survived
// from a previous run of warren — optionally carrying the identity Reconnect needs to rebuild
// it.
func restoredTunnelModel(t *testing.T, withIdentity bool) *Model {
	t.Helper()
	m := mainScreenModel(t)
	tun := &tunnel.Tunnel{
		PID:          liveHelperPID(t),
		Kind:         tunnel.KindRDP,
		LocalPort:    13389,
		InstanceID:   "i-0abc",
		InstanceName: "win-01",
		Restored:     true,
	}
	if withIdentity {
		tun.StartURL = "https://ex.awsapps.com/start"
		tun.AccountID = "111111111111"
		tun.AccountName = "cr-lab"
		tun.RoleName = "AdminRole"
	}
	m.manager.Add(tun)
	m.buildMainList()
	m.list.Select(0)
	return m
}

// The menu says up front that Reconnect on a restored tunnel is not just re-opening a
// window — it says so because it is about to re-authenticate.
func TestRestoredTunnelMenuWarnsItWillReauthenticate(t *testing.T) {
	m := restoredTunnelModel(t, true)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var reconnect item
	for _, it := range m.list.Items() {
		if i := it.(item); i.value == "reconnect" {
			reconnect = i
		}
	}
	if !strings.Contains(reconnect.desc, "re-authenticate") {
		t.Errorf("reconnect row %q does not warn about re-authenticating", reconnect.desc)
	}
}

// Without a session to reassume — an older state file, or a profile-flow connection —
// Reconnect says so plainly instead of trying, and leaves the tunnel for a manual Disconnect.
func TestReconnectOnRestoredTunnelWithoutIdentityExplainsItCannot(t *testing.T) {
	m := restoredTunnelModel(t, false)
	m.sessionActionTunnel = m.manager.Active()[0]

	cmd := m.selectSessionAction("reconnect")

	if cmd != nil {
		t.Error("a command was returned with nothing to reconnect with")
	}
	if !strings.Contains(m.notice, "Disconnect") {
		t.Errorf("notice %q does not point at Disconnect", m.notice)
	}
	if len(m.manager.Active()) != 1 {
		t.Error("the unreconnectable tunnel was removed instead of left for a manual disconnect")
	}
}

// With identity but no matching [sso-session] left in ~/.aws/config, Reconnect reports why
// rather than silently doing nothing.
func TestReconnectOnRestoredTunnelWithUnknownSessionErrors(t *testing.T) {
	m := restoredTunnelModel(t, true)
	m.sessionActionTunnel = m.manager.Active()[0]
	m.ssoSessions = nil // the session named in the tunnel is not configured here

	m.selectSessionAction("reconnect")

	if m.err == nil {
		t.Fatal("m.err = nil, want an explanation that the session is gone")
	}
	if len(m.manager.Active()) != 1 {
		t.Error("the tunnel was removed even though nothing could replace it")
	}
}

// The full path: a matching session is on hand, so Reconnect stages the tunnel's own
// identity — not whatever the model last authenticated as — and starts the sign-in that
// rebuilds it.
func TestReconnectOnRestoredTunnelRebuildsWithItsOwnIdentity(t *testing.T) {
	m := restoredTunnelModel(t, true)
	tun := m.manager.Active()[0]
	m.sessionActionTunnel = tun
	m.ssoSessions = []awsint.SSOSessionConfig{
		{Name: "crlab", StartURL: tun.StartURL, Region: "eu-west-2"},
	}
	// A different identity currently "active", to prove reconnect uses the TUNNEL's, not this.
	m.selSession = &awsint.SSOSessionConfig{Name: "other", StartURL: "https://other.awsapps.com/start"}
	m.selAccount = &awsint.Account{ID: "222222222222", Name: "other-account"}
	m.selRole = "OtherRole"

	cmd := m.selectSessionAction("reconnect")

	if cmd == nil {
		t.Fatal("no command returned — nothing is fetching a token")
	}
	if m.selSession == nil || m.selSession.StartURL != tun.StartURL {
		t.Errorf("selSession = %+v, want the tunnel's own session", m.selSession)
	}
	if m.selAccount == nil || m.selAccount.ID != tun.AccountID {
		t.Errorf("selAccount = %+v, want account %s", m.selAccount, tun.AccountID)
	}
	if m.selRole != tun.RoleName {
		t.Errorf("selRole = %q, want %q", m.selRole, tun.RoleName)
	}
	if m.selInstance == nil || m.selInstance.ID != tun.InstanceID {
		t.Errorf("selInstance = %+v, want instance %s", m.selInstance, tun.InstanceID)
	}
	if m.connType != tunnel.KindRDP {
		t.Errorf("connType = %v, want KindRDP", m.connType)
	}
	if m.resume != resumeConnect {
		t.Errorf("resume = %v, want resumeConnect", m.resume)
	}
	if !m.loading {
		t.Error("loading = false while the token is being fetched")
	}
	if len(m.manager.Active()) != 0 {
		t.Error("the stale tunnel is still listed — it should be superseded, not doubled up")
	}
}

// Esc backs out of the menu to the manager, changing nothing.
func TestEscLeavesTheTunnelMenu(t *testing.T) {
	m := tunnelManagerModel(t, tunnel.KindRDP)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if isQuit(t, cmd) {
		t.Fatal("esc quit the program from the tunnel menu")
	}
	if m.screen != screenMain {
		t.Errorf("screen = %v, want screenMain", m.screen)
	}
	if len(m.manager.Active()) != 1 {
		t.Error("backing out of the menu changed the tunnel")
	}
}
