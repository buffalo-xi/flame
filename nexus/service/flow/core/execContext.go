package flow

import (
	"context"
	"flame/nexus/shared/flowconfig"
	"flame/tools/xlog"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProgressMsg is one outbound streaming progress message.
// Category uses the same values as agent/core's Category* (thinking / text / tool_call / tool_result); anything without an
// explicit category counts as text. There is no "final result" tier here: the final result is only the result in the
// response.
type ProgressMsg struct {
	Category string
	Message  string
}

type ExecContext struct {
	Flow         *Flow
	RunEnvVars   map[string]interface{}
	RunFlowVars  map[string]interface{}
	RunArguments map[string]interface{}
	StdCtx       context.Context // the standard context, used to cancel execution
	ReqID        string
	RunID        string
	SessionID    string
	// Language is the output language of this run (a BCP-47 tag such as zh-CN), empty when nobody specified one.
	//
	// NewFlow takes it from the run argument _output_language (the caller pinned it) or, failing that, from the calling
	// ctx (the caller's already-resolved language). Nodes read it as the builtin variable `language`.
	Language string
	// LanguagePinned says whether the value was pinned by a caller (true) or only resolved / inherited (false).
	//
	// The distinction exists so a default can never override a callee's own default: only a pinned value is forwarded in
	// params._meta.language when this run calls an agent or another flow. Both kinds are visible as `language`.
	LanguagePinned bool
	UserID         string // the calling user (RPC/MCP binding resolution, background login user)
	// CallDepth is the call depth across agents / flows (0 at the top, +1 per crossed layer).
	//
	// NewFlow extracts and strips it from the run argument _call_depth; an rpc node calling an endpoint passes it down as
	// +1.
	// Purpose: stop the cycle formed by agent → local flow endpoint → rpc node → agent endpoint → …
	// (call_agent keeps a depth count of its own, but the flow leg was not on that chain).
	// The semantics are "stack depth", not "number of calls": several rpc nodes in the same flow read the same value, and
	// sibling calls do not accumulate (fan-out is governed by the flow's max_exec_count).
	CallDepth int
	// CallChain is the ancestor chain (see shared/callchain): NewFlow extracts and strips it from the run argument
	// _call_chain.
	//
	// It complements CallDepth: the depth is "at most how many levels", the chain is "reject immediately whichever one
	// re-enters". The chain records ancestors only, and a sibling fan-out does not accumulate — two parallel rpc nodes in
	// the same flow read the same chain.
	CallChain []string
	Schema    string
	// Limits is the limit snapshot frozen for this run: NewFlow resolves it once at start and the run never queries the
	// configuration again. When nil (as in a directly constructed unit test), limits() falls back to resolving once.
	Limits    *flowconfig.FlowLimits
	StartedAt time.Time // this run's start time (the fallback for run.started_at when no node executed)

	Result       string
	ProgressMsgs []string // progress text (for storage, without categories)
	ResultMsgs   []string // result text messages
	Error        string

	ProgressChan       chan ProgressMsg
	ResultChan         chan string
	ExecutedProcessMap *ProcessExecMap
	ExecutedNodeMap    *NodeExecMap

	// progressOverflow records whether the progress channel has ever overflowed (used only for "log the first drop once",
	// to avoid flooding the log)
	progressOverflow atomic.Bool

	ProcessManager *ProcessManager

	monoWG sync.WaitGroup // tracks independent mono concurrent nodes; Start waits before returning

	VarsMu sync.RWMutex

	// OutputBindingFire fires the matching output binding automatically once a node-type variable has been written through
	// SetVariableWithFire. Injected by the executors package at initialisation.
	OutputBindingFire func(nr *NodeResult, varName string, value any) error

	// Debug is the debug controller (step / pause / breakpoint). When nil the engine runs at full speed as before.
	Debug *DebugControl

	// DebugScope is the debug session scope: it decides "do the entities I depend on read the draft or the released
	// version".
	//
	// Only the editor's debugger and a run_flow trial run set it; the production entries (MCP / RPC / the agent's local__
	// tools and so on) always leave it nil (= read the released version). A child call inherits it through runEndpoint, and
	// a cross-process call inherently has none (it is an in-process parameter and never enters the protocol).
	DebugScope *DebugScope

	// OnFlowDone is the end-of-execution callback (called on success, failure and cancellation alike);
	// the debug source (the WS handler) injects it to push the finished event to the client.
	OnFlowDone func(err error)

	// cancel is the cancel function, used to interrupt execution when a debug stop is requested (injected by NewFlow
	// through WithCancel).
	cancel context.CancelFunc
}

// Cancel cancels execution (called on a debug stop, so that StdCtx.Err() is non-nil and wakes a suspended point).
func (e *ExecContext) Cancel() {
	if e.cancel != nil {
		e.cancel()
	}
}

// SetCancel injects the cancel function (called by NewFlow when constructing the context across packages).
func (e *ExecContext) SetCancel(c context.CancelFunc) {
	e.cancel = c
}

func (ctx *ExecContext) GetProgressChan() chan ProgressMsg {
	return ctx.ProgressChan
}

// SendProgress accumulates progress (for storage) and best-effort sends it to the progress channel (category fixed to
// text).
//
// Accumulate first, send second: storage and the final result do not depend on anybody reading the channel.
// The send is non-blocking: progress is only a notification, so a slow or absent consumer (a non-streaming caller never
// reads that channel) must not stall execution; a full buffer drops this frame, and only the first drop is logged (to
// avoid flooding).
// Consistent with how fireSingleBinding in binding.go writes the same channel.
func (ctx *ExecContext) SendProgress(progress string) {
	ctx.SendProgressAs("text", progress)
}

// SendProgressAs sends progress with the given category. The values match agent/core's Category* (thinking / text /
// tool_call / tool_result); they are plain strings here and the actual mapping is decided by the binding in the executors
// layer.
func (ctx *ExecContext) SendProgressAs(category, progress string) {
	ctx.AppendProgressMsg(progress)
	if ctx.ProgressChan == nil {
		return
	}
	select {
	case ctx.ProgressChan <- ProgressMsg{Category: category, Message: progress}:
	default:
		if ctx.progressOverflow.CompareAndSwap(false, true) {
			ctx.LogInfo("progress channel buffer is full; further progress notifications will be dropped (stored records are unaffected)")
		}
	}
}

func (ctx *ExecContext) GetResultChan() chan string {
	return ctx.ResultChan
}

// EmitResult accumulates the result (appended to the final Result and recorded in ResultMsgs for storage) and, when asked,
// streams it as well.
//
// With stream=false it only accumulates: the caller reads the complete result from GetResult at the end (the default of a
// result node).
// The send is a non-blocking write: the stream is only a notification, so a slow or absent consumer (a non-streaming
// caller never reads that channel) must not stall execution; a dropped frame does not affect the final result, because the
// accumulation already happened.
func (ctx *ExecContext) EmitResult(result string, stream bool) {
	ctx.AddResult(result)
	ctx.AppendResultMsg(result)
	if !stream || ctx.ResultChan == nil {
		return
	}
	select {
	case ctx.ResultChan <- result:
	default:
	}
}

func (ctx *ExecContext) GetResult() string {
	ctx.VarsMu.RLock()
	defer ctx.VarsMu.RUnlock()
	return ctx.Result
}
func (ctx *ExecContext) HasResult() bool {
	ctx.VarsMu.RLock()
	defer ctx.VarsMu.RUnlock()
	return ctx.Result != ""
}
func (ctx *ExecContext) AddResult(result string) {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	ctx.Result += result
}

// AppendProgressMsg appends one progress message.
func (ctx *ExecContext) AppendProgressMsg(msg string) {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	ctx.ProgressMsgs = append(ctx.ProgressMsgs, msg)
}

// AppendResultMsg appends one result message.
func (ctx *ExecContext) AppendResultMsg(msg string) {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	ctx.ResultMsgs = append(ctx.ResultMsgs, msg)
}

func (ctx *ExecContext) SetResult(result string) {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	ctx.Result = strings.Trim(result, " ")
}

func (ctx *ExecContext) GetError() string {
	ctx.VarsMu.RLock()
	defer ctx.VarsMu.RUnlock()
	return ctx.Error
}
func (ctx *ExecContext) SetError(err string) {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	ctx.Error = err
}

func (ctx *ExecContext) GetRunID() string {
	return ctx.RunID
}
func (ctx *ExecContext) SetRunID(runID string) {
	ctx.RunID = runID
}
func (ctx *ExecContext) GetReqID() string {
	return ctx.ReqID
}
func (ctx *ExecContext) SetReqID(reqID string) {
	ctx.ReqID = reqID
}

// MonoWGAdd increments the count of independent mono concurrent nodes.
func (ctx *ExecContext) MonoWGAdd(delta int) {
	ctx.monoWG.Add(delta)
}

// MonoWGDone marks one independent mono concurrent node as finished.
func (ctx *ExecContext) MonoWGDone() {
	ctx.monoWG.Done()
}

// MonoWGWait waits for every independent mono concurrent node to finish.
func (ctx *ExecContext) MonoWGWait() {
	ctx.monoWG.Wait()
}

// limits returns the limit snapshot frozen for this run; when none was injected it resolves once as a fallback (only a unit
// test or an abnormal construction gets here).
func (ctx *ExecContext) limits() flowconfig.FlowLimits {
	if ctx.Limits != nil {
		return *ctx.Limits
	}
	return flowconfig.Resolve(ctx.Schema)
}

// ResolveMaxLoopTimes returns the maximum loop count actually in effect (for the loop executor).
func (ctx *ExecContext) ResolveMaxLoopTimes() int {
	return ctx.limits().MaxLoopTimes
}

// ResolveTimeout resolves the timeout in seconds from the value the caller passed in.
// Lookup chain: the argument -> Instance.Timeout -> config.MaxTimeout.
// Rule: among all non -1 values on the chain take the smallest, then compare with MaxTimeout and take the smaller.
func (ctx *ExecContext) ResolveTimeout(timeout int) int {
	limits := ctx.limits()
	result := timeout
	// The argument is not set (<= 0): fall back to Instance.Timeout
	if result <= 0 {
		if ctx.Flow != nil && ctx.Flow.Instance != nil {
			result = ctx.Flow.Instance.Timeout.Int()
		}
	} else {
		// The argument is set, but Instance.Timeout is set too (> 0): take the smaller
		if ctx.Flow != nil && ctx.Flow.Instance != nil {
			insTimeout := ctx.Flow.Instance.Timeout.Int()
			if insTimeout > 0 && insTimeout < result {
				result = insTimeout
			}
		}
	}

	// Still unset: fall back to max_timeout
	if result <= 0 {
		return limits.MaxTimeout
	}

	// Compare with MaxTimeout and take the smaller
	sysMax := limits.MaxTimeout
	if sysMax > 0 && result > sysMax {
		return sysMax
	}
	return result
}

// GetFlowTimeout returns the flow-level timeout in seconds (no argument).
// Lookup chain: Instance.Timeout -> config.MaxTimeout.
// The smaller of the two wins.
func (ctx *ExecContext) GetFlowTimeout() int {
	limits := ctx.limits()
	result := -1

	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		result = ctx.Flow.Instance.Timeout.Int()
	}

	if result <= 0 {
		return limits.MaxTimeout
	}

	sysMax := limits.MaxTimeout
	if sysMax > 0 && result > sysMax {
		return sysMax
	}
	return result
}

