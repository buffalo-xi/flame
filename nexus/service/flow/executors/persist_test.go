package executors

import (
	"context"
	"errors"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/models"
)

// The status stored for a run is the three-way split the history UI relies on: a cancellation is not a failure,
// and an error recorded on the context (a script calling wf_set_error) counts even without a returned error.
func TestBuildRunRecordStatus(t *testing.T) {
	cases := []struct {
		name     string
		flowErr  error
		ctxError string
		want     string
		wantMsg  string
	}{
		{"no error", nil, "", "success", ""},
		{"cancelled", context.Canceled, "", "cancelled", "context canceled"},
		{"deadline", context.DeadlineExceeded, "", "cancelled", "context deadline exceeded"},
		{"failure", errors.New("node failed"), "", "failed", "node failed"},
		{"error on the context only", nil, "script called wf_set_error", "failed", "script called wf_set_error"},
		// Cancellation wins: the context error is often the cancellation itself, and reporting failed would
		// suggest the flow's logic is broken.
		{"cancellation beats the ctx error", context.Canceled, "also set", "cancelled", "context canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ec := newPersistContext()
			if tc.ctxError != "" {
				ec.Error = tc.ctxError
			}
			rec := BuildRunRecord(ec, tc.flowErr)
			if rec.Status != tc.want {
				t.Errorf("status = %q, want %q", rec.Status, tc.want)
			}
			if rec.ErrMsg != tc.wantMsg {
				t.Errorf("ErrMsg = %q, want %q", rec.ErrMsg, tc.wantMsg)
			}
		})
	}
}

// node_count is deduplicated by process + node (a retry or a loop body counts once), while the duration spans the
// earliest start to the latest end.
func TestBuildRunRecordCountsNodesAndDuration(t *testing.T) {
	ec := newPersistContext()
	base := time.Now().Add(-2 * time.Minute)

	first := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p1", ec)
	first.StartTime = base
	first.EndTime = base.Add(time.Second)
	retry := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p1", ec)
	retry.StartTime = base.Add(2 * time.Second)
	retry.EndTime = base.Add(3 * time.Second)
	otherProcess := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p2", ec)
	otherProcess.StartTime = base.Add(4 * time.Second)
	otherProcess.EndTime = base.Add(5 * time.Second)

	for _, nr := range []*flow.NodeResult{first, retry, otherProcess} {
		ec.ExecutedProcessMap.Append(nr.ProcessID, nr)
	}

	rec := BuildRunRecord(ec, nil)
	if rec.NodeCount != 2 {
		t.Errorf("NodeCount = %d, want 2 (the retry merges, the other process does not)", rec.NodeCount)
	}
	if rec.DurationMs != 5000 {
		t.Errorf("DurationMs = %d, want 5000", rec.DurationMs)
	}
	if got := time.Time(rec.StartedAt); !got.Equal(base) {
		t.Errorf("StartedAt = %v, want the earliest node start %v", got, base)
	}
	if rec.FinishedAt == nil || !time.Time(*rec.FinishedAt).Equal(base.Add(5*time.Second)) {
		t.Errorf("FinishedAt = %v, want the latest node end", rec.FinishedAt)
	}
}

// With no node recorded the run still gets a start time (the run's own), so ordering by started_at cannot drop the
// record — and there is no end time to report.
func TestBuildRunRecordWithoutNodes(t *testing.T) {
	ec := newPersistContext()
	rec := BuildRunRecord(ec, errors.New("stopped before the first node"))

	if rec.NodeCount != 0 || rec.DurationMs != 0 {
		t.Errorf("NodeCount = %d DurationMs = %d, want zeroes", rec.NodeCount, rec.DurationMs)
	}
	if got := time.Time(rec.StartedAt); !got.Equal(ec.StartedAt) {
		t.Errorf("StartedAt = %v, want the run start %v", got, ec.StartedAt)
	}
	if rec.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil", rec.FinishedAt)
	}
}

// The run type answers "did this run the draft or the released version": debugger, trial run, real call.
func TestRunTypeOf(t *testing.T) {
	ec := newExecContext()
	if got := runTypeOf(ec); got != "call" {
		t.Errorf("runTypeOf(plain) = %q, want call", got)
	}

	ec.DebugScope = flow.NewDebugScope(true)
	if got := runTypeOf(ec); got != "preview" {
		t.Errorf("runTypeOf(scope) = %q, want preview", got)
	}

	ec.Debug = &flow.DebugControl{Enabled: true}
	if got := runTypeOf(ec); got != "debug" {
		t.Errorf("runTypeOf(debug) = %q, want debug", got)
	}

	// A debugger that is loaded but not enabled does not make the run a debug run.
	ec.Debug = &flow.DebugControl{Enabled: false}
	if got := runTypeOf(ec); got != "preview" {
		t.Errorf("runTypeOf(disabled debugger) = %q, want preview", got)
	}
}

