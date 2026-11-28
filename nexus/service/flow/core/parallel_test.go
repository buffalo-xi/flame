package flow

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A limit is enforced by the semaphore the member takes inside its own goroutine, and the scope releases it when the
// member finishes: the second member starts only after the first one ends.
func TestBodyScopeMaxConcurrencyQueuesMembers(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 1})
	m1 := s.Add("m1", "1_1_1")
	m2 := s.Add("m2", "1_1_2")

	if !s.Acquire(context.Background(), m1) {
		t.Fatal("the first member did not acquire a slot")
	}

	acquired := make(chan struct{})
	go func() {
		if s.Acquire(context.Background(), m2) {
			close(acquired)
		}
	}()

	select {
	case <-acquired:
		t.Fatal("the second member started while the only slot was taken")
	case <-time.After(50 * time.Millisecond):
	}

	s.Finish(m1, StatusSuccess, "", "")
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("the second member never got the slot the first one released")
	}
	s.Finish(m2, StatusSuccess, "", "")
	if code, _ := s.Join(); code != "" {
		t.Fatalf("Join = %q, want no failure", code)
	}
}

// drain: a member that never started is recorded skipped (not failed) - counting stopped members as failures would turn
// one fault into many.
func TestBodyScopeDrainSkipsQueuedMembers(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 1, Join: JoinDrain})
	m1 := s.Add("m1", "1_1_1")
	m2 := s.Add("m2", "1_1_2")

	if !s.Acquire(context.Background(), m1) {
		t.Fatal("the first member did not acquire a slot")
	}
	s.Finish(m1, StatusFailed, ErrCodeTimeout, "timed out")

	if s.Acquire(context.Background(), m2) {
		t.Fatal("a draining scope started a queued member")
	}
	s.Abandon(m2, context.Background())

	code, _ := s.Join()
	if code != ErrCodeTimeout {
		t.Fatalf("Join = %q, want %q", code, ErrCodeTimeout)
	}
	snap := s.Snapshot()
	if len(snap.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(snap.Members))
	}
	if snap.Members[1].Status != StatusSkipped {
		t.Errorf("the queued member status = %q, want skipped", snap.Members[1].Status)
	}
	if len(snap.Failed) != 1 || snap.Failed[0] != "m1" {
		t.Errorf("Failed = %v, want only m1", snap.Failed)
	}
}

// collect never drains: a failure elsewhere lets the remaining members finish, so their outcomes stay real.
func TestBodyScopeCollectNeverDrains(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 1})
	m1 := s.Add("m1", "1_1_1")
	m2 := s.Add("m2", "1_1_2")

	if !s.Acquire(context.Background(), m1) {
		t.Fatal("the first member did not acquire a slot")
	}
	s.Finish(m1, StatusFailed, ErrCodeTimeout, "timed out")
	if s.Draining() {
		t.Fatal("collect must never drain the scope")
	}
	if !s.Acquire(context.Background(), m2) {
		t.Fatal("the second member was refused although no member is draining the scope")
	}
	s.Finish(m2, StatusSuccess, "", "")
	if code, _ := s.Join(); code != ErrCodeTimeout {
		t.Fatalf("Join = %q, want the failed member's code", code)
	}
}

// abort is drain plus cancelling the members still running: their context ends, the record says who cancelled them, and
// the host's error stays the first failure rather than the cancellation it caused.
func TestBodyScopeAbortCancelsRunningMembers(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{Join: JoinAbort})
	s.BindMembers(context.Background())
	memberCtx := s.MemberContext()

	m1 := s.Add("m1", "1_1_1")
	m2 := s.Add("m2", "1_1_2")
	if !s.Acquire(memberCtx, m1) || !s.Acquire(memberCtx, m2) {
		t.Fatal("the members did not acquire a slot")
	}

	s.Finish(m1, StatusFailed, ErrCodeTimeout, "timed out")
	if err := memberCtx.Err(); err == nil {
		t.Fatal("abort did not cancel the member context")
	}
	if !s.Draining() {
		t.Error("abort must also stop starting new work, exactly like drain")
	}

	// What a cancelled executor itself records: the scope rewrites the message so the run says who cancelled it.
	s.Finish(m2, StatusCancelled, ErrCodeCancel, context.Canceled.Error())

	code, _ := s.Join()
	if code != ErrCodeTimeout {
		t.Fatalf("Join = %q, want the first failure (%q) rather than the cancellation it caused", code, ErrCodeTimeout)
	}
	snap := s.Snapshot()
	if snap.Join != JoinAbort {
		t.Errorf("snapshot join = %q, want %q", snap.Join, JoinAbort)
	}
	if len(snap.Failed) != 1 || snap.Failed[0] != "m1" {
		t.Errorf("Failed = %v, want only m1: a cancelled member is not a failure", snap.Failed)
	}
	if got := snap.Members[1]; got.Status != StatusCancelled || !strings.Contains(got.ErrMsg, "aborted by scope") {
		t.Errorf("cancelled member = %+v, want cancelled with the abort reason", got)
	}
}