// ResolveRecursionDepth resolves the maximum recursion depth actually in effect.
// Rules:
//  1. Instance.MaxRecursionDepth is -1 (or unset) -> use system_max_recursion_depth
//  2. both set to a positive value -> min(Instance, system)
//  3. both <= 0 -> -1 (unlimited, backward compatible)
func (ctx *ExecContext) ResolveRecursionDepth() int {
	instanceDepth := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceDepth = ctx.Flow.Instance.MaxRecursionDepth.Int()
	}

	sysDepth := ctx.limits().MaxRecursionDepth

	// Neither set / both -1: unlimited
	if instanceDepth <= 0 && sysDepth <= 0 {
		return -1
	}

	// The instance is -1: the system ceiling alone applies
	if instanceDepth <= 0 {
		return sysDepth
	}

	// The system is -1: the instance ceiling alone applies
	if sysDepth <= 0 {
		return instanceDepth
	}

	// Both set: take the smaller
	if instanceDepth < sysDepth {
		return instanceDepth
	}
	return sysDepth
}

// ResolveMaxNodeCount resolves the maximum node count actually in effect.
// The rules match ResolveRecursionDepth:
//  1. Instance.MaxNodeCount is -1 (or unset) -> use system_max_node_count
//  2. both set to a positive value -> min(Instance, system)
//  3. both <= 0 -> -1 (unlimited, backward compatible)
func (ctx *ExecContext) ResolveMaxNodeCount() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxNodeCount.Int()
	}

	sysMax := ctx.limits().MaxNodeCount

	// Neither set / both -1: unlimited
	if instanceMax <= 0 && sysMax <= 0 {
		return -1
	}

	// The instance is -1: the system ceiling alone applies
	if instanceMax <= 0 {
		return sysMax
	}

	// The system is -1: the instance ceiling alone applies
	if sysMax <= 0 {
		return instanceMax
	}

	// Both set: take the smaller
	if instanceMax < sysMax {
		return instanceMax
	}
	return sysMax
}

