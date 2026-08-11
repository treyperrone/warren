package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	awsint "github.com/treyperrone/postern/internal/aws"
)

// Field order in the setup form.
const (
	fieldName = iota
	fieldStartURL
	fieldRegion
	numFields
)

// methodAddSession is the sentinel value of the "+ Add SSO session" row on the method
// screen. It cannot collide with a session name, which may not contain a space.
const methodAddSession = "+ add sso session"

// defaultRegion prefills the region field. Identity Center is most often homed in us-east-1,
// and an SSO region is a single well-known value rather than something to be discovered, so
// offering it as an editable default costs nothing and saves typing it every time.
const defaultRegion = "us-east-1"

// setupForm collects the three values an [sso-session] block needs. Scopes are not asked
// for: sso_registration_scopes is what makes AWS issue a refresh token, so the default is
// the only correct answer and offering a choice would only invite breaking silent renewal.
type setupForm struct {
	inputs [numFields]textinput.Model
	focus  int
	err    error

	// savedName is the session written, empty until a successful save.
	savedName string
	// cancelable is set when the form was opened from the method screen rather than on
	// first run. First run has nothing behind it, so esc quits; otherwise esc goes back.
	cancelable bool
}

var (
	styleFormLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleFormHint  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleFormFocus = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
)

func newSetupForm() setupForm {
	var f setupForm

	name := textinput.New()
	// An example, not a default — nothing is written unless the user types a name.
	name.Placeholder = "e.g. crlab, crprod"
	name.CharLimit = 64
	name.Width = 60

	startURL := textinput.New()
	startURL.Placeholder = "https://my-org.awsapps.com/start"
	startURL.CharLimit = 256
	startURL.Width = 60

	region := textinput.New()
	region.Placeholder = defaultRegion
	region.CharLimit = 32
	region.Width = 60
	// A real prefilled value, not a placeholder: us-east-1 is a valid answer and the common
	// one, so enter accepts it. This is safe precisely where a placeholder default was not —
	// there is no plausible default name or start URL to invent, but there is a region.
	region.SetValue(defaultRegion)

	f.inputs = [numFields]textinput.Model{name, startURL, region}
	f.inputs[fieldName].Focus()
	return f
}

// init starts the cursor blinking on the focused field.
func (f *setupForm) init() tea.Cmd { return textinput.Blink }

// labels name each field. The session name is the label shown in the picker every time the
// tool runs, so it is worth saying that out loud — it is chosen by the user, not derived.
func (f *setupForm) labels() [numFields]string {
	return [numFields]string{
		"session name — your own label, shown in the picker",
		"SSO start URL",
		"SSO region — enter to accept",
	}
}

// config assembles the form's current values.
//
// Placeholders are examples, never defaults. An earlier version fell back to them for empty
// fields, which meant tabbing straight through wrote a session called "my-sso" pointing at
// https://my-org.awsapps.com/start — a block that looks configured and can never log in.
// Every field is required; ValidateSSOSession reports which one is missing.
func (f *setupForm) config() awsint.SSOSessionConfig {
	value := func(i int) string {
		return strings.TrimSpace(f.inputs[i].Value())
	}
	return awsint.SSOSessionConfig{
		Name:     value(fieldName),
		StartURL: value(fieldStartURL),
		Region:   value(fieldRegion),
	}
}

func (f *setupForm) focusOn(i int) {
	// Wrap, so tabbing off either end lands somewhere useful instead of sticking.
	f.focus = (i + numFields) % numFields
	for j := range f.inputs {
		if j == f.focus {
			f.inputs[j].Focus()
		} else {
			f.inputs[j].Blur()
		}
	}
}

func (f *setupForm) view(width int) string {
	if width < 20 {
		width = 76
	}

	heading := "No AWS SSO configuration found"
	if f.cancelable {
		heading = "Add an SSO session"
	}

	var b strings.Builder
	b.WriteString("\n" + styleTitle.MarginLeft(2).Render(heading) + "\n")
	b.WriteString(styleFormHint.Width(width-4).MarginLeft(2).Render(
		"Name and start URL are required; the region is prefilled. This is appended to "+
			"~/.aws/config as an [sso-session] block — nothing already in the file is "+
			"modified, and a .postern.bak copy is taken first.") + "\n\n")

	labels := f.labels()
	for i := range f.inputs {
		label := styleFormLabel.Render(labels[i])
		if i == f.focus {
			label = styleFormFocus.Render("› " + labels[i])
		} else {
			label = "  " + label
		}
		b.WriteString("  " + label + "\n")
		b.WriteString("    " + f.inputs[i].View() + "\n\n")
	}

	if f.err != nil {
		b.WriteString(styleErr.Width(width-4).MarginLeft(2).Render(f.err.Error()) + "\n\n")
	}

	exit := "esc quit"
	if f.cancelable {
		exit = "esc cancel"
	}
	b.WriteString(styleDim.MarginLeft(2).Render("tab/↑↓ move  •  ctrl+r region list  •  enter save  •  "+exit) + "\n")
	return b.String()
}

