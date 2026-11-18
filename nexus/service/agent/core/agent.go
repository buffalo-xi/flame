package core

import "context"

// EventType agent 运行过程中的事件类型。
type EventType string

const (
	EventThinking   EventType = "thinking"    // 模型推理内容（reasoning）
	EventText       EventType = "text"        // 正文增量（过程说明或最终回答）
	EventToolCall   EventType = "tool_call"   // 开始执行工具
	EventToolResult EventType = "tool_result" // 工具执行完成
)

// Event agent 运行过程中的结构化事件，供 SSE 等流式消费方推送。
// 文本类事件（thinking/text）的 Delta 为增量片段，消费方需自行拼接。
type Event struct {
	Type EventType `json:"type"`
	// thinking / text
	Delta string `json:"delta,omitempty"`
	// tool_call / tool_result
	CallID     string `json:"call_id,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Args       string `json:"args,omitempty"`
	Result     string `json:"result,omitempty"`
	OK         bool   `json:"ok,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// 来源：子 agent 的事件转发给父级时填充（空 = 当前 agent 自身）
	Agent string `json:"agent,omitempty"`
	// 调用深度（0 = 顶层 agent）
	Depth int `json:"depth,omitempty"`
}

// Agent 智能体运行时接口。
//
// 只保留"外部消费方真正会调用"的方法（消费方：admin/agents 的 SSE、
// service/rpc/server、service/mcp/server、service/flow/executors）。
// 其余仅供实现内部使用的能力（如 setResult/setError/logInfo）不进接口，
// 接口越小，新增第二种实现（如 remote / container）时负担越轻。
//
// 与 flow.ExecContext 的约定一致：执行过程把中间事件推入事件 channel，
// 最终结果与错误存在实例上；是否需要流式由消费方决定（读 channel 或只读结果）。
type Agent interface {
	// Stream 执行 agent 循环（LLM 推理 → 工具调用 → 结果回填 → 再推理）。
	//
	// parent 只用于**级联终止**：它被取消时本轮立即退出 —— call_agent 的子智能体
	// 因此能随父一起停（此前子实例自己从 Background 起 ctx，父被停后子仍会跑完）。
	// 顶层入口传 context.Background()：终止由 runcontrol.go 的 StopRun / StopSession
	// 负责，那条路上还要区分「用户请求」与「客户端断开」两类原因。
	Stream(parent context.Context) error

	// Close 释放资源（等价于 CloseChans）。
	Close() error
	// CloseChans 关闭事件 channel。
	CloseChans() error

	// GetEventChan 返回结构化事件 channel（思考 / 正文 / 工具调用）。
	// 这是唯一的流式出口：思考与工具调用不体现在别处。
	GetEventChan() chan Event

	// GetResult 返回最终结果。
	GetResult() string
	// GetDurationMs 返回本轮执行耗时。
	//
	// 写 session_records 时冻结，入口用它渲染「刚跑完」的秒数 —— 与历史回显的
	// duration_ms 必须是同一个值，否则同一个会话里两个数会对不上。
	GetDurationMs() int64
	// GetError 返回执行过程中的错误（空串表示无错）。
	GetError() string

	// LogError 记录错误日志。
	LogError(message string)
}
