package executors

import (
	"sync"

	flow "flame/nexus/service/flow/core"
	"flame/nexus/shared/runledger"
)

// flowRuns are the flow runs executing on this process (run_id -> execution context).
//
// The ledger (runledger) answers "on which machine"; this answers "how to stop it on this machine":
// ExecContext.Cancel cancels StdCtx, and the node loop, wait, sandbox scripts and the ctx handed in when
// an endpoint request disconnects all hang off the same chain, so one cancel starts winding the whole run
// down (the session record is stored as stopped).
var flowRuns = struct {
	mu    sync.Mutex
	byRun map[string]*flow.ExecContext
}{byRun: make(map[string]*flow.ExecContext)}

// registerLocalRun registers this run in the process-local stop table; it returns an idempotent
// unregister function.
func registerLocalRun(ec *flow.ExecContext) func() {
	if ec == nil || ec.GetRunID() == "" {
		return func() {}
	}
	runID := ec.GetRunID()
	flowRuns.mu.Lock()
	flowRuns.byRun[runID] = ec
	flowRuns.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			flowRuns.mu.Lock()
			delete(flowRuns.byRun, runID)
			flowRuns.mu.Unlock()
		})
	}
}

// StopRun cancels a flow run on this process. A false return means it is not here (or already finished).
func StopRun(runID string) bool {
	if runID == "" {
		return false
	}
	flowRuns.mu.Lock()
	ec := flowRuns.byRun[runID]
	flowRuns.mu.Unlock()
	if ec == nil {
		return false
	}
	ec.Cancel()
	return true
}

// handleRunStopSignal handles a cross-instance stop signal from Redis. Every instance receives the same
// signal, and instances not running that run simply ignore it.
func handleRunStopSignal(sig runledger.StopSignal) {
	if sig.Kind != "" && sig.Kind != runledger.KindFlow {
		return
	}
	StopRun(sig.RunID)
}

func init() {
	runledger.SubscribeStop(handleRunStopSignal)
}
