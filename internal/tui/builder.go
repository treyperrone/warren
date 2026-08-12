package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/treyperrone/warren/internal/awscli"
	"github.com/treyperrone/warren/internal/awsexec"
)

// msgBuildDone reports that a built command finished and the screen is ours again.
type msgBuildDone struct{}

var (
	styleCmd     = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	styleCmdDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleEditing = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
)

// builder holds the state of the command being assembled.
type builder struct {
	service *service
	recipe  *recipe

	inputs []textinput.Model
	focus  int

	// editing replaces the parameter form with the command line itself. This is what keeps
	// the recipe list from having to be complete: the nearest recipe plus an edit beats a
	// menu that does not have your task.
	editing bool
	edit    textinput.Model

	err error
}

func (m *Model) buildServiceList() {
	items := make([]list.Item, 0, len(services))
	for _, s := range services {
		items = append(items, item{title: s.name, desc: s.desc, value: s.name})
	}
	m.list.Title = "Which service?  •  " + m.credSummary() + "  •  Esc=back"
	m.list.SetStatusBarItemName("service", "services")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) selectBuildService(name string) tea.Cmd {
	for i := range services {
		if services[i].name == name {
			m.builder = builder{service: &services[i]}
			m.buildTaskList()
			m.screen = screenBuildTask
			return nil
		}
	}
	return nil
}

func (m *Model) buildTaskList() {
	svc := m.builder.service
	items := make([]list.Item, 0, len(svc.recipes))
	for _, r := range svc.recipes {
		items = append(items, item{title: r.title, desc: r.desc, value: r.title})
	}
	m.list.Title = svc.name + "  •  what do you want to know?  •  Esc=back"
	m.list.SetStatusBarItemName("task", "tasks")
	m.setListItems(items)
	m.list.Select(0)
}

func (m *Model) selectBuildTask(title string) tea.Cmd {
	svc := m.builder.service
	for i := range svc.recipes {
		if svc.recipes[i].title == title {
			m.builder.recipe = &svc.recipes[i]
			m.builder.newForm()
			m.screen = screenBuildParams
			return textinput.Blink
		}
	}
	return nil
}

// newForm creates one input per parameter. A recipe with no parameters still gets this screen
// rather than running immediately — seeing the command before it executes is the point.
func (b *builder) newForm() {
	b.inputs = make([]textinput.Model, 0, len(b.recipe.params))
	for _, p := range b.recipe.params {
		in := textinput.New()
		in.Placeholder = p.placeholder
		in.SetValue(p.def)
		in.CharLimit = 256
		in.Width = 56
		b.inputs = append(b.inputs, in)
	}
	b.focus = 0
	b.editing = false
	b.err = nil
	if len(b.inputs) > 0 {
		b.inputs[0].Focus()
	}
}

func (b *builder) values() []string {
	v := make([]string, len(b.inputs))
	for i := range b.inputs {
		v[i] = strings.TrimSpace(b.inputs[i].Value())
	}
	return v
}

// commandLine is what will actually run: the edited text once edited, otherwise the recipe's
// own rendering of the current parameters.
func (b *builder) commandLine() string {
	if b.editing {
		return strings.TrimSpace(b.edit.Value())
	}
	if b.recipe == nil {
		return ""
	}
	return b.recipe.build(b.values())
}

func (b *builder) focusOn(i int) {
	if len(b.inputs) == 0 {
		return
	}
	b.focus = (i + len(b.inputs)) % len(b.inputs)
	for j := range b.inputs {
		if j == b.focus {
			b.inputs[j].Focus()
		} else {
			b.inputs[j].Blur()
		}
	}
}

// startEditing hands the assembled command line over as editable text.
func (b *builder) startEditing() {
	in := textinput.New()
	in.CharLimit = 4096
	in.Width = 68
	in.SetValue(b.commandLine())
	in.CursorEnd()
	in.Focus()
	b.edit = in
	b.editing = true
	for i := range b.inputs {
		b.inputs[i].Blur()
	}
}

