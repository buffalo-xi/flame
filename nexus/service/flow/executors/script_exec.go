package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/capability"
	"flame/nexus/shared/capabilitypolicy"
	"flame/tools/code"
)

// The global script client, initialised through SetCodeClient.
//
// It is only the connection configuration to sandboxd (no pool): the pool and the isolation live on the
// sandboxd side.
var globalCodeClient *code.Client

// SetCodeClient sets the global script execution client
func SetCodeClient(client *code.Client) {
	globalCodeClient = client
}

// capabilitySnapshot is the platform capability snapshot (replaceable in tests, so no DB dependency).
// It shares its source with the agent tool side: capabilitypolicy.Snapshot is fail-closed.
var capabilitySnapshot = capabilitypolicy.Snapshot

// capabilityError means the platform capability is not granted.
//
// It is a type of its own rather than reusing code.Error: it has to be classified as
// flow.ErrCodeCapability, and blending into "the script failed" would make on_error rules treat it as a
// script problem (retryable, look at the traceback) when the right move is to ask a platform administrator.
type capabilityError struct{ capability string }

func (e *capabilityError) Error() string {
	// An error from a flow node belongs to the execution layer, so ShortLabel is used: the scenario
	// enumeration ("workflow code node / branch / binding") is not stuffed into the message (see the Label
	// comment in capability.go).
	return "the platform has not granted capability " + e.capability +
		" (" + capability.ShortLabel(e.capability) + ") to this tenant; a platform administrator has to enable it under Plugins & capabilities"
}

// ensureCodeCapability is the platform capability admission for a workflow code node.
//
// Why it has to be here: a workflow goes through the /v1/code channel (a server-hosted interpreter), which
// is not the same channel as the agent's exec tool, and there used to be **no capability gate at all** —
// after the platform disabled the execution capability an agent could not run a command, while a code node
// inside a flow still ran arbitrary Python. This gate closes that half.
//
// An empty schema (unit tests / calls without tenant semantics) passes: without a tenant there is no
// platform policy to speak of, matching the caps == nil rule in toolgrant.
func ensureCodeCapability(schema string) error {
	if schema == "" {
		return nil
	}
	if capabilitySnapshot(schema)[capability.Code] {
		return nil
	}
	return &capabilityError{capability: capability.Code}
}

// schemaOf takes the tenant schema from a node result (an empty string when ExecContext is nil = no
// capability decision).
func schemaOf(nr *flow.NodeResult) string {
	if nr == nil || nr.Ctx == nil {
		return ""
	}
	return nr.Ctx.Schema
}

// scriptNodeConfig is the shared config of the script-like nodes (code / result / progress).
type scriptNodeConfig struct {
	Lang    string
	Script  string
	Timeout int
	Inputs  map[string]interface{}
	// Category is used by the progress node only: the emitted category text | thinking, text by default.
	Category string
}

