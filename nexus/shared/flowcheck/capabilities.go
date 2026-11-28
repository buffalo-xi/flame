package flowcheck

// Capabilities is the part of a node type's declaration this package needs: which error codes it can produce, which
// on_error actions it supports, whether it supports a post block and which output variables it writes itself.
//
// It mirrors executors.NodeCapabilities as a **copy**, on purpose: the executors package imports service/agent/tools
// (flowtest.go), so a check package that imported executors would make tools -> flowcheck -> executors -> tools a cycle.
// executors/capabilities_drift_test.go asserts this table equals GetExecutor(...).Capabilities() for every registered
// type, so the copy cannot silently drift.
type Capabilities struct {
	ErrorCodes   []string
	Actions      []string
	HasPost      bool
	FixedOutputs []string
}

// AllErrorCodes is every code the runtime can produce (flow.AllErrorCodes), duplicated for the same reason and guarded
// by the same test.
var AllErrorCodes = []string{
	"timeout", "busy", "cancel", "config", "script", "capability", "var",
	"connection", "auth", "not_found", "internal", "other",
}

// RaisableCodes are the codes a raise node may carry: every global code except the three the runtime treats as the
// platform's own. config / internal bypass on_error entirely, and cancel means the user stopped the run - raising it
// would record a failure whose code claims a cancellation that never happened.
//
// Duplicated from executors.IsRaisableCode for the same cycle reason as AllErrorCodes above, and guarded by the same
// drift test.
var RaisableCodes = []string{
	"timeout", "busy", "script", "capability", "var", "connection", "auth", "not_found", "other",
}

// IsRaisableCode reports whether a raise node may carry code.
func IsRaisableCode(code string) bool { return contains(RaisableCodes, code) }

// capabilities is the declaration of every node type with an executor. A type missing here has nothing to check
// against (visual nodes, or a type the caller invented - the unknown-type finding covers that).
var capabilities = map[string]Capabilities{
	"llm": {
		ErrorCodes:   []string{"config", "connection", "timeout", "not_found", "script", "busy", "var", "other"},
		Actions:      []string{"retry", "skip", "handler"},
		HasPost:      true,
		FixedOutputs: []string{"content", "reasoning_content", "tool_calls"},
	},
	"http": {
		ErrorCodes:   []string{"config", "connection", "timeout", "auth", "not_found", "script", "busy", "var", "other"},
		Actions:      []string{"retry", "skip", "handler"},
		HasPost:      true,
		FixedOutputs: []string{"body", "headers", "mode", "sse_events", "status", "stream_finished"},
	},
	"mcp": {
		ErrorCodes:   []string{"config", "connection", "timeout", "auth", "not_found", "script", "busy", "var", "other"},
		Actions:      []string{"retry", "skip", "handler"},
		HasPost:      true,
		FixedOutputs: []string{"error", "progress", "result"},
	},
	"rpc": {
		ErrorCodes:   []string{"config", "connection", "timeout", "auth", "not_found", "script", "busy", "var", "other"},
		Actions:      []string{"retry", "skip", "handler"},
		HasPost:      true,
		FixedOutputs: []string{"error", "progress", "result"},
	},
	"code": {
		ErrorCodes: []string{"script", "capability", "var", "timeout", "busy", "other"},
		Actions:    []string{"skip", "retry", "handler"},
		HasPost:    true,
	},
	"call": {
		// Undeclared: every child-chain code passes through and the global codes apply.
		ErrorCodes: nil,
		Actions:    []string{"retry", "skip", "handler"},
		HasPost:    true,
	},
	"intent": {
		ErrorCodes:   []string{"config", "connection", "timeout", "not_found", "var", "other"},
		Actions:      []string{"retry"},
		FixedOutputs: []string{"index", "intent"},
	},
	"branch": {
		ErrorCodes: []string{"config", "script"},
	},
	"assign": {
		ErrorCodes: []string{"config", "script", "var"},
	},
	"raise": {
		// Undeclared: the author picks the code, so every global code passes through and none is validated here. No
		// actions and no post, for the same reason branch / assign declare none - the failure is authored, so the catch
		// point is the enclosing host or the call node, never this node.
		ErrorCodes: nil,
	},
	"memory": {
		ErrorCodes:   []string{"config", "internal", "var"},
		FixedOutputs: []string{"messages"},
	},
	"result": {
		ErrorCodes:   []string{"script", "capability", "timeout", "busy", "other"},
		FixedOutputs: []string{"output"},
	},
	"progress": {
		ErrorCodes:   []string{"script", "capability", "timeout", "busy", "other"},
		FixedOutputs: []string{"output"},
	},
	// Containers: the child chain's codes pass through, so nothing is declared and nothing is validated. The two hosts
	// accept skip / handler on their own failure (a member failure fails the host): skip lets the main chain continue past
	// an optional concurrent section, handler rewrites the error or turns it into a skip. There is no retry - re-running
	// the body would repeat every member's side effects - and function declares nothing, because its failure belongs to the
	// call node that invoked it. Both hosts accept post: it is the only way a skipped host can leave a marker behind, since
	// a handler script cannot write outputs.
	// A loop's iterations is the one readable name it writes itself: the run record stores it as the node's extra, and
	// NodeResult.GetVariable resolves it (the exact round count, including "no round" and "cut short by break / exit").
	"loop":     {Actions: []string{"skip", "handler"}, HasPost: true, FixedOutputs: []string{"iterations"}},
	"process":  {Actions: []string{"skip", "handler"}, HasPost: true},
	"function": {},
	// Control signals can never fail.
	"break":    {},
	"continue": {},
	"return":   {},
	"exit":     {},
}

// CapabilitiesOf returns the declaration of a node type. ok is false for a type this package does not know (a visual
// node, or an unknown one).
func CapabilitiesOf(nodeType string) (Capabilities, bool) {
	c, ok := capabilities[nodeType]
	return c, ok
}

// HasAction reports whether the type declares an on_error action.
func (c Capabilities) HasAction(action string) bool { return contains(c.Actions, action) }

// ProducesCode reports whether the type declares an error code. An empty declaration means "no validation": the codes
// of the child chain pass through (containers) or every global code applies (call).
func (c Capabilities) ProducesCode(code string) bool {
	if len(c.ErrorCodes) == 0 {
		return true
	}
	return contains(c.ErrorCodes, code)
}
