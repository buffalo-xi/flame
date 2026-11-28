package executors

import (
	"context"
	"encoding/json"
	"errors"
	agentcore "flame/nexus/service/agent/core"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/models"
	sessionrecords "flame/nexus/shared/repository/sessionrecords"
	sessions "flame/nexus/shared/repository/sessions"
	"flame/nexus/shared/runledger"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Diagram struct {
	Nodes []*flow.Node `json:"nodes"`
	Edges []*flow.Edge `json:"edges"`
}

// legacyGlobalRefRe is the old form in workflow configs: {{ .global_vars.NAME }}.
var legacyGlobalRefRe = regexp.MustCompile(`\{\{-?\s*\.global_vars\.([A-Za-z_][A-Za-z0-9_]*)`)

// rejectLegacyGlobalRefs rejects a flow whose node configs still use {{ .global_vars.X }}.
//
// Global variables became one source of node inputs (config.inputs with source_type: "global") and the
// template context no longer injects .global_vars. A Go template renders a missing key as <no value>
// without an error — without this check the old form would break silently ("the request goes out with
// <no value> in it"), which is far harder to trace than an error. The message names the node and the
// variable, so the fix is mechanical.
func rejectLegacyGlobalRefs(nodes []*flow.Node) error {
	for _, node := range nodes {
		if node == nil || node.Data == nil || len(node.Data.Config) == 0 {
			continue
		}
		name, ok := legacyGlobalRefIn(node.Data.Config)
		if !ok {
			continue
		}
		return fmt.Errorf("the config of node %s still uses {{ .global_vars.%s }}: global variables are now only a source of node input "+
			"variables; add an input variable on that node (source: global variable) and switch the template to {{ .input.<name> }}", node.Label(), name)
	}
	return nil
}

// legacyGlobalRefIn finds the old form inside a node config (recursing through map / slice / string).
func legacyGlobalRefIn(v any) (string, bool) {
	switch val := v.(type) {
	case string:
		if m := legacyGlobalRefRe.FindStringSubmatch(val); m != nil {
			return m[1], true
		}
	case map[string]any:
		for _, sub := range val {
			if name, ok := legacyGlobalRefIn(sub); ok {
				return name, true
			}
		}
	case []any:
		for _, sub := range val {
			if name, ok := legacyGlobalRefIn(sub); ok {
				return name, true
			}
		}
	}
	return "", false
}

// checkRequiredArguments enforces flows.arguments.required before anything runs. Order is the declaration's own order, so the
// message reads the way the schema was written.
func checkRequiredArguments(declaration, args map[string]interface{}) error {
	required, _ := declaration["required"].([]interface{})
	missing := make([]string, 0, len(required))
	for _, raw := range required {
		name, ok := raw.(string)
		if !ok || name == "" {
			continue
		}
		if v, exists := args[name]; !exists || v == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required argument(s) %s: the flow's entry arguments mark them as required and this call did not send them",
		strings.Join(missing, ", "))
}

func NewFlow(Instance *models.Flow, args map[string]interface{}, reqID, runID, schema, userID string, ctx context.Context) (*flow.ExecContext, error) {
	if args == nil {
		args = make(map[string]interface{})
	}

	// Extract session_id (validated and stripped, so it never becomes a business argument)
	sessionID, args, err := extractSessionID(args)
	if err != nil {
		return nil, err
	}
	// Output language: the same reserved-key mechanism as session_id. A value in the arguments is a pin (an entry point
	// put it there from params._meta.language); a value on ctx is the caller's resolved language and stays informational.
	language, languagePinned, args := extractOutputLanguage(args, ctx)
	// Session ownership is deliberately **not** checked here: when an agent calls a flow (a local tool / an
	// rpc node) it passes the caller's session down so the sub-flow and its parent land in the same
	// users/{uid}/sessions/{sid}/ area — and that session belongs to the parent agent, not to this flow.
	// Ownership is only checked at the external protocol entry points (MCP runFlowTool / RPC runFlow), where
	// the session really is untrusted client input.

	// Call depth: injected by upstream (a local tool / an rpc node), 0 at the top level. Stripped, so it never
	// becomes a business argument.
	callDepth, args := extractCallDepth(args)
	// Call chain: the same reserved-key mechanism as _call_depth, used only for cycle detection, and it must
	// not leak into the business variables either.
	callChain, args := extractCallChain(args)

	defEnvVars := make(map[string]interface{})
	if !Instance.EnvVars.IsNil() {
		if err := Instance.EnvVars.Unmarshal(&defEnvVars); err != nil {
			return nil, fmt.Errorf("failed to unmarshal EnvVars: %v", err)
		}
	}

	defFlowVars := make(map[string]interface{})
	if !Instance.FlowVars.IsNil() {
		if err := Instance.FlowVars.Unmarshal(&defFlowVars); err != nil {
			return nil, fmt.Errorf("failed to unmarshal FlowVars: %v", err)
		}
	}

	defArguments := make(map[string]interface{})
	if !Instance.Arguments.IsNil() {
		if err := Instance.Arguments.Unmarshal(&defArguments); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Arguments: %v", err)
		}
	}

	// Enforce the declaration's required list here, at the one construction point every entry goes through (agent tool, RPC /
	// MCP call, nested flow, editor debug). flows.arguments only ever becomes the endpoint's input_schema, and no layer
	// validates the payload against it, so without this a missing value surfaces as "var path X not found" - a config error
	// attributed to whichever node happened to read it, long after the caller could still fix it.
	if err := checkRequiredArguments(defArguments, args); err != nil {
		return nil, err
	}

	var nodes []*flow.Node
	var edges []*flow.Edge
	if !Instance.Diagram.IsNil() {
		var diagram Diagram
		if err := Instance.Diagram.Unmarshal(&diagram); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Diagram: %v", err)
		}
		nodes = diagram.Nodes
		edges = diagram.Edges
	}

	// The old form is rejected before the run: a template with a missing key only renders <no value>, so
	// not rejecting it means breaking silently.
	if err := rejectLegacyGlobalRefs(nodes); err != nil {
		return nil, err
	}

	nodeMap := make(map[string]*flow.Node)
	for _, node := range nodes {
		node.InEdges = []*flow.Edge{}
		node.OutEdges = []*flow.Edge{}
		nodeMap[node.ID] = node
	}

	for _, edge := range edges {
		if sourceNode, ok := nodeMap[edge.Source]; ok {
			sourceNode.OutEdges = append(sourceNode.OutEdges, edge)
		}
		if targetNode, ok := nodeMap[edge.Target]; ok {
			targetNode.InEdges = append(targetNode.InEdges, edge)
		}
	}

	f := &flow.Flow{
		Instance:     Instance,
		Nodes:        nodes,
		NodeMap:      nodeMap,
		Edges:        edges,
		DefEnvVars:   defEnvVars,
		DefFlowVars:  defFlowVars,
		DefArguments: defArguments,
	}

	// A snapshot of the run limits: resolved once at start (falling back to the DB on a Redis miss), never
	// queried during the run.
	limits := flowconfig.Resolve(schema)

	stdCtx, cancel := context.WithCancel(ctx)
	// Everything downstream reads the language off the standard ctx: executors that call an agent endpoint (outbound RPC)
	// need it there, and a sub-flow inherits it through its own NewFlow call.
	if language != "" {
		if languagePinned {
			stdCtx = agentcore.WithPinnedLanguage(stdCtx, language)
		} else {
			stdCtx = agentcore.WithLanguageHint(stdCtx, language)
		}
	}
	execCtx := &flow.ExecContext{
		Flow:           f,
		RunEnvVars:     defEnvVars,
		RunFlowVars:    flow.InitFlowVars(defFlowVars),
		RunArguments:   flow.InitArguments(args),
		StdCtx:         stdCtx,
		ReqID:          reqID,
		RunID:          runID,
		SessionID:      sessionID,
		Language:       language,
		LanguagePinned: languagePinned,
		UserID:         userID,
		CallDepth:      callDepth,
		CallChain:      callChain,
		Schema:         schema,
		Limits:         &limits,
		StartedAt:      time.Now(),

		ProgressChan:       make(chan flow.ProgressMsg, 100),
		ResultChan:         make(chan string, 100),
		ExecutedProcessMap: flow.NewProcessExecMap(),
		ExecutedNodeMap:    flow.NewNodeExecMap(),
		ProcessManager:     flow.NewProcessManager(),
		// Shared by every context derived from this run (a concurrency member, see WithContext): one lock per store.
		VarsMu: &sync.RWMutex{},

		OutputBindingFire: fireOutputBinding,
	}
	execCtx.SetCancel(cancel)
	return execCtx, nil
}

