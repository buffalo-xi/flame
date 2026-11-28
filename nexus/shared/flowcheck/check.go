// Package flowcheck statically checks a workflow definition.
//
// The same rule set is used by three call sites that used to disagree: the agent tools (create_flow / create_node warning
// about incomplete config), the save boundary (duplicate labels, node quota) and the validate_flow tool. Keeping one
// implementation is the whole point: a second opinion is how "the check passed but the run failed" becomes normal.
//
// Scope is static and side-effect free: no network, no model, no script execution. Anything that needs real data (a
// connection being enabled, a template rendering without <no value>, the JSON-Schema arguments) belongs to run_flow.
//
// Severity is deliberate: Error means the definition cannot run as written, Warning means it will run but probably not
// do what the author meant. Flagging something that actually works erodes trust faster than missing something, so a
// Warning is only used where the executor really does nothing (an empty children list, an unreachable node).
package flowcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"flame/nexus/shared/flowdoc"
)

// Severity levels of a Finding.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Finding is one problem found in a workflow.
//
// NodeID and Path make it actionable: the caller can go straight to update_node(config_patch=...) instead of guessing
// which node or which key is wrong.
type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	NodeID   string `json:"node_id,omitempty"`
	NodeType string `json:"node_type,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
}

// Known finding codes. Stable strings, so a client can key behaviour on them.
const (
	CodeShape              = "shape"               // a config block has the wrong JSON type (refused at creation)
	CodeIncomplete         = "incomplete"          // a required key is missing: the step fails at run time
	CodeOutputs            = "outputs"             // a code step declares no outputs, so returned variables are dropped
	CodeOnErrorScript      = "on_error_script"     // an on_error handler declares an action but no script
	CodeNoOp               = "no_op"               // the step is config-complete but does nothing (an empty assign)
	CodeUnsupportedLang    = "unsupported_lang"    // the sandbox does not pool that language
	CodeDuplicateLabel     = "duplicate_label"     // labels must be unique (labels are how refs are written)
	CodeUnknownType        = "unknown_type"        // no executor and not a visual node
	CodeEdgeEndpoint       = "edge_endpoint"       // an edge points at a node that does not exist
	CodeCrossParent        = "cross_parent"        // an edge crosses a container boundary
	CodeMultiOutgoing      = "multi_outgoing"      // more than one outgoing edge on a single-edge node
	CodeMultiIncoming      = "multi_incoming"      // more than one incoming edge on a node
	CodeNoIncoming         = "no_incoming"         // an incoming edge on start / entry, which the runner enters itself
	CodeNoOutgoing         = "no_outgoing"         // an outgoing edge on a terminal node
	CodeContainerEntry     = "container_entry"     // a container has no (or several) entry children
	CodeBranchEdge         = "branch_edge"         // a branch child has no outgoing edge to route to
	CodeBranchOutlet       = "branch_outlet"       // a branch child routes through several edges, or a handle names nothing
	CodeBranchCondition    = "branch_condition"    // a branch child has no condition script, so it is skipped
	CodeConditionReturn    = "condition_no_return" // a condition script never returns a value, so it can never be true
	CodeConditionInput     = "condition_input_ref" // a condition reads input.X, which the node does not hand in
	CodeInputSource        = "input_source"        // an input row the runner cannot resolve, so the step fails before it runs
	CodeArgumentUndeclared = "argument_undeclared" // a node reads an entry argument the flow does not declare
	CodeArgumentOptional   = "argument_optional"   // a declared entry argument is not in required, so a caller may omit it
	CodeIntentTemperature  = "intent_temperature"  // an intent step classifies at a sampled temperature, so routing is not stable
	CodeChildID            = "child_id"            // a branch child has no id, so no edge can route to it
	CodeCallTarget         = "call_target"         // call.target does not name a function node
	CodeCallArgs           = "call_args"           // call.args does not match the target function's params
	CodeDanglingRef        = "dangling_ref"        // source_ref points at a node that does not exist
	CodeSelfRef            = "self_ref"            // a node's own inputs read that same node's outputs / locals
	CodeFunctionRef        = "function_ref"        // a function node is referenced from outside its own body
	CodeFunctionEdge       = "function_edge"       // an edge touches a function node
	CodeNestedFunction     = "nested_function"     // a function node sits inside another container
	CodeTargetVar          = "target_var"          // targets.X writes a variable the container does not declare
	CodeSourceKey          = "source_key"          // source_key is not an output the source node declares
	CodeLoopSource         = "loop_source"         // each-mode source_key is not declared in inputs
	CodeControlScope       = "control_scope"       // a break / continue that no loop can consume
	CodeReturnScope        = "return_scope"        // a return outside a function, the signal's only consumer
	CodeUnreachable        = "unreachable"         // no incoming edge and not the start node
	CodeOnErrorAction      = "on_error_action"     // the node does not declare that action, so the runner ignores the block
	CodeOnErrorCode        = "on_error_code"       // an error code that does not exist at all
	CodeOnErrorPost        = "on_error_post"       // post configured on a node that does not support it
	CodeIgnoredInputs      = "ignored_inputs"      // config.inputs on a node type that never reads it
	CodeSkippedOutputRead  = "skipped_output_read" // a skipped node leaves no default, yet a downstream node reads its outputs

	// Naming: `concurrent_*` covers the member flag itself (where it may sit, what it may reach), `parallel_*` covers the
	// scope's runtime shape (visibility, conflicting writers, a scope that can never run two at once, nesting).
	CodeConcurrentMemberParent  = "concurrent_member_parent"       // the member flag on a node whose parent is not a host
	CodeConcurrentMemberOnChain = "concurrent_member_not_on_chain" // parent is a host, but the node is not on its entry chain
	CodeConcurrentMemberType    = "concurrent_member_type"         // the member flag on a control / annotation node
	CodeConcurrentMemberWrite   = "concurrent_member_write"        // a member writes a flow variable or another node's variable
	CodeParallelVisibility      = "parallel_visibility"            // reads a variable its producer has not finished writing
	CodeParallelOutputConflict  = "parallel_output_conflict"       // two writers to one host output, at least one a member
	CodeParallelDegenerate      = "parallel_degenerate"            // a scope that can never run two members at once
	CodeParallelFanout          = "parallel_fanout"                // nested scopes whose depth / limits multiply the work in flight
	CodeParallelMembersQueued   = "parallel_members_queued"        // more members than the tenant ceiling, so the extra ones queue
)

// CodeOnErrorCodeUnused is a real error code the node type never produces: a dead rule, so a warning - and its own code,
// because the wording has to stay apart from "that code does not exist" (which is an error).
const CodeOnErrorCodeUnused = "on_error_code_unused"

// CodeOnErrorShadowed is a handler that an earlier skip rule already claims: matching order is retry -> skip ->
// handler, so those messages never reach the handler script. Its own code for the same reason as the one above - the
// wording ("this handler never runs") has nothing in common with "this node does not declare the action".
const CodeOnErrorShadowed = "on_error_shadowed"

// containerTypes hold child nodes (and bring an entry child in the frontend).
var containerTypes = map[string]bool{"loop": true, "process": true, "function": true}

// multiOutgoingTypes may legitimately have several outgoing edges (they route by handle).
var multiOutgoingTypes = map[string]bool{"branch": true, "intent": true}

// terminalTypes end the chain and have no outlet. function is left out on purpose: any edge touching it is already refused by
// function_edge, and comment / layout are not wired at all. `raise` is here for a different reason than the four control
// signals: they hand control to an enclosing scope, while a raise never succeeds - no retry, no skip - so nothing after
// it can ever run and an outgoing edge from one is dead by construction.
var terminalTypes = map[string]bool{"exit": true, "break": true, "continue": true, "return": true, "raise": true}

// raiseBlankRef matches a template reference into a namespace a raise node never fills: `local` and `output` come from the
// node's own vars (BuildScriptTemplateContext), and the executor writes neither. The reference does not fail at run time -
// text/template renders the literal "<no value>" into the text an operator reads - so it has to be caught here.
// Deliberately narrow: a dot reference inside {{ }}, so the words "local" or "output" in ordinary prose do not trip it.
var raiseBlankRef = regexp.MustCompile(`\{\{[^}]*\.(local|output)\.[\w.]+[^}]*\}\}`)

// nonMemberTypes cannot be a concurrency member: membership belongs to steps whose output the host collects, and these do
// something else. Control nodes express control flow on the chain instead. `exit` is in the set on purpose - it always ends
// the whole run, so dispatching it as a member would only change the bookkeeping (which node is recorded as skipped). An
// `exit` written inside a member's own body still ends the run: the nested host carries the signal out and the outer scope
// lifts it. `result` / `progress` write the run's own output, which is order-carrying: AddResult and AppendProgressMsg append
// in call order, so a member's frame would land wherever the scheduler ran it instead of at its place on the chain.
// `assign` / `memory` never wait: a template write and a session read finish inline, so a slot would only reshuffle the
// chain without overlapping anything. `raise` never succeeds: dispatching it as a member would hand a slot to a step that
// can only fail the host.
var nonMemberTypes = map[string]bool{
	"start": true, "entry": true, "comment": true, "layout": true,
	"break": true, "continue": true, "return": true, "exit": true,
	"result": true, "progress": true, "assign": true, "memory": true,
	"raise": true,
}

// NonMemberTypes returns the types above, sorted. Exported because the editor's "concurrent member" checkbox has to be
// gated by the same set the runner enforces: a hand-kept copy on the client drifts the moment a type is added, and then
// the panel offers a flag the checker refuses - which is exactly what happened when raise was introduced.
func NonMemberTypes() []string {
	out := make([]string, 0, len(nonMemberTypes))
	for t := range nonMemberTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// CanBeConcurrentMember reports whether a node type may carry data.concurrent. Membership belongs to executable steps:
// the tool that writes nodes uses this so the write boundary refuses the same types the validator reports.
func CanBeConcurrentMember(typ string) bool { return !nonMemberTypes[typ] }

// knownTypes are every node type the runtime understands: an executor plus the canvas-only kinds. A type outside this
// set is a dead node (the runner reports "no executor registered" only when it reaches it).
var knownTypes = map[string]bool{
	"llm": true, "code": true, "http": true, "mcp": true, "rpc": true,
	"branch": true, "intent": true, "loop": true, "process": true, "function": true,
	"call": true, "assign": true, "memory": true, "raise": true,
	"result": true, "progress": true, "break": true, "continue": true, "return": true,
	"exit": true, "layout": true, "comment": true,
	"start": true, "entry": true, "knowledge": true,
}

// Step checks one node's config.
//
// The shape rules are refused outright (err != nil): a behaviour block that is not an object cannot be rescued on the
// canvas either. Everything else is a Finding, and the severity follows the run, not the author's convenience: a key the
// runner cannot do without (a code step with no script, a branch with no children) is an error, because the node fails
// the moment it is reached; a key that only costs correctness (missing outputs, an on_error rule that can never fire, an
// assign step with nothing to assign) is a warning. The create tools still hand every finding back as a warning - a skeleton with holes is a normal authoring
// step - so severity means "will it run", not "may I save".
//
// An empty config is judged like any other, not exempted: a code node dropped on the canvas has no config and no script,
// and the runner fails on it exactly as it fails on a code node whose script was cleared. Anything that legitimately has
// nothing to configure (start, entry, comment, layout) has no case in the switch below, so it stays silent.
//
// langs is the sandbox's workflow script language list; nil means "unknown" (the sandbox is down or too old to report
// it) and the language check is skipped rather than guessed.
func Step(typ string, cfg map[string]any, langs []string) ([]Finding, error) {
	var out []Finding
	add := func(severity, code, path, msg string) {
		out = append(out, Finding{Severity: severity, Code: code, NodeType: typ, Path: path, Message: msg})
	}

	// A node's behaviour block must be an object.
	for _, key := range []string{"code", "http", "llm", "loop", "mcp", "rpc", "branch", "raise"} {
		v, exists := cfg[key]
		if !exists {
			continue
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, fmt.Errorf("step %s: config.%s must be an object", typ, key)
		}
	}
	// children only appear on branch / intent.
	if v, exists := cfg["children"]; exists {
		if _, ok := v.([]any); !ok {
			return nil, fmt.Errorf("step %s: config.children must be an array", typ)
		}
	}
	// A node type with no consumer for inputs: the block is dead config, and whoever wrote it believes values were handed
	// in (a process body reads the outer nodes directly). A warning, not an error - the node runs, it just ignores it.
	if _, ok := cfg["inputs"]; ok && IgnoresInputs(typ) {
		add(SeverityWarning, CodeIgnoredInputs, "inputs", fmt.Sprintf(
			"%s nodes read no config.inputs (%s), so this block does nothing at run time", typ, InputsIgnoredReason(typ)))
	}

	// loop.type decides which field is read later, so a wrong value guarantees a run-time error.
	if lc, ok := cfg["loop"].(map[string]any); ok {
		if raw, exists := lc["type"]; exists {
			s, _ := raw.(string)
			switch s {
			case "count", "each", "while", "do_while":
			default:
				return nil, fmt.Errorf("step %s: config.loop.type must be count / each / while / do_while, got %v", typ, raw)
			}
		}
	}

	switch typ {
	case "code":
		code, _ := cfg["code"].(map[string]any)
		if script, ok := code["script"].(string); !ok || strings.TrimSpace(script) == "" {
			add(SeverityError, CodeIncomplete, "code.script", "code step has no code.script and fails with script not found at run time")
		}
		// A step that writes into an ancestor container through targets (a function's outputs, a loop's locals) has no
		// config.outputs and is complete; only a step that declares none of the three sets throws every returned key away.
		if !declaresAnyVariable(cfg) {
			// Not an error: the step runs, it just loses every variable the script returns. Its own code, not
			// "incomplete": a client that words findings by code would otherwise print the same sentence for a missing
			// script (an error) and a missing outputs block (a warning) on the same node.
			add(SeverityWarning, CodeOutputs, "outputs", "code step has no config.outputs / targets / locals: variables returned by the script are rejected as undeclared")
		}
	case "http":
		httpCfg, _ := cfg["http"].(map[string]any)
		if url, _ := httpCfg["url"].(string); strings.TrimSpace(url) == "" {
			add(SeverityError, CodeIncomplete, "http.url", "http step has no http.url and cannot send the request at run time")
		}
		// The mode is read after the request has been sent, so an unknown one costs the side effect and then fails.
		if mode, ok := httpCfg["response_mode"].(string); ok && !validResponseMode(mode) {
			add(SeverityError, CodeIncomplete, "http.response_mode", fmt.Sprintf(
				"http step has response_mode=%q, which is neither complete nor sse_stream; the request is sent and only then the run fails", mode))
		}
	case "llm":
		llmCfg, _ := cfg["llm"].(map[string]any)
		if llmCfg == nil {
			add(SeverityError, CodeIncomplete, "llm", "llm step has no config.llm (model and messages) and fails with a config error at run time")
			break
		}
		if msgs, ok := llmCfg["messages"].([]any); !ok || len(msgs) == 0 {
			add(SeverityError, CodeIncomplete, "llm.messages", "llm step has no llm.messages and fails with messages cannot be empty at run time")
		}
		if mode, ok := llmCfg["response_mode"].(string); ok && !validResponseMode(mode) {
			add(SeverityError, CodeIncomplete, "llm.response_mode", fmt.Sprintf(
				"llm step has response_mode=%q, which is neither complete nor sse_stream; the run fails with a config error", mode))
		}
	case "loop":
		lc := objectOf(cfg["loop"])
		if lc == nil {
			add(SeverityError, CodeIncomplete, "loop", "loop step has no config.loop and fails with loop config not found at run time")
			break
		}
		// The runner dispatches on loop.type and answers "unknown loop type" when it is absent, so a missing type is
		// exactly as fatal as an unknown one (which is refused as a shape problem above).
		switch loopType, _ := lc["type"].(string); loopType {
		case "count":
			addNonNumeric(lc, "total_times", "loop.total_times", add)
		case "while", "do_while":
			addNonNumeric(lc, "max_times", "loop.max_times", add)
			// The condition is a script that must hand a value back, and what it may read is wider than for branch: the
			// runner merges config.inputs -> loop vars -> outputs -> locals before every check.
			loopLang, _ := lc["lang"].(string)
			loopCond, _ := lc["condition"].(string)
			readable := declaredNames(cfg["inputs"])
			for _, key := range []string{"outputs", "locals", "loop_vars"} {
				for name := range objectOf(cfg[key]) {
					readable[name] = true
				}
			}
			addConditionScriptFindings("loop", loopLang, loopCond, "loop.condition",
				"config.inputs / outputs / locals", readable, add)
		case "each":
			// source_key needs config.inputs, so the diagram check owns it.
		default:
			add(SeverityError, CodeIncomplete, "loop.type", "loop step has no loop.type, so the run fails with unknown loop type")
		}
	case "branch", "intent":
		children, ok := cfg["children"].([]any)
		if !ok || len(children) == 0 {
			add(SeverityError, CodeIncomplete, "children", typ+" step has empty config.children: there is no branch to take at run time")
			break
		}
		if typ == "branch" {
			// A child without a condition script is skipped by the runner (one node log line), so the branch can never
			// match: the node runs, just never down that path. Intent children are classified by label / description
			// instead, which is why this loop is branch-only.
			branchLang, _ := objectOf(cfg["branch"])["lang"].(string)
			readable := declaredNames(cfg["inputs"])
			for _, raw := range children {
				child := objectOf(raw)
				id, _ := child["id"].(string)
				if id == "" {
					continue // the diagram check reports a branch child without an id
				}
				cond, _ := child["condition"].(string)
				if strings.TrimSpace(cond) == "" {
					add(SeverityWarning, CodeBranchCondition, "children."+id+".condition", fmt.Sprintf(
						"branch child %q has no condition script, so the runner skips it at run time", id))
					continue
				}
				// The condition itself was never looked at before: a missing return or an undeclared input passes this
				// check and then fails the node on its first evaluation.
				addConditionScriptFindings("branch", branchLang, cond, "children."+id+".condition",
					"config.inputs", readable, add)
			}
		}

	case "call":
		if target, _ := cfg["target"].(string); strings.TrimSpace(target) == "" {
			add(SeverityError, CodeIncomplete, "target", "call step has no config.target (must be the id of a function node in the same flow)")
		}
	case "result", "progress":
		// The two text nodes reuse the script node structure and fail with "code config not found" / "script not
		// found" before they can emit anything: nothing here needs a model, a connection or the network, so the
		// missing key is knowable at save time.
		codeCfg := objectOf(cfg["code"])
		if codeCfg == nil {
			add(SeverityError, CodeIncomplete, "code", typ+" step has no config.code and fails with code config not found at run time")
			break
		}
		if script, _ := codeCfg["script"].(string); strings.TrimSpace(script) == "" {
			add(SeverityError, CodeIncomplete, "code.script", typ+" step has no code.script and fails with script not found at run time")
		}
	case "mcp":
		mcpCfg := objectOf(cfg["mcp"])
		if mcpCfg == nil {
			add(SeverityError, CodeIncomplete, "mcp", "mcp step has no config.mcp and fails with mcp config not found at run time")
			break
		}
		if s, _ := mcpCfg["connection_id"].(string); strings.TrimSpace(s) == "" {
			add(SeverityError, CodeIncomplete, "mcp.connection_id", "mcp step has no mcp.connection_id and fails before the call at run time")
		}
		if s, _ := mcpCfg["tool_name"].(string); strings.TrimSpace(s) == "" {
			add(SeverityError, CodeIncomplete, "mcp.tool_name", "mcp step has no mcp.tool_name and fails before the call at run time")
		}
	case "rpc":
		rpcCfg := objectOf(cfg["rpc"])
		if rpcCfg == nil {
			add(SeverityError, CodeIncomplete, "rpc", "rpc step has no config.rpc and fails with rpc config not found at run time")
			break
		}
		// An empty target_type means "connection": older configs never wrote it, so only "endpoint" switches mode.
		if target, _ := rpcCfg["target_type"].(string); target == "endpoint" {
			if s, _ := rpcCfg["endpoint"].(string); strings.TrimSpace(s) == "" {
				add(SeverityError, CodeIncomplete, "rpc.endpoint", "rpc step targets an endpoint but has no rpc.endpoint and fails before the call at run time")
			}
			break
		}
		if s, _ := rpcCfg["connection_id"].(string); strings.TrimSpace(s) == "" {
			add(SeverityError, CodeIncomplete, "rpc.connection_id", "rpc step has no rpc.connection_id and fails before the call at run time")
		}
		if s, _ := rpcCfg["tool_name"].(string); strings.TrimSpace(s) == "" {
			add(SeverityError, CodeIncomplete, "rpc.tool_name", "rpc step has no rpc.tool_name and fails before the call at run time")
		}
	case "assign":
		// The opposite failure of the cases above: the step succeeds and reports success while doing nothing, so the
		// author's intent is lost without an error anywhere. A warning, and its own code, because that reads
		// differently from "a required key is missing".
		list, ok := cfg["assignments"].([]any)
		if !ok || len(list) == 0 {
			add(SeverityWarning, CodeNoOp, "assignments", "assign step has no config.assignments: it reports success without assigning anything")
			break
		}
		// The items themselves are all-or-nothing: parseAssignments refuses the whole list before the first assignment,
		// so one malformed entry means none of them land.
		for i, raw := range list {
			path := fmt.Sprintf("assignments[%d]", i)
			item := objectOf(raw)
			if item == nil {
				add(SeverityError, CodeIncomplete, path, "assign step has an assignment that is not an object; the run fails before assigning anything")
				continue
			}
			kind, _ := item["kind"].(string)
			target, _ := item["target"].(string)
			if strings.TrimSpace(kind) == "" || strings.TrimSpace(target) == "" {
				add(SeverityError, CodeIncomplete, path, "assign step has an assignment without kind or target; the run fails before assigning anything")
				continue
			}
			if kind != "output" && kind != "target" {
				add(SeverityError, CodeIncomplete, path, fmt.Sprintf(
					"assign step has kind=%q, which is neither output nor target; the run fails before assigning anything", kind))
			}
		}
	case "raise":
		// Both keys are read before anything happens, so a missing one fails the node the moment the run reaches it.
		// An unusable code reuses "incomplete" on purpose: http.response_mode and loop.type are reported the same way -
		// a value that is present but wrong guarantees the same run-time failure as an absent one.
		rc := objectOf(cfg["raise"])
		if rc == nil {
			add(SeverityError, CodeIncomplete, "raise", "raise step has no config.raise and fails with a config error at run time")
			break
		}
		code, _ := rc["code"].(string)
		switch {
		case strings.TrimSpace(code) == "":
			add(SeverityError, CodeIncomplete, "raise.code",
				"raise step has no raise.code, so it cannot report anything at run time")
		case !IsRaisableCode(code):
			add(SeverityError, CodeIncomplete, "raise.code", fmt.Sprintf(
				"raise.code=%q is not raisable: use one of %s (config / internal bypass on_error, and a raised cancel "+
					"would claim the user stopped the run)", code, strings.Join(RaisableCodes, " / ")))
		}
		msg, _ := rc["message"].(string)
		if strings.TrimSpace(msg) == "" {
			add(SeverityError, CodeIncomplete, "raise.message",
				"raise step has no raise.message, so the failure would carry an empty reason")
		} else if m := raiseBlankRef.FindStringSubmatch(msg); m != nil {
			// A warning, not an error: the reference may sit inside an example the author is pasting back to a user.
			add(SeverityWarning, CodeIncomplete, "raise.message", fmt.Sprintf(
				"raise.message reads .%s, which this node always leaves empty - it writes no local and has no outputs - "+
					"so the run renders \"<no value>\" into the message; declare the value under inputs and read "+
					".input.<name> instead", m[1]))
		}
	}

	// Language fields are checked against the sandbox's "workflow script languages" list: a wrong one is invisible when
	// saving and on the canvas, and only fails at run time with unsupported_lang.
	if langs != nil {
		for _, lf := range langFields(typ, cfg) {
			// template is rendered in pure Go and does not use the sandbox's code pool, but it is legal for result /
			// progress.
			if lf.lang == "template" || contains(langs, lf.lang) {
				continue
			}
			add(SeverityError, CodeUnsupportedLang, lf.path, fmt.Sprintf(
				"step %s has %s=%s, which is not among the sandbox's current workflow script languages (%s); it will fail with unsupported_lang at run time",
				typ, lf.path, lf.lang, strings.Join(langs, ", ")))
		}
	}

	out = append(out, checkOnError(typ, cfg)...)
	return out, nil
}

// checkOnError validates the on_error block against the node's declared capabilities.
//
// Both failure modes are silent today: the runner drops an action the node does not declare (one log line, which nobody
// reads), and a code the node can never produce simply never matches - so the fallback the author thought they wrote
// does not exist. An unknown code is an Error because it can never match anything; a code that exists but is not
// declared by this type is a Warning, because it is legal config that just cannot fire here.
func checkOnError(typ string, cfg map[string]any) []Finding {
	caps, known := CapabilitiesOf(typ)
	if !known {
		return nil
	}
	oe := objectOf(cfg["on_error"])
	if oe == nil {
		return nil
	}
	var out []Finding
	add := func(severity, code, path, msg, hint string) {
		out = append(out, Finding{Severity: severity, Code: code, NodeType: typ, Path: path, Message: msg, Hint: hint})
	}

	for _, action := range []string{"retry", "skip", "handler"} {
		block := objectOf(oe[action])
		if block == nil {
			continue
		}
		path := "on_error." + action
		if !caps.HasAction(action) {
			add(SeverityWarning, CodeOnErrorAction, path, fmt.Sprintf(
				"%s nodes do not declare the %s action, so the runner ignores this block at run time", typ, action),
				"remove it, or use an action this node declares")
			continue
		}
		if action == "handler" {
			if script, _ := block["script"].(string); strings.TrimSpace(script) == "" {
				add(SeverityWarning, CodeOnErrorScript, path+".script",
					fmt.Sprintf("%s node has an on_error.handler without a script, so it does nothing", typ),
					"write the handler script or drop the block")
			}
		}
		for _, c := range stringSlice(block["codes"]) {
			if !contains(AllErrorCodes, c) {
				add(SeverityError, CodeOnErrorCode, path+".codes", fmt.Sprintf(
					"%q is not an error code the runtime can produce", c),
					"use one of: "+strings.Join(AllErrorCodes, ", "))
				continue
			}
			if !caps.ProducesCode(c) {
				// A different code from "that code does not exist": same path, but the code is real and the node is the
				// problem, so the client can word it apart (and it is a warning, not an error).
				add(SeverityWarning, CodeOnErrorCodeUnused, path+".codes", fmt.Sprintf(
					"%s nodes never produce %q, so this rule can never fire", typ, c),
					"list only the codes this node type declares")
			}
		}
	}

	if post := objectOf(oe["post"]); post != nil && !caps.HasPost {
		add(SeverityWarning, CodeOnErrorPost, "on_error.post", fmt.Sprintf(
			"%s nodes do not support post, so the runner drops it at run time", typ),
			"remove the post block")
	}

	out = append(out, checkOnErrorShadowed(typ, oe, caps)...)
	return out
}

// checkOnErrorShadowed reports a handler the skip rule in front of it already claims. The runner tries retry -> skip ->
// handler in that order, so a message skip accepts is recorded as skipped and the handler script never runs for it: the
// author wrote a fallback that is not there.
//
// Patterns make this subtler than a code intersection. Patterns narrow only the `other` code (runner.go, matchCodes), so:
//   - a code shared by both rules is a plain overlap, except `other` when skip narrows it with patterns;
//   - for `other` with patterns on both sides, only an overlapping pattern pair proves the handler is unreachable for
//     those messages. Two rules that list `other` with disjoint patterns do not shadow each other at all, and reporting
//     them would teach the author to ignore the checker.
func checkOnErrorShadowed(typ string, oe map[string]any, caps Capabilities) []Finding {
	if !caps.HasAction("skip") || !caps.HasAction("handler") {
		return nil
	}
	skip := objectOf(oe["skip"])
	handler := objectOf(oe["handler"])
	if skip == nil || handler == nil || !hasScript(handler) {
		// No handler, or one without a script: on_error_script already reports what is wrong, and saying it twice is
		// the kind of noise that hides the real finding.
		return nil
	}
	skipCodes := stringSlice(skip["codes"])
	skipPatterns := stringSlice(skip["patterns"])
	handlerCodes := stringSlice(handler["codes"])
	handlerPatterns := stringSlice(handler["patterns"])

	var out []Finding
	var claimed []string
	for _, c := range handlerCodes {
		if !contains(skipCodes, c) {
			continue
		}
		// `other` is matched through patterns: without any on the skip side it takes every `other` message.
		if c != "other" || len(skipPatterns) == 0 {
			claimed = append(claimed, c)
		}
	}
	if len(claimed) > 0 {
		sort.Strings(claimed)
		out = append(out, Finding{
			Severity: SeverityWarning,
			Code:     CodeOnErrorShadowed,
			NodeType: typ,
			Path:     "on_error.handler.codes",
			Message: fmt.Sprintf("skip runs before handler and already claims %s, so this handler never runs for those codes",
				strings.Join(claimed, ", ")),
			Hint: "drop those codes from skip, or narrow skip's patterns so the handler has something left to catch",
		})
	}

	if contains(skipCodes, "other") && contains(handlerCodes, "other") && len(skipPatterns) > 0 && len(handlerPatterns) > 0 {
		if sp, hp, ok := overlappingPatterns(skipPatterns, handlerPatterns); ok {
			out = append(out, Finding{
				Severity: SeverityWarning,
				Code:     CodeOnErrorShadowed,
				NodeType: typ,
				Path:     "on_error.handler.patterns",
				Message: fmt.Sprintf("skip's pattern %q and the handler's %q both match some `other` message, and skip runs first, so the handler never sees it",
					sp, hp),
				Hint: "narrow one of the two patterns so every message has exactly one owner",
			})
		}
	}
	return out
}

// patternShape is the three forms matchPattern understands: `%x%` and a bare `x` both mean "contains", `x%` means
// "prefix", `%x` means "suffix".
type patternShape int

const (
	patternContains patternShape = iota
	patternPrefix
	patternSuffix
)

func splitPattern(p string) (patternShape, string) {
	hasPrefix := strings.HasPrefix(p, "%")
	hasSuffix := strings.HasSuffix(p, "%")
	switch {
	case hasPrefix && hasSuffix:
		return patternContains, strings.Trim(p, "%")
	case hasPrefix:
		return patternSuffix, p[1:]
	case hasSuffix:
		return patternPrefix, p[:len(p)-1]
	}
	return patternContains, p
}

// patternsOverlap reports whether two patterns describe the same message family, i.e. one pattern's text covers the
// other. That is deliberately stricter than "some message could match both": two unrelated contains patterns
// (`%rate limit%` and `%quota%`) can always be satisfied by a contrived message, but a config that splits those two
// error families between skip and handler works exactly as intended, and reporting it would be a false positive.
func patternsOverlap(a string, b string) bool {
	ka, ta := splitPattern(a)
	kb, tb := splitPattern(b)
	switch {
	case ka == patternContains && kb == patternContains:
		return strings.Contains(ta, tb) || strings.Contains(tb, ta)
	case ka == patternPrefix && kb == patternPrefix:
		return strings.HasPrefix(ta, tb) || strings.HasPrefix(tb, ta)
	case ka == patternSuffix && kb == patternSuffix:
		return strings.HasSuffix(ta, tb) || strings.HasSuffix(tb, ta)
	case ka == patternPrefix && kb == patternContains, ka == patternContains && kb == patternPrefix:
		return strings.HasPrefix(ta, tb) || strings.HasPrefix(tb, ta)
	default:
		// suffix vs suffix is handled above; what is left is suffix against prefix / contains, where "one text
		// covers the other" is still the test.
		return strings.Contains(ta, tb) || strings.Contains(tb, ta)
	}
}

// overlappingPatterns returns the first pair of patterns that can match the same message, so the finding can quote the
// concrete pair instead of "some pattern overlaps".
func overlappingPatterns(a, b []string) (string, string, bool) {
	for _, pa := range a {
		for _, pb := range b {
			if patternsOverlap(pa, pb) {
				return pa, pb, true
			}
		}
	}
	return "", "", false
}

// stringSlice reads a config list written either as []any (JSON) or []string (assembled by a tool).
// checkSkippedOutputReads flags the one combination that turns a deliberate "keep going" into a wrong value: a node that
// skips an error while writing nothing, plus a downstream node that reads its outputs. The reader then gets the declared
// default ("" / 0 / false), which is indistinguishable from a real empty result — the run reports success either way.
func checkSkippedOutputReads(doc *diagram, resolve func(string) *node,
	add func(severity, code, nodeID, nodeType, path, msg, hint string)) {
	for i := range doc.Nodes {
		src := &doc.Nodes[i]
		if !skipsWithoutDefault(src.Type, src.Data.Config) {
			continue
		}
		var readers []string
		for j := range doc.Nodes {
			r := &doc.Nodes[j]
			if r.ID == src.ID || IgnoresInputs(r.Type) {
				continue
			}
			if readsNodeOutput(r.Data.Config, src, resolve) {
				readers = append(readers, r.ID)
			}
		}
		if len(readers) == 0 {
			continue
		}
		sort.Strings(readers)
		add(SeverityWarning, CodeSkippedOutputRead, src.ID, src.Type, "on_error.skip",
			fmt.Sprintf("node %s skips without leaving a default and %s read its outputs: they keep \"\" / 0 / false, the same as a real empty result",
				src.ID, strings.Join(readers, ", ")),
			"either add on_error.post.set_outputs with a marker (ok = false) so downstream can tell, or let the step fail")
	}
}

// skipsWithoutDefault reports whether the node is configured to swallow an error without writing anything in its place.
func skipsWithoutDefault(typ string, cfg map[string]any) bool {
	oe := objectOf(cfg["on_error"])
	if oe == nil {
		return false
	}
	skip := objectOf(oe["skip"])
	if skip == nil || len(stringSlice(skip["codes"])) == 0 {
		return false
	}
	caps, known := CapabilitiesOf(typ)
	if !known {
		return false
	}
	// A handler runs on the failure path and may write the fallback itself.
	if hasScript(objectOf(oe["handler"])) && caps.HasAction("handler") {
		return false
	}
	// A post block on a type that does not keep it writes nothing at run time, so it is not a fallback either.
	if !caps.HasPost {
		return true
	}
	post := objectOf(oe["post"])
	if post == nil {
		return true
	}
	if len(objectOf(post["set_outputs"])) > 0 {
		return false
	}
	return !hasScript(objectOf(post["handler"]))
}

func hasScript(block map[string]any) bool {
	if block == nil {
		return false
	}
	script, _ := block["script"].(string)
	return strings.TrimSpace(script) != ""
}

// readsNodeOutput reports whether any input / arg row of the reader resolves to src. The key is not compared here: a wrong
// key is the source_key finding, not this one.
func readsNodeOutput(cfg map[string]any, src *node, resolve func(string) *node) bool {
	for _, section := range []string{"inputs", "args"} {
		for _, raw := range objectOf(cfg[section]) {
			def := objectOf(raw)
			if def == nil {
				continue
			}
			if sourceType, _ := def["source_type"].(string); sourceType != "node" {
				continue
			}
			ref, _ := def["source_ref"].(string)
			if strings.TrimSpace(ref) == "" {
				continue
			}
			if resolve(ref) == src {
				return true
			}
		}
	}
	return false
}

func stringSlice(v any) []string {
	switch raw := v.(type) {
	case []any:
		out := make([]string, 0, len(raw))
		for _, it := range raw {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return raw
	}
	return nil
}

type langField struct {
	path string
	lang string
}

// langFields collects every "script language" field in a node's config.
//
// It covers the nodes a definition can produce: code.lang for code / result / progress, the condition script language of
// branch / loop, the handler of http / rpc / mcp / llm, and the two fallback handlers of on_error. One sweep instead of a
// copy per case: missing one means missing one "created fine, fails at run time" entry point.
func langFields(typ string, cfg map[string]any) []langField {
	var out []langField
	add := func(path string, obj map[string]any) {
		if s, _ := obj["lang"].(string); s != "" {
			out = append(out, langField{path: path, lang: s})
		}
	}
	switch typ {
	case "code", "result", "progress":
		add("code.lang", objectOf(cfg["code"]))
	case "branch":
		add("branch.lang", objectOf(cfg["branch"]))
	case "loop":
		add("loop.lang", objectOf(cfg["loop"]))
	case "http", "rpc", "mcp", "llm":
		add(typ+".handler.lang", objectOf(objectOf(cfg[typ])["handler"]))
	}
	if oe := objectOf(cfg["on_error"]); oe != nil {
		add("on_error.handler.lang", objectOf(oe["handler"]))
		add("on_error.post.handler.lang", objectOf(objectOf(oe["post"])["handler"]))
	}
	return out
}

// validResponseMode accepts the two modes the http / llm executors know; an empty value means "decide at run time"
// (llm defaults to complete, http sniffs the Content-Type) and is valid.
func validResponseMode(mode string) bool {
	return mode == "" || mode == "complete" || mode == "sse_stream"
}

// addNonNumeric reports a loop count that is present but not a number.
//
// The runner's parseCountInt treats a missing value as "use the system ceiling", so only a value of the wrong type is
// a finding - and it is an error, because the run stops before the first iteration.
func addNonNumeric(lc map[string]any, key, path string, add func(severity, code, path, msg string)) {
	v, ok := lc[key]
	if !ok || v == nil {
		return
	}
	switch v.(type) {
	case float64, int, int64:
		return
	}
	add(SeverityError, CodeIncomplete, path, fmt.Sprintf(
		"loop step has %s=%v (%T), which is not a number; the run fails with a config error", key, v, v))
}

func objectOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// declaredNames is the key set of a config variable block (inputs / outputs / locals / loop_vars) as a lookup table.
func declaredNames(block any) map[string]bool {
	out := make(map[string]bool)
	for name := range objectOf(block) {
		out[name] = true
	}
	return out
}

// numberField reads a numeric config field, tolerating the shapes a JSON decode or a Go literal leaves behind.
func numberField(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// knownSourceTypes are the source_type values ExecContext.getVariableNoLock can resolve; anything else fails the whole
// batch before the step runs.
var knownSourceTypes = map[string]bool{
	"argument": true, "env": true, "global": true, "flow": true, "node": true, "builtin": true,
}

// builtinInputKeys mirror BuiltinVars() in service/flow/core/execContext.go.
var builtinInputKeys = map[string]bool{
	"session_id": true, "run_id": true, "user_id": true, "schema": true, "flow_id": true, "language": true,
}

// addInputSourceFindings reports input rows the runner cannot resolve. GetVariables resolves the whole block at once and
// one bad row fails the batch, so such a row is not "runs with nil" - the step cannot run at all. A row that only looks
// fine here (an optional argument nobody passes, an upstream node returning null) stays invisible: that is data, not
// config.
func addInputSourceFindings(block any, add func(severity, code, path, msg string)) {
	inputs := objectOf(block)
	if len(inputs) == 0 {
		return
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := "inputs." + name
		row := objectOf(inputs[name])
		if row == nil {
			add(SeverityError, CodeInputSource, path, fmt.Sprintf(
				"input %q is not an object, so the runner skips it and the script reads a missing variable", name))
			continue
		}
		sourceType, _ := row["source_type"].(string)
		sourceKey, _ := row["source_key"].(string)
		sourceRef, _ := row["source_ref"].(string)
		switch {
		case strings.TrimSpace(sourceType) == "":
			add(SeverityError, CodeInputSource, path, fmt.Sprintf(
				"input %q has no source_type: the runner fails the whole batch with \"variable source is not selected\"", name))
		case !knownSourceTypes[sourceType]:
			add(SeverityError, CodeInputSource, path, fmt.Sprintf(
				"input %q has source_type=%q, which the runner does not know (argument / env / global / flow / node / builtin)",
				name, sourceType))
		case strings.TrimSpace(sourceKey) == "":
			add(SeverityError, CodeInputSource, path, fmt.Sprintf(
				"input %q has no source_key: the runner fails the whole batch with \"variable source is not selected\"", name))
		case sourceType == "builtin" && !builtinInputKeys[sourceKey]:
			add(SeverityError, CodeInputSource, path, fmt.Sprintf(
				"input %q reads builtin %q, which does not exist (session_id / run_id / user_id / schema / flow_id / language)",
				name, sourceKey))
		case sourceType == "node" && strings.TrimSpace(sourceRef) == "":
			add(SeverityError, CodeInputSource, path, fmt.Sprintf(
				"input %q reads a node but has no source_ref, so the step fails with \"node sourceRef is empty\"", name))
		}
	}
}

// Condition scripts are checked by text, not by a parser: no script engine is linked into this package - the scripts
// themselves run inside the sandbox - and a real parse would need one parser per language. The two rules below therefore
// only fire on shapes that are wrong either way:
//   - no return token at all - lua's `input.score > 90` is a syntax error (the node fails), and in js / python the script
//     returns nothing (the condition is never true, so the path silently never runs);
//   - an `input.<name>` the node does not hand in - the value is nil at run time, so the comparison itself fails the node.
//
// A missed finding keeps today's behaviour, while a false error would flag a flow that runs: the check stays on the
// certain end, and says nothing about a name reached through a local alias or a `return` inside a comment.
var (
	wordReturnRe    = regexp.MustCompile(`\breturn\b`)
	inputDotRefRe   = regexp.MustCompile(`\binput\.([A-Za-z_][A-Za-z0-9_]*)`)
	inputIndexRefRe = regexp.MustCompile(`\binput\s*\[\s*["']([^"']+)["']\s*\]`)
)

// addConditionScriptFindings reports the two condition-script shapes above. surface names the node in the message
// ("branch" / "loop"), declaredWhere is the config block list the script may read through input, and declared is every
// name those blocks hold.
func addConditionScriptFindings(surface, lang, script, path, declaredWhere string, declared map[string]bool,
	add func(severity, code, path, msg string)) {
	if strings.TrimSpace(script) == "" {
		return
	}
	token, isScript := conditionReturnToken(lang)
	if !isScript {
		// template / an unknown language: there is no script return form to look for, and a missing key renders instead
		// of failing, so neither rule applies.
		return
	}
	returned := strings.Contains(script, token)
	if token == "return" {
		returned = wordReturnRe.MatchString(script)
	}
	if !returned {
		severity, consequence := SeverityWarning, "the script returns nothing, so the condition is never true"
		if lang == "" || strings.EqualFold(strings.TrimSpace(lang), "lua") {
			severity = SeverityError
			consequence = "in lua a bare expression is a syntax error, so the node fails on its first evaluation"
		}
		add(severity, CodeConditionReturn, path, fmt.Sprintf(
			"%s condition script has no %q: %s", surface, token, consequence))
	}

	seen := make(map[string]bool)
	for _, m := range inputDotRefRe.FindAllStringSubmatch(script, -1) {
		seen[m[1]] = true
	}
	for _, m := range inputIndexRefRe.FindAllStringSubmatch(script, -1) {
		seen[m[1]] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if declared[name] {
			continue
		}
		add(SeverityError, CodeConditionInput, path, fmt.Sprintf(
			"%s condition reads input.%s, which %s does not declare: the value is nil at run time, so the comparison itself fails the node",
			surface, name, declaredWhere))
	}
}

// conditionReturnToken is how a script hands its condition value back; ok=false means the language has no script return
// form (template), so the return rule does not apply.
func conditionReturnToken(lang string) (token string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "lua":
		return "return", true
	case "js", "javascript":
		return "return", true
	case "python", "py", "python3":
		return "__return__", true
	default:
		return "", false
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// nearestContainer returns the type of the closest enclosing container (function / process / loop), or "" for a top-level
// node. A function is the only container that consumes a return signal, which is why the type matters and not just the depth.
func nearestContainer(byID map[string]*node, n *node) string {
	for parentID := n.ParentNode; parentID != ""; {
		parent := byID[parentID]
		if parent == nil {
			return ""
		}
		if containerTypes[parent.Type] {
			return parent.Type
		}
		parentID = parent.ParentNode
	}
	return ""
}

// hasFunctionAncestor reports whether a function encloses n. A process nested in a function body passes the return signal up
// to that function, where it means "leave the function" - so the presence of a function ancestor is what makes a return legal,
// not the nearest container.
func hasFunctionAncestor(byID map[string]*node, n *node) bool {
	for parentID := n.ParentNode; parentID != ""; {
		parent := byID[parentID]
		if parent == nil {
			return false
		}
		if parent.Type == "function" {
			return true
		}
		parentID = parent.ParentNode
	}
	return false
}

// ===== whole diagram =====

type node struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	ParentNode string `json:"parent_node"`
	Data       struct {
		Label  string         `json:"label"`
		Config map[string]any `json:"config"`
		// Concurrent marks a member of the enclosing host body's concurrency scope.
		Concurrent bool `json:"concurrent"`
	} `json:"data"`
}

type edge struct {
	ID           string `json:"id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	SourceHandle string `json:"source_handle"`
	TargetHandle string `json:"target_handle"`
}

