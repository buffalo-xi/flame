package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type NodeStatus string

const (
	StatusSuccess   NodeStatus = "success"
	StatusFailed    NodeStatus = "failed"
	StatusSkipped   NodeStatus = "skipped"
	StatusCancelled NodeStatus = "cancelled"
	StatusRunning   NodeStatus = "running"
)

// ErrorAction is the error handling action.
type ErrorAction string

const (
	ErrorActionSkip    ErrorAction = "skip"
	ErrorActionRetry   ErrorAction = "retry"
	ErrorActionHandler ErrorAction = "handler"
)

// Error codes.
const (
	ErrCodeTimeout    = "timeout"
	ErrCodeBusy       = "busy"   // sandbox concurrency quota exhausted (sandboxd rejected the slot, script never ran)
	ErrCodeCancel     = "cancel" // cancelled by the user
	ErrCodeConfig     = "config"
	ErrCodeScript     = "script"
	ErrCodeCapability = "capability" // the platform has not enabled this capability for the tenant (sandbox scripts etc.)
	ErrCodeVar        = "var"
	ErrCodeConnection = "connection"
	ErrCodeAuth       = "auth"
	ErrCodeNotFound   = "not_found"
	ErrCodeInternal   = "internal"
	ErrCodeOther      = "other"
)

// AllErrorCodes returns every global error code constant.
func AllErrorCodes() []string {
	return []string{
		ErrCodeTimeout,
		ErrCodeBusy,
		ErrCodeCancel,
		ErrCodeConfig,
		ErrCodeScript,
		ErrCodeCapability,
		ErrCodeVar,
		ErrCodeConnection,
		ErrCodeAuth,
		ErrCodeNotFound,
		ErrCodeInternal,
		ErrCodeOther,
	}
}

type NodeResult struct {
	ID          string         `json:"id"`
	NodeID      string         `json:"node_id"` // the id of the flow node
	ProcessID   string         `json:"process_id"`
	Ctx         *ExecContext   `json:"-"`
	Status      NodeStatus     `json:"status"`
	InputVars   map[string]any `json:"input_vars"`
	OutputVars  map[string]any `json:"output_vars"`
	LocalVars   map[string]any `json:"local_vars"`
	RunLogs     []string       `json:"run_logs"`
	StartTime   time.Time      `json:"start_time"`
	EndTime     time.Time      `json:"end_time"`
	DurationMs  int64          `json:"duration_ms"`
	ErrCode     string         `json:"err_code"`
	ErrMsg      string         `json:"err_msg"`
	ErrStack    string         `json:"err_stack"`
	RetryCount  int            `json:"retry_count"`
	ErrorAction ErrorAction    `json:"error_action"`
	PostError   string         `json:"post_error"` // post-processing script error (does not affect the node status)

	NodeType NodeType `json:"node_type"`

	// Per-node-type results, holding the data of the corresponding node type.
	LoopResult     *LoopResult                 `json:"loop_result"`
	ProcessResult  *ProcessResult              `json:"process_result"`
	FunctionResult *FunctionResult             `json:"function_result"`
	CodeResult     *CodeResult                 `json:"code_result"`
	ForkResult     *ForkResult                 `json:"fork_result"`
	WaitResult     *WaitResult                 `json:"wait_result"`
	BranchResult   *BranchResult               `json:"branch_result"`
	HttpResult     *HttpResult                 `json:"http_result"`
	OutputBindings map[string][]*OutputBinding `json:"-"` // streaming bindings of the output variables
	mu             sync.RWMutex
	// warned holds the warning keys already reported, used by WarnOnce for de-duplication
	// (never persisted, never exposed).
	warned map[string]struct{}
}

// NewNodeResult creates a bare NodeResult; the common fields are set by the caller.
func NewNodeResult(node *Node, processID string, ctx *ExecContext) *NodeResult {
	return &NodeResult{
		NodeID:     node.ID,
		ProcessID:  processID,
		ID:         uuid.Must(uuid.NewV7()).String(),
		RetryCount: 0,
		Ctx:        ctx,
		Status:     StatusRunning,
		StartTime:  time.Now(),
		NodeType:   NodeType(node.Type),
	}
}