func NewFlowExecContext(Instance *models.Flow, args map[string]interface{}, reqID, runID, schema, userID string, ctx context.Context) (*flow.ExecContext, error) {
	return NewFlow(Instance, args, reqID, runID, schema, userID, ctx)
}

func Start(execCtx *flow.ExecContext) (err error) {
	// Run ledger: registered when it starts and unregistered when it ends — only then does "what is running in
	// this tenant" include workflows.
	// Note that in a debug run the body lives in the goroutine below (Start returns immediately), so the
	// unregister has to be done there and cannot be deferred in Start.
	unregister := registerActiveRun(execCtx)

	// In debug mode the body goes into a goroutine driven by DebugControl (suspend / resume) and Start returns
	// at once, pushing the result / error back to the frontend over WS.
	if execCtx.Debug != nil && execCtx.Debug.Enabled {
		go func() {
			defer unregister()
			defer execCtx.Cancel()
			flowErr := runFlow(execCtx)
			persistRun(execCtx, flowErr)
			persistSessionMemory(execCtx, flowErr)
			ensureSession(execCtx)
			if execCtx.OnFlowDone != nil {
				execCtx.OnFlowDone(flowErr)
			}
		}()
		return nil
	}
	defer unregister()
	// Cancel the ctx once the flow is over: when a scope is abandoned on timeout, the members are still running LLM /
	// sandbox calls while the ledger has already been unregistered and the user has seen "finished". It comes after
	// runFlow (the defer runs when this function returns) and persistence does not depend on StdCtx, so it is unaffected.
	defer execCtx.Cancel()
	err = runFlow(execCtx)
	persistRun(execCtx, err)
	persistSessionMemory(execCtx, err)
	ensureSession(execCtx)
	if execCtx.OnFlowDone != nil {
		execCtx.OnFlowDone(err)
	}
	return err
}

// registerActiveRun puts this flow run into both the cross-instance ledger and the process-local stop table
// and returns an idempotent unregister function. Debug runs, trial runs and real calls all register here
// (run_type tells them apart), and a sub-flow registers an entry of its own.
func registerActiveRun(execCtx *flow.ExecContext) func() {
	unregisterLocal := registerLocalRun(execCtx)
	unregisterLedger := func() {}
	if execCtx != nil && execCtx.Flow != nil && execCtx.Flow.Instance != nil {
		unregisterLedger = runledger.Register(runledger.Entry{
			Kind:       runledger.KindFlow,
			RunID:      execCtx.GetRunID(),
			SessionID:  execCtx.SessionID,
			Schema:     execCtx.Schema,
			TargetID:   execCtx.GetflowId(),
			TargetName: execCtx.Flow.Instance.Name,
			UserID:     execCtx.UserID,
			RunType:    runTypeOf(execCtx),
			StartedAt:  execCtx.StartedAt,
		})
	}
	return func() {
		unregisterLedger()
		unregisterLocal()
	}
}

