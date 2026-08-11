package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyperrone/postern/internal/awsexec"
)

// Action values on the screen shown once credentials exist.
const (
	actionInstances = "instances"
	actionCLI       = "cli"
	actionBuild     = "build"
)

// msgCredsShellDone reports that the credentialed shell exited.
type msgCredsShellDone struct{}

// buildActionList asks what to do with the role just assumed.
//
// This screen exists because connecting to a host and calling the API diverge here: an API
// call needs no instance, so the old flow — straight from role to the instance list — had no
// room for it. Without this screen `exec` and `shell` are real but invisible, reachable only
// by someone who already read the help.
func (m *Model) buildActionList() {
	items := []list.Item{
		// First, and therefore the default: connecting to a host is still what the tool is
		// mostly for, so the established flow costs one extra keystroke and no thought.
		item{
			title: "Browse EC2 instances",
			desc:  "connect to a host over SSM — shell, SSH, or RDP",
			value: actionInstances,
		},
		item{
			title: "Run AWS CLI commands",
			desc:  "open a shell with these credentials; run as many as you like, exit to return",
			value: actionCLI,
		},
		item{
			title: "Build an AWS CLI command",
			desc:  "pick a service and a task; edit the command before it runs",
			value: actionBuild,
		},
	}

	m.list.Title = "What next?  •  " + m.credSummary() + "  •  Esc=back"
	m.list.SetStatusBarItemName("action", "actions")
	m.setListItems(items)
	m.list.Select(0)
}

// credSummary names the identity and how long it lasts. The remaining time is the part worth
// showing: it is the difference between starting a task and starting one that will fail
// halfway through.
func (m *Model) credSummary() string {
	if m.awsSess == nil {
		return "no credentials"
	}
	s := m.awsSess.Label
	if left := m.awsSess.ExpiresIn(time.Now()); left != "" {
		s += fmt.Sprintf(" (expires in %s)", left)
	}
	// Renewal happens on a timer the user did not ask for, so it reports itself here rather
	// than interrupting them — silence means it is working.
	if note := m.credRefreshNote(); note != "" {
		s += " — " + note
	}
	return s
}

func (m *Model) selectAction(val string) tea.Cmd {
	switch val {
	case actionInstances:
		m.loading = true
		return m.fetchInstances()
	case actionCLI:
		return m.startCredsShell()
	case actionBuild:
		m.buildServiceList()
		m.screen = screenBuildService
		return nil
	}
	return nil
}

// startCredsShell hands the user a shell holding these credentials, then takes the screen
// back when it exits. tea.ExecProcess is the same mechanism the SSM shell session uses.
func (m *Model) startCredsShell() tea.Cmd {
	cmd, err := awsexec.Command(m.awsSess, awsexec.ShellArgv())
	if err != nil {
		m.err = err
		return nil
	}

	// Same banner treatment as an SSM shell. An authenticated shell that looks identical to
	// an unauthenticated one is how commands get run against the wrong account.
	return tea.ExecProcess(m.wrapWithHeader(cmd, "AWS CLI"), func(error) tea.Msg {
		return msgCredsShellDone{}
	})
}