// Finish ends the execution: sets the final status and duration and logs it.
// First writer wins: an ErrCode/ErrMsg already set by a script through wf_set_error is not overwritten.
func (nr *NodeResult) Finish(status NodeStatus, errCode string, errMsg string) {
	nr.Status = status
	nr.EndTime = time.Now()
	nr.DurationMs = nr.EndTime.Sub(nr.StartTime).Milliseconds()

	// Values set by a script through wf_set_error are kept (first writer wins).
	if nr.ErrCode == "" {
		if status != StatusSuccess && strings.TrimSpace(errCode) == "" {
			errCode = ErrCodeOther
		}
		nr.ErrCode = errCode
	}
	if nr.ErrMsg == "" {
		nr.ErrMsg = errMsg
	}

	if status == StatusSuccess {
		nr.AddLog("execution succeeded")
	} else if status == StatusSkipped {
		nr.AddLog("execution skipped")
		// In debug mode, tell the debug source that the node was skipped (the frontend marks it "skipped").
		if nr.Ctx != nil && nr.Ctx.Debug != nil {
			if hook := nr.Ctx.Debug.skipHook; hook != nil {
				hook(nr.NodeID, nr.nodeName())
			}
		}
	} else {
		nr.AddLog("execution failed: " + nr.ErrMsg)
		// In debug mode, tell the debug source that the node failed (the frontend marks it "failed").
		if nr.Ctx != nil && nr.Ctx.Debug != nil {
			if hook := nr.Ctx.Debug.failHook; hook != nil {
				hook(nr.NodeID, nr.nodeName(), nr.ErrMsg)
			}
		}
	}

	// In debug mode, tell the debug source that the node finished (success, failure or skip),
	// so the WS debug source can accumulate the latest data of the node (last execution per node_id).
	if nr.Ctx != nil && nr.Ctx.Debug != nil {
		if hook := nr.Ctx.Debug.doneHook; hook != nil {
			hook(nr)
		}
	}
}

// nodeName returns the node name, looked up in flow.NodeMap (empty when not found).
// The name lives in Data.Label (the convention of the frontend Vue flow), there is no separate name field.
func (nr *NodeResult) nodeName() string {
	if nr.Ctx != nil && nr.Ctx.Flow != nil && nr.Ctx.Flow.NodeMap != nil {
		if n := nr.Ctx.Flow.NodeMap[nr.NodeID]; n != nil {
			if n.Data != nil {
				return n.Data.Label
			}
		}
	}
	return ""
}

// Skip marks the node as skipped (called when on_error skip/handler returns skip).
// Unlike Finish(StatusSkipped), Skip does not reset EndTime/DurationMs/ErrCode: it only sets the
// status and fires the debug-source callback (consistent with the skipped branch of Finish).
func (nr *NodeResult) Skip() {
	nr.Status = StatusSkipped
	nr.AddLog("execution skipped")
	if nr.Ctx != nil && nr.Ctx.Debug != nil {
		if hook := nr.Ctx.Debug.skipHook; hook != nil {
			hook(nr.NodeID, nr.nodeName())
		}
		// After skipping, tell the debug source to refresh the data as well (overriding a previous failed state).
		if hook := nr.Ctx.Debug.doneHook; hook != nil {
			hook(nr)
		}
	}
}

// SetInputVars sets the unified input variables of the node (locked).
func (nr *NodeResult) SetInputVars(vars map[string]any) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.InputVars = vars
}

// GetInputVars reads the unified input variables of the node (locked).
func (nr *NodeResult) GetInputVars() map[string]any {
	nr.mu.RLock()
	defer nr.mu.RUnlock()
	return nr.InputVars
}

type NodeType string

