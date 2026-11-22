package flows

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	flowruns "flame/nexus/shared/repository/flowruns"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// runRepo is the run-record repository (separate from the flow instance repo).
var runRepo = flowruns.NewRepository()

// runDetail is the display struct for a run: []byte fields become json.RawMessage so JSON does not serialise them as base64.
type runDetail struct {
	ID                   string          `json:"id"`
	FlowID               string          `json:"flow_id"`
	Status               string          `json:"status"`
	RunType              string          `json:"run_type"`
	Result               string          `json:"result"`
	ErrMsg               string          `json:"err_msg"`
	NodeCount            int             `json:"node_count"`
	DurationMs           int64           `json:"duration_ms"`
	StartedAt            xsql.Time       `json:"started_at"`
	FinishedAt           *xsql.Time      `json:"finished_at"`
	RunArgs              json.RawMessage `json:"run_args"`
	RunLimits            json.RawMessage `json:"run_limits"`
	RunFlowVars          json.RawMessage `json:"run_flow_vars"`
	RunFlowVarsTruncated bool            `json:"run_flow_vars_truncated"`
	RunFlowVarsSize      int             `json:"run_flow_vars_size"`
	Progress             json.RawMessage `json:"progress"`
	ProgressTruncated    bool            `json:"progress_truncated"`
	ProgressSize         int             `json:"progress_size"`
}

// nodeDetail is the display struct for a node record: variables and logs become friendly types, matching the debug node_snapshot frame.
type nodeDetail struct {
	ID                  string          `json:"id"`
	RunID               string          `json:"run_id"`
	NodeID              string          `json:"node_id"`
	ProcessID           string          `json:"process_id"`
	NodeType            string          `json:"node_type"`
	Status              string          `json:"status"`
	InputVars           json.RawMessage `json:"input_vars"`
	InputVarsTruncated  bool            `json:"input_vars_truncated"`
	InputVarsSize       int             `json:"input_vars_size"`
	OutputVars          json.RawMessage `json:"output_vars"`
	OutputVarsTruncated bool            `json:"output_vars_truncated"`
	OutputVarsSize      int             `json:"output_vars_size"`
	LocalVars           json.RawMessage `json:"local_vars"`
	LocalVarsTruncated  bool            `json:"local_vars_truncated"`
	LocalVarsSize       int             `json:"local_vars_size"`
	RunLogs             []string        `json:"run_logs"`
	RunLogsTruncated    bool            `json:"run_logs_truncated"`
	RunLogsLines        int             `json:"run_logs_lines"`
	ErrCode             string          `json:"err_code"`
	ErrMsg              string          `json:"err_msg"`
	ErrStack            string          `json:"err_stack"`
	RetryCount          int             `json:"retry_count"`
	ErrorAction         string          `json:"error_action"`
	StartTime           *xsql.Time      `json:"start_time"`
	EndTime             *xsql.Time      `json:"end_time"`
	DurationMs          int64           `json:"duration_ms"`
	Extra               json.RawMessage `json:"extra"` // result specific to the node type
}

func toRunDetail(r *models.FlowRunRecord) *runDetail {
	if r == nil {
		return nil
	}
	return &runDetail{
		ID:                   r.ID,
		FlowID:               r.FlowID,
		Status:               r.Status,
		RunType:              r.RunType,
		Result:               r.Result,
		ErrMsg:               r.ErrMsg,
		NodeCount:            r.NodeCount,
		DurationMs:           r.DurationMs,
		StartedAt:            r.StartedAt,
		FinishedAt:           r.FinishedAt,
		RunArgs:              json.RawMessage(r.RunArgs),
		RunLimits:            json.RawMessage(r.RunLimits),
		RunFlowVars:          json.RawMessage(r.RunFlowVars),
		RunFlowVarsTruncated: r.RunFlowVarsTruncated,
		RunFlowVarsSize:      r.RunFlowVarsSize,
		Progress:             json.RawMessage(r.Progress),
		ProgressTruncated:    r.ProgressTruncated,
		ProgressSize:         r.ProgressSize,
	}
}

