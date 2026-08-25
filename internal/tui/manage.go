package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/browser"
)

// favInlineMax is how many favorites live directly on the method screen before they
// collapse into one row. Favorites exist to be the first thing Enter lands on, but a dozen
// of them buries the sessions and profiles the screen is actually named for — at that
// point one "★ Favorites (12)" row keeps the speed (Enter, Enter connects the first one)
// without the burial.
const favInlineMax = 4

const (
	methodFavList      = "= favorites list"
	methodRemoveProfil = "= remove profile"
)

// buildFavoritesList is the dedicated favorites screen: every bookmark, plus the removal
// flow — which lives here rather than on a keybind because a key nobody can see is a
// feature nobody has.
func (m *Model) buildFavoritesList() {
	m.favorites = browser.Favorites()
	var items []list.Item
	for i, f := range m.favorites {
		items = append(items, item{
			title:  "★ " + favTitle(f),
			desc:   favDesc(f) + " — " + f.Nickname + ", via " + m.sessionLabelFor(f.StartURL) + "  •  x removes",
			value:  fmt.Sprintf("fav:%d", i),
			search: f.Nickname,
		})
	}
	items = append(items, item{
		title: "✕ Remove favorites…",
		desc:  "pick bookmarks to delete",
		value: "favrm",
	})
	m.list.Title = "Favorites  •  Esc=back"
	m.list.SetStatusBarItemName("favorite", "favorites")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) buildFavoriteRemoveList() {
	m.favorites = browser.Favorites()
	var items []list.Item
	for i, f := range m.favorites {
		items = append(items, item{
			title:  "✕ " + favTitle(f),
			desc:   "enter deletes this bookmark (the config file keeps everything else)",
			value:  fmt.Sprintf("favdel:%d", i),
			search: f.Nickname,
		})
	}
	m.list.Title = "Remove favorites  •  Esc=back"
	m.list.SetStatusBarItemName("favorite", "favorites")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) selectFavoriteRemove(val string) tea.Cmd {
	idx := strings.TrimPrefix(val, "favdel:")
	for i := range m.favorites {
		if fmt.Sprintf("%d", i) == idx {
			if err := browser.RemoveFavorite(m.favorites[i]); err != nil {
				m.err = err
				return nil
			}
			m.notice = "removed " + m.favorites[i].Nickname
			m.buildFavoriteRemoveList() // the row disappears in place
			if len(m.favorites) == 0 {
				m.buildMethodList()
				m.screen = screenMethod
			}
			return nil
		}
	}
	return nil
}

// removeFavoriteAt deletes the favorite behind a fav:N row and rebuilds whichever screen it
// was rendered on, so the row vanishes under the cursor rather than after a detour.
func (m *Model) removeFavoriteAt(idx string) {
	for i := range m.favorites {
		if fmt.Sprintf("%d", i) == idx {
			if err := browser.RemoveFavorite(m.favorites[i]); err != nil {
				m.err = err
				return
			}
			m.notice = "removed " + m.favorites[i].Nickname
			if m.screen == screenFavorites {
				m.buildFavoritesList()
				// The last bookmark leaving the dedicated screen strands it; go home.
				if len(m.favorites) == 0 {
					m.buildMethodList()
					m.screen = screenMethod
				}
				return
			}
			m.buildMethodList()
			return
		}
	}
}

func favTitle(f browser.Favorite) string {
	name := f.AccountName
	if name == "" {
		name = f.AccountID
	}
	return name + " / " + f.Role
}

func favDesc(f browser.Favorite) string {
	if f.Connects() {
		return f.InstanceName + " (" + f.ConnType + ")"
	}
	return "straight to credentials"
}

// ---- profile removal ---------------------------------------------------------------------

// buildProfileRemoveList offers every named profile for deletion. It exists because the
// alternative to a remove function is hand-editing a file one typo away from breaking every
// AWS tool on the machine — the exact risk warren was built to keep people away from.
func (m *Model) buildProfileRemoveList() {
	var items []list.Item
	for _, p := range m.profiles {
		desc := "static keys or assume-role"
		switch {
		case p.SSOSession != "":
			desc = "via sso-session " + p.SSOSession
		case p.SSOStartURL != "":
			desc = "legacy SSO (" + p.SSOStartURL + ")"
		}
		items = append(items, item{
			title: "✕ " + p.Name,
			desc:  desc + " — enter previews exactly what would be removed",
			value: p.Name,
		})
	}
	m.list.Title = "Remove an AWS profile  •  Esc=back"
	m.list.SetStatusBarItemName("profile", "profiles")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) selectProfileRemove(name string) tea.Cmd {
	block, err := awsint.ProfileBlockText(name)
	if err != nil {
		m.err = err
		return nil
	}
	m.profileRemoveName = name
	m.profileRemoveBlock = block
	m.buildProfileConfirmList()
	m.screen = screenProfileConfirm
	return nil
}

// buildProfileConfirmList is the last stop before the only destructive write warren makes
// to ~/.aws/config: the exact doomed lines are on screen, and "keep" is the first row so an
// Enter-through does nothing.
func (m *Model) buildProfileConfirmList() {
	m.list.Title = "Remove [profile " + m.profileRemoveName + "]?  •  a .warren.bak backup is taken first"
	m.list.SetStatusBarItemName("option", "options")
	m.setListItems([]list.Item{
		item{title: "Keep it", desc: "change nothing", value: "keep"},
		item{title: "Remove it", desc: "delete ONLY the lines shown below from ~/.aws/config", value: "remove"},
	})
	m.list.Select(0)
}

func (m *Model) selectProfileConfirm(val string) tea.Cmd {
	if val != "remove" {
		m.buildProfileRemoveList()
		m.screen = screenProfileRemove
		return nil
	}
	if err := awsint.RemoveProfileBlock(m.profileRemoveName); err != nil {
		m.err = err
		return nil
	}
	m.notice = "removed [profile " + m.profileRemoveName + "] — backup at ~/.aws/config.warren.bak"
	// The picker's world changed; reload it the way + Add SSO session does.
	if sessions, profiles, err := awsint.ParseConfig(); err == nil {
		m.ssoSessions, m.profiles = sessions, profiles
	}
	if len(m.profiles) > 0 {
		m.buildProfileRemoveList()
		m.screen = screenProfileRemove
		return nil
	}
	m.buildMethodList()
	m.screen = screenMethod
	return nil
}

// profileConfirmView pins the doomed lines under the keep/remove list, so the decision and
// its exact consequences share one screen.
func (m *Model) profileConfirmView() string {
	return m.banner() + m.noticeLine() + m.list.View() + "\n\n" +
		styleDim.MarginLeft(2).Render("lines to be removed:") + "\n" +
		styleErr.MarginLeft(4).Render(strings.TrimRight(m.profileRemoveBlock, "\n")) + "\n"
}