const (
	// Structural nodes: start is generated by the tooling/frontend, entry by the container, and
	// layout is a pure placeholder. None of the three has an executor; the runner skips it and
	// continues along the outgoing edges.
	NodeTypeStart  NodeType = "start"
	NodeTypeEntry  NodeType = "entry"
	NodeTypeLayout NodeType = "layout"

	// The names below map one to one to the names registered by executors.RegisterExecutor;
	// keep them lower-case and singular.
	NodeTypeLLM      NodeType = "llm"
	NodeTypeCode     NodeType = "code"
	NodeTypeHTTP     NodeType = "http"
	NodeTypeMCP      NodeType = "mcp"
	NodeTypeRPC      NodeType = "rpc"
	NodeTypeBranch   NodeType = "branch"
	NodeTypeIntent   NodeType = "intent"
	NodeTypeLoop     NodeType = "loop"
	NodeTypeProcess  NodeType = "process"
	NodeTypeFunction NodeType = "function"
	NodeTypeCall     NodeType = "call"
	NodeTypeAssign   NodeType = "assign"
	NodeTypeMemory   NodeType = "memory"
	NodeTypeWait     NodeType = "wait"
	NodeTypeFork     NodeType = "fork"
	NodeTypeResult   NodeType = "result"
	NodeTypeProgress NodeType = "progress"
	NodeTypeBreak    NodeType = "break"
	NodeTypeContinue NodeType = "continue"
	NodeTypeReturn   NodeType = "return"
	NodeTypeExit     NodeType = "exit"
)

type CodeResult struct {
	mu sync.RWMutex
}

type LoopResult struct {
	LoopConfig map[string]interface{} `json:"loop"` // loop node config (including type)
	LoopVars   map[string]interface{} `json:"loop_vars"`
	Iterations int                    `json:"iterations"` // number of iterations actually executed
	mu         sync.RWMutex
}
type ProcessResult struct {
	mu sync.RWMutex
}

type FunctionResult struct {
	ParamVars map[string]any `json:"param_vars"`
	mu        sync.RWMutex
}

// ForkState is the group state of a ForkResult.
type ForkState int

// Group states of a ForkResult.
const (
	ForkStateActive    ForkState = iota // normal, further Add calls are allowed
	ForkStateWaiting                    // waiting, Add is rejected
	ForkStateDone                       // finished (every task Done), Add/Wait are rejected
	ForkStateAbandoned                  // waiting abandoned (timeout); tasks may still run, Add/Wait are rejected
)

type ForkResult struct {
	MaxConcurrency int `json:"max_concurrency"`
	mu             sync.Mutex
	wg             sync.WaitGroup
	sem            chan struct{} // concurrency semaphore; nil when MaxConcurrency <= 0 (unlimited)
	count          int           // number of tasks added so far
	state          ForkState     // ForkStateActive / ForkStateWaiting / ForkStateDone / ForkStateAbandoned
	members        []ForkMember  // added members (processID + nodeID), used by wait to scan statuses
}

// ForkMember identifies one concurrent branch added to a fork group, used by wait to scan node statuses.
type ForkMember struct {
	ProcessID string
	NodeID    string
}

// NewForkResult creates a ForkResult; maxConcurrency <= 0 means unlimited.
func NewForkResult(maxConcurrency int) *ForkResult {
	fr := &ForkResult{MaxConcurrency: maxConcurrency}
	if maxConcurrency > 0 {
		fr.sem = make(chan struct{}, maxConcurrency)
	}
	return fr
}

// Add adds delta tasks; once in the wait/done state it refuses to add (a design error).
// With MaxConcurrency configured, acquiring the semaphore blocks until a slot frees up (that is the
// concurrency limit: one slot is handed over when one task exits); a cancelled ctx rolls back the
// acquired slots and the counters.
// member is the identity added by this call (processID + nodeID), used by wait to scan statuses; it
// is ignored when delta > 1.
func (fr *ForkResult) Add(delta int, ctx context.Context, member ForkMember) error {
	fr.mu.Lock()
	if fr.state >= ForkStateWaiting {
		fr.mu.Unlock()
		return fmt.Errorf("cannot add: waitgroup is already in wait/done state")
	}
	fr.count += delta
	fr.wg.Add(delta)
	if delta == 1 {
		fr.members = append(fr.members, member)
	}
	fr.mu.Unlock()

	// Semaphore throttling (blocking outside the lock, so the lock is not held for long).
	if fr.sem != nil {
		for i := 0; i < delta; i++ {
			select {
			case fr.sem <- struct{}{}:
			case <-ctx.Done():
				// ctx cancelled: roll back the acquired semaphore slots and the wg/count bookkeeping.
				for j := 0; j < i; j++ {
					<-fr.sem
				}
				fr.mu.Lock()
				fr.count -= delta
				if delta == 1 {
					// Roll back the member record (drop the one appended last).
					if len(fr.members) > 0 {
						fr.members = fr.members[:len(fr.members)-1]
					}
				}
				fr.mu.Unlock()
				fr.wg.Add(-delta)
				return ctx.Err()
			}
		}
	}
	return nil
}