// ResolveScriptTimeout returns the script execution timeout in seconds.
// lang: the script language (lua/python/js); timeout: the value set by the caller (the node's "timeout control").
//
// When the node sets no timeout control (timeout <= 0) there is no longer a per-language default in nexus — it goes
// straight to ResolveTimeout's generic fallback chain: the instance's Instance.Timeout -> flow_configs.max_timeout
// (configured per tenant on the platform side, 300 by default) -> the code fallback 300.
//
// ⚠️ What is computed here is only the "desired ceiling"; **there is still a hard gate on the sandbox side below it**:
// sandboxd's code_max_sec (codeDeadline in handle_code.go). In other words the effective ceiling for a script run is
// min(this function's result, code_max_sec, 120 by default).
func (ctx *ExecContext) ResolveScriptTimeout(lang string, timeout int) int {
	return ctx.ResolveTimeout(timeout)
}

// ResolveMaxExecCount resolves the maximum execution count actually in effect.
// The rules match ResolveRecursionDepth:
//  1. Instance.MaxExecCount is -1 (or unset) -> use system_max_exec_count
//  2. both set to a positive value -> min(Instance, system)
//  3. both <= 0 -> -1 (unlimited, backward compatible)
func (ctx *ExecContext) ResolveMaxExecCount() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxExecCount.Int()
	}

	sysMax := ctx.limits().MaxExecCount

	// Neither set / both -1: unlimited
	if instanceMax <= 0 && sysMax <= 0 {
		return -1
	}

	// The instance is -1: the system ceiling alone applies
	if instanceMax <= 0 {
		return sysMax
	}

	// The system is -1: the instance ceiling alone applies
	if sysMax <= 0 {
		return instanceMax
	}

	// Both set: take the smaller
	if instanceMax < sysMax {
		return instanceMax
	}
	return sysMax
}