type diagram struct {
	Nodes []node `json:"nodes"`
	Edges []edge `json:"edges"`
}

// Diagram checks a whole stored diagram: structure (edges, containers, branch routing, references) plus every node's
// config through Step.
//
// raw accepts both shapes a diagram has in the database: a JSON object (tools write it) and a JSON string (the canvas
// stringifies once).
func Diagram(raw json.RawMessage, langs []string) ([]Finding, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}
	var out []Finding
	add := func(severity, code, nodeID, nodeType, path, msg, hint string) {
		out = append(out, Finding{Severity: severity, Code: code, NodeID: nodeID, NodeType: nodeType, Path: path, Message: msg, Hint: hint})
	}

	// id -> node, label -> node (labels are the writable form of source_ref / call.target).
	byID := make(map[string]*node, len(doc.Nodes))
	byLabel := make(map[string]*node, len(doc.Nodes))
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.ID == "" {
			add(SeverityError, CodeShape, "", n.Type, "id", "a node has no id and cannot be referenced", "")
			continue
		}
		byID[n.ID] = n
		if l := strings.TrimSpace(n.Data.Label); l != "" {
			byLabel[l] = n
		}
	}
	resolve := func(ref string) *node {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil
		}
		if n, ok := byID[ref]; ok {
			return n
		}
		return byLabel[ref]
	}

	// Duplicate labels: labels are how references are written, so a duplicate points at the wrong node.
	if d, ok := flowdoc.Parse(raw); ok {
		for _, dup := range d.DuplicateLabels() {
			add(SeverityError, CodeDuplicateLabel, "", "", "label", "duplicate node label: "+dup,
				"labels must be unique across the flow; pick another one or leave it empty so the tool adds a suffix")
		}
	}

	// Per-node config: unknown type + Step (shape / completeness / language).
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "" && !knownTypes[n.Type] {
			add(SeverityError, CodeUnknownType, n.ID, n.Type, "type",
				fmt.Sprintf("node type %q has no executor", n.Type), "delete the node or change its type")
		}
		fs, err := Step(n.Type, n.Data.Config, langs)
		if err != nil {
			add(SeverityError, CodeShape, n.ID, n.Type, "", err.Error(), "")
			continue
		}
		for _, f := range fs {
			f.NodeID = n.ID
			f.NodeType = n.Type
			out = append(out, f)
		}
		// An input row is resolved before the step runs, and one unresolvable row fails the whole batch: at run time that
		// is "variable source is not selected", not a step that runs with an empty value. Judged here rather than in
		// Step because create_node fills placeholder rows on a bare node and must stay quiet; a diagram that still has
		// them is what cannot run.
		if !IgnoresInputs(n.Type) {
			addInputSourceFindings(n.Data.Config["inputs"], func(severity, code, path, msg string) {
				add(severity, code, n.ID, n.Type, path, msg,
					"pick a source for the row: source_type + source_key (node rows also need source_ref)")
			})
		}
		// An intent classification is a model call, and at the model's default temperature the same input can route
		// differently between runs. Judged in the diagram check rather than Step, so a bare create_node stays quiet and only
		// a definition that is about to run gets flagged.
		if n.Type == "intent" {
			params := objectOf(objectOf(n.Data.Config["llm"])["params"])
			switch temp, hasTemp := numberField(params, "temperature"); {
			case !hasTemp:
				add(SeverityWarning, CodeIntentTemperature, n.ID, n.Type, "llm.params.temperature",
					fmt.Sprintf("intent node %s sets no llm.params.temperature, so it classifies at the model's default (sampled): the same input can take a different branch between runs", n.ID),
					"set llm.params.temperature to 0: a classification has to be reproducible")
			case temp > 0:
				add(SeverityWarning, CodeIntentTemperature, n.ID, n.Type, "llm.params.temperature",
					fmt.Sprintf("intent node %s classifies at temperature %v: the same input can take a different branch between runs", n.ID, temp),
					"set llm.params.temperature to 0: a classification has to be reproducible")
			}
		}
	}

	// Edges: both ends must exist.
	valid := make([]edge, 0, len(doc.Edges))
	for _, e := range doc.Edges {
		src, dst := byID[e.Source], byID[e.Target]
		if src == nil || dst == nil {
			add(SeverityError, CodeEdgeEndpoint, e.Source, "", "edges",
				fmt.Sprintf("edge %s connects %q to %q, and at least one of them does not exist", e.ID, e.Source, e.Target),
				"delete the edge and wire it again with create_edge")
			continue
		}
		valid = append(valid, e)
	}

	// Cross-parent edges are invalid on the canvas and at run time.
	for _, e := range valid {
		src, dst := byID[e.Source], byID[e.Target]
		if src.ParentNode != dst.ParentNode {
			add(SeverityError, CodeCrossParent, e.Source, src.Type, "edges",
				fmt.Sprintf("edge %s crosses a container boundary (%q is inside %q, %q is not)", e.ID, src.ID, src.ParentNode, dst.ID),
				"wire the edge inside one container, or move the node into it with update_node(parent_node=<container id>) and re-wire the edge that was dropped")
		}
	}

	// A function is entered by a call node, never by an edge: an in-edge runs the body right there with empty params, and an
	// edge out of a function is never followed (the body runs under the call, not as a step of the chain).
	for _, e := range valid {
		for _, end := range []string{e.Source, e.Target} {
			if byID[end].Type != "function" {
				continue
			}
			add(SeverityError, CodeFunctionEdge, end, "function", "edges",
				fmt.Sprintf("edge %s touches function node %s; a function is entered by a call node, not by an edge", e.ID, end),
				"delete the edge and let a call node target the function")
		}
	}

	// A function is a top-level container. create_node refuses a nested one; a hand-written diagram could still place it
	// inside another container, where nothing can call it and the canvas cannot lay it out.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "function" || n.ParentNode == "" {
			continue
		}
		add(SeverityError, CodeNestedFunction, n.ID, n.Type, "parent_node",
			fmt.Sprintf("function node %s is nested inside %s; a function has to sit at the top level", n.ID, n.ParentNode),
			"move it out of the container: a function is called through call.target, it is not a step of a parent body")
	}

	// Outgoing edges: one at most, except branch / intent which route by handle. Containers are checked the same way.
	outCount := make(map[string]int, len(doc.Nodes))
	inCount := make(map[string]int, len(doc.Nodes))
	for _, e := range valid {
		outCount[e.Source]++
		inCount[e.Target]++
	}
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if c := outCount[n.ID]; c > 1 && !multiOutgoingTypes[n.Type] {
			add(SeverityError, CodeMultiOutgoing, n.ID, n.Type, "edges",
				fmt.Sprintf("node %s (%s) has %d outgoing edges; only branch / intent may have more than one", n.ID, n.Type, c),
				"delete the extra edges: after the node finishes it follows the first outgoing edge")
		}
		if outCount[n.ID] > 0 && terminalTypes[n.Type] {
			add(SeverityError, CodeNoOutgoing, n.ID, n.Type, "edges",
				fmt.Sprintf("node %s (%s) has an outgoing edge; a %s node ends the chain and has no outlet", n.ID, n.Type, n.Type),
				"delete the edge: nothing runs after this node")
		}
	}

	// Incoming edges: one at most, and none for the nodes the runner enters on its own. create_edge already refuses both (a
	// second inlet displaces the first, start / entry have no inlet, a function takes no edge) - this catches stored diagrams.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		switch c := inCount[n.ID]; {
		case c > 1:
			add(SeverityError, CodeMultiIncoming, n.ID, n.Type, "edges",
				fmt.Sprintf("node %s (%s) has %d incoming edges; a node takes exactly one", n.ID, n.Type, c),
				"delete the extra edges: the runner reaches a node through a single incoming edge")
		case c > 0 && (n.Type == "start" || n.Type == "entry"):
			add(SeverityError, CodeNoIncoming, n.ID, n.Type, "edges",
				fmt.Sprintf("node %s (%s) has an incoming edge; a %s node is entered by the runner, never by an edge", n.ID, n.Type, n.Type),
				"delete the edge: the flow starts at start, and a container body starts at its entry child")
		}
	}
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if !containerTypes[n.Type] {
			continue
		}
		entries := 0
		for j := range doc.Nodes {
			if doc.Nodes[j].ParentNode == n.ID && doc.Nodes[j].Type == "entry" {
				entries++
			}
		}
		if entries != 1 {
			add(SeverityError, CodeContainerEntry, n.ID, n.Type, "children",
				fmt.Sprintf("container %s (%s) has %d entry children; it needs exactly one", n.ID, n.Type, entries),
				"create the entry child (parent_node=<container id>, type=entry); without it the container fails with 'body entry node not found'")
		}
	}

	// Branch routing: every declared child must own exactly one outgoing edge, named source<childId><left|right>. Left and
	// right of the same child are the same outlet, so a second edge would route that child to two next nodes; and a handle
	// whose owner is neither the node itself nor a declared child routes nowhere at run time.
	//
	// The owner is resolved exactly (see handleOwner), never by prefix: with children "cA" and "cA2", a prefix match let cA
	// claim sourcecA2left and reported "cA routes through 2 edges" for a correctly wired diagram.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "branch" && n.Type != "intent" {
			continue
		}
		children, _ := n.Data.Config["children"].([]any)
		if len(children) == 0 {
			continue // already reported as incomplete by Step
		}
		declared := make(map[string]bool, len(children))
		for _, raw := range children {
			child, _ := raw.(map[string]any)
			id, _ := child["id"].(string)
			if strings.TrimSpace(id) == "" {
				// Its own code: same node, same area, but a different problem and a different severity from "child has no
				// outgoing edge" - one text per code lets the client word them apart.
				add(SeverityError, CodeChildID, n.ID, n.Type, "children",
					fmt.Sprintf("%s node %s has a child without an id, so no edge can route to it", n.Type, n.ID),
					"give every child an id")
				continue
			}
			declared[id] = true
		}
		// One pass over the outgoing edges: an edge whose owner is the node itself or a declared child is that outlet's
		// route, and anything else names nothing.
		routed := make(map[string]int, len(declared))
		for _, e := range valid {
			if e.Source != n.ID {
				continue
			}
			if owner, ok := handleOwner(e.SourceHandle); ok && (owner == n.ID || declared[owner]) {
				routed[owner]++
				continue
			}
			add(SeverityError, CodeBranchOutlet, n.ID, n.Type, "edges",
				fmt.Sprintf("edge %s leaves %s node %s on handle %q, which names neither the node itself nor a declared child", e.ID, n.Type, n.ID, e.SourceHandle),
				"wire it from source<node id><pos> (the default branch) or source<children[].id><left|right>")
		}
		for _, raw := range children {
			child, _ := raw.(map[string]any)
			id, _ := child["id"].(string)
			if strings.TrimSpace(id) == "" {
				continue
			}
			switch routed[id] {
			case 0:
				// An error, not a warning: the child can never run. When its condition matches there is nowhere to go, and
				// the run reports it at that moment instead of skipping quietly (see BranchExecutor.executeBranch).
				add(SeverityError, CodeBranchEdge, n.ID, n.Type, "edges",
					fmt.Sprintf("%s node %s declares child %q but no outgoing edge starts at source%s", n.Type, n.ID, id, id),
					"create the sub-chain edge for that child, or remove the child")
			case 1:
				// The normal shape: one outlet, one route.
			default:
				add(SeverityError, CodeBranchOutlet, n.ID, n.Type, "edges",
					fmt.Sprintf("%s node %s routes child %q through %d edges; one child routes to exactly one next node (left and right are the same outlet)", n.Type, n.ID, id, routed[id]),
					"delete the extra edge: keep the one that goes where the child should continue")
			}
		}
	}

	// Control signals: break / continue are consumed by a loop, so one that no loop reaches fails the run (the runner
	// turns a leak into "a break node outside any loop is a config error", and a function body refuses it outright).
	inLoop := loopScope(doc)
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "break" && n.Type != "continue" {
			continue
		}
		if inLoop[n.ID] {
			continue
		}
		add(SeverityError, CodeControlScope, n.ID, n.Type, "",
			fmt.Sprintf("%s node %s is outside every loop, so the run fails with a config error (a function boundary also ends the loop scope)", n.Type, n.ID),
			"move it into a loop body, or delete it")
	}

	// A return belongs to a function body: that is the only consumer (function.go catches the signal), and the only shape in
	// which the node reads as "leave the function". Anywhere else the same node would end the whole flow while the run reports
	// success - a shape that reads as "leave this container" and does something else, so it is refused rather than explained
	// away. A function ancestor is the rule, so the top level counts as well: there the node is a second spell of exit.
	// create_flow / create_node already refuse to build one; this catches hand-written and imported diagrams.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "return" || hasFunctionAncestor(byID, n) {
			continue
		}
		msg := fmt.Sprintf("return node %s has no function above it, and a return is only allowed inside a function", n.ID)
		if container := nearestContainer(byID, n); container != "" {
			msg += fmt.Sprintf(": inside a %s body it would end the whole flow instead of returning from it", container)
		} else {
			msg += ": at the top level it would end the whole flow, which is what exit is for"
		}
		add(SeverityError, CodeReturnScope, n.ID, n.Type, "",
			msg,
			"delete it, move it into a function body where it really returns from the function, or use exit to end the whole flow")
	}

	// Reference checks: a dangling source_ref silently yields an empty value at run time.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		cfg := n.Data.Config
		for path, obj := range namedObjects(cfg, "inputs", "targets", "args") {
			ref, _ := obj["source_ref"].(string)
			sourceType, _ := obj["source_type"].(string)
			if sourceType != "node" || strings.TrimSpace(ref) == "" {
				continue
			}
			src := resolve(ref)
			if src == nil {
				add(SeverityError, CodeDanglingRef, n.ID, n.Type, path,
					fmt.Sprintf("%s refers to node %q, which does not exist", path, ref),
					"point source_ref at an existing node id or label")
				continue
			}
			// A node never reads itself: config.inputs / config.args are resolved before the node runs, so its own outputs
			// and locals are still empty and GetVariables fails the whole batch ("var path X not found") instead of handing
			// over a value. Only the read side: targets may point at the node itself, because a node really does write its
			// own outputs.
			//
			// The one exception is a while / do_while loop reading its own values: that condition re-resolves config.inputs
			// before every check (loop.go conditionInputs), precisely so "total < 3" can see what the body wrote back into
			// the loop's outputs / locals. It is the documented accumulate pattern and it runs, so flagging it reported a
			// runnable definition as unrunnable. count / each keep the startup snapshot, so they stay errors.
			if src.ID == n.ID && (strings.HasPrefix(path, "inputs.") || strings.HasPrefix(path, "args.")) &&
				!(strings.HasPrefix(path, "inputs.") && loopReResolvesInputs(n)) {
				add(SeverityError, CodeSelfRef, n.ID, n.Type, path,
					fmt.Sprintf("%s reads node %s itself: inputs are resolved before the node runs, so its own outputs and locals do not exist yet", path, src.ID),
					"read an upstream node's output instead; for a value that has to change between rounds use a flow variable, or write into the enclosing container's outputs / locals")
			}
			// A function node is a body, not a value producer: its params / outputs are readable only from inside that
			// body, and from outside only a call node may name it (through config.target). Any other reference reads an
			// empty value at run time, or writes into a variable nobody reads.
			if src.Type == "function" && !insideContainer(n, src.ID, byID) {
				add(SeverityError, CodeFunctionRef, n.ID, n.Type, path+".source_ref",
					fmt.Sprintf("%s refers to function node %s from outside its body", path, src.ID),
					"outside the body only a call node may name a function: use call.target and read the call node's outputs")
			}
		}
	}

	// inputs / args read a variable from a source node. The dangling check above only proves the node exists; a
	// source_key the node does not produce yields an empty value with no error at all, so it is compared here against
	// the outputs the source really has.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		for _, section := range []string{"inputs", "args"} {
			for name, raw := range objectOf(n.Data.Config[section]) {
				def := objectOf(raw)
				if def == nil {
					continue
				}
				if sourceType, _ := def["source_type"].(string); sourceType != "node" {
					continue
				}
				ref, _ := def["source_ref"].(string)
				key, _ := def["source_key"].(string)
				if strings.TrimSpace(ref) == "" || strings.TrimSpace(key) == "" {
					continue
				}
				src := resolve(ref)
				if src == nil {
					continue // the reference checks above report a missing node / a self reference
				}
				outs, known := declaredOutputs(src, resolve, n, byID)
				if !known {
					continue // a visual node has no outputs to compare against
				}
				if outs[topLevelName(key)] {
					continue
				}
				add(SeverityError, CodeSourceKey, n.ID, n.Type, section+"."+name+".source_key",
					fmt.Sprintf("%s %s reads %q from %s node %s, which produces no such output", section+"."+name, n.ID, key, src.Type, src.ID),
					"use one of: "+strings.Join(sortedNames(outs), ", "))
			}
		}
	}

	// A skip is silent by design: the run continues and nothing reaches the data channel. A downstream reader turns that
	// silence into a wrong value rather than a visible gap, so the pair is worth a warning at save time.
	checkSkippedOutputReads(doc, resolve, add)

	// call.target must be a function node in this flow.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "call" {
			continue
		}
		target, _ := n.Data.Config["target"].(string)
		if strings.TrimSpace(target) == "" {
			continue // already reported as incomplete
		}
		t := resolve(target)
		if t == nil {
			add(SeverityError, CodeCallTarget, n.ID, n.Type, "target",
				fmt.Sprintf("call node %s targets %q, which does not exist", n.ID, target),
				"use the id of a function node from read_flow")
			continue
		}
		if t.Type != "function" {
			add(SeverityError, CodeCallTarget, n.ID, n.Type, "target",
				fmt.Sprintf("call node %s targets %q, which is a %s node, not a function", n.ID, target, t.Type),
				"call can only invoke a function node")
			continue
		}
		// The arguments must match the target's params: call.go refuses an unknown arg and a missing required one
		// before the child process starts, and both sides of that comparison are in this diagram.
		checkCallArgs(n, t, add)
	}

	// A targets entry writes into a variable the container declares: miss the name and the run fails with
	// "output var X not defined" - the same shape as the source_key check, in the write direction. A missing source_key
	// means the targets key itself, which is what the executors fall back to.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		for key, raw := range objectOf(n.Data.Config["targets"]) {
			def := objectOf(raw)
			if def == nil {
				continue
			}
			if sourceType, _ := def["source_type"].(string); sourceType != "node" {
				continue
			}
			ref, _ := def["source_ref"].(string)
			container := resolve(ref)
			if container == nil || !containerTypes[container.Type] {
				continue // a dangling / non-container write target is reported elsewhere
			}
			name, _ := def["source_key"].(string)
			if strings.TrimSpace(name) == "" {
				name = key
			}
			name = topLevelName(name)
			if _, ok := objectOf(container.Data.Config["outputs"])[name]; ok {
				continue
			}
			if _, ok := objectOf(container.Data.Config["locals"])[name]; ok {
				continue
			}
			add(SeverityError, CodeTargetVar, n.ID, n.Type, "targets."+key+".source_key",
				fmt.Sprintf("targets.%s writes %q into %s node %s, which declares no such output or local", key, name, container.Type, container.ID),
				"declare it in that container's outputs (or locals), or fix the name")
		}
	}

	// each-mode loops read their array from config.inputs.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type != "loop" {
			continue
		}
		lc := objectOf(n.Data.Config["loop"])
		if t, _ := lc["type"].(string); t != "each" {
			continue
		}
		key, _ := lc["source_key"].(string)
		if strings.TrimSpace(key) == "" {
			add(SeverityError, CodeLoopSource, n.ID, n.Type, "loop.source_key",
				fmt.Sprintf("loop node %s iterates in each mode but declares no source_key", n.ID),
				"set loop.source_key to the name of an array declared in config.inputs")
			continue
		}
		if _, ok := objectOf(n.Data.Config["inputs"])[key]; !ok {
			add(SeverityError, CodeLoopSource, n.ID, n.Type, "loop.source_key",
				fmt.Sprintf("loop node %s iterates source_key %q, which is not declared in config.inputs", n.ID, key),
				"declare it in config.inputs (an array) or fix the name")
		}
	}

	// Concurrency scopes: which nodes are members, and whether anyone reads a result the join has not produced yet.
	checkConcurrencyScopes(doc, byID, resolve, add)
	checkConcurrencyOutputs(concurrencyBodies(doc), byID, resolve, add)
	checkConcurrencyFanout(doc, byID, add)

	// Unreachable nodes: no incoming edge and not the start node. A warning, because a node inside a container is
	// entered from its parent rather than by an edge, and a function node is entered by a call node's target rather
	// than by an edge - neither needs an incoming edge to run.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.Type == "start" || n.Type == "entry" || n.Type == "comment" || n.Type == "function" || n.ParentNode != "" {
			continue
		}
		if inCount[n.ID] == 0 {
			add(SeverityWarning, CodeUnreachable, n.ID, n.Type, "",
				fmt.Sprintf("node %s (%s) has no incoming edge and never runs", n.ID, n.Type),
				"wire it into the chain, or delete it")
		}
	}

	sortFindings(out)
	return out, nil
}

