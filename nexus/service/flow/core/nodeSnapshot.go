package flow

import (
	"encoding/json"
	"sort"
	"time"
)

// 截断阈值：与落库共用，保证「调试拉取」与「落库存储」的截断行为完全一致。
const (
	// MaxVarJSONBytes 变量 JSON 的最大字节数，超过则按顶层 key 截断。
	MaxVarJSONBytes = 64 * 1024 // 64KB
	// MaxLogLines 单节点日志最大保留行数，超过则保留最后 MaxLogLines 行。
	MaxLogLines = 200
	// MaxProgressEntries 每个 category 最多保留的消息条数，超过则保留最后 N 条。
	MaxProgressEntries = 1000
)

// NodeSnapshot 单个节点执行数据的展示快照。
// 调试实时推送（node_inspect / node_snapshot）与历史查询（落库）共用同一字段结构，
// 保证「调试看到的」与「落库存的」一致。
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
	StartTime           *time.Time      `json:"start_time"`
	EndTime             *time.Time      `json:"end_time"`
	DurationMs          int64           `json:"duration_ms"`
	Extra               json.RawMessage `json:"extra"` // 节点类型特有的运行结果（branch/loop/function/http），其他为 null
}

// BuildSnapshot 将 NodeResult 组装为展示快照，并对变量/日志做截断。
// 与落库 buildNodeRecord 共用同一套截断函数，保证两处产出一致。
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
		StartTime:           st,
		EndTime:             et,
		DurationMs:          nr.DurationMs,
		Extra:               nr.BuildExtra(),
	}
}

// BuildExtra 序列化节点类型特有的运行结果（branch/loop/function/http），其他节点返回 nil。
// 供落库与调试推送共用，保证两处一致。
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
		return json.RawMessage("null") // JSON null；不能用 Go nil，否则 pq 序列化成空字符串导致 jsonb 报错
	}
	b, _, _ := MarshalAndTruncate(extra, MaxVarJSONBytes)
	return json.RawMessage(b)
}

// MarshalAndTruncate 将 map 序列化为 JSON 字节，超过 maxBytes 时按顶层 key（字典序）逐个剔除，
// 直到字节数 <= maxBytes。返回 (jsonBytes, truncated, originalSize)。
// originalSize 始终为截断前的完整字节数；truncated 标记是否发生了截断。
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

	// 超阈值：按 key 字典序从后往前逐个删除，直到达标。
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

	// 极端情况：删到空仍超限（单个 value 本身超阈值），退化为空对象。
	return []byte("{}"), true, origSize
}

// TruncateLogs 将日志切片截断，超过 maxLines 行时保留最后 maxLines 行。
// 返回 (kept, truncated, originalLines)。originalLines 为原始总行数。
func TruncateLogs(logs []string, maxLines int) ([]string, bool, int) {
	origLines := len(logs)
	if origLines <= maxLines {
		return logs, false, origLines
	}
	return logs[origLines-maxLines:], true, origLines
}

// MarshalLogsAndTruncate 将日志切片截断并序列化为 JSON 数组。
// 返回 (jsonBytes, truncated, originalLines)。用于落库（FlowNodeRecord.RunLogs 为 []byte）。
func MarshalLogsAndTruncate(logs []string, maxLines int) ([]byte, bool, int) {
	kept, truncated, origLines := TruncateLogs(logs, maxLines)
	if origLines == 0 {
		return []byte("[]"), false, 0
	}
	b, _ := json.Marshal(kept)
	return b, truncated, origLines
}

// TruncateTail 截断消息切片，超过 maxEntries 时保留最后 maxEntries 条。
// 返回 (kept, truncated, originalCount)。
func TruncateTail(msgs []string, maxEntries int) ([]string, bool, int) {
	orig := len(msgs)
	if orig <= maxEntries {
		return msgs, false, orig
	}
	return msgs[orig-maxEntries:], true, orig
}
