package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
)

// builderModel walks the real flow to the parameter screen for a named service and task.
func builderModel(t *testing.T, svc, task string) *Model {
	t.Helper()
	m := newFormModel(t)
	m.awsSess = &awsint.Session{Label: "crlab/AdminRole", AccessKeyID: "AKIA", Region: "us-east-1"}

	if cmd := m.selectAction(actionBuild); cmd != nil {
		t.Fatal("selecting the builder should not need a command")
	}
	if m.screen != screenBuildService {
		t.Fatalf("screen = %v, want screenBuildService", m.screen)
	}

	m.selectBuildService(svc)
	if m.screen != screenBuildTask {
		t.Fatalf("screen = %v, want screenBuildTask", m.screen)
	}

	m.selectBuildTask(task)
	if m.screen != screenBuildParams {
		t.Fatalf("screen = %v, want screenBuildParams", m.screen)
	}
	return m
}

// Every recipe has to produce a runnable command. A typo in a format string shows up as a "%!"
// verb error rather than a compile failure, so it needs asserting.
func TestEveryRecipeBuildsAnAwsCommand(t *testing.T) {
	for _, svc := range services {
		for _, r := range svc.recipes {
			t.Run(svc.name+"/"+r.title, func(t *testing.T) {
				// nil, not empty strings: arg() must tolerate a build called before the form
				// has produced any values.
				line := r.build(nil)

				if !strings.HasPrefix(line, "aws ") {
					t.Errorf("command does not start with aws: %q", line)
				}
				if strings.Contains(line, "%!") || strings.Contains(line, "%s") {
					t.Errorf("unrendered format verb in %q", line)
				}
				if len(strings.Fields(line)) < 3 {
					t.Errorf("command looks incomplete: %q", line)
				}
			})
		}
	}
}

// The builder is read-only by design: it is aimed at people who would not spot a destructive
// command, so it must not be able to produce one. Mutations stay in the shell.
func TestEveryRecipeIsReadOnly(t *testing.T) {
	readOnlyPrefixes := []string{"describe-", "list-", "get-"}
	readOnlyExact := map[string]bool{"ls": true, "tail": true}

	for _, svc := range services {
		for _, r := range svc.recipes {
			t.Run(svc.name+"/"+r.title, func(t *testing.T) {
				fields := strings.Fields(r.build(nil))
				if len(fields) < 3 {
					t.Fatalf("cannot find the operation in %q", strings.Join(fields, " "))
				}
				op := fields[2]

				if readOnlyExact[op] {
					return
				}
				for _, p := range readOnlyPrefixes {
					if strings.HasPrefix(op, p) {
						return
					}
				}
				t.Errorf("operation %q is not a read-only verb", op)
			})
		}
	}
}

// Selection is by name and by title, so duplicates would make a row unreachable.
func TestServiceAndTaskNamesAreUnique(t *testing.T) {
	seenSvc := map[string]bool{}
	for _, svc := range services {
		if seenSvc[svc.name] {
			t.Errorf("duplicate service %q", svc.name)
		}
		seenSvc[svc.name] = true

		seenTask := map[string]bool{}
		for _, r := range svc.recipes {
			if seenTask[r.title] {
				t.Errorf("%s has duplicate task %q", svc.name, r.title)
			}
			seenTask[r.title] = true
		}
	}
}

// Partial tag matching is the point — "web" has to find "globogym-web-01" — and a blank value
// must widen to "any value" rather than searching for the empty string.
func TestTagRecipeWrapsValueForPartialMatch(t *testing.T) {
	m := builderModel(t, "EC2", "list instances by tag")

	if got := m.builder.inputs[0].Value(); got != "Name" {
		t.Errorf("tag key default = %q, want Name", got)
	}

	m.builder.inputs[1].SetValue("web")
	if got := m.builder.commandLine(); !strings.Contains(got, "Name=tag:Name,Values=*web*") {
		t.Errorf("command = %q, want a wildcarded tag filter", got)
	}

	m.builder.inputs[1].SetValue("")
	if got := m.builder.commandLine(); !strings.Contains(got, "Values=*") {
		t.Errorf("command = %q, want Values=* for a blank value", got)
	}
}