// declaredOutputs returns the output variables a node can hand downstream: the ones it declares in config.outputs plus
// the fixed ones its executor writes. ok is false when this package cannot judge the type (a visual node), so the caller
// stays silent instead of guessing.
//
// A call node is the exception twice over: it mirrors the target function's outputs (SetOutputVarsFrom replaces the whole
// map) and its own copy is only a front-end convenience, so the function's declaration is the truth.
//
// A function node has a second readable set: a step inside its body reads a parameter through the function node itself
// (source_type=node, source_ref=<function id>), so params count as readable - but only for a reader inside that body.
// Handing params to an outside reader would hide a real mistake: the params exist only while the call runs.
//
// A loop node has the same second set: the variables it publishes each round (index / item / total) are declared in
// config.loop_vars, and a step inside the body reads them through the loop node itself. Outside the body they are not
// readable either - after the loop they only hold the last round, so such a reference is always an accident.
func declaredOutputs(n *node, resolve func(string) *node, reader *node, byID map[string]*node) (map[string]bool, bool) {
	caps, known := CapabilitiesOf(n.Type)
	if !known {
		return nil, false
	}
	out := make(map[string]bool)
	for _, name := range caps.FixedOutputs {
		out[name] = true
	}
	if n.Type == "call" {
		target, _ := n.Data.Config["target"].(string)
		fn := resolve(target)
		if fn == nil || fn.Type != "function" {
			return nil, false // call_target reports this
		}
		for name := range objectOf(fn.Data.Config["outputs"]) {
			out[name] = true
		}
		return out, true
	}
	if n.Type == "function" && reader != nil && insideContainer(reader, n.ID, byID) {
		for name := range objectOf(n.Data.Config["params"]) {
			out[name] = true
		}
	}
	// Same shape for a loop: index / item / total live in config.loop_vars and are readable only inside the body.
	if n.Type == "loop" && reader != nil && insideContainer(reader, n.ID, byID) {
		for name := range objectOf(n.Data.Config["loop_vars"]) {
			out[name] = true
		}
	}
	// A while / do_while loop reading itself in its condition is served by conditionInputs, which merges the loop's own
	// outputs, locals and loop vars - all three are readable names there, so the source_key check must accept them too.
	if n.Type == "loop" && reader != nil && reader.ID == n.ID && loopReResolvesInputs(n) {
		for _, section := range []string{"locals", "loop_vars"} {
			for name := range objectOf(n.Data.Config[section]) {
				out[name] = true
			}
		}
	}
	for name := range objectOf(n.Data.Config["outputs"]) {
		out[name] = true
	}
	return out, true
}