// ensureSession makes sure the sessions table has a record for this session_id after a flow run.
//
// A session the entry point already created is only refreshed here, so the failure that matters is the one where this
// run is the first to see the id: EnsureExists then creates it subject to the per-user session-count quota, and a
// refusal is logged rather than raised - the run has already happened, and aborting it over a bookkeeping row would be a
// worse outcome than a run whose session is missing from the console list. Its session_records are still written (that
// table does not depend on the sessions row), so the memory itself is not lost, only its entry in the session list.
func ensureSession(execCtx *flow.ExecContext) {
	if execCtx == nil || execCtx.SessionID == "" {
		return
	}
	if err := sessions.NewRepository().EnsureExists(
		execCtx.Schema,
		execCtx.SessionID,
		models.SessionOwnerTypeFlow,
		execCtx.GetflowId(),
		execCtx.UserID,
		"",
	); err != nil {
		execCtx.LogError("failed to write the session record: " + err.Error())
	}
}

// extractSessionID extracts and validates session_id from the run arguments.
// Rules: key absent -> disabled (empty); value not a string -> error; empty after trim -> treated as absent;
// non-empty but not a valid UUID -> error; valid UUID -> returned and stripped from the business arguments.
func extractSessionID(args map[string]interface{}) (string, map[string]interface{}, error) {
	raw, ok := args["session_id"]
	if !ok {
		return "", args, nil
	}
	sid, ok := raw.(string)
	if !ok {
		return "", nil, fmt.Errorf("session_id must be a string")
	}
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return "", agentcore.StripSessionID(args), nil
	}
	if _, err := uuid.Parse(sid); err != nil {
		return "", nil, fmt.Errorf("session_id is not a valid UUID: %s", sid)
	}
	return sid, agentcore.StripSessionID(args), nil
}

// extractOutputLanguage takes the output language out of the run arguments and strips it (the original map is not
// modified), falling back to the caller's ctx hint.
//
// The arguments carry a **pin**: an entry point put the tag there from params._meta.language, i.e. "the caller demanded
// this language". A tag that arrives only on ctx is the caller's already-resolved language (its own default, or the
// product default) - shown to nodes as the builtin variable `language`, but not forwarded as a pin, because that would
// let a default override the next callee's own default.
//
// A malformed tag counts as "not given" rather than an error: the value is advisory, and refusing a whole run over a
// mistyped tag would be worse than ignoring it. session_id is the opposite, because it is a real switch.
func extractOutputLanguage(args map[string]interface{}, ctx context.Context) (string, bool, map[string]interface{}) {
	if _, ok := args[agentcore.OutputLanguageKey]; ok {
		tag, _ := agentcore.OutputLanguage(args)
		return tag, tag != "", agentcore.StripOutputLanguage(args)
	}
	tag, _ := agentcore.NormalizeOutputLanguage(agentcore.LanguageHintFrom(ctx))
	if tag == "" {
		return "", false, args
	}
	return tag, agentcore.LanguagePinnedFrom(ctx), args
}

// extractCallDepth takes the call depth out of the run arguments and strips it (the original map is not
// modified).
//
// The value may come from JSON decoding (float64) or an in-process call (int) and both are accepted; an invalid
// value counts as 0 — the depth only limits recursion, and dirty data must not reject a normal call.
func extractCallDepth(args map[string]interface{}) (int, map[string]interface{}) {
	raw, ok := args["_call_depth"]
	if !ok {
		return 0, args
	}
	depth := 0
	switch v := raw.(type) {
	case int:
		depth = v
	case int32:
		depth = int(v)
	case int64:
		depth = int(v)
	case float64:
		depth = int(v)
	}
	if depth < 0 {
		depth = 0
	}
	clean := make(map[string]interface{}, len(args))
	for k, val := range args {
		if k == "_call_depth" {
			continue
		}
		clean[k] = val
	}
	return depth, clean
}

// extractCallChain takes the call chain out of the run arguments and strips it (the original map is not
// modified).
//
// Same reserved key and same reasoning as _call_depth: the chain is pure control information, and leaking it
// into RunArguments would turn it into a flow variable ({{_call_chain}}) and store it with the session memory.
// Invalid elements are dropped by callchain.Normalize — dirty data must not reject a normal call.
func extractCallChain(args map[string]interface{}) ([]string, map[string]interface{}) {
	raw, ok := args[callchain.ArgumentKey]
	if !ok {
		return nil, args
	}
	chain := callchain.Normalize(raw)
	clean := make(map[string]interface{}, len(args))
	for k, val := range args {
		if k == callchain.ArgumentKey {
			continue
		}
		clean[k] = val
	}
	return chain, clean
}