// Members returns a snapshot of the member list (used by wait to scan node statuses).
func (fr *ForkResult) Members() []ForkMember {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	result := make([]ForkMember, len(fr.members))
	copy(result, fr.members)
	return result
}

// Done marks one task finished and releases its semaphore slot (handing it to the next task).
func (fr *ForkResult) Done() {
	if fr.sem != nil {
		<-fr.sem
	}
	fr.wg.Done()
}

// Wait switches to the waiting state and waits for every added task to finish (no timeout).
// It returns true on normal completion and false on an invalid state (Wait was already called).
func (fr *ForkResult) Wait() (bool, error) {
	return fr.waitInternal(context.Background(), -1)
}

// WaitTimeout switches to the waiting state and waits for every added task to finish.
// Returns (completed, err):
//   - completed: true on normal completion, false on timeout or invalid state;
//   - err: non-nil on an invalid state.
//
// timeoutSeconds <= 0 means no timeout.
func (fr *ForkResult) WaitTimeout(timeoutSeconds int) (bool, error) {
	return fr.waitInternal(context.Background(), timeoutSeconds)
}

// WaitTimeoutCtx is WaitTimeout plus a ctx: it returns immediately on cancellation or timeout, with
// err = ctx.Err(). Node-level waits must use this one — otherwise a wait node keeps hanging until the
// concurrent branches finish on their own after the user stopped the flow.
func (fr *ForkResult) WaitTimeoutCtx(ctx context.Context, timeoutSeconds int) (bool, error) {
	return fr.waitInternal(ctx, timeoutSeconds)
}