// Node records keep the last execution per node_id (a loop body would otherwise write one row per round), while
// every other node keeps its own row.
func TestBuildNodeRecordsKeepsLatestPerNodeID(t *testing.T) {
	ec := newPersistContext()
	base := time.Now().Add(-time.Minute)

	older := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p1", ec)
	older.StartTime = base
	newer := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p1", ec)
	newer.StartTime = base.Add(time.Second)
	other := flow.NewNodeResult(&flow.Node{ID: "n2", Type: "assign"}, "p1", ec)
	other.StartTime = base.Add(2 * time.Second)

	for _, nr := range []*flow.NodeResult{older, newer, other} {
		ec.ExecutedProcessMap.Append(nr.ProcessID, nr)
	}

	recs := buildNodeRecords(ec)
	if len(recs) != 2 {
		t.Fatalf("got %d node records, want 2", len(recs))
	}
	byNode := map[string]*models.FlowNodeRecord{}
	for _, rec := range recs {
		byNode[rec.NodeID] = rec
	}
	if byNode["n1"].ID != newer.ID {
		t.Errorf("n1 kept %s, want the latest execution %s", byNode["n1"].ID, newer.ID)
	}
	if _, ok := byNode["n2"]; !ok {
		t.Error("n2 is missing: a different node must keep its own record")
	}
}

// One node record carries the status, the error, the counters and the snapshot variables of the run.
func TestBuildNodeRecord(t *testing.T) {
	ec := newPersistContext()
	nr := flow.NewNodeResult(&flow.Node{ID: "n1", Type: "code"}, "p1", ec)
	nr.InitOutputVars(map[string]any{"out": map[string]any{"type": "string"}})
	if err := nr.SetOutputVar("out", "value"); err != nil {
		t.Fatalf("SetOutputVar = %v", err)
	}
	nr.RetryCount = 2
	nr.ErrCode = flow.ErrCodeScript
	nr.ErrMsg = "boom"
	nr.Finish(flow.StatusFailed, "", "")
	nr.EndTime = nr.StartTime.Add(150 * time.Millisecond)
	nr.DurationMs = 150

	rec := buildNodeRecord(nr)
	if rec.NodeID != "n1" || rec.NodeType != "code" || rec.ProcessID != "p1" {
		t.Errorf("identity = %s/%s/%s, want n1/code/p1", rec.NodeID, rec.NodeType, rec.ProcessID)
	}
	if rec.Status != string(flow.StatusFailed) {
		t.Errorf("Status = %q, want failed", rec.Status)
	}
	if rec.ErrCode != flow.ErrCodeScript || rec.ErrMsg != "boom" {
		t.Errorf("error = %q/%q, want script/boom", rec.ErrCode, rec.ErrMsg)
	}
	if rec.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2", rec.RetryCount)
	}
	if rec.DurationMs != 150 {
		t.Errorf("DurationMs = %d, want 150", rec.DurationMs)
	}
	if rec.StartTime == nil || rec.EndTime == nil {
		t.Error("the times were not copied")
	}
	if rec.OutputVarsTruncated {
		t.Error("a tiny output was marked truncated")
	}
	if len(rec.OutputVars) == 0 || string(rec.OutputVars) == "null" {
		t.Errorf("OutputVars = %s, want the snapshot", rec.OutputVars)
	}
	if rec.RunID != "" {
		t.Errorf("RunID = %q, want empty: SaveRun fills it in", rec.RunID)
	}
}

// persistRun has to bail out before touching the repository for every "nothing to store" case.
func TestPersistRunEarlyReturns(t *testing.T) {
	persistRun(nil, nil)

	noFlow := newPersistContext()
	noFlow.Flow = nil
	persistRun(noFlow, nil)

	noInstance := newPersistContext()
	noInstance.Flow = &flow.Flow{}
	persistRun(noInstance, nil)

	noID := newPersistContext()
	noID.Flow = &flow.Flow{Instance: &models.Flow{}}
	persistRun(noID, nil)
}

// newPersistContext is the scaffolding a run record needs: the run's own identity, an empty execution list and the
// run start time.
func newPersistContext() *flow.ExecContext {
	ec := newExecContext()
	ec.ExecutedProcessMap = flow.NewProcessExecMap()
	ec.ExecutedNodeMap = flow.NewNodeExecMap()
	ec.StartedAt = time.Now().Add(-time.Minute)
	ec.Flow = &flow.Flow{Instance: &models.Flow{ID: "f1", SessionMemory: true}}
	return ec
}
