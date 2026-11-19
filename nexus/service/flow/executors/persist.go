package executors

import (
	"context"
	"encoding/json"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/models"
	flowruns "flame/nexus/shared/repository/flowruns"
	"flame/tools/xsql"
)

// persistRun stores the result of one run (the run plus its node records).
// A storage failure is only logged; it does not affect the outcome of the run itself.
func persistRun(execCtx *flow.ExecContext, flowErr error) {
	if execCtx == nil || execCtx.Flow == nil || execCtx.Flow.Instance == nil {
		return
	}
	flowId := execCtx.Flow.Instance.ID
	if flowId == "" {
		return
	}

	run := BuildRunRecord(execCtx, flowErr)
	nodes := buildNodeRecords(execCtx)

	maxRecords := execCtx.ResolveMaxRunRecords()

	// 0 = store nothing: no run record is kept at all
	if maxRecords == 0 {
		return
	}

	if err := flowruns.NewRepository().SaveRun(execCtx.Schema, run, nodes, maxRecords); err != nil {
		execCtx.LogError("failed to persist the run records: " + err.Error())
	}
}

// BuildRunRecord builds the run record from the execution context (shared by persistence and the debug
// "finished" frame).
func BuildRunRecord(ec *flow.ExecContext, flowErr error) *models.FlowRunRecord {
	status := "success"
	errMsg := ""
	switch {
	case flowErr != nil && (context.Canceled == flowErr || context.DeadlineExceeded == flowErr):
		status = "cancelled"
		errMsg = flowErr.Error()
	case flowErr != nil:
		status = "failed"
		errMsg = flowErr.Error()
	case ec.GetError() != "":
		status = "failed"
		errMsg = ec.GetError()
	}

	runType := runTypeOf(ec)

	all := ec.ExecutedProcessMap.GetAll()

	// node_count is deduplicated by processID + nodeID (retries merge; loop/branch/concurrent branches
	// each count once)
	seen := make(map[string]bool, len(all))
	nodeCount := 0
	var minStart, maxEnd time.Time
	for _, nr := range all {
		key := nr.ProcessID + "_" + nr.NodeID
		if !seen[key] {
			seen[key] = true
			nodeCount++
		}
		if minStart.IsZero() || nr.StartTime.Before(minStart) {
			minStart = nr.StartTime
		}
		if nr.EndTime.After(maxEnd) {
			maxEnd = nr.EndTime
		}
	}

	cost := int64(0)
	if !minStart.IsZero() && !maxEnd.IsZero() {
		cost = maxEnd.Sub(minStart).Milliseconds()
	}

	var startedAt xsql.Time
	var finishedAt *xsql.Time
	if !minStart.IsZero() {
		startedAt = xsql.Time(minStart)
	} else {
		// No node ran (a breakpoint stops on the very first node, say): fall back to the run's start
		// time, so a zero time cannot make trimExcess delete this record when ordering by started_at.
		startedAt = xsql.Time(ec.StartedAt)
	}
	if !maxEnd.IsZero() {
		t := xsql.Time(maxEnd)
		finishedAt = &t
	}

	// run_args: the arguments actually used this time, stored in full without truncation
	argsJSON, err := json.Marshal(ec.RunArguments)
	if err != nil || argsJSON == nil {
		argsJSON = []byte("{}")
	}

	// run_limits: the limits actually in force (scalars, not truncated)
	limits := ec.ResolveRunLimits()
	limitsJSON, _ := json.Marshal(limits)

	// run_flow_vars: the final flow variable values at the end of the run (truncated)
	fvJSON, fvTrunc, fvSize := flow.MarshalAndTruncate(ec.RunFlowVars, flow.MaxVarJSONBytes)

	// progress: the streamed messages of this run (grouped by category, the last N kept per category)
	pMsgs, pTrunc, pCnt := flow.TruncateTail(ec.ProgressMsgs, flow.MaxProgressEntries)
	rMsgs, rTrunc, rCnt := flow.TruncateTail(ec.ResultMsgs, flow.MaxProgressEntries)

	stream := map[string][]string{}
	if len(pMsgs) > 0 {
		stream["progress"] = pMsgs
	}
	if len(rMsgs) > 0 {
		stream["result"] = rMsgs
	}
	progressJSON, err := json.Marshal(stream)
	if err != nil || progressJSON == nil {
		progressJSON = []byte("{}")
	}
	progressTrunc := pTrunc || rTrunc
	progressSize := pCnt + rCnt

	// The list of drafts this run followed: a debug run works on a working copy whose content changes,
	// and this list is the only evidence of "which drafts it ran" afterwards. Empty for a real run.
	draftsJSON, err := json.Marshal(ec.DebugScope.Drafts())
	if err != nil || draftsJSON == nil {
		draftsJSON = []byte("[]")
	}

	return &models.FlowRunRecord{
		ID:                   ec.GetRunID(),
		FlowID:               ec.GetflowId(),
		Status:               status,
		RunType:              runType,
		Result:               ec.GetResult(),
		ErrMsg:               errMsg,
		NodeCount:            nodeCount,
		DurationMs:           cost,
		StartedAt:            startedAt,
		FinishedAt:           finishedAt,
		RunArgs:              argsJSON,
		RunLimits:            limitsJSON,
		RunFlowVars:          fvJSON,
		RunFlowVarsTruncated: fvTrunc,
		RunFlowVarsSize:      fvSize,
		Progress:             progressJSON,
		ProgressTruncated:    progressTrunc,
		ProgressSize:         progressSize,
		Drafts:               draftsJSON,
	}
}