// waitInternal returns (completed bool, err error):
//   - completed: true on normal completion, false on timeout or cancellation;
//   - err: set on an invalid state or when ctx is cancelled.
func (fr *ForkResult) waitInternal(ctx context.Context, timeoutSeconds int) (bool, error) {
	fr.mu.Lock()
	if fr.state != ForkStateActive {
		fr.mu.Unlock()
		return false, fmt.Errorf("cannot wait: already in wait/done state")
	}
	fr.state = ForkStateWaiting
	fr.mu.Unlock()

	done := make(chan struct{})
	go func() {
		fr.wg.Wait()
		close(done)
	}()

	var timeoutCh <-chan time.Time
	if timeoutSeconds > 0 {
		timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	completed := true
	select {
	case <-done:
	case <-timeoutCh:
		completed = false
	case <-ctx.Done():
		// Cancellation is not a timeout: the wait is abandoned the same way and marked Abandoned;
		// the background tasks still call Done() and settle their counters, nobody collects results.
		fr.mu.Lock()
		fr.state = ForkStateAbandoned
		fr.mu.Unlock()
		return false, ctx.Err()
	}

	fr.mu.Lock()
	if completed {
		fr.state = ForkStateDone
	} else {
		// Timeout abandoned the wait: the background tasks may still run, so mark it Abandoned.
		// Their Done() calls still decrement the wg counter, but no wait collects the results anymore.
		fr.state = ForkStateAbandoned
	}
	fr.mu.Unlock()

	return completed, nil
}

// State returns the current state.
func (fr *ForkResult) State() ForkState {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.state
}

// IsAbandoned reports whether the wait was abandoned by a timeout (tasks may still be running).
func (fr *ForkResult) IsAbandoned() bool {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.state == ForkStateAbandoned
}

type WaitResult struct {
	ForkNodeID string `json:"fork_node_id"`
	mu         sync.RWMutex
}

type BranchResult struct {
	ExecutedBranches []string `json:"executed_branches"` // every branch ID actually executed, in order
	mu               sync.RWMutex
}

type HttpResult struct {
	URL     string              `json:"url"` // the rendered, actual url
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"` // headers actually sent
	Status  int                 `json:"status"`  // response status code (summary)
	mu      sync.RWMutex
}

func (br *BranchResult) AddExecutedBranch(edgeKey string) {
	br.mu.Lock()
	defer br.mu.Unlock()
	br.ExecutedBranches = append(br.ExecutedBranches, edgeKey)
}

func (br *BranchResult) GetExecutedBranches() []string {
	br.mu.RLock()
	defer br.mu.RUnlock()
	return br.ExecutedBranches
}

func (nr *NodeResult) AddLog(log string) {
	if nr == nil {
		return
	}
	nr.mu.Lock()
	defer nr.mu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	// RunLogs only: persistence is handled by persist, the live push by the websocket.
	// Do not write to stderr here, node logs are high frequency.
	nr.RunLogs = append(nr.RunLogs, "["+ts+"] "+log)
}

func (nr *NodeResult) AddLogf(format string, args ...interface{}) {
	nr.AddLog(fmt.Sprintf(format, args...))
}

// WarnOnce logs once per key inside a node and reports whether this call actually logged.
//
// It covers "a script called per frame keeps hitting the same warning": the handler runs once per
// chunk, and logging each one would flood the node log with hundreds of identical lines and hide the
// information that matters.
func (nr *NodeResult) WarnOnce(key, format string, args ...interface{}) bool {
	if nr == nil {
		return false
	}
	nr.mu.Lock()
	if nr.warned == nil {
		nr.warned = make(map[string]struct{})
	}
	if _, done := nr.warned[key]; done {
		nr.mu.Unlock()
		return false
	}
	nr.warned[key] = struct{}{}
	nr.mu.Unlock()

	nr.AddLogf(format, args...)
	return true
}

func (nr *NodeResult) GetLogs() []string {
	if nr == nil {
		return nil
	}
	nr.mu.RLock()
	defer nr.mu.RUnlock()
	result := make([]string, len(nr.RunLogs))
	copy(result, nr.RunLogs)
	return result
}

func (nr *NodeResult) GetVariable(path string) (interface{}, error) {
	if nr == nil {
		return nil, errors.New("nodeResult is nil")
	}

	nr.mu.RLock()
	defer nr.mu.RUnlock()

	if nr.OutputVars != nil {
		if value, err := GetVarByPath(nr.OutputVars, path); err == nil {
			return value, nil
		}
	}

	if nr.LocalVars != nil {
		if value, err := GetVarByPath(nr.LocalVars, path); err == nil {
			return value, nil
		}
	}

	switch nr.NodeType {
	case NodeTypeLoop:
		if nr.LoopResult == nil {
			return nil, fmt.Errorf("node type is loop but LoopResult is nil")
		}
		return nr.LoopResult.GetVariable(path)
	case NodeTypeProcess:
		if nr.ProcessResult == nil {
			return nil, fmt.Errorf("node type is process but ProcessResult is nil")
		}
		return nr.ProcessResult.GetVariable(path)
	case NodeTypeFork:
		if nr.ForkResult == nil {
			return nil, fmt.Errorf("node type is fork but ForkResult is nil")
		}
		return nr.ForkResult.GetVariable(path)
	case NodeTypeWait:
		if nr.WaitResult == nil {
			return nil, fmt.Errorf("node type is wait but WaitResult is nil")
		}
		return nr.WaitResult.GetVariable(path)
	case NodeTypeFunction:
		if nr.FunctionResult == nil {
			return nil, fmt.Errorf("node type is function but FunctionResult is nil")
		}
		return nr.FunctionResult.GetVariable(path)
	default:
		return nil, fmt.Errorf("var path %s not found", path)
	}
}

func (lr *LoopResult) GetVariable(path string) (interface{}, error) {
	if lr == nil {
		return nil, errors.New("loopResult is nil")
	}

	lr.mu.RLock()
	defer lr.mu.RUnlock()

	if lr.LoopVars != nil {
		if value, err := GetVarByPath(lr.LoopVars, path); err == nil {
			return value, nil
		}
	}

	return nil, fmt.Errorf("var path %s not found in loop", path)
}

func (pr *ProcessResult) GetVariable(path string) (interface{}, error) {
	if pr == nil {
		return nil, errors.New("processResult is nil")
	}

	return nil, fmt.Errorf("var path %s not found in process", path)
}

func (fr *ForkResult) GetVariable(path string) (interface{}, error) {
	if fr == nil {
		return nil, errors.New("forkResult is nil")
	}

	return nil, fmt.Errorf("var path %s not found in fork", path)
}

func (wr *WaitResult) GetVariable(path string) (interface{}, error) {
	if wr == nil {
		return nil, errors.New("waitResult is nil")
	}

	return nil, fmt.Errorf("var path %s not found in wait", path)
}

func (fr *FunctionResult) GetVariable(path string) (interface{}, error) {
	if fr == nil {
		return nil, errors.New("functionResult is nil")
	}

	fr.mu.RLock()
	defer fr.mu.RUnlock()

	if fr.ParamVars != nil {
		if value, err := GetVarByPath(fr.ParamVars, path); err == nil {
			return value, nil
		}
	}

	return nil, fmt.Errorf("var path %s not found in function params", path)
}

// SetParamVar sets an entry parameter value, passed in when a call node invokes the function.
func (fr *FunctionResult) SetParamVar(path string, value interface{}) error {
	if fr == nil {
		return errors.New("functionResult is nil")
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.ParamVars == nil {
		fr.ParamVars = make(map[string]interface{})
	}
	return SetVarByPath(fr.ParamVars, path, value)
}

// GetTargetDef looks up a writable variable definition.
// It searches the variable definition of the given path among the node's targets config.
func (nr *NodeResult) GetTargetDef(path string) (interface{}, error) {
	if nr == nil {
		return nil, errors.New("nodeResult is nil")
	}
	if nr.Ctx == nil || nr.Ctx.Flow == nil {
		return nil, errors.New("execution context or flow is nil")
	}

	// Extract the variable name: take the first segment split by ".", then the first split by "[".
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]

	// Look up the current node.
	node, exists := nr.Ctx.Flow.NodeMap[nr.NodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found in flow", nr.NodeID)
	}
	if node.Data == nil || node.Data.Config == nil {
		return nil, fmt.Errorf("node %s has no config", nr.NodeID)
	}

	// Read the targets config.
	targets, ok := node.Data.Config["targets"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("node %s has no targets config", nr.NodeID)
	}

	// Look up the variable definition.
	def, exists := targets[varName]
	if !exists {
		return nil, fmt.Errorf("target %s not defined", varName)
	}

	return def, nil
}

func (nr *NodeResult) SetLocalVar(path string, value interface{}) error {
	if nr == nil {
		return errors.New("nodeResult is nil")
	}
	nr.mu.Lock()
	defer nr.mu.Unlock()
	if nr.LocalVars == nil {
		nr.LocalVars = make(map[string]interface{})
	}
	// Extract the variable name: take the first segment split by ".", then the first split by "[".
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]
	if _, exists := nr.LocalVars[varName]; !exists {
		return fmt.Errorf("local var %s not defined", varName)
	}
	return SetVarByPath(nr.LocalVars, path, value)
}