// persistSessionMemory records the session memory automatically once a run is over.
// It writes only when a valid session_id exists and the workflow allows memory
// (ResolveMaxSessionRecords()>0).
//
// runErr goes into status/error: a failed round that leaves only an empty output line looks exactly like
// "finished normally with no output" in the history, so there is no telling a workflow error from really
// having said nothing.
func persistSessionMemory(execCtx *flow.ExecContext, runErr error) {
	if execCtx == nil || execCtx.SessionID == "" {
		return
	}
	maxN := execCtx.ResolveMaxSessionRecords()
	if maxN <= 0 {
		return
	}

	inputJSON, err := json.Marshal(execCtx.RunArguments)
	if err != nil || inputJSON == nil {
		inputJSON = []byte("{}")
	}

	runType := "call"
	switch {
	case execCtx.Debug != nil && execCtx.Debug.Enabled:
		runType = "debug"
	case execCtx.DebugScope.Enabled():
		runType = "preview"
	}

	// Same rule as the agent side: cancelled on purpose -> stopped ("stopped" and "failed" are two different
	// things, and the former is not a bug so it must not show up red in the history), any other error -> failed.
	status, errMsg := models.SessionStatusOK, ""
	if runErr != nil {
		status, errMsg = models.SessionStatusFailed, runErr.Error()
		if errors.Is(runErr, context.Canceled) {
			status = models.SessionStatusStopped
		}
	}

	mem := &models.SessionRecord{
		OwnerType: models.SessionOwnerTypeFlow,
		OwnerID:   execCtx.GetflowId(),
		SessionID: execCtx.SessionID,
		RunID:     execCtx.GetRunID(),
		Input:     inputJSON,
		Output:    execCtx.GetResult(),
		RunType:   runType,
		Status:    status,
		Error:     errMsg,
	}
	if err := sessionrecords.NewRepository().Append(execCtx.Schema, mem, maxN); err != nil {
		execCtx.LogError("failed to write the session memory: " + err.Error())
	}
}

// runFlow is the body: timeout control, the main flow, waiting for concurrent nodes.
func runFlow(execCtx *flow.ExecContext) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v", r)
			execCtx.LogError(fmt.Sprintf("flow panic: %v", r))
		}
	}()

	// Enable the flow-level timeout
	flowTimeout := execCtx.GetFlowTimeout()
	if flowTimeout > 0 {
		var cancel context.CancelFunc
		execCtx.StdCtx, cancel = context.WithTimeout(execCtx.StdCtx, time.Duration(flowTimeout)*time.Second)
		defer cancel()
	}

	f := execCtx.Flow
	startNode := GetStartNode(f)
	if startNode == nil {
		execCtx.LogError("start node (type 'start') not found")
		return fmt.Errorf("start node (type 'start') not found")
	}
	currentProcess, err := execCtx.ProcessManager.NewProcess("", nil)
	if err != nil {
		execCtx.LogError("failed to create the process: " + err.Error())
		return errors.New("failed to create the process: " + err.Error())
	}
	err = execute(startNode, execCtx, currentProcess, 0)
	if err != nil {
		if errors.Is(err, SignalReturn) || errors.Is(err, SignalExit) {
			err = nil // return/exit signals: normal termination of the flow
		} else if errors.Is(err, SignalBreak) || errors.Is(err, SignalContinue) {
			// Safety net only: the break / continue node itself already fails when no loop can consume it (control.go
			// checkSignalScope), so a signal reaching the root means something bypassed that check. Keep a classified error
			// instead of a bare string, so a caller can still tell this apart from a script failure.
			msg := "a break / continue signal reached the flow root: no loop consumed it (config error)"
			execCtx.LogError(msg)
			err = newExecError(flow.ErrCodeConfig, msg)
		} else {
			execCtx.LogError(fmt.Sprintf("flow execution error: %v", err))
		}
	}

	// No detached goroutines are left to collect here: a concurrency scope joins its members before its host returns, so a
	// finished walk means every member has already reached a terminal state.

	return err
}