// updateSetup drives the form.
func (m *Model) updateSetup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// On first run there is no screen behind this one, so esc leaves the program.
		if !m.setup.cancelable {
			return m, tea.Quit
		}
		m.screen = screenMethod
		m.buildMethodList()
		return m, nil

	case "tab", "down":
		m.setup.focusOn(m.setup.focus + 1)
		return m, nil

	case "shift+tab", "up":
		m.setup.focusOn(m.setup.focus - 1)
		return m, nil

	case "ctrl+r":
		return m, m.openRegionPicker()

	case "enter":
		// Enter on any field but the last advances, so the form fills the way a form is
		// expected to. Only the last field commits.
		if m.setup.focus < numFields-1 {
			m.setup.focusOn(m.setup.focus + 1)
			return m, nil
		}
		return m, m.saveSetup()
	}

	var cmd tea.Cmd
	m.setup.inputs[m.setup.focus], cmd = m.setup.inputs[m.setup.focus].Update(msg)
	return m, cmd
}

// openRegionPicker lists the regions Identity Center actually runs in. It is an aid to
// typing, never a gate: the field stays free text, so a region AWS adds after this list was
// vendored can still be entered by hand.
func (m *Model) openRegionPicker() tea.Cmd {
	current := strings.TrimSpace(m.setup.inputs[fieldRegion].Value())

	items := make([]list.Item, 0, len(awsint.SSORegions))
	selected := 0
	for i, r := range awsint.SSORegions {
		// GovCloud is a different partition reached with different credentials entirely, so
		// say so rather than leaving it looking like just another us- region.
		var notes []string
		if strings.HasPrefix(r, "us-gov-") {
			notes = append(notes, "GovCloud")
		}
		switch r {
		case current:
			notes = append(notes, "current")
			selected = i
		case defaultRegion:
			notes = append(notes, "default")
		}
		items = append(items, item{title: r, desc: strings.Join(notes, " • "), value: r})
	}

	m.list.Title = "Select SSO region  •  /=search  •  Esc=back"
	m.list.SetStatusBarItemName("region", "regions")
	m.setListItems(items)
	m.list.Select(selected)
	m.screen = screenRegion
	return nil
}

// selectRegion writes the choice back into the form and returns to it.
func (m *Model) selectRegion(region string) tea.Cmd {
	m.setup.inputs[fieldRegion].SetValue(region)
	// Land on the region field so the change is visible where it happened.
	m.setup.focusOn(fieldRegion)
	m.screen = screenSetup
	return m.setup.init()
}

// StartSetup opens the form over whatever is currently on screen. Used by the method
// screen's "+ Add SSO session" row and by the `postern setup` subcommand.
func (m *Model) StartSetup() tea.Cmd {
	m.setup = newSetupForm()
	m.setup.cancelable = true
	m.screen = screenSetup
	return m.setup.init()
}

// saveSetup validates and appends the block, then enters the normal flow with the session
// it just created — the login it triggers is the real test of the start URL, and any
// failure surfaces in the usual error view.
func (m *Model) saveSetup() tea.Cmd {
	cfg := m.setup.config()

	if err := awsint.ValidateSSOSession(cfg); err != nil {
		m.setup.err = err
		return nil
	}
	if err := awsint.AddSSOSession(cfg); err != nil {
		m.setup.err = err
		return nil
	}

	// Re-read the file rather than appending cfg to the in-memory slice. Adding a second
	// session must not drop the first, and re-parsing is the only version of this that
	// cannot drift from what was actually written to disk.
	sessions, profiles, err := awsint.ParseConfig()
	if err != nil {
		m.setup.err = err
		return nil
	}
	m.ssoSessions, m.profiles = sessions, profiles

	// Select the session just created — it is what the user came here to use. Cleared
	// first so the not-found check below is not satisfied by a previous selection.
	m.selSession = nil
	for i := range m.ssoSessions {
		if m.ssoSessions[i].Name == cfg.Name {
			m.selSession = &m.ssoSessions[i]
			break
		}
	}
	if m.selSession == nil {
		m.setup.err = fmt.Errorf("wrote [sso-session %s] but could not read it back from %s",
			cfg.Name, awsint.ConfigPath())
		return nil
	}

	m.setup.err = nil
	m.setup.savedName = cfg.Name
	m.screen = screenAccount
	m.loading = true
	return tea.Batch(m.spin.Tick, m.fetchToken())
}

// SetupHint is printed after the program exits when a config was written, so the path is
// visible in scrollback rather than lost with the alt-screen. Empty if nothing was written.
func (m *Model) SetupHint() string {
	if m.setup.savedName == "" {
		return ""
	}
	return fmt.Sprintf("wrote [sso-session %s] to %s\n", m.setup.savedName, awsint.ConfigPath())
}