func (nr *NodeResult) SetLocalVars(vars map[string]interface{}) error {
	if nr == nil {
		return errors.New("nodeResult is nil")
	}
	nr.mu.Lock()
	defer nr.mu.Unlock()
	if nr.LocalVars == nil {
		nr.LocalVars = make(map[string]interface{})
	}
	for path, value := range vars {
		varName := strings.Split(path, ".")[0]
		varName = strings.Split(varName, "[")[0]
		if _, exists := nr.LocalVars[varName]; !exists {
			return fmt.Errorf("local var %s not defined", varName)
		}
		if err := SetVarByPath(nr.LocalVars, path, value); err != nil {
			return err
		}
	}
	return nil
}

func (nr *NodeResult) SetOutputVar(path string, value interface{}) error {
	if nr == nil {
		return errors.New("nodeResult is nil")
	}
	nr.mu.Lock()
	defer nr.mu.Unlock()
	if nr.OutputVars == nil {
		nr.OutputVars = make(map[string]interface{})
	}
	// Extract the variable name: take the first segment split by ".", then the first split by "[".
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]
	if _, exists := nr.OutputVars[varName]; !exists {
		return fmt.Errorf("output var %s not defined", varName)
	}
	return SetVarByPath(nr.OutputVars, path, value)
}