func execute(node *flow.Node, execCtx *flow.ExecContext, processID string, depth int) error {
	// Maintain the debug call stack: push on entering a child process, pop on leaving, and depth is the current
	// nesting level
	if execCtx.Debug != nil && execCtx.Debug.Enabled {
		execCtx.Debug.PushStack(processID)
		defer execCtx.Debug.PopStack()
		depth = execCtx.Debug.CurrentDepth()
	}

	currentNode := node
	f := execCtx.Flow
	// The scope this walk is inside: nil for the flow root, a function body and a branch lane, which are not hosts.
	bodyScope := execCtx.BodyScope(processID)
	for currentNode != nil {
		select {
		case <-execCtx.StdCtx.Done():
			return execCtx.StdCtx.Err()
		default:
		}

		// A draining scope stops starting new work: no further chain node and no further member. The node where the
		// walk stops is recorded as skipped so the missing tail of the body is explained by the run itself.
		if bodyScope != nil && bodyScope.Draining() {
			execCtx.LogInfo("scope stopped starting work, stopping the walk at node: " + currentNode.ID)
			markScopeDraining(currentNode, execCtx, processID, bodyScope)
			return nil
		}

		// Debug suspend hook: decide before executing whether to pause / step / hit a breakpoint
		if execCtx.Debug != nil && execCtx.Debug.Enabled {
			execCtx.Debug.SuspendIfNeeded(currentNode, processID, depth)
			// The all-stop barrier: if another node has already suspended, this node blocks too, which is what
			// makes it an all-stop
			execCtx.Debug.WaitIfGloballySuspended()
			// stop wakes it through cancel; check again whether it has been cancelled
			if execCtx.StdCtx.Err() != nil {
				return execCtx.StdCtx.Err()
			}
		}

		// A scope member (data.concurrent): dispatched to its own goroutine and child process. The goroutine executes only
		// this node; this walk starts it and continues along the outgoing edge, which is how the next member is reached.
		if isConcurrencyMember(currentNode) {
			if bodyScope == nil {
				return newExecError(flow.ErrCodeConfig, fmt.Sprintf(
					"node %s carries the concurrent flag but its body has no concurrency scope (only a process / loop body is one)",
					currentNode.ID))
			}
			if err := dispatchMember(currentNode, execCtx, processID, bodyScope); err != nil {
				execCtx.LogError(fmt.Sprintf("failed to dispatch the member: %s: %v", currentNode.ID, err))
				return err
			}
			execCtx.LogInfo("dispatching concurrent member: " + currentNode.ID)
		} else {
			err := executeNode(currentNode, execCtx, processID)
			if err != nil {
				// `exit` is global and immediate: it stops the work this scope already started, not just the walk. The
				// scope records the node that fired it, so the members it cancels say why, and a queued member is
				// abandoned instead of starting after the run has already ended.
				if bodyScope != nil && errors.Is(err, SignalExit) {
					bodyScope.StopOnExit(currentNode.ID)
				}
				// Name the failure: the container above reports this error as its own, and its ErrMsg is what the author
				// reads in the run record and what an on_error handler receives as error_message. Left unstamped, a
				// process or loop says "connection refused" with no hint of which child said it. Only when empty, so a
				// nested container names its own direct child and the inner text keeps the deeper detail.
				var ee *ExecError
				if errors.As(err, &ee) && ee.NodeID == "" {
					ee.NodeID = currentNode.ID
				}
				return err
			}
		}

		if len(currentNode.OutEdges) == 0 {
			break
		}

		// A branch node: every outgoing edge is a branch edge and was already handled by BranchExecutor in a child
		// process.
		// An intent node is the same: all its outgoing edges are branch edges, handled by IntentExecutor in a child
		// process.
		if currentNode.Type == string(flow.NodeTypeBranch) || currentNode.Type == string(flow.NodeTypeIntent) {
			break
		}

		// Apart from branch / intent nodes an outgoing edge must be unique: with several, only OutEdges[0] is
		// followed and the rest look valid while never executing — the hardest class of fault to trace (the flow
		// looks finished, a section is missing, and the log says nothing).
		// The UI deduplicates by handle base name (right / bottom of one node collapse into the same base name),
		// so normal drawing cannot produce several; this mainly catches imported asset packages and diagrams saved
		// by calling the API directly.
		if len(currentNode.OutEdges) > 1 {
			return newExecError(flow.ErrCodeConfig, fmt.Sprintf(
				"node %s(%s) has %d outgoing edges; this type allows only one - remove the extra edges",
				currentNode.ID, currentNode.Type, len(currentNode.OutEdges)))
		}

		// Determine the next node
		var nextNodeID string
		nextNodeID = currentNode.OutEdges[0].Target

		currentNode = GetNodeByID(f, nextNodeID)
	}
	return nil
}

// shouldBypassOnError decides whether an error should bypass on_error handling
// (control signals / ctx cancellation / internal errors / config errors / a non-Failed status).
// With (true, err) the caller returns err straight away; with (false, nil) the on_error flow continues.
// The first attempt and retries share this decision, so both paths behave the same.
func shouldBypassOnError(err error, nr *flow.NodeResult, ctx *flow.ExecContext) (bool, error) {
	// Control signals: no on_error, returned as they are
	if isControlSignal(err) {
		return true, err
	}
	// ctx cancelled: no on_error, returned straight away
	// Note: nr's status should have been set inside executor.Execute, so nr.Finish() is not called again here
	if ctx.StdCtx.Err() != nil {
		return true, ctx.StdCtx.Err()
	}
	// A system internal error: no on_error, returned straight away
	var ee *ExecError
	if errors.As(err, &ee) && ee.Code == flow.ErrCodeInternal {
		return true, err
	}
	// A system configuration error: no on_error, returned straight away
	if errors.As(err, &ee) && ee.Code == flow.ErrCodeConfig {
		return true, err
	}
	if nr.Status != flow.StatusFailed {
		return true, err
	}
	return false, nil
}

