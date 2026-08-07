package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyperrone/ssm-tool/internal/buildinfo"
	"github.com/treyperrone/ssm-tool/internal/pathhint"
	"github.com/treyperrone/ssm-tool/internal/tui"
)

const usage = `ssm-tool — browse AWS accounts and connect to EC2 instances over SSM.

usage:
  ssm-tool            launch the interactive picker
  ssm-tool setup      add an [sso-session] block to ~/.aws/config
  ssm-tool version    print the version and exit
  ssm-tool help       print this message and exit
`

func main() {
	// Anything other than a bare invocation is a command, not a flag to ignore.
	// Silently launching the TUI on an unrecognised argument makes a typo look
	// like the command ran and printed nothing.
	var startInSetup bool
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			// No PATH hint here: version output should stay a single parseable line for
			// anything scripting against it.
			fmt.Println(buildinfo.Version())
			return
		case "help", "--help", "-h":
			fmt.Print(usage)
			fmt.Print(pathhint.Hint())
			return
		case "setup":
			startInSetup = true
		default:
			fmt.Fprintf(os.Stderr, "unknown argument %q\n\n%s", os.Args[1], usage)
			os.Exit(2)
		}
	}

	ctx := context.Background()

	m, err := tui.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if startInSetup {
		// The returned cmd only starts the cursor blinking; Init runs for the setup screen
		// anyway once the program starts, so it is safe to drop here.
		_ = m.StartSetup()
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// The alt-screen is torn down on exit, taking the TUI's own confirmation with it. If a
	// config block was written, say so on the real terminal so the path survives in scrollback.
	if hint := m.SetupHint(); hint != "" {
		fmt.Print(hint)
	}

	// Printed last, and only while it is still true: once the directory is on PATH the
	// hint disappears on its own, so this cannot become a permanent nag.
	fmt.Print(pathhint.Hint())
}
