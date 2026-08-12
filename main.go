package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/treyperrone/warren/internal/awsexec"
	"github.com/treyperrone/warren/internal/buildinfo"
	"github.com/treyperrone/warren/internal/pathhint"
	"github.com/treyperrone/warren/internal/plugin"
	"github.com/treyperrone/warren/internal/tui"
)

const usage = `warren — browse AWS accounts and connect to EC2 instances over SSM.

usage:
  warren                  launch the interactive picker
  warren exec -- <cmd>    pick an account and role, then run <cmd> with its credentials
  warren shell            pick an account and role, then open a shell with its credentials
  warren setup            add an [sso-session] block to ~/.aws/config
  warren version          print the version and exit
  warren help             print this message and exit

examples:
  warren exec -- aws s3 ls
  warren exec -- aws ec2 describe-instances --query 'Reservations[].Instances[].Tags'
  warren shell
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
		// being wrapped, so `warren exec -- aws s3 ls | grep foo` would otherwise pipe
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
		// warren's own version stays alone on the first line, so `warren version | head -1` and
		// anything scraping it keep working. The plugin version belongs here too: it is half of
		// what identifies a session's behaviour, and "which plugin version?" is the first thing
		// asked about an SSM problem. No PATH hint — that would be a third line of noise.
		fmt.Println(buildinfo.Version())
		fmt.Printf("session-manager-plugin %s (built from source)\n", plugin.Version())
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
			fmt.Fprintf(os.Stderr, "exec needs a command to run, e.g. warren exec -- aws s3 ls\n\n%s", usage)
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

	// Serve credentials over loopback rather than handing the child a copy, so a shell that
	// outlives its credentials keeps working instead of failing at the one-hour mark. If the
	// endpoint cannot start, fall back to the copy: a frozen hour beats not running at all.
	var credEnv []string
	if env, stop, err := m.CredentialEndpoint(); err == nil {
		credEnv = env
		defer stop()
	} else {
		fmt.Fprintf(os.Stderr, "note: credential endpoint unavailable (%v); "+
			"credentials will not renew inside this process\n", err)
	}

	code, err := awsexec.Run(sess, run.argv, credEnv...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return code
}