// SetOutputVarWithFire writes an output variable and fires its output binding.
func (nr *NodeResult) SetOutputVarWithFire(path string, value interface{}) error {
	if err := nr.SetOutputVar(path, value); err != nil {
		return err
	}
	if nr.Ctx != nil && nr.Ctx.OutputBindingFire != nil {
		varName := strings.Split(strings.Split(path, ".")[0], "[")[0]
		return nr.Ctx.OutputBindingFire(nr, varName, value)
	}
	return nil
}

func (nr *NodeResult) SetOutputVars(vars map[string]interface{}) error {
	if nr == nil {
		return errors.New("nodeResult is nil")
	}
	nr.mu.Lock()
	defer nr.mu.Unlock()
	if nr.OutputVars == nil {
		nr.OutputVars = make(map[string]interface{})
	}
	for path, value := range vars {
		// Extract the variable name: take the first segment split by ".", then the first split by "[".
		varName := strings.Split(path, ".")[0]
		varName = strings.Split(varName, "[")[0]
		if _, exists := nr.OutputVars[varName]; !exists {
			return fmt.Errorf("output var %s not defined", varName)
		}
		if err := SetVarByPath(nr.OutputVars, path, value); err != nil {
			return err
		}
	}
	return nil
}

// SetOutputVarsWithFire writes output variables in bulk and fires their output bindings.
func (nr *NodeResult) SetOutputVarsWithFire(vars map[string]interface{}) error {
	if err := nr.SetOutputVars(vars); err != nil {
		return err
	}
	if nr.Ctx != nil && nr.Ctx.OutputBindingFire != nil {
		for varName, value := range vars {
			rootName := strings.Split(strings.Split(varName, ".")[0], "[")[0]
			if err := nr.Ctx.OutputBindingFire(nr, rootName, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// AppendOutputVarWithFire appends to an output variable and fires its output binding.
// When chunk is a string it is appended to the current value and the binding is fired with the chunk
// (the increment); otherwise the value is replaced and the binding is fired with the final value.
func (nr *NodeResult) AppendOutputVarWithFire(path string, chunk interface{}) error {
	if nr == nil {
		return errors.New("nodeResult is nil")
	}

	nr.mu.Lock()
	if nr.OutputVars == nil {
		nr.OutputVars = make(map[string]interface{})
	}

	// Extract the variable name.
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]
	if _, exists := nr.OutputVars[varName]; !exists {
		nr.mu.Unlock()
		return fmt.Errorf("output var %s not defined", varName)
	}

	// Read the current value.
	current, _ := GetVarByPath(nr.OutputVars, path)

	var finalValue interface{}
	var fireValue interface{}

	if chunkStr, ok := chunk.(string); ok {
		if currentStr, ok := current.(string); ok {
			finalValue = currentStr + chunkStr
		} else {
			finalValue = chunkStr
		}
		fireValue = chunk // the increment
	} else {
		finalValue = chunk
		fireValue = chunk // the full value
	}

	if err := SetVarByPath(nr.OutputVars, path, finalValue); err != nil {
		nr.mu.Unlock()
		return err
	}
	nr.mu.Unlock()

	// Fire the binding (outside the lock).
	if nr.Ctx != nil && nr.Ctx.OutputBindingFire != nil {
		return nr.Ctx.OutputBindingFire(nr, varName, fireValue)
	}
	return nil
}

// InitLocalVars initializes LocalVars under the lock, reusing the package-level InitLocalVars helper.
func (nr *NodeResult) InitLocalVars(defVars map[string]interface{}) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.LocalVars = InitLocalVars(defVars)
}

// InitOutputVars initializes OutputVars under the lock, reusing the package-level InitOutputVars helper.
func (nr *NodeResult) InitOutputVars(defVars map[string]interface{}) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.OutputVars = InitOutputVars(defVars)
}

// InitOutputBindings extracts the binding field from the outputs config and initializes OutputBindings.
// Only string / number / boolean variables support a binding.
// The config accepts both the single-object shorthand and an array of several bindings.
func (nr *NodeResult) InitOutputBindings(outputs map[string]interface{}) {
	if outputs == nil {
		return
	}
	for varName, v := range outputs {
		def, ok := v.(map[string]interface{})
		if !ok {
			continue
		}

		// Only string / number / boolean variables support a binding.
		varType, _ := def["type"].(string)
		if varType != "string" && varType != "number" && varType != "boolean" {
			continue
		}

		// Extract the binding field from the variable definition.
		rawBinding, exists := def["binding"]
		if !exists {
			continue
		}

		// The value is either an array (several bindings) or a single object (the shorthand).
		var items []interface{}
		switch val := rawBinding.(type) {
		case []interface{}:
			items = val
		case map[string]interface{}:
			items = []interface{}{val}
		default:
			continue
		}
		for _, item := range items {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			ob := &OutputBinding{}
			ob.Channel, _ = m["channel"].(string)
			if stream, ok := m["stream"].(bool); ok {
				ob.Stream = &stream
			}
			if t, ok := m["transform"].(map[string]interface{}); ok {
				ob.Transform = &BindingTransform{}
				ob.Transform.Lang, _ = t["lang"].(string)
				ob.Transform.Script, _ = t["script"].(string)
				// Parse timeout: {enabled: bool, seconds: int}.
				if timeoutMap, ok := t["timeout"].(map[string]interface{}); ok {
					ob.Transform.Timeout = &BindingTimeout{}
					if enabled, ok := timeoutMap["enabled"].(bool); ok {
						ob.Transform.Timeout.Enabled = enabled
					}
					if sec, ok := timeoutMap["seconds"].(float64); ok {
						ob.Transform.Timeout.Seconds = int(sec)
					}
				}
				if inputs, ok := t["inputs"].(map[string]interface{}); ok {
					ob.Transform.Inputs = inputs
				}
			}
			if nr.OutputBindings == nil {
				nr.OutputBindings = make(map[string][]*OutputBinding)
			}
			nr.OutputBindings[varName] = append(nr.OutputBindings[varName], ob)
		}
	}
}

// SetOutputVarsFrom sets OutputVars under the lock (copied straight from another NodeResult).
func (nr *NodeResult) SetOutputVarsFrom(vars map[string]interface{}) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.OutputVars = vars
}

