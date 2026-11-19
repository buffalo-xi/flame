package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"flame/nexus/shared/runledger"
)

// Stop reasons: carried through context.Cause so the upper layer can say something human instead of "context canceled".
var (
	// ErrRunStopped means the user / admin stopped it deliberately.
	ErrRunStopped = errors.New("run stopped by user")
	// ErrClientGone means the client disconnected (SSE connection closed / page closed).
	ErrClientGone = errors.New("client disconnected")
)

// runHandle is the cancel handle of one running run.
type runHandle struct {
	cancel    context.CancelCauseFunc
	sessionID string
	// cancelled means cancellation has been triggered. It makes "stop" idempotent: repeated calls return 0 instead of counting the
	// same batch of runs again.
	cancelled atomic.Bool
}

// runRegistry is the in-process ledger: run_id -> handle; session_id -> set of run_ids (secondary index).
//
// Why this table exists: Stream used to start from context.Background(), so the whole chain (LLM stream, tool execution) had no
// cancellation point. When a user closed the page or wanted to force a stop, all it could do was wait for the model to finish on its own -
// while exec / browser kept holding sandbox resources.
//
// The primary key is run_id (unique per call); the secondary index is session_id, because the UI only knows the session.
var runRegistry = struct {
	mu     sync.Mutex
	byRun  map[string]*runHandle
	bySess map[string]map[string]struct{}
}{
	byRun:  make(map[string]*runHandle),
	bySess: make(map[string]map[string]struct{}),
}

// registerRun registers a run and returns an idempotent unregister function (deferred by Stream when it ends).
func registerRun(entry runledger.Entry, cancel context.CancelCauseFunc) func() {
	if entry.RunID == "" || cancel == nil {
		return func() {}
	}
	runID, sessionID := entry.RunID, entry.SessionID
	h := &runHandle{cancel: cancel, sessionID: sessionID}

	runRegistry.mu.Lock()
	runRegistry.byRun[runID] = h
	if sessionID != "" {
		set := runRegistry.bySess[sessionID]
		if set == nil {
			set = make(map[string]struct{})
			runRegistry.bySess[sessionID] = set
		}
		set[runID] = struct{}{}
	}
	runRegistry.mu.Unlock()

	// Cross-instance ledger: write a "running" record and join the heartbeat (runledger merges per tenant internally).
	// Best effort: without Redis only memory is kept, which does not affect the run itself.
	unregisterLedger := runledger.Register(entry)

	// A stop may precede the registration: there is a window between the client disconnecting and Stream starting, and a StopRunCause in
	// it finds no handle. A matching pending stop cancels immediately and is marked cancelled (later repeats still return false).
	if cause := takePendingStop(runID, sessionID); cause != nil {
		h.cancelled.Store(true)
		cancel(cause)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			unregisterRun(runID, sessionID)
			unregisterLedger()
		})
	}
}

func unregisterRun(runID, sessionID string) {
	runRegistry.mu.Lock()
	defer runRegistry.mu.Unlock()
	delete(runRegistry.byRun, runID)
	if sessionID == "" {
		return
	}
	if set := runRegistry.bySess[sessionID]; set != nil {
		delete(set, runID)
		if len(set) == 0 {
			delete(runRegistry.bySess, sessionID)
		}
	}
}

// pendingStopTTL is the longest lifetime of a pending stop record.
//
// A stop may precede the registration (there is a window between the client disconnecting and Stream starting), so it has to be
// "noted down first and cancelled the moment it registers". But it must expire: otherwise a stop much later would kill a new round in the
// same session as if it were the one to stop immediately.
const pendingStopTTL = 10 * time.Second

// pendingStop is a stop request that has not matched yet. An empty runID = session level (applies to the run registered next in that
// session).
type pendingStop struct {
	runID     string
	sessionID string
	cause     error
	at        time.Time
}

var pendingStops = struct {
	mu  sync.Mutex
	all []pendingStop
}{}

// rememberPendingStop notes a stop request that matched nothing yet, for registerRun to consume.
//
// Memory only, alive for one TTL: it bridges the window of "registration vs stop ordering", not cross-process notification - with several
// instances, a run on another machine will never register here.
func rememberPendingStop(runID, sessionID string, cause error) {
	if sessionID == "" {
		return
	}
	now := time.Now()
	pendingStops.mu.Lock()
	defer pendingStops.mu.Unlock()
	pendingStops.all = prunePendingStops(pendingStops.all, now)
	pendingStops.all = append(pendingStops.all,
		pendingStop{runID: runID, sessionID: sessionID, cause: cause, at: now})
}

// takePendingStop takes away the pending stop matching this registration (consumed on take: one stop takes effect once).
func takePendingStop(runID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	now := time.Now()
	pendingStops.mu.Lock()
	defer pendingStops.mu.Unlock()
	pendingStops.all = prunePendingStops(pendingStops.all, now)
	for i, p := range pendingStops.all {
		if p.sessionID != sessionID {
			continue
		}
		// A run-level stop must match exactly; a session-level one (empty runID) applies to the newly registered run.
		if p.runID != "" && p.runID != runID {
			continue
		}
		pendingStops.all = append(pendingStops.all[:i], pendingStops.all[i+1:]...)
		return p.cause
	}
	return nil
}