// parseScriptNode parses the shared config of a script-like node: config.code (lang/script/timeout) plus
// config.inputs, and the progress-specific node-level config.category.
// The three nodes are structurally the same and differ only in "how the result lands", so there is no
// reason to write the parsing three times. On failure the node is already finished through nr, so the
// caller can simply return.
func parseScriptNode(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) (*scriptNodeConfig, error) {
	if node.Data == nil || node.Data.Config == nil {
		return nil, failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	codeData, ok := node.Data.Config["code"].(map[string]interface{})
	if !ok {
		return nil, failf(nr, flow.ErrCodeConfig, "code config not found")
	}

	inputs := make(map[string]interface{})
	if raw, exists := node.Data.Config["inputs"].(map[string]interface{}); exists {
		resolved, err := ctx.GetVariables(raw, processID)
		if err != nil {
			return nil, failf(nr, flow.ErrCodeConfig, err.Error())
		}
		inputs = resolved
	}
	nr.SetInputVars(inputs)

	script, ok := codeData["script"].(string)
	if !ok {
		return nil, failf(nr, flow.ErrCodeConfig, "script not found")
	}

	lang, _ := codeData["lang"].(string)
	if lang == "" {
		lang = "lua"
	}

	timeout := ctx.ResolveScriptTimeout(lang, parseTimeoutConfig(codeData["timeout"]))

	// category is the progress node's own output behaviour and no longer sits inside the code script block
	category, _ := node.Data.Config["category"].(string)
	if category == "" {
		category = "text"
	}

	return &scriptNodeConfig{
		Lang:     lang,
		Script:   script,
		Timeout:  timeout,
		Inputs:   inputs,
		Category: category,
	}, nil
}

// runScript runs a node script.
// With templateAsString true the template language returns the rendered string directly (result / progress
// only need text and skip the JSON decode); with false it goes through ExecuteScript and keeps the
// structured return value (the code node).
// Errors are already finished under the shared rules, so the caller can simply return.
func runScript(ctx *flow.ExecContext, nr *flow.NodeResult, cfg *scriptNodeConfig, templateAsString bool) (any, error) {
	if templateAsString && cfg.Lang == "template" {
		tmplCtx := BuildScriptTemplateContext(nr, cfg.Inputs)
		rendered, err := RenderTemplate(cfg.Script, tmplCtx)
		if err != nil {
			return nil, failf(nr, flow.ErrCodeScript, err.Error())
		}
		return rendered, nil
	}

	result, err := ExecuteScript(ctx.StdCtx, cfg.Lang, cfg.Script, cfg.Inputs, nr, cfg.Timeout)
	if err != nil {
		return nil, finishScriptError(ctx, nr, err)
	}
	return result, nil
}

// finishScriptError maps a script execution error to a node error code and finishes the node.
//
// The three script-like nodes code / result / progress share this mapping: the same class of error has to
// get the same code in whichever node it happens, otherwise a retry/skip configured by code in on_error
// would stop working depending on the node type. The return value is always an *ExecError, so the runner's
// shouldBypassOnError can read the code.
func finishScriptError(ctx *flow.ExecContext, nr *flow.NodeResult, err error) error {
	var capErr *capabilityError
	if errors.As(err, &capErr) {
		// The platform capability is not granted: a separate code from a script failure (retrying is
		// useless, the administrator has to enable it)
		return failf(nr, flow.ErrCodeCapability, err.Error())
	}

	var scriptErr *code.Error
	if !errors.As(err, &scriptErr) {
		return failf(nr, flow.ErrCodeInternal, err.Error())
	}

	switch scriptErr.Code {
	case code.ErrCodeTimeout:
		return failf(nr, flow.ErrCodeTimeout, err.Error())
	case code.ErrCodeBusy:
		// Rejected because the pool is full: the script never ran, so a retry is safe — a separate code so
		// on_error.retry can match it
		return failf(nr, flow.ErrCodeBusy, err.Error())
	case code.ErrCodeScriptError:
		return failf(nr, flow.ErrCodeScript, err.Error())
	case code.ErrCodeCancel:
		// Cancellation errors must distinguish "the user stopped it" from "the workflow timed out": use ctx
		// rather than matching the message text, since the two allow different actions in on_error.
		errCode := flow.ErrCodeCancel
		if ctx != nil && ctx.StdCtx.Err() == context.DeadlineExceeded {
			errCode = flow.ErrCodeTimeout
		}
		nr.Finish(flow.StatusCancelled, errCode, err.Error())
		return newExecError(nr.ErrCode, nr.ErrMsg)
	case code.ErrCodeConnectFail,
		code.ErrCodeWorkerComm,
		code.ErrCodeSendFail:
		// A connection problem with the sandbox / worker: usually fixed by trying again, so it has to be
		// classified as connection for on_error.retry to match. Classified as internal, shouldBypassOnError
		// would return it outright, locking down the error that most deserves a retry.
		return failf(nr, flow.ErrCodeConnection, err.Error())
	case code.ErrCodeUnsupportedLang:
		// The language the user picked is not supported by the platform: a configuration problem, fixed by
		// changing the config (config also bypasses on_error)
		return failf(nr, flow.ErrCodeConfig, err.Error())
	case code.ErrCodeUnknownAction:
		// The sandbox returned an unexpected action: a protocol problem on the platform side, classified as
		// other so it can at least reach on_error
		return failf(nr, flow.ErrCodeOther, err.Error())
	default:
		return failf(nr, flow.ErrCodeOther, err.Error())
	}
}

// scriptErrCode normalises a script execution error into a flow error code.
//
// Only "the sandbox is busy" is singled out: it is a retryable transient rejection (the script never ran)
// and completely different from "the script itself failed" — blended into script, a retry keyed on the code
// would never match (the code values come from sandboxd, see ErrCodeBusy in tools/code).
func scriptErrCode(err error) string {
	var ce *capabilityError
	if errors.As(err, &ce) {
		return flow.ErrCodeCapability
	}
	var se *code.Error
	if errors.As(err, &se) && se.Code == code.ErrCodeBusy {
		return flow.ErrCodeBusy
	}
	return flow.ErrCodeScript
}

// ExecuteScript runs a script: a pure "input -> return" with no callback side effects.
//
// template does not go through sandboxd, it is rendered in pure Go; every other language is delegated to
// the global script client.
func ExecuteScript(ctx context.Context, lang, scriptTxt string,
	inputs map[string]any, nr *flow.NodeResult, timeout int) (any, error) {

	if lang == "template" {
		ctxData := BuildScriptTemplateContext(nr, inputs)
		result, err := ExecuteTemplateScript(scriptTxt, ctxData)
		if err != nil {
			return nil, code.NewError(code.ErrCodeScriptError, err.Error(), err)
		}
		return result, nil
	}

	// The platform capability gate: template is pure Go rendering (it does not touch the sandbox and
	// returned above), every other language has to pass. It lives here because every script entry (code /
	// result / progress / branch / loop / binding / the handlers and posts of each node) funnels into this
	// function — one missed spot would mean no gate at all.
	if err := ensureCodeCapability(schemaOf(nr)); err != nil {
		return nil, err
	}

	if globalCodeClient == nil {
		return nil, errors.New("code client not initialized")
	}
	r, err := globalCodeClient.Execute(ctx, lang, scriptTxt, inputs, timeout)
	if err != nil {
		// The full stderr on error (including the traceback) goes into the node log, not into the agent
		// context.
		var se *code.Error
		if errors.As(err, &se) && se.Logs != "" {
			nr.AddLogf("script stderr:\n%s", se.Logs)
		}
		return nil, err
	}
	// print output goes into the node log; the data a machine needs still comes from return (r.Value).
	if r != nil && r.Logs != "" {
		nr.AddLogf("script output:\n%s", r.Logs)
	}
	return r.Value, nil
}

// ---------- dispatch the return value by declaration (outputs / targets / locals) ----------

// topLevelName takes the top-level name of a variable path: "a.b" / "a[0]" -> "a"
func topLevelName(path string) string {
	name := strings.Split(path, ".")[0]
	return strings.Split(name, "[")[0]
}

// outputDecls is the set of declared output variable names (OutputVars is pre-filled by InitOutputVars).
func outputDecls(nr *flow.NodeResult) map[string]bool {
	set := make(map[string]bool, len(nr.OutputVars))
	for k := range nr.OutputVars {
		set[k] = true
	}
	return set
}

// localDecls is the set of declared local variable names (LocalVars is pre-filled by InitLocalVars).
func localDecls(nr *flow.NodeResult) map[string]bool {
	set := make(map[string]bool, len(nr.LocalVars))
	for k := range nr.LocalVars {
		set[k] = true
	}
	return set
}

// targetDecls reads the set of variable names declared in the node config's targets.
func targetDecls(nr *flow.NodeResult) map[string]bool {
	set := make(map[string]bool)
	if nr.Ctx == nil || nr.Ctx.Flow == nil || nr.Ctx.Flow.NodeMap == nil {
		return set
	}
	node := nr.Ctx.Flow.NodeMap[nr.NodeID]
	if node == nil || node.Data == nil || node.Data.Config == nil {
		return set
	}
	targets, ok := node.Data.Config["targets"].(map[string]interface{})
	if !ok {
		return set
	}
	for k := range targets {
		set[k] = true
	}
	return set
}

// checkDeclConflict requires the three declaration sets to be pairwise disjoint.
func checkDeclConflict(outs, targets, locals map[string]bool) error {
	if c := intersect(outs, targets); len(c) > 0 {
		return fmt.Errorf("the variable is declared in both outputs and targets: %v", c)
	}
	if c := intersect(outs, locals); len(c) > 0 {
		return fmt.Errorf("the variable is declared in both outputs and locals: %v", c)
	}
	if c := intersect(targets, locals); len(c) > 0 {
		return fmt.Errorf("the variable is declared in both targets and locals: %v", c)
	}
	return nil
}

func intersect(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if b[k] {
			out = append(out, k)
		}
	}
	return out
}