// ResolveMaxRunRecords resolves the number of run records actually retained.
// The three-state semantics of the workflow-level max_run_records:
//   - 0: store nothing (no run record is kept)
//   - -1: unset / unlimited -> use the system-level MaxRunRecords
//   - a positive N: keep N, clamped by the system-level ceiling (min)
func (ctx *ExecContext) ResolveMaxRunRecords() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxRunRecords.Int()
	}

	// 0 = store nothing
	if instanceMax == 0 {
		return 0
	}

	sysMax := ctx.limits().MaxRunRecords

	// The instance is -1 (unset / unlimited) -> the system ceiling applies
	if instanceMax < 0 {
		return sysMax
	}

	// The instance is positive -> clamped by the system ceiling (min)
	if sysMax > 0 && instanceMax > sysMax {
		return sysMax
	}
	return instanceMax
}

// ResolveMaxSessionRecords resolves the number of session records actually retained.
// The semantics of the workflow-level max_session_records (different from max_run_records — note the distinction):
//   - -1: no session memory at all (the switch is off) -> return 0
//   - 0: an invalid value, treated as no memory
//   - a positive N: keep N, clamped by the system-level ceiling (min)
func (ctx *ExecContext) ResolveMaxSessionRecords() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxSessionRecords.Int()
	}

	// -1 or 0: no session memory
	if instanceMax <= 0 {
		return 0
	}

	// The instance is positive -> clamped by the system ceiling (min)
	sysMax := ctx.limits().MaxSessionRecords
	if sysMax > 0 && instanceMax > sysMax {
		return sysMax
	}
	return instanceMax
}

