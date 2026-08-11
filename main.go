package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyperrone/postern/internal/awsexec"
	"github.com/treyperrone/postern/internal/buildinfo"
	"github.com/treyperrone/postern/internal/pathhint"
	"github.com/treyperrone/postern/internal/tui"
)

const usage = `postern — browse AWS accounts and connect to EC2 instances over SSM.

usage:
  postern                  launch the interactive picker
  postern exec -- <cmd>    pick an account and role, then run <cmd> with its credentials
  postern shell            pick an account and role, then open a shell with its credentials
  postern setup            add an [sso-session] block to ~/.aws/config
  postern version          print the version and exit
  postern help             print this message and exit

examples:
  postern exec -- aws s3 ls
  postern exec -- aws ec2 describe-instances --query 'Reservations[].Instances[].Tags'
  postern shell
`

// mode is what to do once the picker has produced credentials.
type mode int

const (
	modeTUI   mode = iota // the normal interactive tool
	modeExec              // run one command, then exit with its status
	modeShell             // run $SHELL
)

func main() {
	run := parseArgs()

	ctx := context.Background()

	m, err := tui.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if run.startInSetup {
		// The returned cmd only starts the cursor blinking; Init runs for the setup screen
		// anyway once the program starts, so it is safe to drop here.
		_ = m.StartSetup()
	}

	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if run.mode != modeTUI {
		// The picker must not write to stdout in these modes: stdout belongs to the command
		// being wrapped, so `postern exec -- aws s3 ls | grep foo` would otherwise pipe
		// terminal control sequences into grep.
		m.StartCredsMode()
		opts = append(opts, tea.WithOutput(os.Stderr))
	}

	p := tea.NewProgram(m, opts...)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if run.mode != modeTUI {
		os.Exit(runWithCreds(m, run))
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

// invocation is the parsed command line.
type invocation struct {
	mode         mode
	argv         []string // command to run, for modeExec
	startInSetup bool
}

func parseArgs() invocation {
	// Anything other than a bare invocation is a command, not a flag to ignore.
	// Silently launching the TUI on an unrecognised argument makes a typo look
	// like the command ran and printed nothing.
	if len(os.Args) < 2 {
		return invocation{mode: modeTUI}
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		// No PATH hint here: version output should stay a single parseable line for
		// anything scripting against it.
		fmt.Println(buildinfo.Version())
		os.Exit(0)

	case "help", "--help", "-h":
		fmt.Print(usage)
		fmt.Print(pathhint.Hint())
		os.Exit(0)

	case "setup":
		return invocation{mode: modeTUI, startInSetup: true}

	case "shell":
		return invocation{mode: modeShell, argv: awsexec.ShellArgv()}

	case "exec":
		argv := os.Args[2:]
		// The `--` is conventional and worth accepting, but not worth requiring: it exists to
		// stop a wrapper eating the wrapped command's flags, and nothing here parses flags
		// after "exec" anyway.
		if len(argv) > 0 && argv[0] == "--" {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			fmt.Fprintf(os.Stderr, "exec needs a command to run, e.g. postern exec -- aws s3 ls\n\n%s", usage)
			os.Exit(2)
		}
		return invocation{mode: modeExec, argv: argv}

	default:
		fmt.Fprintf(os.Stderr, "unknown argument %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	panic("unreachable")
}

// runWithCreds runs the requested command with the picked session and returns its exit code.
func runWithCreds(m *tui.Model, run invocation) int {
	sess := m.Session()
	if sess == nil {
		// Quitting the picker before choosing is a cancellation, not an error: say nothing
		// and exit clean, the way ^C out of any other picker behaves.
		return 0
	}

	// Identity goes to stderr, never stdout — stdout is the wrapped command's to own.
	who := sess.Label
	if left := sess.ExpiresIn(time.Now()); left != "" {
		who += "  •  credentials expire in " + left
	}
	fmt.Fprintf(os.Stderr, "%s\n", who)
	if run.mode == modeShell {
		fmt.Fprintf(os.Stderr, "%s is set; exit the shell to return.\n", awsexec.SessionLabelVar)
	}

	code, err := awsexec.Run(sess, run.argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return code
}