// applyScriptResult dispatches the map a script returned onto output / target / local by the node's
// declarations.
//
// A script is a stateless pure function: every key it returns is the name of a "declared variable" and the
// engine consults the declarations to route it to the right landing spot. An undeclared key is an error
// (fail-fast, so a mistyped variable name cannot slip through).
func applyScriptResult(nr *flow.NodeResult, result any) error {
	m, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("the script must return a map, got %T", result)
	}
	outs, targets, locals := outputDecls(nr), targetDecls(nr), localDecls(nr)
	if err := checkDeclConflict(outs, targets, locals); err != nil {
		return err
	}
	for key, value := range m {
		root := topLevelName(key)
		switch {
		case outs[root]:
			if err := nr.SetOutputVarWithFire(key, value); err != nil {
				return err
			}
		case targets[root]:
			if err := applyTargetVar(nr, key, value); err != nil {
				return err
			}
		case locals[root]:
			if err := nr.SetLocalVar(key, value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the script returned an undeclared variable: %s", key)
		}
	}
	return nil
}

// applyTargetVar lands the return value on the target declared in targets.
func applyTargetVar(nr *flow.NodeResult, path string, value any) error {
	def, err := nr.GetTargetDef(path)
	if err != nil {
		return err
	}
	target, ok := def.(map[string]interface{})
	if !ok {
		return errors.New("invalid target definition")
	}
	// Copy the target before changing source_key: GetTargetDef returns a map reference from the node
	// config, and changing it in place would pollute the config (the old implementation ran into exactly
	// that).
	tgt := make(map[string]interface{}, len(target))
	for k, v := range target {
		tgt[k] = v
	}
	sourceKey, _ := tgt["source_key"].(string)
	if firstDot := strings.IndexAny(path, ".["); firstDot != -1 {
		tgt["source_key"] = sourceKey + path[firstDot:]
	}
	return nr.Ctx.SetVariableWithFire(tgt, value, nr.ProcessID)
}