func executeNode(node *flow.Node, ctx *flow.ExecContext, processID string) error {
	// start / entry / layout are structural nodes with no executor: start is the flow's root entry, entry is a
	// container's entry, layout is a pure placeholder (not executed, the outgoing edge is followed).
	if node.Type == string(flow.NodeTypeStart) ||
		node.Type == string(flow.NodeTypeEntry) ||
		node.Type == string(flow.NodeTypeLayout) {
		return nil
	}

	// Preventive check: when ctx is already cancelled / timed out, create nr and record the error status
	if err := ctx.StdCtx.Err(); err != nil {
		nr := flow.NewNodeResult(node, processID, ctx)

		if recordErr := ctx.RecordNodeExecution(processID, node.ID, nr); recordErr != nil {
			return failf(nr, flow.ErrCodeInternal, "failed to record the node run: "+recordErr.Error())
		}

		// Set the right status and error code for the kind of cancellation
		if err == context.Canceled {
			nr.Finish(flow.StatusCancelled, flow.ErrCodeCancel, err.Error())
		} else {
			nr.Finish(flow.StatusFailed, flow.ErrCodeTimeout, err.Error())
		}
		return err
	}

	// The first attempt
	nr := flow.NewNodeResult(node, processID, ctx)

	if err := ctx.RecordNodeExecution(processID, node.ID, nr); err != nil {
		return failf(nr, flow.ErrCodeInternal, "failed to record the node run: "+err.Error())
	}

	executor, exists := GetExecutor(node.Type)
	if !exists {
		// Classified as config rather than internal: reaching this almost always means "the diagram still holds a
		// retired node type" (such as knowledge, which the frontend keeps only to render old diagrams). Reporting
		// internal suggests the service is broken, while this is a configuration problem the user can fix.
		return failf(nr, flow.ErrCodeConfig,
			fmt.Sprintf("node type %q has no executor: the type may have been retired (for example knowledge); replace the node and try again", node.Type))
	}
	currentErr := executor.Execute(node, ctx, processID, nr)
	if currentErr == nil {
		return nil
	}

	// Safety check: a fallback in case the executor did not set nr.Status correctly
	if nr.Status == flow.StatusRunning {
		nr.Finish(flow.StatusFailed, flow.ErrCodeInternal,
			fmt.Sprintf("the executor of node %s(%s) did not set the node status: %v", node.Label(), node.ID, currentErr))
	}

	// Control signals / ctx cancellation / internal errors / config errors / a non-Failed status: no on_error, returned
	// straight away
	if bypass, retErr := shouldBypassOnError(currentErr, nr, ctx); bypass {
		return retErr
	}

	// Validate the errCode (a code set by a script is bound by the same rule)
	// An empty ErrorCodes means no validation (a container node passes the child chain's code through); a non-empty
	// one is validated
	caps := executor.Capabilities()
	if len(caps.ErrorCodes) > 0 && !containsString(caps.ErrorCodes, nr.ErrCode) {
		nr.AddLogf("warning: errCode %s is not declared by node %s, downgraded to other", nr.ErrCode, node.Type)
		// Put the original code into the message: otherwise the user only sees other, cannot tell what the node
		// actually reported, and the on_error they configured on the original code can never match.
		nr.ErrMsg = fmt.Sprintf("%s (original error code %s)", nr.ErrMsg, nr.ErrCode)
		nr.ErrCode = flow.ErrCodeOther
	}

	cfg := parseOnErrorConfig(node)
	// An action the node does not declare is treated as unconfigured, from the same source as HasPost: the frontend
	// panel renders from Capabilities.Actions, so if the backend accepted anything, a hand-written diagram would
	// bypass the panel's limits (configuring skip on intent, retry on branch, say) and the declaration would mean
	// nothing.
	if cfg.Retry != nil && !containsAction(caps.Actions, flow.ErrorActionRetry) {
		nr.AddLogf("warning: node %s does not declare a retry action, ignoring that config", node.Type)
		cfg.Retry = nil
	}
	if cfg.Skip != nil && !containsAction(caps.Actions, flow.ErrorActionSkip) {
		nr.AddLogf("warning: node %s does not declare a skip action, ignoring that config", node.Type)
		cfg.Skip = nil
	}
	if cfg.Handler != nil && !containsAction(caps.Actions, flow.ErrorActionHandler) {
		nr.AddLogf("warning: node %s does not declare a handler action, ignoring that config", node.Type)
		cfg.Handler = nil
	}
	// When the node does not declare HasPost, ignore the configured post so frontend and backend agree
	if !caps.HasPost {
		cfg.Post = nil
	}
	currentCode := nr.ErrCode
	currentMsg := nr.ErrMsg
	retryCount := 0
	// activeNR is "the current attempt": nr for the first one, replaced by retryNR after a retry.
	//
	// Why the distinction is necessary: RecordNodeExecution keys on processID_nodeID, so a retry overwrites the
	// previous entry, which means ExecutedNodeMap (and the final status downstream and in the UI read from it)
	// points at the **last** attempt. If skip / post still acted on the first nr, the node would show failed while
	// the flow moved on — a contradiction — and the outputs post sets would be written into a record nobody reads.
	activeNR := nr

	for {
		// 1. retry
		if cfg.Retry != nil && matchCodes(cfg.Retry.Codes, currentCode, currentMsg, cfg.Retry.Patterns) {
			if retryCount < cfg.Retry.Max {
				retryCount++
				if cfg.Retry.DelayMs > 0 {
					// Log it on the current attempt (the first one or the previous retry), never forever on the first
					// record
					activeNR.AddLogf("retry #%d, running in %dms", retryCount, cfg.Retry.DelayMs)
					select {
					case <-time.After(time.Duration(cfg.Retry.DelayMs) * time.Millisecond):
					case <-ctx.StdCtx.Done():
						return ctx.StdCtx.Err()
					}
				}

				retryNR := flow.NewNodeResult(node, processID, ctx)
				retryNR.RetryCount = retryCount

				if err := ctx.RecordNodeExecution(processID, node.ID, retryNR); err != nil {
					retryNR.Finish(flow.StatusFailed, flow.ErrCodeInternal, "failed to record the node run: "+err.Error())
					return newExecError(retryNR.ErrCode, retryNR.ErrMsg)
				}

				retryErr := executor.Execute(node, ctx, processID, retryNR)
				if retryErr == nil {
					return nil // the retry succeeded
				}

				//nr.RetryCount = retryCount

				// A failed retry also needs the bypass check: control signals / timeouts / internal errors do not go
				// through on_error
				if bypass, retErr := shouldBypassOnError(retryErr, retryNR, ctx); bypass {
					return retErr
				}

				// Validate the errCode (same as the first attempt)
				if len(caps.ErrorCodes) > 0 && !containsString(caps.ErrorCodes, retryNR.ErrCode) {
					retryNR.AddLogf("warning: retry errCode %s is not declared by node %s, downgraded to other", retryNR.ErrCode, node.Type)
					retryNR.ErrCode = flow.ErrCodeOther
				}

				// Update the error and go back to the top of the chain to match again
				activeNR = retryNR
				currentErr = retryErr
				currentCode = retryNR.ErrCode
				currentMsg = retryNR.ErrMsg
				continue
			}
		}

		// 2. skip
		// Note that all three branches below use activeNR: the matched code is "this attempt's", so the status and
		// outputs to change have to land on the same record (see the note on activeNR).
		if cfg.Skip != nil && matchCodes(cfg.Skip.Codes, currentCode, currentMsg, cfg.Skip.Patterns) {
			activeNR.ErrorAction = flow.ErrorActionSkip
			activeNR.Skip()
			applyPost(activeNR, cfg.Post)
			return nil
		}

		// 3. handler
		if cfg.Handler != nil && matchCodes(cfg.Handler.Codes, currentCode, currentMsg, cfg.Handler.Patterns) {
			action, handlerErr := runHandlerScript(ctx, cfg.Handler, node, activeNR)
			if handlerErr != nil {
				// The node keeps failing with its original code and message: the handler failed, so it did not decide
				// anything. The failure reason gets its own field instead of being appended to ErrMsg, so the code that
				// on_error matches on stays the one the root cause produced.
				activeNR.HandlerError = handlerErr.Error()
				activeNR.AddLogf("failed to run the handler script: %v", handlerErr)
				applyPost(activeNR, cfg.Post)
				return newExecError(activeNR.ErrCode, activeNR.ErrMsg)
			}
			if action == "skip" {
				activeNR.ErrorAction = flow.ErrorActionSkip
				activeNR.Skip()
				applyPost(activeNR, cfg.Post)
				return nil
			}
			applyPost(activeNR, cfg.Post)
			// Use the values on the record rather than the locals from the top of the loop: the handler script may
			// change error_code / error_message in its return value, those changes land on activeNR, and using the
			// locals would lose them.
			return newExecError(activeNR.ErrCode, activeNR.ErrMsg)
		}

		// 4. nothing matched -> fail
		applyPost(activeNR, cfg.Post)
		return newExecError(activeNR.ErrCode, activeNR.ErrMsg)
	}
}

