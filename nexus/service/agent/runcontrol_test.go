package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"flame/nexus/shared/runledger"
)

// entry 造一条最小登记信息（Schema 留空 → 不碰 Redis，只走进程内台账）。
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

	// 注销后不再计入
	unreg()
	if n := RunningCount(); n != 0 {
		t.Fatalf("RunningCount() after unregister = %d, want 0", n)
	}

	// 已结束的运行：停止是幂等的，返回 false
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
	// 会话不匹配是明确的越权：不能给这条 run 留下待取消，否则它登记时会被误杀。
	// （别的测试会留下「已结束的 run」那种无害登记，所以只查这一条。）
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

// 停止早于登记：客户端断开与 Stream 启动之间有窗口，那时的停止不能丢。
func TestStopRunCauseBeforeRegister(t *testing.T) {
	if StopRunCause("run-early", "sess-early", ErrClientGone) {
		t.Fatal("StopRunCause on an unregistered run returned true")
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-early", "sess-early"), cancel)()

	if cause := context.Cause(ctx); !errors.Is(cause, ErrClientGone) {
		t.Fatalf("cause = %v, want ErrClientGone", cause)
	}
	// 已经按待取消停过：再停一次仍应返回 false（幂等）。
	if StopRunCause("run-early", "sess-early", ErrRunStopped) {
		t.Fatal("StopRunCause should be idempotent after a pending stop")
	}
}

// 待取消会过期：很久以前的一次停止不能误杀后来同一会话的新运行。
func TestPendingStopExpires(t *testing.T) {
	rememberPendingStop("run-late", "sess-late", ErrClientGone)

	// 手动把时间戳推回 TTL 之外（同包测试，直接改登记表）。
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

// 空 session_id 一律拒绝：否则只凭 run_id 就能停掉别人的运行（台账跨 agent 共享）。
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

// 管理端按 run_id 终止：无会话的运行（RPC / MCP 单轮）也要能停。
func TestStopRunByID(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer registerRun(entry("run-by-id", ""), cancel)()

	if !StopRunByID("run-by-id") {
		t.Fatal("StopRunByID returned false, want true")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrRunStopped) {
		t.Fatalf("cause = %v, want ErrRunStopped", cause)
	}
	// 幂等：再停一次返回 false
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
	// 再停一次：已全部结束
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