// applyHandlerAppend writes a handler script's per-chunk return value back to the custom output variables
// with "append" semantics.
//
// The handler of a streaming node (LLM / HTTP / RPC / MCP) is called once per chunk and the script is a
// stateless pure transform: a returned string field is appended to the current value while a non-string
// field overwrites it — matching AppendOutputVarWithFire.
//
// The names in deny are the node's fixed output variables (RPC's result/progress/error and so on): the
// executor writes those itself and a script may not. The reason is that a string appends, so once the
// executor has written one a script write would only concatenate a second copy and "is this variable the
// final result or a concatenation of fragments" would have no answer. A hit is logged and skipped, so the
// script author does not believe the write took effect. nil means unrestricted (a node that has not been
// tightened yet).
func applyHandlerAppend(nr *flow.NodeResult, result any, deny map[string]struct{}) error {
	m, ok := result.(map[string]any)
	if !ok {
		return fmt.Errorf("the handler script must return a map, got %T", result)
	}
	for k, v := range m {
		if _, fixed := deny[k]; fixed {
			// A per-frame call hits the same line repeatedly, so WarnOnce folds it into one to avoid
			// flooding the log.
			nr.WarnOnce("handler-fixed-output:"+k,
				"the handler returned the fixed output variable %s, ignored (the node writes it automatically; scripts may only write custom variables)", k)
			continue
		}
		if err := nr.AppendOutputVarWithFire(k, v); err != nil {
			return err
		}
	}
	return nil
}