// ResolveRunLimits returns the limits actually in effect for this run (the final values after the three-level resolve).
// It is used for the stored run_limits and for the debug finished frame, so both carry identical data.
func (ctx *ExecContext) ResolveRunLimits() map[string]any {
	limits := map[string]any{
		"timeout":             ctx.GetFlowTimeout(),
		"max_recursion_depth": ctx.ResolveRecursionDepth(),
		"max_node_count":      ctx.ResolveMaxNodeCount(),
		"max_exec_count":      ctx.ResolveMaxExecCount(),
		"max_run_records":     ctx.ResolveMaxRunRecords(),
		"max_session_records": ctx.ResolveMaxSessionRecords(),
	}
	limits["max_loop_times"] = ctx.limits().MaxLoopTimes
	return limits
}

// RecordNodeExecution records one node execution atomically: it appends to ExecutedProcessMap and sets it in
// ExecutedNodeMap.
// It checks whether the total execution count would exceed ResolveMaxExecCount first, and if so marks nr as an internal
// error and returns the error.
func (ctx *ExecContext) RecordNodeExecution(processID, nodeID string, nr *NodeResult) error {
	max := ctx.ResolveMaxExecCount()
	if max > 0 {
		if !ctx.ExecutedProcessMap.TryAppend(processID, nr, max) {
			nr.Finish(StatusFailed, ErrCodeInternal,
				fmt.Sprintf("exceeded max exec count: %d", max))
			return fmt.Errorf("exceeded max exec count: %d", max)
		}
	} else {
		ctx.ExecutedProcessMap.Append(processID, nr)
	}
	ctx.ExecutedNodeMap.Set(processID+"_"+nodeID, nr)
	return nil
}

// GetflowId returns the current workflow instance's id (from flow.Instance, nil-safe).
func (ctx *ExecContext) GetflowId() string {
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		return ctx.Flow.Instance.ID
	}
	return ""
}

// GetFlowName returns the current workflow instance's name (from flow.Instance, nil-safe).
func (ctx *ExecContext) GetFlowName() string {
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		return ctx.Flow.Instance.Name
	}
	return ""
}