func toNodeDetail(n *models.FlowNodeRecord) *nodeDetail {
	if n == nil {
		return nil
	}
	var logs []string
	if len(n.RunLogs) > 0 {
		_ = json.Unmarshal(n.RunLogs, &logs)
	}
	return &nodeDetail{
		ID:                  n.ID,
		RunID:               n.RunID,
		NodeID:              n.NodeID,
		ProcessID:           n.ProcessID,
		NodeType:            n.NodeType,
		Status:              n.Status,
		InputVars:           json.RawMessage(n.InputVars),
		InputVarsTruncated:  n.InputVarsTruncated,
		InputVarsSize:       n.InputVarsSize,
		OutputVars:          json.RawMessage(n.OutputVars),
		OutputVarsTruncated: n.OutputVarsTruncated,
		OutputVarsSize:      n.OutputVarsSize,
		LocalVars:           json.RawMessage(n.LocalVars),
		LocalVarsTruncated:  n.LocalVarsTruncated,
		LocalVarsSize:       n.LocalVarsSize,
		RunLogs:             logs,
		RunLogsTruncated:    n.RunLogsTruncated,
		RunLogsLines:        n.RunLogsLines,
		ErrCode:             n.ErrCode,
		ErrMsg:              n.ErrMsg,
		ErrStack:            n.ErrStack,
		RetryCount:          n.RetryCount,
		ErrorAction:         n.ErrorAction,
		StartTime:           n.StartTime,
		EndTime:             n.EndTime,
		DurationMs:          n.DurationMs,
		Extra:               json.RawMessage(n.Extra),
	}
}

// ListRuns lists the historical runs of a workflow.
// GET /admin/flows/:id/runs?limit=20&offset=0&run_type=debug
func ListRuns(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	flowId := c.Param("id")
	schema := authInfo.(*auth.AuthInfo).Schema

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// run_type filter (optional): debug / task / call; empty means no filter.
	runType := c.Query("run_type")

	// status filter (optional): success / failed / cancelled; empty means no filter.
	status := c.Query("status")

	// time range filter (RFC3339); empty means no filter.
	var startFrom, startTo *time.Time
	if v := c.Query("start_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			startFrom = &t
		}
	}
	if v := c.Query("start_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			startTo = &t
		}
	}

	// user_id is optional, like the other filters: it narrows this flow's runs to the ones a given user triggered.
	// session_id is not exposed here: this page lists one workflow's runs from the editor, where "which conversation"
	// is not a question it answers (the cross-flow history page does).
	records, total, err := runRepo.ListRuns(schema, flowId, runType, status, c.Query("user_id"), "", startFrom, startTo, limit, offset)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	items := make([]*runDetail, 0, len(records))
	for _, r := range records {
		items = append(items, toRunDetail(r))
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}, "")
}

// GetRunDetail returns one run together with all of its node records.
// GET /admin/flows/:id/runs/:runId
func GetRunDetail(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	runID := c.Param("runId")
	schema := authInfo.(*auth.AuthInfo).Schema

	run, nodes, err := runRepo.GetRunDetail(schema, runID)
	if err != nil {
		if err == sql.ErrNoRows {
			xhttp.ErrCode(c, "run.not_found", nil, "Run record not found", "")
			return
		}
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	detailNodes := make([]*nodeDetail, 0, len(nodes))
	for _, n := range nodes {
		detailNodes = append(detailNodes, toNodeDetail(n))
	}

	xhttp.OkJSON(c, "Success", gin.H{
		"run":   toRunDetail(run),
		"nodes": detailNodes,
	}, "")
}

// DeleteRun deletes one historical run together with its node records.
// DELETE /admin/flows/:id/runs/:runId
func DeleteRun(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	flowId := c.Param("id")
	runID := c.Param("runId")
	if flowId == "" || runID == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request", "")
		return
	}

	deleted, err := runRepo.DeleteRun(authInfo.(*auth.AuthInfo).Schema, flowId, runID)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	if deleted == 0 {
		xhttp.ErrCode(c, "run.not_found", nil, "Run record not found", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": deleted}, "")
}

// DeleteRuns clears every historical run of a workflow (debug and production alike).
// DELETE /admin/flows/:id/runs
func DeleteRuns(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	flowId := c.Param("id")
	if flowId == "" {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request", "")
		return
	}

	deleted, err := runRepo.DeleteRuns(authInfo.(*auth.AuthInfo).Schema, flowId)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"deleted": deleted}, "")
}