// loopReResolvesInputs reports whether a loop re-resolves config.inputs on every condition check: while / do_while do (the
// condition has to see what the body wrote back), count / each keep the snapshot taken when the node started.
func loopReResolvesInputs(n *node) bool {
	if n.Type != "loop" {
		return false
	}
	switch t, _ := objectOf(n.Data.Config["loop"])["type"].(string); t {
	case "while", "do_while":
		return true
	}
	return false
}

// insideContainer reports whether reader sits somewhere below containerID (a direct child or deeper).
func insideContainer(reader *node, containerID string, byID map[string]*node) bool {
	for id := reader.ParentNode; id != ""; {
		if id == containerID {
			return true
		}
		parent, ok := byID[id]
		if !ok {
			return false
		}
		id = parent.ParentNode
	}
	return false
}

// declaresAnyVariable reports whether a code step declares at least one writable variable. The script may return a key
// only if it is declared in outputs, targets or locals, so a step carrying any of the three is complete.
//
// An explicit (even empty) config.outputs counts as declared: the author wrote the block, and the finding is about the
// missing block, not about its content.
func declaresAnyVariable(cfg map[string]any) bool {
	if _, ok := cfg["outputs"].(map[string]any); ok {
		return true
	}
	for _, key := range []string{"targets", "locals"} {
		if len(objectOf(cfg[key])) > 0 {
			return true
		}
	}
	return false
}

