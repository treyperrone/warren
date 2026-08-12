package main

import (
	"context"
	"testing"

	"github.com/treyperrone/warren/internal/tui"
)

// The bug this guards: quitting the picker without choosing must not run the wrapped command.
// A nil Session is how that cancellation is signalled, and treating it as anything other than
// "do nothing, exit clean" would run the command with whatever credentials the ambient
// environment happened to hold — the opposite of what was asked for.
func TestCancelledPickerRunsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m, err := tui.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Session() != nil {
		t.Fatal("a fresh model already has a session")
	}

	// Exits 7 if it runs at all, so a pass cannot be a false negative.
	code := runWithCreds(m, invocation{mode: modeExec, argv: []string{"sh", "-c", "exit 7"}})
	if code != 0 {
		t.Errorf("exit code = %d, want 0 — the command should not have run", code)
	}
}
