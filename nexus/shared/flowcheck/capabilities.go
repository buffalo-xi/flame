package flowcheck

// Capabilities is the part of a node type's declaration this package needs: which error codes it can produce, which
// on_error actions it supports and whether it supports a post block.
//
// It mirrors executors.NodeCapabilities as a **copy**, on purpose: the executors package imports service/agent/tools
// (flowtest.go), so a check package that imported executors would make tools -> flowcheck -> executors -> tools a cycle.
// executors/capabilities_drift_test.go asserts this table equals GetExecutor(...).Capabilities() for every registered
// type, so the copy cannot silently drift.
type Capabilities struct {
	ErrorCodes []string
	Actions    []string
	HasPost    bool
}

// AllErrorCodes is every code the runtime can produce (flow.AllErrorCodes), duplicated for the same reason and guarded
// by the same test.
var AllErrorCodes = []string{
	"timeout", "busy", "cancel", "config", "script", "capability", "var",
	"connection", "auth", "not_found", "internal", "other",
}

// capabilities is the declaration of every node type with an executor. A type missing here has nothing to check
// against (visual nodes, or a type the caller invented - the unknown-type finding covers that).
var capabilities = map[string]Capabilities{
	"llm": {
		ErrorCodes: []string{"config", "connection", "timeout", "not_found", "script", "busy", "var", "other"},
		Actions:    []string{"retry", "skip", "handler"},
		HasPost:    true,
	},
	"http": {
		ErrorCodes: []string{"config", "connection", "timeout", "auth", "not_found", "script", "busy", "var", "other"},
		Actions:    []string{"retry", "skip", "handler"},
		HasPost:    true,
	},
	"mcp": {
		ErrorCodes: []string{"config", "connection", "timeout", "auth", "not_found", "script", "busy", "var", "other"},
		Actions:    []string{"retry", "skip", "handler"},
		HasPost:    true,
	},
	"rpc": {
		ErrorCodes: []string{"config", "connection", "timeout", "auth", "not_found", "script", "busy", "var", "other"},
		Actions:    []string{"retry", "skip", "handler"},
		HasPost:    true,
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
		ErrorCodes: []string{"config", "connection", "timeout", "not_found", "var", "other"},
		Actions:    []string{"retry"},
	},
	"wait": {
		ErrorCodes: []string{"config", "timeout", "other"},
		Actions:    []string{"skip"},
	},
	"fork": {
		ErrorCodes: []string{"config", "other"},
	},
	"branch": {
		ErrorCodes: []string{"config", "script"},
	},
	"assign": {
		ErrorCodes: []string{"config", "script", "var"},
	},
	"memory": {
		ErrorCodes: []string{"config", "internal", "var"},
	},
	"result": {
		ErrorCodes: []string{"script", "capability", "timeout", "busy", "other"},
	},
	"progress": {
		ErrorCodes: []string{"script", "capability", "timeout", "busy", "other"},
	},
	// Containers: the child chain's codes pass through, so nothing is declared and nothing is validated.
	"loop":     {},
	"process":  {},
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
