package executors

import (
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/models"
	sessionrecords "flame/nexus/shared/repository/sessionrecords"
)

var _ Executor = (*MemoryExecutor)(nil)

// MemoryExecutor is the memory node: it reads the current session's past inputs and outputs and emits
// messages (the conversation message array).
type MemoryExecutor struct{}

func (e *MemoryExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// Merge the output variables: the fixed messages one (conversation message format)
	outputs := make(map[string]interface{})
	if v, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
		for k, val := range v {
			outputs[k] = val
		}
	}
	if _, ok := outputs["messages"]; !ok {
		outputs["messages"] = map[string]interface{}{"type": "array[object]"}
	}
	nr.InitOutputVars(outputs)
	nr.InitOutputBindings(outputs)

	// No session_id or memory not allowed -> emit an empty array
	if ctx.SessionID == "" {
		_ = nr.SetOutputVarWithFire("messages", []map[string]interface{}{})
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	// Parse limit (<=0 falls back to max_session_records)
	limit := 0
	if v, ok := node.Data.Config["limit"].(float64); ok {
		limit = int(v)
	}
	if limit <= 0 {
		limit = ctx.ResolveMaxSessionRecords()
	}
	if limit <= 0 {
		_ = nr.SetOutputVarWithFire("messages", []map[string]interface{}{})
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	// Parse the window (only in effect when enabled and value>0)
	var since *time.Time
	if w, ok := node.Data.Config["window"].(map[string]interface{}); ok {
		if enabled, _ := w["enabled"].(bool); enabled {
			unit, _ := w["unit"].(string)
			value := 0
			if v, ok := w["value"].(float64); ok {
				value = int(v)
			}
			if d := timeWindowDuration(unit, value); d > 0 {
				t := time.Now().Add(-d)
				since = &t
			}
		}
	}

	records, err := sessionrecords.NewRepository().ListBySession(ctx.Schema, models.SessionOwnerTypeFlow, ctx.GetflowId(), ctx.SessionID, limit, since)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, "failed to load session memory: "+err.Error())
	}

	// Assemble the messages array (oldest first), splitting each record into a user and an assistant
	// message (the OpenAI messages format)
	messages := make([]map[string]interface{}, 0, len(records)*2)
	for _, rec := range records {
		ts := time.Time(rec.CreatedAt).Format(time.RFC3339)
		// user message: content is the full input JSON (raw jsonb bytes as a string, "{}" when empty)
		userContent := "{}"
		if len(rec.Input) > 0 {
			userContent = string(rec.Input)
		}
		messages = append(messages,
			map[string]interface{}{"role": "user", "content": userContent, "timestamp": ts},
			map[string]interface{}{"role": "assistant", "content": rec.Output, "timestamp": ts},
		)
	}

	if err := nr.SetOutputVarWithFire("messages", messages); err != nil {
		return failf(nr, flow.ErrCodeVar, err.Error())
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *MemoryExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes:   []string{flow.ErrCodeConfig, flow.ErrCodeInternal, flow.ErrCodeVar},
		HasPost:      false,
		FixedOutputs: []string{"messages"},
	}
}

func NewMemoryExecutor() *MemoryExecutor {
	return &MemoryExecutor{}
}

func init() {
	RegisterExecutor("memory", NewMemoryExecutor())
}

// timeWindowDuration converts a window unit plus value into a Duration; invalid or non-positive values
// return 0
func timeWindowDuration(unit string, value int) time.Duration {
	if value <= 0 {
		return 0
	}
	switch unit {
	case "second":
		return time.Duration(value) * time.Second
	case "minute":
		return time.Duration(value) * time.Minute
	case "hour":
		return time.Duration(value) * time.Hour
	case "day":
		return time.Duration(value) * 24 * time.Hour
	default:
		return 0
	}
}