// ---- on_error config parsing ----

type OnErrorConfig struct {
	Retry   *RetryRule
	Skip    *SkipRule
	Handler *HandlerRule
	Post    *PostConfig
}

type RetryRule struct {
	Codes    []string
	Patterns []string
	Max      int
	DelayMs  int
}

type SkipRule struct {
	Codes    []string
	Patterns []string
}

type HandlerRule struct {
	Codes    []string
	Patterns []string
	Lang     string
	Inputs   map[string]interface{}
	Script   string
}

type PostConfig struct {
	SetOutputs map[string]interface{}
	Handler    *PostScript
}

type PostScript struct {
	Lang   string
	Inputs map[string]interface{}
	Script string
}

func parseOnErrorConfig(node *flow.Node) *OnErrorConfig {
	cfg := &OnErrorConfig{}
	if node.Data == nil || node.Data.Config == nil {
		return cfg
	}
	onError, ok := node.Data.Config["on_error"].(map[string]interface{})
	if !ok {
		return cfg
	}

	if retry, ok := onError["retry"].(map[string]interface{}); ok {
		cfg.Retry = &RetryRule{}
		cfg.Retry.Codes = parseStringSlice(retry["codes"])
		cfg.Retry.Patterns = parseStringSlice(retry["patterns"])
		if max, ok := retry["max"].(float64); ok {
			cfg.Retry.Max = int(max)
		}
		if delay, ok := retry["delay_ms"].(float64); ok {
			cfg.Retry.DelayMs = int(delay)
		}
	}

	if skip, ok := onError["skip"].(map[string]interface{}); ok {
		cfg.Skip = &SkipRule{}
		cfg.Skip.Codes = parseStringSlice(skip["codes"])
		cfg.Skip.Patterns = parseStringSlice(skip["patterns"])
	}

	if handler, ok := onError["handler"].(map[string]interface{}); ok {
		cfg.Handler = &HandlerRule{}
		cfg.Handler.Codes = parseStringSlice(handler["codes"])
		cfg.Handler.Patterns = parseStringSlice(handler["patterns"])
		cfg.Handler.Lang, _ = handler["lang"].(string)
		if cfg.Handler.Lang == "" {
			cfg.Handler.Lang = "lua"
		}
		cfg.Handler.Script, _ = handler["script"].(string)
		if inputs, ok := handler["inputs"].(map[string]interface{}); ok {
			cfg.Handler.Inputs = inputs
		}
	}

	if post, ok := onError["post"].(map[string]interface{}); ok {
		cfg.Post = &PostConfig{}
		if outputs, ok := post["set_outputs"].(map[string]interface{}); ok {
			cfg.Post.SetOutputs = outputs
		}
		if handler, ok := post["handler"].(map[string]interface{}); ok {
			cfg.Post.Handler = &PostScript{}
			cfg.Post.Handler.Lang, _ = handler["lang"].(string)
			cfg.Post.Handler.Script, _ = handler["script"].(string)
			if inputs, ok := handler["inputs"].(map[string]interface{}); ok {
				cfg.Post.Handler.Inputs = inputs
			}
		}
	}

	return cfg
}

func parseStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var result []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// ---- matching ----

func matchCodes(codes []string, errCode string, errMsg string, patterns []string) bool {

	for _, code := range codes {
		if code == errCode {
			if code == flow.ErrCodeOther && len(patterns) > 0 {
				for _, p := range patterns {
					if matchPattern(errMsg, p) {
						return true
					}
				}
				continue
			}
			return true
		}
	}
	return false
}

