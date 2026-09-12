package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/awscli"
	"github.com/treyperrone/warren/internal/awsexec"
	"github.com/treyperrone/warren/internal/browser"
)

// Action values on the screen shown once credentials exist.
const (
	actionInstances   = "instances"
	actionCLI         = "cli"
	actionBuild       = "build"
	actionSaveProfile = "saveprofile"
	actionFavAdd      = "favadd"
	actionFavRemove   = "favremove"
	actionTunnels     = "tunnels"
)

// msgCredsShellDone reports that the credentialed shell exited.
type msgCredsShellDone struct{}

// cliNote marks the two entries that need the aws CLI when it is not installed.
//
// Said here rather than only on failure, and the entries are still selectable: the shell is useful
// with or without the CLI, and finding out what a menu item needs by picking it and watching it
// fail is worse than being told on the row.
func cliNote() string {
	if awscli.Detect().Found() {
		return ""
	}
	return "  —  needs the aws CLI, which is not installed"
}

// buildActionList asks what to do with the role just assumed.
//
// This screen exists because connecting to a host and calling the API diverge here: an API
// call needs no instance, so the old flow — straight from role to the instance list — had no
// room for it. Without this screen `exec` and `shell` are real but invisible, reachable only
// by someone who already read the help.
func (m *Model) buildActionList() {
	var items []list.Item

	// Active tunnels lead when there are any: they are time-sensitive, and the manager —
	// where you reconnect or tear one down — was otherwise reachable only by starting a new
	// connection, so a tunnel that outlived a re-auth had nowhere to be seen.
	if n := len(m.manager.Active()); n > 0 {
		items = append(items, item{
			title: fmt.Sprintf("Active tunnels (%d)", n),
			desc:  "open the tunnel manager — reconnect, favorite, or disconnect a live session",
			value: actionTunnels,
		})
	}

	items = append(items,
		// First, and therefore the default: connecting to a host is still what the tool is
		// mostly for, so the established flow costs one extra keystroke and no thought.
		item{
			title: "Browse EC2 instances",
			desc:  "connect to a host over SSM — shell, SSH, or RDP",
			value: actionInstances,
		},
		item{
			title: "Run AWS CLI commands",
			desc:  "open a shell with these credentials; run as many as you like, exit to return" + cliNote(),
			value: actionCLI,
		},
		item{
			title: "Browse S3 buckets",
			desc:  "download objects, or upload by dragging a file into the window",
			value: actionS3,
		},
		item{
			title: "Build an AWS CLI command",
			desc:  "pick a service and a task; edit the command before it runs" + cliNote(),
			value: actionBuild,
		},
	)

	// Only for the sso-session flow: the block this writes names the session, the account
	// and the role, and a named-profile flow has no session of its own to point at (it IS
	// already a profile).
	if m.selSession != nil && m.selAccount != nil && m.awsSess != nil && m.awsSess.RoleName != "" {
		items = append(items, item{
			title: "Save as AWS profile",
			desc: "append [profile " + profileSlug(m.selAccount.Name, m.awsSess.RoleName) +
				"] to ~/.aws/config — credentials via `warren creds`, fresh on every use, for any AWS tool",
			value: actionSaveProfile,
		})
		// The star toggles: one row, phrased by what selecting it will do. Favorites pin
		// this exact account+role to the top of the picker and give it a CLI nickname —
		// the returning-to answer, where the picker is the finding answer.
		probe := browser.Favorite{StartURL: m.selSession.StartURL, AccountID: m.selAccount.ID, Role: m.awsSess.RoleName}
		if fav, ok := browser.FindFavorite(probe); ok {
			items = append(items, item{
				title: "★ Remove from favorites",
				desc:  "unpin " + fav.Nickname + " from the top of the picker",
				value: actionFavRemove,
			})
		} else {
			items = append(items, item{
				title: "☆ Add to favorites",
				desc: "pin to the top of the picker; usable from the CLI as: warren exec " +
					profileSlug(m.selAccount.Name, m.awsSess.RoleName) + " -- <cmd>",
				value: actionFavAdd,
			})
		}
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
	case actionFavAdd:
		fav := browser.Favorite{
			StartURL:    m.selSession.StartURL,
			AccountID:   m.selAccount.ID,
			AccountName: m.selAccount.Name,
			Role:        m.awsSess.RoleName,
		}
		fav.Nickname = browser.UniqueNickname(profileSlug(m.selAccount.Name, m.awsSess.RoleName), fav)
		if err := browser.AddFavorite(fav); err != nil {
			m.err = err
			return nil
		}
		m.notice = "favorited as " + fav.Nickname + " — pinned to the picker, and: warren exec " + fav.Nickname + " -- <cmd>"
		m.buildActionList() // the star flips in place
		return nil

	case actionFavRemove:
		fav := browser.Favorite{StartURL: m.selSession.StartURL, AccountID: m.selAccount.ID, Role: m.awsSess.RoleName}
		if err := browser.RemoveFavorite(fav); err != nil {
			m.err = err
			return nil
		}
		m.notice = "removed from favorites"
		m.buildActionList()
		return nil

	case actionS3:
		m.loading = true
		return m.fetchS3Buckets()

	case actionSaveProfile:
		name := profileSlug(m.selAccount.Name, m.awsSess.RoleName)
		err := awsint.AddCredentialProcessProfile(name, m.selSession.Name, m.selAccount.ID, m.awsSess.RoleName)
		if err != nil {
			m.err = err
			return nil
		}
		m.notice = "profile " + name + " appended to ~/.aws/config — usable now: aws --profile " + name + " sts get-caller-identity"
		// Rebuilt so the new profile appears on the method screen without a restart, the
		// same way + Add SSO session refreshes what the picker knows.
		if sessions, profiles, perr := awsint.ParseConfig(); perr == nil {
			m.ssoSessions, m.profiles = sessions, profiles
		}
		return nil
	case actionTunnels:
		m.buildMainList()
		m.screen = screenMain
		return nil
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
	// The shell fetches credentials from the loopback endpoint rather than carrying a copy, so it
	// keeps working past the hour instead of dying with the credentials it started with.
	credEnv, err := m.credentialEnv()
	if err != nil {
		m.err = err
		return nil
	}

	cmd, err := awsexec.Command(m.awsSess, awsexec.ShellArgv(), credEnv...)
	if err != nil {
		m.err = err
		return nil
	}
	m.beginCredRefresh()

	// Same banner treatment as an SSM shell. An authenticated shell that looks identical to
	// an unauthenticated one is how commands get run against the wrong account.
	return tea.ExecProcess(m.wrapWithHeader(cmd, "AWS CLI"), func(error) tea.Msg {
		return msgCredsShellDone{}
	})
}

// profileSlug builds a config-safe profile name from what the user already recognises: the
// account name and role. Everything outside the [profile] allowlist becomes a dash, runs
// collapse, and the result is lowercase — "Corp Lab" + "AdminRole" -> corp-lab-adminrole.
func profileSlug(account, role string) string {
	slug := func(s string) string {
		var b []rune
		lastDash := true // also trims leading dashes
		for _, r := range strings.ToLower(s) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
				b = append(b, r)
				lastDash = false
			default:
				if !lastDash {
					b = append(b, '-')
					lastDash = true
				}
			}
		}
		return strings.TrimRight(string(b), "-")
	}
	return slug(account) + "-" + slug(role)
}