// runTypeOf is where this run came from: debug is the editor's debugger (breakpoints, stepping), preview
// is a trial run such as run_flow (runs drafts without the debugger), and anything else is a real call.
// Telling them apart makes "did this run the draft or the released version" answerable later. The run
// ledger and persistence share this decision, so the list and the history never disagree.
func runTypeOf(ec *flow.ExecContext) string {
	switch {
	case ec.Debug != nil && ec.Debug.Enabled:
		return "debug"
	case ec.DebugScope.Enabled():
		return "preview"
	default:
		return "call"
	}
}

// buildNodeRecords builds the node records from the context's executed-node list.
// Within one run a node_id keeps only its last execution (the latest StartTime), so a loop or repeated
// execution cannot blow up the data volume. Failed nodes are deduplicated the same way.
func buildNodeRecords(ec *flow.ExecContext) []*models.FlowNodeRecord {
	all := ec.ExecutedProcessMap.GetAll()

	// Deduplicate by node_id, keeping the latest StartTime; in the same-nanosecond edge case the ID (UUID
	// v7, time-ordered) decides
	last := make(map[string]*flow.NodeResult, len(all))
	for _, nr := range all {
		if prev, ok := last[nr.NodeID]; !ok ||
			nr.StartTime.After(prev.StartTime) ||
			(nr.StartTime.Equal(prev.StartTime) && nr.ID > prev.ID) {
			last[nr.NodeID] = nr
		}
	}

	recs := make([]*models.FlowNodeRecord, 0, len(last))
	for _, nr := range last {
		recs = append(recs, buildNodeRecord(nr))
	}
	return recs
}

// buildNodeRecord turns one NodeResult into a node record, truncating the variables and logs.
func buildNodeRecord(nr *flow.NodeResult) *models.FlowNodeRecord {
	local, output := nr.SnapshotVars()
	inputs := nr.GetInputVars()
	logs := nr.GetLogs()

	ivJSON, ivTrunc, ivSize := flow.MarshalAndTruncate(inputs, flow.MaxVarJSONBytes)
	ovJSON, ovTrunc, ovSize := flow.MarshalAndTruncate(output, flow.MaxVarJSONBytes)
	lvJSON, lvTrunc, lvSize := flow.MarshalAndTruncate(local, flow.MaxVarJSONBytes)
	lgJSON, lgTrunc, lgLines := flow.MarshalLogsAndTruncate(logs, flow.MaxLogLines)

	var st, et *xsql.Time
	if !nr.StartTime.IsZero() {
		t := xsql.Time(nr.StartTime)
		st = &t
	}
	if !nr.EndTime.IsZero() {
		t := xsql.Time(nr.EndTime)
		et = &t
	}

	return &models.FlowNodeRecord{
		ID:                  nr.ID,
		RunID:               "", // filled in by SaveRun with run.ID
		NodeID:              nr.NodeID,
		ProcessID:           nr.ProcessID,
		NodeType:            string(nr.NodeType),
		Status:              string(nr.Status),
		InputVars:           ivJSON,
		InputVarsTruncated:  ivTrunc,
		InputVarsSize:       ivSize,
		OutputVars:          ovJSON,
		OutputVarsTruncated: ovTrunc,
		OutputVarsSize:      ovSize,
		LocalVars:           lvJSON,
		LocalVarsTruncated:  lvTrunc,
		LocalVarsSize:       lvSize,
		RunLogs:             lgJSON,
		RunLogsTruncated:    lgTrunc,
		RunLogsLines:        lgLines,
		ErrCode:             nr.ErrCode,
		ErrMsg:              nr.ErrMsg,
		ErrStack:            nr.ErrStack,
		RetryCount:          nr.RetryCount,
		ErrorAction:         string(nr.ErrorAction),
		StartTime:           st,
		EndTime:             et,
		DurationMs:          nr.DurationMs,
		Extra:               []byte(nr.BuildExtra()),
	}
}
