package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/awsexec"
	"github.com/treyperrone/warren/internal/buildinfo"
	"github.com/treyperrone/warren/internal/pathhint"
	"github.com/treyperrone/warren/internal/plugin"
	"github.com/treyperrone/warren/internal/tui"
	"github.com/treyperrone/warren/internal/tunnel"
)

const usage = `warren — browse AWS accounts and connect to EC2 instances over SSM.

usage:
  warren                     launch the interactive picker
  warren exec -- <cmd>       pick an account and role, then run <cmd> with its credentials
  warren shell               pick an account and role, then open a shell with its credentials
  warren ssm-shell <target>  pick an account and role, then open an SSM shell on <target>
  warren login [identity]    sign in without the TUI: device-code by default — URL + code
                             shown, URL sent to your local clipboard (OSC 52)
  warren login --browser     opt in to opening a browser (uses your saved browser/profile,
                             or asks when nothing is saved)
  warren login --status      report token liveness without signing in; exit 0 live, 1 not
  warren setup               add an [sso-session] block to ~/.aws/config
  warren version             print the version and exit
  warren help                print this message and exit

examples:
  warren exec -- aws s3 ls
  warren exec -- aws ec2 describe-instances --query 'Reservations[].Instances[].Tags'
  warren shell
  warren ssm-shell i-0123456789abcdef0
  tmux new-window warren ssm-shell i-0123456789abcdef0
`

// mode is what to do once the picker has produced credentials.
type mode int

const (
	modeTUI      mode = iota // the normal interactive tool
	modeExec                 // run one command, then exit with its status
	modeShell                // run $SHELL
	modeSSMShell             // open an interactive SSM session on one instance
)

func main() {
	stripInheritedNeutralization()
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
		os.Exit(runWithCreds(ctx, m, run))
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

// stripInheritedNeutralization undoes, at the start of every warren process, what a warren-spawned
// child was given to protect ITS OWN AWS calls from an ambient profile.
//
// The bug this closes: `warren exec`/`shell` set AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE
// on the child to a path that never exists, so the child's own AWS calls cannot fall back to a
// bare [default] profile in ~/.aws/credentials — see internal/awsexec.Env. That child is, for
// `warren shell`, an ordinary interactive shell the user goes on using. Running `warren` again
// from inside it inherits the same two variables, and both warren's own config.ParseConfig (via
// ConfigPath) and every AWS SDK call this tool makes (config.LoadDefaultConfig, used by
// ProfileSession, ListInstances, and the tunnel package) read AWS_SHARED_CREDENTIALS_FILE
// directly — a check inside ConfigPath alone cannot reach that second one. So a warren launched
// from inside its own spawned shell would see no sso-sessions and be unable to resolve any named
// profile, reproducing on itself exactly the failure this was built to prevent for everything
// else. This must run before anything else touches the environment or loads AWS config.
func stripInheritedNeutralization() {
	cfg, creds := awsint.NeutralizedProfilePaths()
	if os.Getenv("AWS_CONFIG_FILE") == cfg {
		os.Unsetenv("AWS_CONFIG_FILE")
	}
	if os.Getenv("AWS_SHARED_CREDENTIALS_FILE") == creds {
		os.Unsetenv("AWS_SHARED_CREDENTIALS_FILE")
	}
}

// invocation is the parsed command line.
type invocation struct {
	mode         mode
	argv         []string // command to run, for modeExec
	target       string   // SSM target, for modeSSMShell
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

	case "login":
		// Handled here rather than through the TUI plumbing below: login needs no picker,
		// no alt screen, and no instance list — that absence is its entire reason to exist.
		os.Exit(runLogin(context.Background(), os.Args[2:]))

	case "setup":
		return invocation{mode: modeTUI, startInSetup: true}

	case "shell":
		return invocation{mode: modeShell, argv: awsexec.ShellArgv()}

	case "ssm-shell":
		target, err := parseTarget(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage)
			os.Exit(2)
		}
		return invocation{mode: modeSSMShell, target: target}

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

// parseTarget validates the SSM target for `warren ssm-shell`.
//
// Checked before the picker runs rather than after, because the picker involves an SSO round-trip.
// Learning about a bad argument on the far side of that is the difference between correcting it
// immediately and sitting through a device authorization to be told.
//
// The shape check is deliberately loose. It rejects the one mistake a client can actually catch —
// passing the instance *name*, which is what the TUI displays, where an id is required — and
// otherwise gets out of the way. StartSession also accepts targets that are not instance ids at
// all, such as the ecs:cluster_task_container form used for ECS exec, so an allowlist of i-/mi-
// would reject valid targets the moment warren grows to cover them. AWS is the authority on
// whether a target exists; this only catches arguments that cannot be targets at all.
func parseTarget(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("ssm-shell needs a target, e.g. warren ssm-shell i-0123456789abcdef0")
	}
	if len(args) > 1 {
		return "", fmt.Errorf("ssm-shell takes one target, got %d: %s", len(args), strings.Join(args, " "))
	}

	target := args[0]
	switch {
	case strings.HasPrefix(target, "-"):
		// Otherwise a mistyped flag becomes an instance id and the error arrives from AWS.
		return "", fmt.Errorf("%q looks like a flag, not a target; ssm-shell takes no flags", target)
	case strings.HasPrefix(target, "i-"), strings.HasPrefix(target, "mi-"):
		return target, nil
	case strings.Contains(target, ":"):
		// A document-style target, e.g. ecs:my-cluster_taskid_runtimeid. Passed through.
		return target, nil
	default:
		return "", fmt.Errorf("%q is not an instance id — ssm-shell takes the id (i-0123456789abcdef0), "+
			"not the name shown in the picker", target)
	}
}

// runSSMShell opens an interactive Session Manager shell on one target.
//
// Nothing here wraps the session in tmux, unlike the TUI's own shell action. That wrapper exists
// to keep warren's banner pinned while a session has the screen; this command hands the terminal
// over and stays out of the way, which is the whole reason it exists — the caller decides whether
// this lands in a tmux window, a new tab, or a separate SSH connection, and warren imposing its
// own multiplexer would take that choice back.
//
// There is also no credential endpoint, unlike exec and shell. Those hand credentials to a child
// that may keep calling AWS for hours, so a static copy goes stale at the one-hour mark. An SSM
// shell spends its credentials once, on StartSession; after that the plugin holds a stream token
// and never authenticates again, so a renewing endpoint would have nothing to renew.
func runSSMShell(ctx context.Context, sess *awsint.Session, target string) int {
	// Printed before StartSession, which is a network round-trip: otherwise the first thing the
	// user sees is a pause with no indication of what is being waited on.
	fmt.Fprintf(os.Stderr, "opening an SSM session on %s\n", target)

	cmd, err := tunnel.ShellCmd(ctx, target, sess)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// The plugin exiting non-zero is its status to report, not an warren error to wrap.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// runWithCreds runs the requested command with the picked session and returns its exit code.
func runWithCreds(ctx context.Context, m *tui.Model, run invocation) int {
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

	if run.mode == modeSSMShell {
		return runSSMShell(ctx, sess, run.target)
	}
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
