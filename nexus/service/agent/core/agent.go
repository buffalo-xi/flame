package core

import "context"

// EventType is the type of an event emitted while an agent runs.
type EventType string

const (
	EventThinking   EventType = "thinking"    // model reasoning content
	EventText       EventType = "text"        // text delta (running commentary or the final answer)
	EventToolCall   EventType = "tool_call"   // a tool is starting
	EventToolResult EventType = "tool_result" // a tool finished
)

// Event is a structured event emitted while an agent runs, pushed to streaming consumers such as SSE.
// For text-like events (thinking / text) Delta is the incremental fragment, and the consumer joins the pieces.
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
	// Origin: filled when a child agent's event is forwarded to its parent (empty = this agent itself)
	Agent string `json:"agent,omitempty"`
	// Call depth (0 = the top-level agent)
	Depth int `json:"depth,omitempty"`
}

// Agent is the agent runtime interface.
//
// It keeps only the methods an external consumer really calls (consumers: the SSE endpoints under admin/agents,
// service/rpc/server, service/mcp/server, service/flow/executors).
// Everything internal to the implementation (setResult/setError/logInfo and the like) stays out of the interface - the smaller the
// interface, the lighter a second implementation (remote / container) will be to add.
//
// The convention matches flow.ExecContext: execution pushes intermediate events into the event channel, while the final result and error
// live on the instance; whether to stream is the consumer's decision (read the channel, or read the result only).
type Agent interface {
	// Stream runs the agent loop (LLM reasoning -> tool calls -> results fed back -> reason again).
	//
	// parent is only used for **cascading termination**: when it is cancelled the round exits immediately - which is how a call_agent
	// child stops together with its parent (previously a child instance started its own ctx from Background, so it kept running after
	// the parent was stopped).
	// A top-level entry passes context.Background(): termination is handled by runcontrol.go's StopRun / StopSession, which also has to
	// tell "the user asked" and "the client disconnected" apart.
	Stream(parent context.Context) error

	// Close releases resources (equivalent to CloseChans).
	Close() error
	// CloseChans closes the event channel.
	CloseChans() error

	// GetEventChan returns the structured event channel (thinking / text / tool calls).
	// It is the only streaming outlet: thinking and tool calls appear nowhere else.
	GetEventChan() chan Event

	// GetResult returns the final result.
	GetResult() string
	// GetDurationMs returns how long this round took.
	//
	// It is frozen when session_records is written, and the entry point uses it to render the seconds of "just finished" - it must be the
	// same value as the duration_ms replayed from history, otherwise the two numbers in one session disagree.
	GetDurationMs() int64
	// GetError returns the error from execution (an empty string means none).
	GetError() string

	// LogError records an error log line.
	LogError(message string)
}