// The scope deadline does what `abort` does to the members in flight, and the join reports `timeout` with each member's
// state - not the failure of whichever member happened to be cancelled.
func TestBodyScopeTimeoutCancelsRunningMembers(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{Timeout: 20 * time.Millisecond})
	s.BindMembers(context.Background())
	memberCtx := s.MemberContext()

	m := s.Add("slow", "1_1_1")
	if !s.Acquire(memberCtx, m) {
		t.Fatal("the member did not acquire a slot")
	}
	select {
	case <-memberCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the deadline did not cancel the member context")
	}
	if !s.Draining() {
		t.Error("the deadline must also stop the walk, exactly like drain")
	}
	// What the cancelled executor itself records.
	s.Finish(m, StatusCancelled, ErrCodeCancel, context.Canceled.Error())

	code, msg := s.Join()
	if code != ErrCodeTimeout {
		t.Fatalf("Join = %q, want %q", code, ErrCodeTimeout)
	}
	if !strings.Contains(msg, "scope timed out after 20ms") || !strings.Contains(msg, "node slow") {
		t.Errorf("msg = %q, want the deadline and the member state at that point", msg)
	}
	snap := s.Snapshot()
	if !snap.Abandoned {
		t.Error("snapshot abandoned = false, want the deadline recorded")
	}
	if got := snap.Members[0]; got.Status != StatusCancelled || !strings.Contains(got.ErrMsg, "scope timed out") {
		t.Errorf("cancelled member = %+v, want cancelled with the timeout reason", got)
	}
	if len(snap.Failed) != 0 {
		t.Errorf("Failed = %v, want a cancelled member not counted as a failure", snap.Failed)
	}
}

// A deadline that fires after every member finished is not a timeout: the body completed, and turning that into a failure
// because a timer was a moment late would be a lie the author cannot debug.
func TestBodyScopeTimeoutIgnoresAFinishedBody(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{Timeout: 10 * time.Millisecond})
	s.BindMembers(context.Background())
	m := s.Add("m1", "1_1_1")
	if !s.Acquire(s.MemberContext(), m) {
		t.Fatal("the member did not acquire a slot")
	}
	s.Finish(m, StatusSuccess, "", "")

	time.Sleep(40 * time.Millisecond)
	if s.TimedOut() {
		t.Error("TimedOut = true, want false: every member had finished before the deadline")
	}
	if code, msg := s.Join(); code != "" {
		t.Errorf("Join = %q, %q; want no error for a finished body", code, msg)
	}
	if s.Snapshot().Abandoned {
		t.Error("snapshot abandoned = true, want false")
	}
}

// Aggregation: one shared code propagates as is, mixed codes collapse into other with one line per member.
func TestBodyScopeJoinAggregation(t *testing.T) {
	same := NewBodyScope("host", "1_1", ParallelConfig{})
	a := same.Add("a", "1_1_1")
	b := same.Add("b", "1_1_2")
	same.Finish(a, StatusFailed, ErrCodeTimeout, "a timed out")
	same.Finish(b, StatusFailed, ErrCodeTimeout, "b timed out")
	if code, _ := same.Join(); code != ErrCodeTimeout {
		t.Fatalf("same-code Join = %q, want %q", code, ErrCodeTimeout)
	}

	mixed := NewBodyScope("host", "1_1", ParallelConfig{})
	c := mixed.Add("c", "1_1_1")
	d := mixed.Add("d", "1_1_2")
	mixed.Finish(c, StatusFailed, ErrCodeTimeout, "c timed out")
	mixed.Finish(d, StatusFailed, ErrCodeScript, "d failed")
	code, msg := mixed.Join()
	if code != ErrCodeOther {
		t.Fatalf("mixed-code Join = %q, want %q", code, ErrCodeOther)
	}
	if !strings.Contains(msg, "c") || !strings.Contains(msg, "d") {
		t.Errorf("msg = %q, want one line per member", msg)
	}
}

// The snapshot records start/end and duration for every member, and lists the failed ones by node id.
func TestBodyScopeSnapshot(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 2})
	m := s.Add("m1", "1_1_1")
	if !s.Acquire(context.Background(), m) {
		t.Fatal("the member did not acquire a slot")
	}
	time.Sleep(10 * time.Millisecond)
	s.Finish(m, StatusFailed, ErrCodeTimeout, "timed out")

	snap := s.Snapshot()
	if snap.MaxConcurrency != 2 || snap.Join != JoinCollect {
		t.Errorf("snapshot config = %+v, want the scope's own settings", snap)
	}
	if len(snap.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(snap.Members))
	}
	if snap.Members[0].DurationMs <= 0 {
		t.Errorf("DurationMs = %d, want the measured duration", snap.Members[0].DurationMs)
	}
	if len(snap.Failed) != 1 || snap.Failed[0] != "m1" {
		t.Errorf("Failed = %v, want [m1]", snap.Failed)
	}
}