// topLevelName takes the top-level name of a variable path: "a.b" / "a[0]" -> "a" (the same rule the executors use).
func topLevelName(path string) string {
	name := strings.Split(path, ".")[0]
	return strings.Split(name, "[")[0]
}

// sortedNames returns the keys of a set in a stable order, so a hint does not change between two runs.
func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkCallArgs compares a call node's args with the target function's params.
//
// call.go refuses an unknown arg and a missing required one before the child process starts, and both lists live in this
// diagram, so the run must not be the first place the mismatch shows up. `required` defaults to true, as in the executor.
func checkCallArgs(call, fn *node, add func(severity, code, nodeID, nodeType, path, msg, hint string)) {
	params := objectOf(fn.Data.Config["params"])
	args := objectOf(call.Data.Config["args"])
	for name := range args {
		if _, ok := params[name]; !ok {
			add(SeverityError, CodeCallArgs, call.ID, call.Type, "args."+name,
				fmt.Sprintf("call node %s passes arg %q, which function %s does not declare in its params", call.ID, name, fn.ID),
				"declare it in the function's params, or remove it from args")
		}
	}
	for name, raw := range params {
		required := true
		if v, ok := objectOf(raw)["required"].(bool); ok {
			required = v
		}
		if _, ok := args[name]; !ok && required {
			add(SeverityError, CodeCallArgs, call.ID, call.Type, "args."+name,
				fmt.Sprintf("call node %s passes no %q, a required param of function %s", call.ID, name, fn.ID),
				"pass it in args, or mark the param optional (required: false)")
		}
	}
}