func matchPattern(errMsg string, pattern string) bool {
	hasPrefix := strings.HasPrefix(pattern, "%")
	hasSuffix := strings.HasSuffix(pattern, "%")
	if hasPrefix && hasSuffix {
		return strings.Contains(errMsg, pattern[1:len(pattern)-1])
	}
	if hasPrefix {
		return strings.HasSuffix(errMsg, pattern[1:])
	}
	if hasSuffix {
		return strings.HasPrefix(errMsg, pattern[:len(pattern)-1])
	}
	return strings.Contains(errMsg, pattern)
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// containsAction reports whether a node's capability declaration includes an on_error action.
func containsAction(actions []flow.ErrorAction, a flow.ErrorAction) bool {
	for _, item := range actions {
		if item == a {
			return true
		}
	}
	return false
}

// ---- handler script execution ----

func runHandlerScript(ctx *flow.ExecContext, cfg *HandlerRule,
	node *flow.Node, nr *flow.NodeResult) (string, error) {

	// GetVariables is all-or-nothing: a non-nil error means none of the declared inputs were resolved. Running the
	// script anyway would hand the author a map that only carries the built-ins, and the symptom would surface as a
	// Lua nil somewhere in their code instead of "this input could not be read".
	inputs, err := ctx.GetVariables(cfg.Inputs, nr.ProcessID)
	if err != nil {
		return "", fmt.Errorf("resolve handler inputs: %w", err)
	}
	inputs["error_code"] = nr.ErrCode
	inputs["error_message"] = nr.ErrMsg
	inputs["error_node_id"] = node.ID
	inputs["error_node_type"] = node.Type
	inputs["retry_count"] = nr.RetryCount

	result, err := ExecuteScript(ctx.StdCtx, cfg.Lang, cfg.Script, inputs, nr, 10)
	if err != nil {
		return "", err
	}

	// Return parsing: a string keeps the old forms ("skip"/"fail"); a map may carry action plus error_code /
	// error_message (replacing wf_set_error).
	action := "fail"
	switch v := result.(type) {
	case string:
		if v == "skip" {
			action = "skip"
		}
	case map[string]any:
		if a, ok := v["action"].(string); ok && a == "skip" {
			action = "skip"
		}
		if code, ok := v["error_code"].(string); ok && code != "" {
			nr.ErrCode = code
		}
		if msg, ok := v["error_message"].(string); ok && msg != "" {
			nr.ErrMsg = msg
		}
	}
	return action, nil
}

// ---- post handling ----

func applyPost(nr *flow.NodeResult, post *PostConfig) {
	if post == nil {
		return
	}
	// Sorted so that two undeclared keys always produce the same PostError text: it is stored on the record, and a
	// message whose order depends on map iteration is a diff nobody can read.
	paths := make([]string, 0, len(post.SetOutputs))
	for path := range post.SetOutputs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		// A key that is not declared in the node's outputs cannot be written (SetOutputVar refuses it). This is a post
		// failure like any other, not just a log line: dropping it silently leaves the author believing the marker is
		// there while downstream reads the type default - the exact failure post exists to prevent.
		if err := nr.SetOutputVarWithFire(path, post.SetOutputs[path]); err != nil {
			addPostError(nr, fmt.Sprintf("failed to write post output %s: %v", path, err))
		}
	}
	if post.Handler != nil {
		inputs, err := nr.Ctx.GetVariables(post.Handler.Inputs, nr.ProcessID)
		if err != nil {
			// Same reasoning as the set_outputs case above: the author declared these inputs for a reason, and running
			// the script without them turns their absence into a type default rather than an error.
			nr.AddLogf("failed to resolve the post-processing inputs: %v", err)
			addPostError(nr, fmt.Sprintf("failed to resolve the post-processing inputs: %v", err))
		} else {
			inputs["error_code"] = nr.ErrCode
			inputs["error_message"] = nr.ErrMsg
			inputs["retry_count"] = nr.RetryCount
			result, err := ExecuteScript(nr.Ctx.StdCtx, post.Handler.Lang, post.Handler.Script, inputs, nr, 10)
			if err != nil {
				nr.AddLogf("failed to run the post-processing script: %v", err)
				addPostError(nr, err.Error())
			} else if result != nil {
				if aerr := applyScriptResult(nr, result); aerr != nil {
					nr.AddLogf("failed to write the post-processing script result: %v", aerr)
					addPostError(nr, aerr.Error())
				}
			}
		}
	}
}

// addPostError appends to PostError instead of overwriting it: a bad set_outputs key and a failing script are two
// separate facts, and the second one must not erase the first (the record is the only structured trace either way).
func addPostError(nr *flow.NodeResult, msg string) {
	if nr.PostError == "" {
		nr.PostError = msg
		return
	}
	nr.PostError += "; " + msg
}

func GetNodeByID(f *flow.Flow, id string) *flow.Node {
	if f.NodeMap == nil {
		return nil
	}
	return f.NodeMap[id]
}

// GetStartNode finds the workflow's start node by node type.
// The id "1" is only a convention of the frontend and the generators; it is preferred so existing flows keep
// their behaviour, and the type is the fallback — the identity of "the start node" must not be pinned to a
// literal id.
func GetStartNode(f *flow.Flow) *flow.Node {
	if f.NodeMap == nil {
		return nil
	}
	if n, ok := f.NodeMap["1"]; ok && n.Type == string(flow.NodeTypeStart) {
		return n
	}
	for _, n := range f.Nodes {
		if n.Type == string(flow.NodeTypeStart) {
			return n
		}
	}
	return nil
}
