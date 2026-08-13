package main

import (
	"context"
	"os"
	"strings"
	"testing"

	awsint "github.com/treyperrone/warren/internal/aws"
	"github.com/treyperrone/warren/internal/tui"

	"github.com/treyperrone/warren/internal/testenv"
)

// The bug this guards: quitting the picker without choosing must not run the wrapped command.
// A nil Session is how that cancellation is signalled, and treating it as anything other than
// "do nothing, exit clean" would run the command with whatever credentials the ambient
// environment happened to hold — the opposite of what was asked for.
func TestCancelledPickerRunsNothing(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	m, err := tui.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Session() != nil {
		t.Fatal("a fresh model already has a session")
	}

	// Exits 7 if it runs at all, so a pass cannot be a false negative.
	code := runWithCreds(context.Background(), m, invocation{mode: modeExec, argv: []string{"sh", "-c", "exit 7"}})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 — the command should not have run", code)
	}
}

// Cancelling applies to ssm-shell too: with no session there are no credentials to call
// StartSession with, so it must exit clean rather than attempt the API call.
func TestCancelledPickerOpensNoSSMSession(t *testing.T) {
	testenv.SetHome(t, t.TempDir())

	m, err := tui.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	code := runWithCreds(context.Background(), m, invocation{
		mode:   modeSSMShell,
		target: "i-0123456789abcdef0",
	})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 — no session means nothing to connect with", code)
	}
}

func TestParseTargetAcceptsRealTargets(t *testing.T) {
	for _, target := range []string{
		"i-0123456789abcdef0",
		"mi-0123456789abcdef0",              // hybrid activation
		"ecs:my-cluster_task-id_runtime-id", // document-style, for when ECS exec lands
	} {
		got, err := parseTarget([]string{target})
		if err != nil {
			t.Errorf("parseTarget(%q) rejected a valid target: %v", target, err)
			continue
		}
		if got != target {
			t.Errorf("parseTarget(%q) = %q, want it passed through unchanged", target, got)
		}
	}
}

// The mistake worth catching client-side: the picker displays instance *names*, so reaching for
// ssm-shell with a name is the natural error. AWS would answer with TargetNotConnected, which
// reads as "the instance is broken" rather than "you passed the wrong field".
func TestParseTargetRejectsAnInstanceName(t *testing.T) {
	_, err := parseTarget([]string{"web-01"})
	if err == nil {
		t.Fatal("parseTarget accepted an instance name as a target")
	}
	if !strings.Contains(err.Error(), "not the name") {
		t.Errorf("error %q does not explain that a name was passed where an id belongs", err)
	}
}

func TestParseTargetRejectsBadArgumentCounts(t *testing.T) {
	if _, err := parseTarget(nil); err == nil {
		t.Error("parseTarget accepted no target at all")
	}
	// Two ids is most likely someone expecting ssm-shell to open several sessions at once. It
	// opens one; silently ignoring the rest would look like it had honoured them.
	if _, err := parseTarget([]string{"i-0123456789abcdef0", "i-abcdef0123456789a"}); err == nil {
		t.Error("parseTarget accepted two targets")
	}
}

// A mistyped flag must not be sent to AWS as an instance id.
func TestParseTargetRejectsAFlag(t *testing.T) {
	if _, err := parseTarget([]string{"--region"}); err == nil {
		t.Error("parseTarget accepted a flag as a target")
	}
}

// ssm-shell has to be reachable, and the usage text is the only place it is discoverable from the
// command line. It also documents the tmux one-liner, which is the point of the subcommand.
func TestUsageDocumentsSSMShell(t *testing.T) {
	for _, want := range []string{"warren ssm-shell <target>", "tmux new-window warren ssm-shell"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage text does not mention %q", want)
		}
	}
}

// The bug this guards: `warren shell`/`exec` set AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE
// on the child to a sentinel so that child's own AWS calls cannot see an ambient [default]
// profile — see internal/awsexec.Env. That child, for `warren shell`, is an ordinary interactive
// shell the user goes on using. Running `warren` again from inside it inherited the same two
// variables, and every AWS SDK call this tool makes (config.LoadDefaultConfig — used by
// ProfileSession, ListInstances, and the tunnel package) reads AWS_SHARED_CREDENTIALS_FILE
// directly, which nothing in internal/aws.ConfigPath's own check can reach. Confirmed live: this
// happened on the first real machine warren shipped to.
func TestStripInheritedNeutralizationClearsItsOwnSentinel(t *testing.T) {
	cfg, creds := awsint.NeutralizedProfilePaths()
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", creds)

	stripInheritedNeutralization()

	if v := os.Getenv("AWS_CONFIG_FILE"); v != "" {
		t.Errorf("AWS_CONFIG_FILE = %q after stripping, want unset", v)
	}
	if v := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); v != "" {
		t.Errorf("AWS_SHARED_CREDENTIALS_FILE = %q after stripping, want unset", v)
	}
}

// A value the user set deliberately — pointing warren and the aws CLI at a real, shared alternate
// config — must survive. Only the exact sentinel this tool generates is special-cased.
func TestStripInheritedNeutralizationLeavesARealOverrideAlone(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", "/home/trey/work/aws-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/home/trey/work/aws-credentials")

	stripInheritedNeutralization()

	if v := os.Getenv("AWS_CONFIG_FILE"); v != "/home/trey/work/aws-config" {
		t.Errorf("AWS_CONFIG_FILE = %q, want the deliberate override left alone", v)
	}
	if v := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); v != "/home/trey/work/aws-credentials" {
		t.Errorf("AWS_SHARED_CREDENTIALS_FILE = %q, want the deliberate override left alone", v)
	}
}

// The common case: neither variable set at all. Stripping must be a no-op, not an error, and must
// not itself set anything.
func TestStripInheritedNeutralizationIsANoOpWithNeitherSet(t *testing.T) {
	for _, k := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		if orig, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { os.Setenv(k, orig) })
		}
		os.Unsetenv(k)
	}

	stripInheritedNeutralization()

	if v := os.Getenv("AWS_CONFIG_FILE"); v != "" {
		t.Errorf("AWS_CONFIG_FILE = %q, want it to remain unset", v)
	}
	if v := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); v != "" {
		t.Errorf("AWS_SHARED_CREDENTIALS_FILE = %q, want it to remain unset", v)
	}
}