// handleOwner resolves the id a source_handle names. A handle is source<id><left|right|top|bottom>, so exactly one position
// suffix is stripped and the rest is the owner - the same rule the authoring tools use (see the tools' splitHandle), which is
// what keeps validation and the runner agreeing with each other.
//
// A bare prefix match is not good enough: with children "cA" and "cA2", sourcecA2left starts with "sourcecA", so cA would
// claim the longer child's edge - validation counted two routes for cA, and the runner could send cA down cA2's chain
// depending on edge order. A handle without any position suffix keeps the whole remainder as the owner, so a hand-written
// diagram still resolves instead of failing on a shape the canvas never produces.
func handleOwner(handle string) (string, bool) {
	body, ok := strings.CutPrefix(handle, "source")
	if !ok || body == "" {
		return "", false
	}
	for _, pos := range []string{"left", "right", "top", "bottom"} {
		if owner, cut := strings.CutSuffix(body, pos); cut && owner != "" {
			return owner, true
		}
	}
	return body, true
}

// loopScope marks every node a loop can consume a break / continue from: the loop body, the body of a process nested in
// that body, and whatever the chains inside them lead to.
//
// A function body is a separate invocation - function.go turns a leaking break / continue into a config error even when
// the call site sits inside a loop - so it is not entered. The loop's own outgoing edge runs after the loop, so it is not
// body either.
func loopScope(doc *diagram) map[string]bool {
	children := make(map[string][]string, len(doc.Nodes))
	byID := make(map[string]*node, len(doc.Nodes))
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.ID == "" {
			continue
		}
		byID[n.ID] = n
		if n.ParentNode != "" {
			children[n.ParentNode] = append(children[n.ParentNode], n.ID)
		}
	}
	next := make(map[string][]string, len(doc.Edges))
	for _, e := range doc.Edges {
		next[e.Source] = append(next[e.Source], e.Target)
	}

	in := make(map[string]bool, len(doc.Nodes))
	var queue []string
	push := func(id string) {
		if id == "" || in[id] {
			return
		}
		in[id] = true
		queue = append(queue, id)
	}
	for i := range doc.Nodes {
		if doc.Nodes[i].Type == "loop" {
			push(doc.Nodes[i].ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		n := byID[id]
		if n != nil && (n.Type == "loop" || n.Type == "process") {
			for _, c := range children[id] {
				push(c)
			}
		}
		if n != nil && n.Type == "loop" {
			continue // the loop's outgoing edge runs after the loop, not inside it
		}
		for _, t := range next[id] {
			push(t)
		}
	}
	return in
}

// concurrencyBody is one host body's entry chain, in walk order. The entry node itself is not part of it: it is the
// container's inlet, not a step.
type concurrencyBody struct {
	host    *node
	order   []string
	index   map[string]int
	members map[string]bool
	child   map[string]bool
}

// concurrencyBodies builds the entry chain of every host body. The walk stops at a node that is not a child of the host
// (a branch lane), at a branch / intent (they terminate the chain) and at a fork in the edges, which the runner itself
// rejects anyway.
func concurrencyBodies(doc *diagram) map[string]*concurrencyBody {
	children := make(map[string][]*node, len(doc.Nodes))
	byID := make(map[string]*node, len(doc.Nodes))
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if n.ID == "" {
			continue
		}
		byID[n.ID] = n
		if n.ParentNode != "" {
			children[n.ParentNode] = append(children[n.ParentNode], n)
		}
	}
	next := make(map[string][]string, len(doc.Edges))
	for _, e := range doc.Edges {
		next[e.Source] = append(next[e.Source], e.Target)
	}

	bodies := make(map[string]*concurrencyBody)
	for i := range doc.Nodes {
		host := &doc.Nodes[i]
		if host.Type != "process" && host.Type != "loop" {
			continue
		}
		body := &concurrencyBody{
			host:    host,
			index:   make(map[string]int),
			members: make(map[string]bool),
			child:   make(map[string]bool),
		}
		cur := ""
		for _, c := range children[host.ID] {
			body.child[c.ID] = true
			if c.Type == "entry" && len(next[c.ID]) > 0 {
				cur = next[c.ID][0]
			}
		}
		for cur != "" && body.child[cur] {
			if _, seen := body.index[cur]; seen {
				break
			}
			body.index[cur] = len(body.order)
			body.order = append(body.order, cur)
			n := byID[cur]
			if n == nil {
				break
			}
			if n.Data.Concurrent {
				body.members[cur] = true
			}
			if n.Type == "branch" || n.Type == "intent" || len(next[cur]) != 1 {
				break
			}
			cur = next[cur][0]
		}
		bodies[host.ID] = body
	}
	return bodies
}

