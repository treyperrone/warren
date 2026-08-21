package aws

import (
	"context"
	"testing"
	"time"
)

// A device code that expired before anyone approved it must time out, not poll forever: the
// deadline comes from AWS's ExpiresIn, and past it every CreateToken would fail anyway.
func TestPendingLoginWaitTimesOut(t *testing.T) {
	p := &PendingLogin{
		interval: time.Millisecond,
		deadline: time.Now().Add(-time.Second),
	}
	if _, err := p.Wait(context.Background()); err == nil || err.Error() != "login timed out" {
		t.Fatalf("Wait past deadline = %v, want login timed out", err)
	}
}

// Cancelling the context must end the wait within one poll interval. The old loop slept with
// bare time.Sleep, so a quit TUI kept its process alive until the next poll came round.
func TestPendingLoginWaitHonoursCancellation(t *testing.T) {
	p := &PendingLogin{
		interval: time.Hour, // poll would otherwise not fire for an hour
		deadline: time.Now().Add(time.Hour),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := p.Wait(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait returned nil on a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}
