package flow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// WaitTimeoutCtx must return as soon as ctx is cancelled: after the user stops the flow a wait node
// must not hang until the concurrent branches finish on their own (fatal when timeout <= 0).
func TestWaitTimeoutCtxCancelled(t *testing.T) {
	fr := NewForkResult(0)
	if err := fr.Add(1, context.Background(), ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Deliberately never call Done: this simulates a concurrent branch stuck somewhere uninterruptible.

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	completed, err := fr.WaitTimeoutCtx(ctx, -1)
	elapsed := time.Since(start)

	if completed {
		t.Fatal("completed must be false when the wait did not finish")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("waited %v before returning, so cancellation was not observed", elapsed)
	}
	if fr.State() != ForkStateAbandoned {
		t.Fatalf("state = %v, want Abandoned", fr.State())
	}
}

// The normal completion path is unaffected.
func TestWaitTimeoutCtxCompleted(t *testing.T) {
	fr := NewForkResult(0)
	if err := fr.Add(1, context.Background(), ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	go fr.Done()

	completed, err := fr.WaitTimeoutCtx(context.Background(), 5)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !completed {
		t.Fatal("the wait should complete normally")
	}
	if fr.State() != ForkStateDone {
		t.Fatalf("state = %v, want Done", fr.State())
	}
}

// A timeout still returns false with err == nil (distinct from cancellation).
func TestWaitTimeoutCtxTimeout(t *testing.T) {
	fr := NewForkResult(0)
	if err := fr.Add(1, context.Background(), ForkMember{ProcessID: "p1", NodeID: "n1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	completed, err := fr.WaitTimeoutCtx(context.Background(), 1)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if completed {
		t.Fatal("a timeout must return completed=false")
	}
	if fr.State() != ForkStateAbandoned {
		t.Fatalf("state = %v, want Abandoned", fr.State())
	}
}