// prunePendingStops drops expired records (the caller holds the lock).
func prunePendingStops(all []pendingStop, now time.Time) []pendingStop {
	kept := all[:0]
	for _, p := range all {
		if now.Sub(p.at) <= pendingStopTTL {
			kept = append(kept, p)
		}
	}
	return kept
}

// StopRunCause stops a run with an explicit reason (a client disconnect, say).
//
// sessionID is required: it is the anchor for "which runs to stop" and the only basis for the ownership check.
// An empty sessionID is always refused - the ledger is process-wide and cross-agent, so a run_id alone would be enough to stop someone
// else's run; this hole is not guarded in each caller but right here, where no entry can bypass it.
// Returning false means there is no matching running instance (already finished / wrong id / session mismatch / no session given).
func StopRunCause(runID, sessionID string, cause error) bool {
	if runID == "" || sessionID == "" {
		return false
	}
	if cause == nil {
		cause = ErrRunStopped
	}
	runRegistry.mu.Lock()
	h := runRegistry.byRun[runID]
	ok := h != nil && h.sessionID == sessionID
	if ok && h.cancelled.Swap(true) {
		ok = false // already stopped: idempotent
	}
	missing := h == nil
	runRegistry.mu.Unlock()
	if !ok {
		// This run is not registered yet (Stream just started), so note it down and cancel as soon as it registers.
		// Only "this run was not found" is noted: a session mismatch is a clear authorization failure and must not leave a pending stop;
		// a finished run never reuses its run_id either, so a record would only expire with the TTL.
		if missing {
			rememberPendingStop(runID, sessionID, cause)
		}
		return false
	}
	h.cancel(cause)
	return true
}

// StopRunByID stops a run exactly by run_id, without requiring the session.
//
// Only for the admin side's "run list -> stop": that run was read from this tenant's ledger first, so ownership is already cleared there.
// A session-less run (a single RPC / MCP round) has no session_id to pass and could never be stopped through StopRunCause, hence this
// separate entry rather than relaxing the "session is required" check for everyone.
func StopRunByID(runID string) bool {
	if runID == "" {
		return false
	}
	runRegistry.mu.Lock()
	h := runRegistry.byRun[runID]
	ok := h != nil
	if ok && h.cancelled.Swap(true) {
		ok = false // already stopped: idempotent
	}
	runRegistry.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel(ErrRunStopped)
	return true
}

// StopSession stops every running run under a session and returns how many were stopped.
// One session may have several runs at once (child agents, concurrent requests), so they are cancelled one by one.
func StopSession(sessionID string) int {
	if sessionID == "" {
		return 0
	}
	runRegistry.mu.Lock()
	handles := make([]*runHandle, 0, len(runRegistry.bySess[sessionID]))
	for id := range runRegistry.bySess[sessionID] {
		h := runRegistry.byRun[id]
		if h == nil || h.cancelled.Swap(true) {
			continue // skip the ones already stopped, so repeated calls return 0
		}
		handles = append(handles, h)
	}
	runRegistry.mu.Unlock()

	for _, h := range handles {
		h.cancel(ErrRunStopped)
	}
	return len(handles)
}

// RunningCount is the number of runs in flight (for observability and tests).
func RunningCount() int {
	runRegistry.mu.Lock()
	defer runRegistry.mu.Unlock()
	return len(runRegistry.byRun)
}

// handleRunStopSignal handles a cross-instance stop signal from Redis.
//
// Every instance receives the same signal, and only the instance holding that run matches it; the others simply lack it in their ledger
// (StopRunCause returns false) and ignore it - no "who is on which machine" routing needed.
func handleRunStopSignal(sig runledger.StopSignal) {
	if sig.Kind != "" && sig.Kind != runledger.KindAgent {
		return
	}
	if sig.RunID != "" {
		StopRunCause(sig.RunID, sig.SessionID, ErrRunStopped)
		return
	}
	// Session-level stop: an empty run_id stops by session (with several instances, each stops its own part)
	if sig.SessionID != "" {
		StopSession(sig.SessionID)
	}
}

func init() {
	runledger.SubscribeStop(handleRunStopSignal)
}

// cancelMessage turns a stop reason into a sentence for the user.
// The strings stay in Chinese on purpose: they are shown to the user, not diagnostics.
func cancelMessage(cause error) string {
	switch {
	case errors.Is(cause, ErrRunStopped):
		return "运行已终止（用户请求）"
	case errors.Is(cause, ErrClientGone):
		return "运行已终止（客户端断开）"
	case cause != nil:
		return "运行已终止：" + cause.Error()
	}
	return "运行已终止"
}