// BuiltinVars is this run's read-only context, for the variable source "builtin".
//
// It is deliberately not a default input: session / run information is useless to most scripts, and it only reaches a
// script when a node declares source_type="builtin" explicitly in its inputs (see getVariableNoLock). That also keeps it a
// peer of the argument / env / flow / node sources rather than an implicit global.
//
// The selection criterion is "for the program, not for the eye": everything immutable, character-safe and usable as a key.
// So it deliberately omits user_name (mutable, may contain special characters), req_id (only for log correlation, may be
// empty and is not unique) and env (may hold secrets).
//
// It is built fresh on every call, never cached: the Debug controller is injected by the debug entry after NewFlow, so a
// cache would hold a stale value; besides, these fields are all strings and cost nothing to build.
// No lock is taken — the caller getVariableNoLock already holds the VarsMu read lock, and re-entering risks a deadlock.
func (ctx *ExecContext) BuiltinVars() map[string]interface{} {
	return map[string]interface{}{
		"session_id": ctx.SessionID, // an empty string for a stateless call
		"run_id":     ctx.RunID,
		"user_id":    ctx.UserID, // an empty string for an unauthenticated call
		"schema":     ctx.Schema,
		"flow_id":    ctx.GetflowId(),
		"language":   ctx.Language, // empty when no caller specified an output language
	}
}

func (ctx *ExecContext) LogInfo(message string) {
	xlog.Info("req: %s run: %s flow: %s(%s) info: %s",
		ctx.GetReqID(), ctx.GetRunID(), ctx.GetFlowName(), ctx.GetflowId(), message)
}
func (ctx *ExecContext) LogError(message string) {
	ctx.SetError(message)
	xlog.Error("req: %s run: %s flow: %s(%s) err: %s",
		ctx.GetReqID(), ctx.GetRunID(), ctx.GetFlowName(), ctx.GetflowId(), message)
}

func (c *ExecContext) CloseChans() error {
	c.CloseProgressChan()
	c.CloseResultChan()
	return nil
}
func (c *ExecContext) CloseProgressChan() {
	defer func() {
		if r := recover(); r != nil {
			// A double close (several exits call CloseChans): an expected idempotent call, so it goes to the log rather
			// than to stderr.
			xlog.Error("req: %s run: %s closing the progress channel failed: %v", c.GetReqID(), c.GetRunID(), r)
		}
	}()
	if c.ProgressChan != nil {
		close(c.ProgressChan)
	}
}
func (c *ExecContext) CloseResultChan() {
	defer func() {
		if r := recover(); r != nil {
			xlog.Error("req: %s run: %s closing the result channel failed: %v", c.GetReqID(), c.GetRunID(), r)
		}
	}()
	if c.ResultChan != nil {
		close(c.ResultChan)
	}
}

func (ctx *ExecContext) GetVariable(source map[string]interface{}, processID string) (interface{}, error) {
	ctx.VarsMu.RLock()
	defer ctx.VarsMu.RUnlock()
	return ctx.getVariableNoLock(source, processID)
}

