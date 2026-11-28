package flow

import (
	"encoding/json"
	"sort"
	"time"
)

// Truncation thresholds, shared with persistence so that "debug pull" and "stored record" truncate alike.
const (
	// MaxVarJSONBytes is the maximum size of a variable JSON document; beyond it, top-level keys are dropped.
	MaxVarJSONBytes = 64 * 1024 // 64KB
	// MaxLogLines is the maximum number of log lines kept per node; beyond it, the last MaxLogLines lines are kept.
	MaxLogLines = 200
	// MaxProgressEntries is the maximum number of messages kept per category; beyond it, the last N are kept.
	MaxProgressEntries = 1000
)

// NodeSnapshot is the display snapshot of one node execution.
// The live debug push (node_inspect / node_snapshot) and the history query (persistence) share the same
// field structure, so "what the debugger saw" equals "what was stored".
type NodeSnapshot struct {
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
	PostError           string          `json:"post_error"`
	HandlerError        string          `json:"handler_error"`
	StartTime           *time.Time      `json:"start_time"`
	EndTime             *time.Time      `json:"end_time"`
	DurationMs          int64           `json:"duration_ms"`
	Extra               json.RawMessage `json:"extra"` // node-type specific result (branch/loop/function/http), null otherwise
}

// BuildSnapshot assembles a NodeResult into a display snapshot and truncates variables and logs.
// It shares the truncation helpers with the persistence path (buildNodeRecord), so both produce the same output.
func (nr *NodeResult) BuildSnapshot() *NodeSnapshot {
	local, output := nr.SnapshotVars()
	inputs := nr.GetInputVars()
	logs := nr.GetLogs()

	ivJSON, ivTrunc, ivSize := MarshalAndTruncate(inputs, MaxVarJSONBytes)
	ovJSON, ovTrunc, ovSize := MarshalAndTruncate(output, MaxVarJSONBytes)
	lvJSON, lvTrunc, lvSize := MarshalAndTruncate(local, MaxVarJSONBytes)
	lg, lgTrunc, lgLines := TruncateLogs(logs, MaxLogLines)

	var st, et *time.Time
	if !nr.StartTime.IsZero() {
		t := nr.StartTime
		st = &t
	}
	if !nr.EndTime.IsZero() {
		t := nr.EndTime
		et = &t
	}

	return &NodeSnapshot{
		NodeID:              nr.NodeID,
		ProcessID:           nr.ProcessID,
		NodeType:            string(nr.NodeType),
		Status:              string(nr.Status),
		InputVars:           json.RawMessage(ivJSON),
		InputVarsTruncated:  ivTrunc,
		InputVarsSize:       ivSize,
		OutputVars:          json.RawMessage(ovJSON),
		OutputVarsTruncated: ovTrunc,
		OutputVarsSize:      ovSize,
		LocalVars:           json.RawMessage(lvJSON),
		LocalVarsTruncated:  lvTrunc,
		LocalVarsSize:       lvSize,
		RunLogs:             lg,
		RunLogsTruncated:    lgTrunc,
		RunLogsLines:        lgLines,
		ErrCode:             nr.ErrCode,
		ErrMsg:              nr.ErrMsg,
		ErrStack:            nr.ErrStack,
		RetryCount:          nr.RetryCount,
		ErrorAction:         string(nr.ErrorAction),
		PostError:           nr.PostError,
		HandlerError:        nr.HandlerError,
		StartTime:           st,
		EndTime:             et,
		DurationMs:          nr.DurationMs,
		Extra:               nr.BuildExtra(),
	}
}

// BuildExtra serializes the node-type specific result (branch/loop/function/http); other nodes return null.
// Shared by persistence and the debug push so both stay consistent.
func (nr *NodeResult) BuildExtra() json.RawMessage {
	var extra map[string]any
	switch nr.NodeType {
	case "branch", "intent":
		if nr.BranchResult != nil {
			extra = map[string]any{"executed_branches": nr.BranchResult.GetExecutedBranches()}
		}
	case "loop":
		if nr.LoopResult != nil {
			extra = map[string]any{
				"loop":       nr.LoopResult.LoopConfig,
				"loop_vars":  nr.LoopResult.LoopVars,
				"iterations": nr.LoopResult.Iterations,
			}
		}
	case "function":
		if nr.FunctionResult != nil {
			extra = map[string]any{"param_vars": nr.FunctionResult.ParamVars}
		}
	case "http":
		if nr.HttpResult != nil {
			extra = map[string]any{
				"url":     nr.HttpResult.URL,
				"method":  nr.HttpResult.Method,
				"headers": nr.HttpResult.Headers,
				"status":  nr.HttpResult.Status,
			}
		}
	}
	if extra == nil {
		return json.RawMessage("null") // JSON null: Go nil would make pq serialize an empty string and break jsonb
	}
	b, _, _ := MarshalAndTruncate(extra, MaxVarJSONBytes)
	return json.RawMessage(b)
}

// MarshalAndTruncate serializes a map to JSON bytes; beyond maxBytes it drops top-level keys one by one
// (lexicographic order) until the size is <= maxBytes. Returns (jsonBytes, truncated, originalSize).
// originalSize is always the full size before truncation; truncated reports whether anything was dropped.
func MarshalAndTruncate(vars map[string]any, maxBytes int) ([]byte, bool, int) {
	if len(vars) == 0 {
		return []byte("{}"), false, 0
	}

	full, err := json.Marshal(vars)
	if err != nil {
		return []byte("{}"), false, 0
	}
	origSize := len(full)
	if origSize <= maxBytes {
		return full, false, origSize
	}

	// Over the threshold: drop keys from the end of the lexicographic order until it fits.
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	keep := make(map[string]any, len(vars))
	for k, v := range vars {
		keep[k] = v
	}

	for i := len(keys) - 1; i >= 0 && len(keep) > 0; i-- {
		delete(keep, keys[i])
		b, err := json.Marshal(keep)
		if err == nil && len(b) <= maxBytes {
			return b, true, origSize
		}
	}

	// Extreme case: still over the limit after dropping everything (a single value exceeds it) — fall back to {}.
	return []byte("{}"), true, origSize
}

// TruncateLogs truncates a log slice; beyond maxLines it keeps the last maxLines lines.
// Returns (kept, truncated, originalLines), where originalLines is the original total.
func TruncateLogs(logs []string, maxLines int) ([]string, bool, int) {
	origLines := len(logs)
	if origLines <= maxLines {
		return logs, false, origLines
	}
	return logs[origLines-maxLines:], true, origLines
}

// MarshalLogsAndTruncate truncates a log slice and serializes it as a JSON array.
// Returns (jsonBytes, truncated, originalLines); used for persistence (FlowNodeRecord.RunLogs is a []byte).
func MarshalLogsAndTruncate(logs []string, maxLines int) ([]byte, bool, int) {
	kept, truncated, origLines := TruncateLogs(logs, maxLines)
	if origLines == 0 {
		return []byte("[]"), false, 0
	}
	b, _ := json.Marshal(kept)
	return b, truncated, origLines
}

// TruncateTail truncates a message slice; beyond maxEntries it keeps the last maxEntries entries.
// Returns (kept, truncated, originalCount).
func TruncateTail(msgs []string, maxEntries int) ([]string, bool, int) {
	orig := len(msgs)
	if orig <= maxEntries {
		return msgs, false, orig
	}
	return msgs[orig-maxEntries:], true, orig
}
