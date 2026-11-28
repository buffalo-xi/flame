package flowcheck

// inputsIgnoringTypes are the node types that never read config.inputs.
//
// The key is worth this list because it is easy to write and impossible to notice afterwards: the executor does not read it,
// the run passes, and the author believes values were handed in.
//
// Why each one ignores it:
//   - process: the body chain reads the outer nodes' outputs directly (a source_type: node reference resolves through the
//     process chain, see ExecContext.FindNodeResultByProcessChain) and writes back through targets - the container is not a
//     call boundary, so there is nothing to hand to it;
//   - function: parameters arrive through a call node's args;
//   - call: argument values go in config.args;
//   - memory: it reads no input at all;
//   - control signals: break / continue / return / exit carry nothing.
var inputsIgnoringTypes = map[string]bool{
	"process":  true,
	"function": true,
	"call":     true,
	"memory":   true,
	"break":    true,
	"continue": true,
	"return":   true,
	"exit":     true,
}

// inputsIgnoredReason is the one-line "why", echoed back by the tools next to the removal and mirrored in the finding's
// wording. English, like every other model-visible string.
var inputsIgnoredReason = map[string]string{
	"process": "a process body reads the outer nodes' outputs directly and writes back through targets, so the container " +
		"itself reads no inputs",
	"function": "a function's parameters arrive through a call node's args",
	"call":     "argument values go in config.args",
	"memory":   "a memory node reads no inputs",
	"break":    "a control signal carries no input",
	"continue": "a control signal carries no input",
	"return":   "a control signal carries no input",
	"exit":     "a control signal carries no input",
}

// IgnoresInputs reports whether typ has no consumer for config.inputs.
func IgnoresInputs(typ string) bool {
	return inputsIgnoringTypes[typ]
}

// InputsIgnoredReason returns why typ ignores config.inputs (an empty string for a type that reads it).
func InputsIgnoredReason(typ string) string {
	return inputsIgnoredReason[typ]
}