// checkConcurrencyScopes enforces the four static rules of the scope model: a member's parent must be a host, a member
// must sit on the body's entry chain, a member may write only its own variables and the host's outputs, and nobody may
// read a value whose producer has not finished.
//
// All four exist because the failure they prevent is invisible at run time: the flow reports success while a variable it
// read was still being written, or a member's result silently never crossed the join.
func checkConcurrencyScopes(doc *diagram, byID map[string]*node, resolve func(string) *node,
	add func(severity, code, nodeID, nodeType, path, msg, hint string)) {
	bodies := concurrencyBodies(doc)

	// 1. Membership: the parent is a host, and the member is on that host's entry chain.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if !n.Data.Concurrent {
			continue
		}
		parent := byID[n.ParentNode]
		if parent == nil || (parent.Type != "process" && parent.Type != "loop") {
			add(SeverityError, CodeConcurrentMemberParent, n.ID, n.Type, "concurrent",
				fmt.Sprintf("node %s is marked concurrent but its parent is not a concurrency host: only a process or loop body is a scope", n.ID),
				"move the node into a process (or loop) body, or clear the concurrent flag")
			continue
		}
		if nonMemberTypes[n.Type] {
			add(SeverityError, CodeConcurrentMemberType, n.ID, n.Type, "concurrent",
				fmt.Sprintf("node %s (%s) cannot be a concurrent member: control nodes express control flow on the body's chain, annotations never execute, result / progress write the run's output in chain order, and assign / memory never wait so a slot buys nothing", n.ID, n.Type),
				"remove data.concurrent from this node; a step that never waits runs inline on the chain, and the host's outputs stay reachable through targets")
			continue
		}
		body := bodies[parent.ID]
		if body == nil || !body.members[n.ID] {
			add(SeverityError, CodeConcurrentMemberOnChain, n.ID, n.Type, "concurrent",
				fmt.Sprintf("node %s is marked concurrent but is not on the entry chain of %s %s, so no scope ever dispatches it", n.ID, parent.Type, parent.ID),
				"a member must be reachable from the body's entry along the chain; wrap a sub-flow in its own process instead")
		}
	}

	// 2. Write side: a member writes only its own outputs / locals and the host's outputs.
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if !n.Data.Concurrent {
			continue
		}
		host := byID[n.ParentNode]
		for key, raw := range objectOf(n.Data.Config["targets"]) {
			def := objectOf(raw)
			if def == nil {
				continue
			}
			sourceType, _ := def["source_type"].(string)
			switch sourceType {
			case "flow":
				add(SeverityError, CodeConcurrentMemberWrite, n.ID, n.Type, "targets."+key,
					fmt.Sprintf("member %s writes flow variable %q: flow variables are shared and unsynchronised, so a concurrent write is last-write-wins", n.ID, key),
					"write into the host's outputs through a node source instead, or collect after the host")
			case "node":
				ref, _ := def["source_ref"].(string)
				if strings.TrimSpace(ref) == "" {
					continue // own outputs, resolved from the targets key
				}
				target := resolve(ref)
				if target == nil {
					continue // dangling, reported as dangling_ref
				}
				if target.ID == n.ID {
					continue
				}
				if host != nil && target.ID == host.ID {
					continue
				}
				add(SeverityError, CodeConcurrentMemberWrite, n.ID, n.Type, "targets."+key+".source_ref",
					fmt.Sprintf("member %s writes into %s node %s: a member may write only its own variables and its host's outputs", n.ID, target.Type, target.ID),
					"point source_ref at the host (its outputs are the scope's only result channel) or at the member itself")
			}
		}
	}

	// 3. Read side: a node reads only producers that already finished. Inside one body that means earlier chain nodes
	// that are not members - a member never reads a sibling member, and no chain node reads a member.
	for _, body := range bodies {
		for i, id := range body.order {
			n := byID[id]
			if n == nil {
				continue
			}
			for path, obj := range namedObjects(n.Data.Config, "inputs") {
				if st, _ := obj["source_type"].(string); st != "node" {
					continue
				}
				ref, _ := obj["source_ref"].(string)
				src := resolve(ref)
				if src == nil {
					continue
				}
				j, sameBody := body.index[src.ID]
				if !sameBody {
					continue // an ancestor body already executed; another body is a separate check
				}
				if body.members[src.ID] {
					add(SeverityError, CodeParallelVisibility, n.ID, n.Type, path+".source_ref",
						fmt.Sprintf("node %s reads %s, a member of the same body: a member's result is readable only after the body's join, through the host's outputs", n.ID, src.ID),
						"write the member's value into the host's outputs and read it after the host, or put the consumer in a second host")
					continue
				}
				if j >= i {
					add(SeverityError, CodeParallelVisibility, n.ID, n.Type, path+".source_ref",
						fmt.Sprintf("node %s reads %s, which runs later in the same body: the value is not written yet", n.ID, src.ID),
						"read an earlier chain node, or move this node after its producer")
				}
			}
		}
	}
}

