package main

import (
	"context"
	"strings"
	"testing"

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