// getVariableNoLock is the version without locking, for internal batch calls.
func (ctx *ExecContext) getVariableNoLock(source map[string]interface{}, processID string) (interface{}, error) {
	if source == nil {
		return nil, fmt.Errorf("variable source is nil")
	}

	sourceType, _ := source["source_type"].(string)
	sourceKey, _ := source["source_key"].(string)
	// An empty reference must not silently return nil: when the UI created a variable without picking a source, that has
	// to be an explicit error, otherwise a script / template keeps running with an empty value and the runtime has nothing
	// to point at.
	if sourceType == "" || strings.TrimSpace(sourceKey) == "" {
		return nil, fmt.Errorf("variable source is not selected")
	}

	switch sourceType {
	case "argument":
		return GetVarByPath(ctx.RunArguments, sourceKey)
	case "env":
		return GetEnvVar(ctx.RunEnvVars, sourceKey)
	case "global":
		// Tenant-level global variables ({schema}.global_vars): an input source on a par with environment variables.
		// A misspelled name / a missing variable must fail hard, for the same reason as the builtin note — a silent nil
		// lets the script continue with an empty value, while the run log merely looks like "no value this time".
		return ResolveGlobalSource(ctx.Schema, sourceKey)
	case "flow":
		return GetVarByPath(ctx.RunFlowVars, sourceKey)
	case "node":
		nodeID, _ := source["source_ref"].(string)
		if nodeID == "" {
			return nil, fmt.Errorf("node sourceRef is empty")
		}
		nodeResult := ctx.FindNodeResultByProcessChain(nodeID, processID)
		if nodeResult == nil {
			return nil, fmt.Errorf("node result not found for id %s", nodeID)
		}
		return nodeResult.GetVariable(sourceKey)
	case "builtin":
		// Run context: independent of topology, so a node anywhere can reference it
		v, ok := ctx.BuiltinVars()[sourceKey]
		if !ok {
			// A misspelled key must be an explicit error. A silent nil lets the script continue with an empty value, and
			// that class of mistake is almost impossible to locate in the run log (it merely looks like "no value this
			// time").
			return nil, fmt.Errorf("unknown builtin variable %q", sourceKey)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported source type %s", sourceType)
	}
}

// GetVariables resolves several variable values from a variable definition map.
// Example of the variables definition: {"arg1": {"type": "string", "source_key": "input", "source_ref": "", "source_type": "argument"}}
func (ctx *ExecContext) GetVariables(variables map[string]interface{}, processID string) (map[string]interface{}, error) {
	ctx.VarsMu.RLock()
	defer ctx.VarsMu.RUnlock()

	results := make(map[string]interface{})

	for key, value := range variables {
		if sourceDef, ok := value.(map[string]interface{}); ok {
			// Build the source map GetVariable expects
			source := make(map[string]interface{})

			if sourceType, exists := sourceDef["source_type"]; exists {
				source["source_type"] = sourceType
			}
			if sourceKey, exists := sourceDef["source_key"]; exists {
				source["source_key"] = sourceKey
			}
			if sourceRef, exists := sourceDef["source_ref"]; exists {
				source["source_ref"] = sourceRef
			}

			// Resolve the value through GetVariable
			varValue, err := ctx.getVariableNoLock(source, processID)
			if err != nil {
				// One failed lookup fails the whole batch
				return nil, fmt.Errorf("failed to get variable %s: %v", key, err)
			}
			results[key] = varValue
		}
	}

	return results, nil
}

func (ctx *ExecContext) GetVariableSimple(source map[string]interface{}, processID string) interface{} {
	result, _ := ctx.GetVariable(source, processID)
	return result
}

func (ctx *ExecContext) SetFlowVar(path string, value interface{}) error {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	return ctx.setFlowVarNoLock(path, value)
}

// setFlowVarNoLock is the version without locking, for internal batch calls.
func (ctx *ExecContext) setFlowVarNoLock(path string, value interface{}) error {
	return SetVarByPath(ctx.RunFlowVars, path, value)
}

// SetVariable sets a value, supporting both flow variables and node-local variables.
// Example of the target definition: {"source_type": "flow", "source_key": "result", "source_ref": ""}
// or {"source_type": "node", "source_key": "myVar", "source_ref": "nodeID"}
func (ctx *ExecContext) SetVariable(target map[string]interface{}, value interface{}, processID string) error {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	return ctx.setVariableNoLock(target, value, processID)
}

// setVariableNoLock is the version without locking, for internal batch calls.
func (ctx *ExecContext) setVariableNoLock(target map[string]interface{}, value interface{}, processID string) error {
	if target == nil {
		return fmt.Errorf("variable target is nil")
	}

	targetType, _ := target["source_type"].(string)
	targetKey, _ := target["source_key"].(string)

	switch targetType {
	case "flow":
		return ctx.setFlowVarNoLock(targetKey, value)
	case "node":
		nodeID, _ := target["source_ref"].(string)
		if nodeID == "" {
			return fmt.Errorf("node sourceRef is empty")
		}
		nodeResult := ctx.FindNodeResultByProcessChain(nodeID, processID)
		if nodeResult == nil {
			return fmt.Errorf("node result not found for id %s", nodeID)
		}
		if err := nodeResult.SetLocalVar(targetKey, value); err != nil {
			return nodeResult.SetOutputVar(targetKey, value)
		} else {
			return nil
		}
	case "builtin", "env", "argument":
		// Run-context variables (session / run / tenant identity, environment variables, entry arguments) are this run's
		// read-only inputs: a node may only write its own output / local variables or the flow variables.
		return fmt.Errorf("run-context variables (%s source) are read-only and cannot be an assignment target: %s", targetType, targetKey)
	case "":
		return fmt.Errorf("assignment target is not selected (source_type is empty)")
	default:
		return fmt.Errorf("unsupported target type %s", targetType)
	}
}

// SetVariables sets several values in one batch.
// Example of the targets definition: {"w1": {"type": "string", "source_key": "result", "source_ref": "", "source_type": "flow"}}
func (ctx *ExecContext) SetVariables(targets map[string]interface{}, values map[string]interface{}, processID string) error {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()

	for key, targetDef := range targets {
		target, ok := targetDef.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid target definition for key %s", key)
		}
		value, exists := values[key]
		if !exists {
			continue
		}
		if err := ctx.setVariableNoLock(target, value, processID); err != nil {
			return fmt.Errorf("failed to set variable %s: %v", key, err)
		}
	}
	return nil
}