func (lr *LoopResult) SetLoopVar(path string, value interface{}) error {
	if lr == nil {
		return errors.New("loopResult is nil")
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.LoopVars == nil {
		lr.LoopVars = make(map[string]interface{})
	}
	return SetVarByPath(lr.LoopVars, path, value)
}

// IncIteration counts one iteration actually executed.
func (lr *LoopResult) IncIteration() {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.Iterations++
}

func (lr *LoopResult) SetLoopVars(vars map[string]interface{}) error {
	if lr == nil {
		return errors.New("loopResult is nil")
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.LoopVars == nil {
		lr.LoopVars = make(map[string]interface{})
	}
	for path, value := range vars {
		if err := SetVarByPath(lr.LoopVars, path, value); err != nil {
			return err
		}
	}
	return nil
}

// SnapshotVars takes a shallow copy of the local and output variables (guarded by RLock).
func (nr *NodeResult) SnapshotVars() (local, output map[string]any) {
	nr.mu.RLock()
	defer nr.mu.RUnlock()
	localCopy := make(map[string]any, len(nr.LocalVars))
	for k, v := range nr.LocalVars {
		localCopy[k] = v
	}
	outputCopy := make(map[string]any, len(nr.OutputVars))
	for k, v := range nr.OutputVars {
		outputCopy[k] = v
	}
	return localCopy, outputCopy
}