// Finish is idempotent: a panic path that runs after a normal finish must not release the slot twice (which would block
// or corrupt the count).
func TestBodyScopeFinishIsIdempotent(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 1})
	m1 := s.Add("m1", "1_1_1")
	m2 := s.Add("m2", "1_1_2")
	if !s.Acquire(context.Background(), m1) {
		t.Fatal("the first member did not acquire a slot")
	}
	s.Finish(m1, StatusSuccess, "", "")
	s.Finish(m1, StatusFailed, ErrCodeOther, "second call must be ignored")

	if !s.Acquire(context.Background(), m2) {
		t.Fatal("the slot was not released exactly once")
	}
	s.Finish(m2, StatusSuccess, "", "")
	snap := s.Snapshot()
	if snap.Members[0].Status != StatusSuccess {
		t.Errorf("status = %q, want the first recorded outcome to stand", snap.Members[0].Status)
	}
}

// A cancelled run records a member that never started as cancelled, not as skipped: "draining" would be a lie.
func TestBodyScopeAbandonCancelled(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 1})
	m := s.Add("m1", "1_1_1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Abandon(m, ctx)

	snap := s.Snapshot()
	if snap.Members[0].Status != StatusCancelled {
		t.Errorf("status = %q, want cancelled", snap.Members[0].Status)
	}
	if snap.Members[0].ErrCode != ErrCodeCancel {
		t.Errorf("code = %q, want %q", snap.Members[0].ErrCode, ErrCodeCancel)
	}
}

// `exit` is global and immediate: StopOnExit stops the queued members from starting, cancels the ones in flight, and
// reports the node that fired it. The first exit wins, so a nested host unwinding the same signal cannot rename it.
func TestBodyScopeStopOnExitStopsEverything(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 1, Timeout: 50 * time.Millisecond})
	s.BindMembers(context.Background())
	m1 := s.Add("m1", "1_1_1")
	m2 := s.Add("m2", "1_1_2")
	if !s.Acquire(s.MemberContext(), m1) {
		t.Fatal("the first member did not acquire a slot")
	}

	s.StopOnExit("x1")
	if !s.Draining() {
		t.Error("Draining = false, want true: an exit stops starting work")
	}
	if s.ExitedBy() != "x1" {
		t.Errorf("ExitedBy = %q, want x1", s.ExitedBy())
	}
	if s.Acquire(s.MemberContext(), m2) {
		t.Error("a queued member started after the exit")
	}
	s.Abandon(m2, s.MemberContext())
	s.Finish(m1, StatusCancelled, ErrCodeCancel, context.Canceled.Error())

	// The first node is the one the run record names; a second call cannot rewrite it.
	s.StopOnExit("x2")
	if s.ExitedBy() != "x1" {
		t.Errorf("ExitedBy = %q after a second exit, want the first node x1", s.ExitedBy())
	}

	if code, msg := s.Join(); code != "" || msg != "" {
		t.Errorf("Join = %q/%q, want no failure code for an exited scope", code, msg)
	}
	snap := s.Snapshot()
	if snap.ExitedBy != "x1" || snap.Abandoned {
		t.Errorf("snapshot exited_by/abandoned = %q/%v, want x1/false", snap.ExitedBy, snap.Abandoned)
	}
	if got := snap.Members[1]; got.Status != StatusSkipped || !strings.Contains(got.ErrMsg, "exit at node x1") {
		t.Errorf("queued member = %+v, want skipped with the exit reason", got)
	}
	if got := snap.Members[0]; !strings.Contains(got.ErrMsg, "stopped by exit at node x1") {
		t.Errorf("cancelled member = %+v, want the scope's exit reason", got)
	}
}

// A deadline that fires after the run was exited must not relabel the members the exit stopped.
func TestBodyScopeExitOutranksALateDeadline(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{Timeout: 20 * time.Millisecond})
	s.BindMembers(context.Background())
	m := s.Add("m1", "1_1_1")
	if !s.Acquire(s.MemberContext(), m) {
		t.Fatal("the member did not acquire a slot")
	}
	s.StopOnExit("x1")
	time.Sleep(40 * time.Millisecond)
	if s.TimedOut() {
		t.Error("TimedOut = true, want false: the exit ended the run before the deadline mattered")
	}
	s.Finish(m, StatusCancelled, ErrCodeCancel, context.Canceled.Error())
	if snap := s.Snapshot(); snap.Abandoned {
		t.Error("snapshot abandoned = true, want false")
	}
}

// A scope with no members joins immediately and reports nothing: an empty body is not a failure.
func TestBodyScopeEmpty(t *testing.T) {
	s := NewBodyScope("host", "1_1", ParallelConfig{MaxConcurrency: 3})
	if code, msg := s.Join(); code != "" || msg != "" {
		t.Fatalf("Join = %q/%q, want empty", code, msg)
	}
	if snap := s.Snapshot(); len(snap.Members) != 0 || snap.Join != JoinCollect {
		t.Errorf("snapshot = %+v, want an empty collect scope", snap)
	}
}