func (m *Model) updateBuildParams(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	b := &m.builder

	switch msg.String() {
	case "esc":
		// Leaving the editor returns to the form, not out of the screen — an accidental esc
		// mid-edit should not throw away the whole command.
		if b.editing {
			b.editing = false
			b.focusOn(b.focus)
			return m, textinput.Blink
		}
		m.buildTaskList()
		m.screen = screenBuildTask
		return m, nil

	case "ctrl+e":
		if !b.editing {
			b.startEditing()
			return m, textinput.Blink
		}

	case "enter":
		// While editing, enter runs what is on screen. On the form, enter walks the fields
		// first and only runs from the last one, the way a form is expected to behave.
		if !b.editing && b.focus < len(b.inputs)-1 {
			b.focusOn(b.focus + 1)
			return m, nil
		}
		return m, m.runBuilt()

	case "tab", "down":
		if !b.editing {
			b.focusOn(b.focus + 1)
			return m, nil
		}

	case "shift+tab", "up":
		if !b.editing {
			b.focusOn(b.focus - 1)
			return m, nil
		}
	}

	var cmd tea.Cmd
	if b.editing {
		b.edit, cmd = b.edit.Update(msg)
		return m, cmd
	}
	if len(b.inputs) > 0 {
		b.inputs[b.focus], cmd = b.inputs[b.focus].Update(msg)
	}
	return m, cmd
}

// runBuilt suspends the TUI, runs the command against the real terminal so its output lands in
// scrollback like any other command, and takes the screen back afterwards.
func (m *Model) runBuilt() tea.Cmd {
	line := m.builder.commandLine()

	// Same endpoint as the shell: a `logs tail --follow` left running for an hour should not
	// stop working halfway through.
	credEnv, err := m.credentialEnv()
	if err != nil {
		m.builder.err = err
		return nil
	}

	// Checked before running rather than letting exec fail: the raw failure is
	// `aws: executable file not found in $PATH`, which does not say that this is the only part
	// of warren needing the CLI, nor where to get it.
	if !awscli.Detect().Found() {
		m.builder.err = errors.New(awscli.MissingError())
		return nil
	}

	cmd, err := awsexec.CommandLine(m.awsSess, line, credEnv...)
	if err != nil {
		m.builder.err = err
		return nil
	}
	m.builder.err = nil
	m.beginCredRefresh()
	return tea.ExecProcess(cmd, func(error) tea.Msg { return msgBuildDone{} })
}

func (b *builder) view(width int) string {
	if width < 20 {
		width = 76
	}
	inner := width - 4

	var s strings.Builder
	s.WriteString("\n" + styleTitle.MarginLeft(2).Render(b.service.name+" — "+b.recipe.title) + "\n")
	s.WriteString(styleFormHint.Width(inner).MarginLeft(2).Render(b.recipe.desc) + "\n\n")

	if b.editing {
		s.WriteString("  " + styleEditing.Render("editing the command directly") + "\n")
		s.WriteString("    " + b.edit.View() + "\n\n")
	} else {
		for i, p := range b.recipe.params {
			label := "  " + styleFormLabel.Render(p.label)
			if i == b.focus {
				label = styleFormFocus.Render("› " + p.label)
			}
			s.WriteString("  " + label + "\n")
			s.WriteString("    " + b.inputs[i].View() + "\n\n")
		}
		if len(b.recipe.params) == 0 {
			s.WriteString(styleFormHint.MarginLeft(2).Render("No parameters needed.") + "\n\n")
		}

		// The command is always on screen, even when it is not being edited. Seeing the real
		// thing every time is how the menu stops being necessary.
		s.WriteString("  " + styleCmdDim.Render("command") + "\n")
		s.WriteString(styleCmd.Width(inner).MarginLeft(4).Render(b.commandLine()) + "\n\n")
	}

	if b.err != nil {
		s.WriteString(styleErr.Width(inner).MarginLeft(2).Render(b.err.Error()) + "\n\n")
	}

	keys := "tab/↑↓ move  •  ctrl+e edit the command  •  enter run  •  esc back"
	if b.editing {
		keys = "enter run  •  esc back to the form"
	}
	s.WriteString(styleDim.MarginLeft(2).Render(keys) + "\n")
	return s.String()
}
