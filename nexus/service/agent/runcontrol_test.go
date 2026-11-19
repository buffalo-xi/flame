package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"flame/nexus/shared/runledger"
)

// entry builds a minimal ledger record (Schema left empty -> no Redis, in-process ledger only).
func entry(runID, sessionID string) runledger.Entry {
	return runledger.Entry{RunID: runID, SessionID: sessionID}
}

func TestStopRunCancelsWithCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	unreg := registerRun(entry("run-stop-1", "sess-1"), cancel)
	defer unreg()

	if n := RunningCount(); n != 1 {
		t.Fatalf("RunningCount() = %d, want 1", n)
	}

	if !StopRunCause("run-stop-1", "sess-1", ErrRunStopped) {
		t.Fatal("StopRunCause returned false, want true")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrRunStopped) {
		t.Fatalf("cause = %v, want ErrRunStopped", cause)
	}

	// No longer counted once unregistered
	unreg()
	if n := RunningCount(); n != 0 {
		t.Fatalf("RunningCount() after unregister = %d, want 0", n)
	}

	// A finished run: stopping is idempotent and returns false
	if StopRunCause("run-stop-1", "sess-1", ErrRunStopped) {
		t.Fatal("StopRunCause on finished run returned true")
	}
}

func TestStopRunRejectsSessionMismatch(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-mismatch", "sess-a"), cancel)()

	if StopRunCause("run-mismatch", "sess-b", ErrRunStopped) {
		t.Fatal("StopRunCause should reject a different session")
	}
	if cause := context.Cause(ctx); cause != nil {
		t.Fatalf("cause = %v, want nil", cause)
	}
	// A session mismatch is a clear authorization failure: no pending stop may be left for this run, or it would be killed by mistake
	// when it registers. (Other tests leave harmless "finished run" records behind, so only this one is checked.)
	pendingStops.mu.Lock()
	stale := 0
	for _, p := range pendingStops.all {
		if p.runID == "run-mismatch" {
			stale++
		}
	}
	pendingStops.mu.Unlock()
	if stale != 0 {
		t.Fatalf("pending stops for run-mismatch = %d, want 0", stale)
	}
}

// A stop before registration: there is a window between the client disconnecting and Stream starting, and a stop in it must not be lost.
func TestStopRunCauseBeforeRegister(t *testing.T) {
	if StopRunCause("run-early", "sess-early", ErrClientGone) {
		t.Fatal("StopRunCause on an unregistered run returned true")
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-early", "sess-early"), cancel)()

	if cause := context.Cause(ctx); !errors.Is(cause, ErrClientGone) {
		t.Fatalf("cause = %v, want ErrClientGone", cause)
	}
	// Already stopped through the pending stop: stopping again must still return false (idempotent).
	if StopRunCause("run-early", "sess-early", ErrRunStopped) {
		t.Fatal("StopRunCause should be idempotent after a pending stop")
	}
}

// A pending stop expires: a stop from long ago must not kill a later run in the same session by mistake.
func TestPendingStopExpires(t *testing.T) {
	rememberPendingStop("run-late", "sess-late", ErrClientGone)

	// Push the timestamp beyond the TTL by hand (same-package test, so the registry is edited directly).
	pendingStops.mu.Lock()
	for i := range pendingStops.all {
		if pendingStops.all[i].runID == "run-late" {
			pendingStops.all[i].at = time.Now().Add(-pendingStopTTL - time.Second)
		}
	}
	pendingStops.mu.Unlock()

	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-late", "sess-late"), cancel)()

	if cause := context.Cause(ctx); cause != nil {
		t.Fatalf("stale pending stop cancelled the run: %v", cause)
	}
}

// An empty session_id is always refused: otherwise a run_id alone would be enough to stop someone else's run (the ledger is shared
// across agents).
func TestStopRunRequiresSession(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-no-sess", "sess-owner"), cancel)()

	if StopRunCause("run-no-sess", "", ErrRunStopped) {
		t.Fatal("StopRunCause should reject an empty session id")
	}
	if cause := context.Cause(ctx); cause != nil {
		t.Fatalf("run was cancelled without a session id: %v", cause)
	}
}

// Admin-side stop by run_id: a session-less run (a single RPC / MCP round) has to be stoppable too.
func TestStopRunByID(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-by-id", ""), cancel)()

	if !StopRunByID("run-by-id") {
		t.Fatal("StopRunByID returned false, want true")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrRunStopped) {
		t.Fatalf("cause = %v, want ErrRunStopped", cause)
	}
	// Idempotent: a second stop returns false
	if StopRunByID("run-by-id") {
		t.Fatal("StopRunByID should be idempotent")
	}
	if StopRunByID("run-not-exist") {
		t.Fatal("StopRunByID on unknown run returned true")
	}
}

func TestStopSessionStopsAllRuns(t *testing.T) {
	ctx1, cancel1 := context.WithCancelCause(context.Background())
	ctx2, cancel2 := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-s1", "sess-multi"), cancel1)()
	defer registerRun(entry("run-s2", "sess-multi"), cancel2)()

	if n := StopSession("sess-multi"); n != 2 {
		t.Fatalf("StopSession() = %d, want 2", n)
	}
	if cause := context.Cause(ctx1); !errors.Is(cause, ErrRunStopped) {
		t.Fatalf("run1 cause = %v, want ErrRunStopped", cause)
	}
	if cause := context.Cause(ctx2); !errors.Is(cause, ErrRunStopped) {
		t.Fatalf("run2 cause = %v, want ErrRunStopped", cause)
	}
	// Stopping again: everything has finished already
	if n := StopSession("sess-multi"); n != 0 {
		t.Fatalf("second StopSession() = %d, want 0", n)
	}
}

func TestCancelMessage(t *testing.T) {
	cases := map[error]string{
		ErrRunStopped: "运行已终止（用户请求）",
		ErrClientGone: "运行已终止（客户端断开）",
	}
	for cause, want := range cases {
		if got := cancelMessage(cause); got != want {
			t.Fatalf("cancelMessage(%v) = %q, want %q", cause, got, want)
		}
	}
}