func TestBuilderFormMatchesRecipeParameters(t *testing.T) {
	m := builderModel(t, "CloudWatch Logs", "read a log group")

	if got := len(m.builder.inputs); got != 2 {
		t.Fatalf("form has %d inputs, want 2", got)
	}
	// A default counts as an answer, unlike a placeholder.
	if got := m.builder.inputs[1].Value(); got != "10m" {
		t.Errorf("since default = %q, want 10m", got)
	}

	m.builder.inputs[0].SetValue("/aws/lambda/thing")
	if got := m.builder.commandLine(); got != "aws logs tail /aws/lambda/thing --since 10m" {
		t.Errorf("command = %q", got)
	}
}

// A parameterless recipe still gets the screen rather than running on selection: seeing the
// command before it executes is the whole point of the builder.
func TestParameterlessRecipeStillShowsTheCommand(t *testing.T) {
	m := builderModel(t, "SSM", "list managed instances")

	if len(m.builder.inputs) != 0 {
		t.Errorf("expected no inputs, got %d", len(m.builder.inputs))
	}
	if got := m.builder.commandLine(); !strings.Contains(got, "describe-instance-information") {
		t.Errorf("command = %q", got)
	}
	if view := m.builder.view(100); !strings.Contains(view, "No parameters needed") {
		t.Error("view does not explain that there is nothing to fill in")
	}
}

// Editing is what stops the recipe list needing to be complete: start from the nearest task and
// change it. The editor must open seeded with the built command, not empty.
func TestEditingSeedsFromTheBuiltCommand(t *testing.T) {
	m := builderModel(t, "S3", "list buckets")
	built := m.builder.commandLine()

	m.builder.startEditing()
	if got := m.builder.edit.Value(); got != built {
		t.Errorf("editor seeded with %q, want %q", got, built)
	}

	m.builder.edit.SetValue(built + " | head -5")
	if got := m.builder.commandLine(); got != built+" | head -5" {
		t.Errorf("commandLine() = %q, want the edited text", got)
	}
}

// Esc mid-edit returns to the form. Leaving the screen instead would discard the command over a
// keystroke people press reflexively.
func TestEscapeWhileEditingReturnsToTheForm(t *testing.T) {
	m := builderModel(t, "EC2", "describe one instance")
	m.builder.startEditing()

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if m.screen != screenBuildParams {
		t.Errorf("screen = %v, want to stay on screenBuildParams", m.screen)
	}
	if m.builder.editing {
		t.Error("still editing after esc")
	}

	// A second esc does leave.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenBuildTask {
		t.Errorf("screen = %v, want screenBuildTask", m.screen)
	}
}

// The parameter screen owns the keyboard. Without that guard the list shortcuts eat the
// characters — "/" opens a search box instead of typing a path.
func TestParameterScreenTakesLiteralKeystrokes(t *testing.T) {
	m := builderModel(t, "S3", "list objects in a bucket")

	for _, r := range []rune{'l', 'o', 'g', 's', '/'} {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got := m.builder.inputs[0].Value(); got != "logs/" {
		t.Errorf("bucket input = %q, want logs/ — keystrokes were intercepted", got)
	}
	if m.list.SettingFilter() {
		t.Error(`"/" opened the list search instead of typing into the form`)
	}
}

// Backing out of the builder returns to the action screen, not out of the credentials entirely.
func TestBuilderGoesBackToTheActionScreen(t *testing.T) {
	m := builderModel(t, "IAM", "who am I")

	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // params -> task
	m.goBack()                             // task -> service
	if m.screen != screenBuildService {
		t.Fatalf("screen = %v, want screenBuildService", m.screen)
	}
	m.goBack() // service -> action
	if m.screen != screenAction {
		t.Errorf("screen = %v, want screenAction", m.screen)
	}
}
