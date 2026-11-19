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

// persistRun 将一次运行的结果落库（run + 节点记录）。
// 落库失败仅记录日志，不影响主流程执行结果。
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

	// 0 = 不存储：不保留任何运行记录
	if maxRecords == 0 {
		return
	}

	if err := flowruns.NewRepository().SaveRun(execCtx.Schema, run, nodes, maxRecords); err != nil {
		execCtx.LogError("failed to persist the run records: " + err.Error())
	}
}

// BuildRunRecord 从执行上下文构建运行记录（落库与调试 finished 帧共用）。
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

	// node_count 去重：按 processID + nodeID 去重（retry 合并，loop/branch/并发各算一次）
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
		// 无节点执行（如断点挂在第一个节点就 stop）：用运行启动时间兜底，
		// 避免 zero time 导致 trimExcess 按 started_at 排序时把本条记录误删。
		startedAt = xsql.Time(ec.StartedAt)
	}
	if !maxEnd.IsZero() {
		t := xsql.Time(maxEnd)
		finishedAt = &t
	}

	// run_args：本次实际入参，完整存储不截断
	argsJSON, err := json.Marshal(ec.RunArguments)
	if err != nil || argsJSON == nil {
		argsJSON = []byte("{}")
	}

	// run_limits：实际生效限额（标量，不截断）
	limits := ec.ResolveRunLimits()
	limitsJSON, _ := json.Marshal(limits)

	// run_flow_vars：运行结束时流程变量最终值（截断）
	fvJSON, fvTrunc, fvSize := flow.MarshalAndTruncate(ec.RunFlowVars, flow.MaxVarJSONBytes)

	// progress：本次流式消息（按 category 分组，每类保留最后 N 条）
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

	// 本次跟随过的草稿清单：调试跑的是工作副本，内容会变，
	// 这份清单是事后复现「当时跑的是哪几份」的唯一凭据。正式运行为空数组。
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

// runTypeOf 本次运行的来源：debug 是编辑页调试器（可断点/单步），preview 是 run_flow
// 这类试跑（跑草稿但没有调试器），其余是正式调用。区分它们是为了让「这次跑的是草稿还是
// 正式版」可查。运行台账与落库共用同一套判定，避免列表与历史各说各话。
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

// buildNodeRecords 从执行上下文的已执行节点列表构建节点记录。
// 单次 run 内，每个 node_id 只保留最后执行（StartTime 最晚）的一条记录，
// 避免循环/重复执行导致数据量爆炸。失败节点同样去重。
func buildNodeRecords(ec *flow.ExecContext) []*models.FlowNodeRecord {
	all := ec.ExecutedProcessMap.GetAll()

	// 按 node_id 去重，保留 StartTime 最晚的一条；同纳秒极端情况下用 ID（UUID v7 时间有序）兜底
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

// buildNodeRecord 将单个 NodeResult 转为节点记录，并对变量/日志做截断。
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
		RunID:               "", // 由 SaveRun 内部用 run.ID 回填
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