// checkConcurrencyOutputs reports two writers to one host output when at least one of them is a member, and a scope that
// can never run two members at once.
//
// Both are about the same false green. The host's outputs are the only channel out of the body, so a second writer
// silently drops one result - precisely the last-write-wins behaviour the scope replaces. And a scope with one member, or
// a limit of 1, reads as "runs concurrently" while running strictly serially.
func checkConcurrencyOutputs(bodies map[string]*concurrencyBody, byID map[string]*node, resolve func(string) *node,
	add func(severity, code, nodeID, nodeType, path, msg, hint string)) {
	for _, body := range bodies {
		host := body.host
		writers := make(map[string][]string)
		var order []string
		for _, id := range body.order {
			n := byID[id]
			if n == nil {
				continue
			}
			for key, raw := range objectOf(n.Data.Config["targets"]) {
				def := objectOf(raw)
				if def == nil {
					continue
				}
				if st, _ := def["source_type"].(string); st != "node" {
					continue
				}
				ref, _ := def["source_ref"].(string)
				if strings.TrimSpace(ref) == "" {
					continue
				}
				target := resolve(ref)
				if target == nil || target.ID != host.ID {
					continue
				}
				if _, seen := writers[key]; !seen {
					order = append(order, key)
				}
				writers[key] = append(writers[key], n.ID)
			}
		}
		for _, name := range order {
			ids := writers[name]
			if len(ids) < 2 {
				continue
			}
			hasMember := false
			for _, id := range ids {
				if body.members[id] {
					hasMember = true
					break
				}
			}
			if !hasMember {
				continue // two serial steps writing one output is the pre-existing behaviour, not this model's business
			}
			writer := byID[ids[0]]
			writerType := ""
			if writer != nil {
				writerType = writer.Type
			}
			add(SeverityError, CodeParallelOutputConflict, ids[0], writerType, "targets."+name,
				fmt.Sprintf("host output %q of %s is written by %s: only one value can survive the join, so the others are lost silently", name, host.ID, strings.Join(ids, ", ")),
				"give each producer its own output and merge after the host, or let exactly one node write this output")
		}

		limit := parallelLimit(host)
		switch {
		case len(body.members) == 0:
		case len(body.members) == 1:
			add(SeverityWarning, CodeParallelDegenerate, host.ID, host.Type, "concurrent",
				fmt.Sprintf("%s %s dispatches exactly one member, so nothing runs concurrently", host.Type, host.ID),
				"mark at least two direct children as concurrent members, or drop the flag and run those steps serially")
		case limit == 1:
			add(SeverityWarning, CodeParallelDegenerate, host.ID, host.Type, "parallel.max_concurrency",
				fmt.Sprintf("%s %s has max_concurrency=1, so its %d members run one at a time", host.Type, host.ID, len(body.members)),
				"raise max_concurrency to at least 2, or leave it unset for unlimited")
		}
	}
}

// parallelLimit reads config.parallel.max_concurrency. 0 means unlimited (absent, 0 or a negative value), which is the
// default and therefore not something to warn about.
func parallelLimit(n *node) int {
	if n == nil {
		return 0
	}
	p := objectOf(n.Data.Config["parallel"])
	if p == nil {
		return 0
	}
	switch v := p["max_concurrency"].(type) {
	case float64:
		if int(v) > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return 0
}

// checkConcurrencyFanout warns about scopes that multiply rather than add: three levels of nesting, or a chain of limited
// hosts whose limits multiply past 8. Both are legitimate shapes, so this stays a warning - but the cost of a depth-4
// diagram is not visible on the canvas, and "at most 4" per level reads as 4 nodes in flight while the innermost level can
// see 64.
//
// Only limits are multiplied: an unlimited level already has no ceiling, so a product through it says nothing and warning
// on the default (unlimited) would fire on every nested host.
func checkConcurrencyFanout(doc *diagram, byID map[string]*node,
	add func(severity, code, nodeID, nodeType, path, msg, hint string)) {
	for i := range doc.Nodes {
		host := &doc.Nodes[i]
		if host.Type != "process" && host.Type != "loop" {
			continue
		}
		depth := 1
		product := parallelLimit(host)
		allLimited := product > 0
		var innerToOuter []string
		for cur := byID[host.ParentNode]; cur != nil; cur = byID[cur.ParentNode] {
			if cur.Type != "process" && cur.Type != "loop" {
				continue
			}
			depth++
			innerToOuter = append(innerToOuter, cur.ID)
			if lim := parallelLimit(cur); lim > 0 {
				product *= lim
			} else {
				allLimited = false
			}
		}
		ancestors := make([]string, 0, len(innerToOuter))
		for j := len(innerToOuter) - 1; j >= 0; j-- {
			ancestors = append(ancestors, innerToOuter[j])
		}
		if depth >= 3 {
			add(SeverityWarning, CodeParallelFanout, host.ID, host.Type, "parallel",
				fmt.Sprintf("%s %s sits at nesting depth %d (inside %s): every level multiplies the nodes in flight, and a failure has to unwind through all of them",
					host.Type, host.ID, depth, strings.Join(ancestors, " > ")),
				"flatten the nesting by moving the inner work into the outer body, or keep the depth and check the limit at each level")
		}
		if depth > 1 && allLimited && product > 8 {
			add(SeverityWarning, CodeParallelFanout, host.ID, host.Type, "parallel.max_concurrency",
				fmt.Sprintf("%s %s is nested inside %s and their max_concurrency multiply to %d: up to %d members can be in flight at once",
					host.Type, host.ID, strings.Join(ancestors, " > "), product, product),
				"lower one of the limits, or leave a level unlimited only if the run can afford that many members")
		}
	}
}

// namedObjects returns "<section>.<name>" -> object for the given config sections, so a path in a Finding matches what the
// caller has to edit.
func namedObjects(cfg map[string]any, sections ...string) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for _, s := range sections {
		for name, raw := range objectOf(cfg[s]) {
			if obj := objectOf(raw); obj != nil {
				out[s+"."+name] = obj
			}
		}
	}
	return out
}

func parse(raw json.RawMessage) (*diagram, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("the diagram is empty")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("the diagram string does not parse: %w", err)
		}
		raw = []byte(s)
	}
	var doc diagram
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the diagram is not a JSON object with a nodes array: %w", err)
	}
	return &doc, nil
}

// sortFindings puts errors first, then orders by node id so a report of the same diagram is stable.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity == SeverityError
		}
		if fs[i].NodeID != fs[j].NodeID {
			return fs[i].NodeID < fs[j].NodeID
		}
		return fs[i].Path < fs[j].Path
	})
}

// Summarize counts findings by severity.
func Summarize(fs []Finding) (errors, warnings int) {
	for _, f := range fs {
		if f.Severity == SeverityError {
			errors++
			continue
		}
		warnings++
	}
	return errors, warnings
}

// ScopeOverCeiling reports bodies that declare more concurrent members than the tenant's per-scope ceiling.
//
// This is the one parallel_* rule that cannot live inside Diagram: every other rule is a property of the diagram alone,
// while this one compares the diagram with the tenant's configured ceiling. The run still succeeds - the extra members
// wait for a slot - but the canvas reads as "all of them at once", and that difference is what the author needs to know
// before blaming their prompt for a slower run.
func ScopeOverCeiling(raw json.RawMessage, ceiling int) []Finding {
	if ceiling <= 0 {
		return nil
	}
	doc, err := parse(raw)
	if err != nil {
		return nil
	}
	bodies := concurrencyBodies(doc)
	ids := make([]string, 0, len(bodies))
	for id := range bodies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Finding, 0, len(ids))
	for _, id := range ids {
		body := bodies[id]
		if body == nil || body.host == nil || len(body.members) <= ceiling {
			continue
		}
		out = append(out, Finding{
			Severity: SeverityWarning,
			Code:     CodeParallelMembersQueued,
			NodeID:   body.host.ID,
			NodeType: body.host.Type,
			Path:     "concurrent",
			Message: fmt.Sprintf("%s %s has %d concurrent members, more than the tenant ceiling %d: the extra members wait for a slot instead of running at once",
				body.host.Type, body.host.ID, len(body.members), ceiling),
			Hint: "reduce the members, raise the ceiling (platform settings, max_concurrency), or accept the queue - the run still succeeds",
		})
	}
	return out
}