// SetVariableWithFire writes a variable and fires the matching output binding.
// It only fires for source_type="node" and only once the OutputBindingFire hook has been injected.
// It applies to executors (HTTP and other non-script nodes) and to the targets a script's return value lands on.
func (ctx *ExecContext) SetVariableWithFire(target map[string]any, value any, processID string) error {
	err := ctx.SetVariable(target, value, processID)
	if err != nil {
		return err
	}
	if ctx.OutputBindingFire == nil {
		return nil
	}
	targetType, _ := target["source_type"].(string)
	if targetType != "node" {
		return nil
	}
	targetKey, _ := target["source_key"].(string)
	nodeID, _ := target["source_ref"].(string)
	if nodeID == "" {
		return nil
	}
	targetNR := ctx.FindNodeResultByProcessChain(nodeID, processID)
	if targetNR == nil {
		return nil
	}
	varName := strings.Split(targetKey, ".")[0]
	varName = strings.Split(varName, "[")[0]
	if err := ctx.OutputBindingFire(targetNR, varName, value); err != nil {
		return err
	}
	return nil
}

func (ctx *ExecContext) SetFlowVars(vars map[string]interface{}) error {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	for path, value := range vars {
		if err := SetVarByPath(ctx.RunFlowVars, path, value); err != nil {
			return err
		}
	}
	return nil
}

// FindNodeResultByProcessChain looks a NodeResult up in ExecutedNodeMap by processID and nodeID.
// When it is not found under the current processID it walks up to the parent processID, recursively, until it is found or
// there is no parent left.
func (ctx *ExecContext) FindNodeResultByProcessChain(nodeID string, processID string) *NodeResult {
	if ctx.ExecutedNodeMap == nil {
		return nil
	}
	currentProcessID := processID
	for currentProcessID != "" {
		key := currentProcessID + "_" + nodeID
		if nodeResult, ok := ctx.ExecutedNodeMap.Get(key); ok && nodeResult != nil {
			return nodeResult
		}
		if ctx.ProcessManager == nil {
			return nil
		}
		parentID, ok := ctx.ProcessManager.GetParentID(currentProcessID)
		if !ok {
			return nil
		}
		currentProcessID = parentID
	}
	return nil
}
