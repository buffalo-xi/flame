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

// ErrorAction 错误处理动作
type ErrorAction string

const (
	ErrorActionSkip    ErrorAction = "skip"
	ErrorActionRetry   ErrorAction = "retry"
	ErrorActionHandler ErrorAction = "handler"
)

// 错误编码常量
const (
	ErrCodeTimeout    = "timeout"
	ErrCodeBusy       = "busy"   // 沙箱并发额度耗尽（sandboxd 取槽超时拒绝，脚本未执行）
	ErrCodeCancel     = "cancel" // 用户主动取消
	ErrCodeConfig     = "config"
	ErrCodeScript     = "script"
	ErrCodeCapability = "capability" // 平台未给本租户开通这类能力（沙箱脚本等）
	ErrCodeVar        = "var"
	ErrCodeConnection = "connection"
	ErrCodeAuth       = "auth"
	ErrCodeNotFound   = "not_found"
	ErrCodeInternal   = "internal"
	ErrCodeOther      = "other"
)

// AllErrorCodes 返回所有全局错误编码常量
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
	NodeID      string         `json:"node_id"` //就是flow.node的id
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
	PostError   string         `json:"post_error"` // 后处理脚本执行错误（不影响节点主状态）

	NodeType NodeType `json:"node_type"`

	//各种节点的结果，存储节点类型数据结果
	LoopResult     *LoopResult                 `json:"loop_result"`
	ProcessResult  *ProcessResult              `json:"process_result"`
	FunctionResult *FunctionResult             `json:"function_result"`
	CodeResult     *CodeResult                 `json:"code_result"`
	ForkResult     *ForkResult                 `json:"fork_result"`
	WaitResult     *WaitResult                 `json:"wait_result"`
	BranchResult   *BranchResult               `json:"branch_result"`
	HttpResult     *HttpResult                 `json:"http_result"`
	OutputBindings map[string][]*OutputBinding `json:"-"` // 输出变量的流式绑定配置
	mu             sync.RWMutex
	// warned 记录已经提示过的告警 key，供 WarnOnce 去重（不落库、不对外）。
	warned map[string]struct{}
}

// NewNodeResult 创建基础 NodeResult，公共字段由外部统一设置
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

// Finish 结束执行，设置最终状态、耗时，并自动记录日志
// 先到先得：脚本通过 wf_set_error 设过的 ErrCode/ErrMsg 不会被覆盖
func (nr *NodeResult) Finish(status NodeStatus, errCode string, errMsg string) {
	nr.Status = status
	nr.EndTime = time.Now()
	nr.DurationMs = nr.EndTime.Sub(nr.StartTime).Milliseconds()

	// 脚本通过 wf_set_error 设过的不覆盖（先到先得）
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
		nr.AddLog("执行成功")
	} else if status == StatusSkipped {
		nr.AddLog("执行跳过")
		// 调试模式下，通知调试源该节点被跳过（用于前端标记「跳过」节点）
		if nr.Ctx != nil && nr.Ctx.Debug != nil {
			if hook := nr.Ctx.Debug.skipHook; hook != nil {
				hook(nr.NodeID, nr.nodeName())
			}
		}
	} else {
		nr.AddLog("执行失败: " + nr.ErrMsg)
		// 调试模式下，通知调试源该节点执行失败（用于前端标记「出错」节点）
		if nr.Ctx != nil && nr.Ctx.Debug != nil {
			if hook := nr.Ctx.Debug.failHook; hook != nil {
				hook(nr.NodeID, nr.nodeName(), nr.ErrMsg)
			}
		}
	}

	// 调试模式下，通知调试源该节点已完成（无论成功/失败/跳过），
	// 供 WS 调试源累积该节点最新数据（按 node_id 去重保留最后一次执行）。
	if nr.Ctx != nil && nr.Ctx.Debug != nil {
		if hook := nr.Ctx.Debug.doneHook; hook != nil {
			hook(nr)
		}
	}
}

// nodeName 返回节点名字（从 flow.NodeMap 查，查不到返回空）。
// 节点名字存在 Data.Label（前端 Vue flow 保存节点的惯例），无独立 name 属性。
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

// Skip 将节点标记为「跳过」（on_error skip/handler 返回 skip 时调用）。
// 与 Finish(StatusSkipped) 的差异：Skip 不重设 EndTime/DurationMs/ErrCode，
// 仅设置状态并触发调试源回调（与 Finish 内 skipped 分支保持一致）。
func (nr *NodeResult) Skip() {
	nr.Status = StatusSkipped
	nr.AddLog("执行跳过")
	if nr.Ctx != nil && nr.Ctx.Debug != nil {
		if hook := nr.Ctx.Debug.skipHook; hook != nil {
			hook(nr.NodeID, nr.nodeName())
		}
		// 跳过后同样通知调试源更新最新数据（覆盖之前可能存在的 failed 状态）
		if hook := nr.Ctx.Debug.doneHook; hook != nil {
			hook(nr)
		}
	}
}

// SetInputVars 设置节点的统一输入变量（加锁）
func (nr *NodeResult) SetInputVars(vars map[string]any) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.InputVars = vars
}

// GetInputVars 读取节点的统一输入变量（加锁）
func (nr *NodeResult) GetInputVars() map[string]any {
	nr.mu.RLock()
	defer nr.mu.RUnlock()
	return nr.InputVars
}

type NodeType string

const (
	// 结构节点：start 由工具/前端生成，entry 由容器自动生成，layout 为纯占位节点；
	// 三者都没有执行器，runner 直接跳过并继续沿出边前进。
	NodeTypeStart  NodeType = "start"
	NodeTypeEntry  NodeType = "entry"
	NodeTypeLayout NodeType = "layout"

	// 以下与 executors.RegisterExecutor 的注册名一一对应，命名保持小写、单数
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
	LoopConfig map[string]interface{} `json:"loop"` // loop 节点配置（含 type）
	LoopVars   map[string]interface{} `json:"loop_vars"`
	Iterations int                    `json:"iterations"` // 实际执行次数
	mu         sync.RWMutex
}
type ProcessResult struct {
	mu sync.RWMutex
}

type FunctionResult struct {
	ParamVars map[string]any `json:"param_vars"`
	mu        sync.RWMutex
}

// ForkState ForkResult 的 group 状态
type ForkState int

// ForkResult 的 group 状态
const (
	ForkStateActive    ForkState = iota // 普通状态，可继续 Add
	ForkStateWaiting                    // 等待中，不可 Add
	ForkStateDone                       // 已完成（所有任务 Done），不可 Add/Wait
	ForkStateAbandoned                  // 已放弃等待（超时），后台任务可能仍在跑，不可 Add/Wait
)

type ForkResult struct {
	MaxConcurrency int `json:"max_concurrency"`
	mu             sync.Mutex
	wg             sync.WaitGroup
	sem            chan struct{} // 并发上限信号量，MaxConcurrency<=0 时为 nil（不限流）
	count          int           // 累计加入数
	state          ForkState     // ForkStateActive / ForkStateWaiting / ForkStateDone / ForkStateAbandoned
	members        []ForkMember  // 加入的成员（processID + nodeID），供 wait 扫描状态
}

// ForkMember 记录加入 fork 组的并发分支身份，供 wait 扫描节点状态
type ForkMember struct {
	ProcessID string
	NodeID    string
}

// NewForkResult 创建 ForkResult，maxConcurrency<=0 表示不限流
func NewForkResult(maxConcurrency int) *ForkResult {
	fr := &ForkResult{MaxConcurrency: maxConcurrency}
	if maxConcurrency > 0 {
		fr.sem = make(chan struct{}, maxConcurrency)
	}
	return fr
}

// Add 加入 delta 个任务，进入 wait/done 状态后拒绝加入（设计错误）。
// 若配置了 MaxConcurrency，获取信号量时阻塞等待空位（实现并发限流，退出一个递补一个）；
// ctx 取消时回滚已获取的信号量与计数。
// member 为本次加入的成员身份（processID + nodeID），用于 wait 扫描状态；delta>1 时忽略 member。
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

	// 信号量限流（在锁外阻塞，避免长时间持锁）
	if fr.sem != nil {
		for i := 0; i < delta; i++ {
			select {
			case fr.sem <- struct{}{}:
			case <-ctx.Done():
				// ctx 取消：回滚已获取的 sem 与 wg/count
				for j := 0; j < i; j++ {
					<-fr.sem
				}
				fr.mu.Lock()
				fr.count -= delta
				if delta == 1 {
					// 回滚成员记录（移除末尾刚追加的）
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

// Members 返回成员列表的快照（供 wait 扫描节点状态）
func (fr *ForkResult) Members() []ForkMember {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	result := make([]ForkMember, len(fr.members))
	copy(result, fr.members)
	return result
}

// Done 标记一个任务完成，并释放信号量（触发递补）
func (fr *ForkResult) Done() {
	if fr.sem != nil {
		<-fr.sem
	}
	fr.wg.Done()
}

// Wait 标记进入等待状态，等待所有已加入的任务完成（无超时）
// 返回 true 正常完成，false 状态异常（已被 Wait 过）
func (fr *ForkResult) Wait() (bool, error) {
	return fr.waitInternal(context.Background(), -1)
}

// WaitTimeout 标记进入等待状态，等待所有已加入的任务完成
// 返回 (completed, err)
// completed: true 正常完成，false 超时或状态异常
// err: 状态异常时非 nil
// timeoutSeconds <= 0 表示无超时
func (fr *ForkResult) WaitTimeout(timeoutSeconds int) (bool, error) {
	return fr.waitInternal(context.Background(), timeoutSeconds)
}

// WaitTimeoutCtx 同 WaitTimeout，但额外监听 ctx：取消/超时时立即返回，err 为 ctx.Err()。
// 节点级等待必须用它 —— 否则用户停止工作流后，wait 节点会一直挂到并发分支自己结束。
func (fr *ForkResult) WaitTimeoutCtx(ctx context.Context, timeoutSeconds int) (bool, error) {
	return fr.waitInternal(ctx, timeoutSeconds)
}

// waitInternal 返回 (completed bool, err error)
// completed: true 正常完成，false 超时/取消
// err: 状态异常或 ctx 取消时返回错误
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
		// 取消不等于超时：同样放弃等待，标记 Abandoned；
		// 后台任务照常 Done() 收尾计数，只是没人再收结果
		fr.mu.Lock()
		fr.state = ForkStateAbandoned
		fr.mu.Unlock()
		return false, ctx.Err()
	}

	fr.mu.Lock()
	if completed {
		fr.state = ForkStateDone
	} else {
		// 超时放弃等待：后台任务可能仍在跑，标记为 Abandoned
		// 后续这些任务的 Done() 仍会正常递减 wg 计数，但不再有 wait 收结果
		fr.state = ForkStateAbandoned
	}
	fr.mu.Unlock()

	return completed, nil
}

// State 返回当前状态
func (fr *ForkResult) State() ForkState {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.state
}

// IsAbandoned 是否因超时被放弃等待（后台任务可能仍在跑）
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
	ExecutedBranches []string `json:"executed_branches"` // 所有实际执行过的分支 ID（按顺序）
	mu               sync.RWMutex
}

type HttpResult struct {
	URL     string              `json:"url"` // 渲染后的实际 url
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"` // 实际发送的请求头
	Status  int                 `json:"status"`  // 响应状态码(摘要)
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
	// 只进 RunLogs：落库由 persist 负责，实时推送由 websocket 负责。
	// 这里不要往 stderr 打，节点日志是高频的。
	nr.RunLogs = append(nr.RunLogs, "["+ts+"] "+log)
}

func (nr *NodeResult) AddLogf(format string, args ...interface{}) {
	nr.AddLog(fmt.Sprintf(format, args...))
}

// WarnOnce 同一个 key 在一个节点内只记一次日志，返回本次是否真的记录了。
//
// 用于「逐帧调用的脚本反复命中同一条告警」的场景：handler 每来一个 chunk 调一次，
// 逐条记日志会把节点日志刷成几百行同样的内容，反而看不起真正的信息。
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

// SetParamVar 设置入口参数值，供 call 节点调用时传入参数
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

// GetTargetDef 获取可写变量定义
// 从节点配置的 targets 属性中查找指定路径的变量定义
func (nr *NodeResult) GetTargetDef(path string) (interface{}, error) {
	if nr == nil {
		return nil, errors.New("nodeResult is nil")
	}
	if nr.Ctx == nil || nr.Ctx.Flow == nil {
		return nil, errors.New("execution context or flow is nil")
	}

	// 提取变量名：先用 . 切分取第一段，再用 [ 切分取第一段
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]

	// 获取当前节点
	node, exists := nr.Ctx.Flow.NodeMap[nr.NodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found in flow", nr.NodeID)
	}
	if node.Data == nil || node.Data.Config == nil {
		return nil, fmt.Errorf("node %s has no config", nr.NodeID)
	}

	// 获取 targets 配置
	targets, ok := node.Data.Config["targets"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("node %s has no targets config", nr.NodeID)
	}

	// 查找变量定义
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
	// 提取变量名：先用 . 切分取第一段，再用 [ 切分取第一段
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
	// 提取变量名：先用 . 切分取第一段，再用 [ 切分取第一段
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]
	if _, exists := nr.OutputVars[varName]; !exists {
		return fmt.Errorf("output var %s not defined", varName)
	}
	return SetVarByPath(nr.OutputVars, path, value)
}

// SetOutputVarWithFire 写入输出变量 + 自动触发 output binding
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
		// 提取变量名：先用 . 切分取第一段，再用 [ 切分取第一段
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

// SetOutputVarsWithFire 批量写入输出变量 + 自动触发 output binding
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

// AppendOutputVarWithFire 追加输出变量并触发 output binding。
// chunk 为 string 时拼接到当前值末尾，binding fire 用 chunk（增量）；
// chunk 非 string 时直接覆盖，binding fire 用最终值（全量）。
func (nr *NodeResult) AppendOutputVarWithFire(path string, chunk interface{}) error {
	if nr == nil {
		return errors.New("nodeResult is nil")
	}

	nr.mu.Lock()
	if nr.OutputVars == nil {
		nr.OutputVars = make(map[string]interface{})
	}

	// 提取变量名
	varName := strings.Split(path, ".")[0]
	varName = strings.Split(varName, "[")[0]
	if _, exists := nr.OutputVars[varName]; !exists {
		nr.mu.Unlock()
		return fmt.Errorf("output var %s not defined", varName)
	}

	// 读当前值
	current, _ := GetVarByPath(nr.OutputVars, path)

	var finalValue interface{}
	var fireValue interface{}

	if chunkStr, ok := chunk.(string); ok {
		if currentStr, ok := current.(string); ok {
			finalValue = currentStr + chunkStr
		} else {
			finalValue = chunkStr
		}
		fireValue = chunk // 增量
	} else {
		finalValue = chunk
		fireValue = chunk // 全量
	}

	if err := SetVarByPath(nr.OutputVars, path, finalValue); err != nil {
		nr.mu.Unlock()
		return err
	}
	nr.mu.Unlock()

	// fire binding（锁外执行）
	if nr.Ctx != nil && nr.Ctx.OutputBindingFire != nil {
		return nr.Ctx.OutputBindingFire(nr, varName, fireValue)
	}
	return nil
}

// InitLocalVars 带锁初始化 LocalVars，内部复用包级 InitLocalVars 函数
func (nr *NodeResult) InitLocalVars(defVars map[string]interface{}) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.LocalVars = InitLocalVars(defVars)
}

// InitOutputVars 带锁初始化 OutputVars，内部复用包级 InitOutputVars 函数
func (nr *NodeResult) InitOutputVars(defVars map[string]interface{}) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	nr.OutputVars = InitOutputVars(defVars)
}

// InitOutputBindings 从 outputs 配置中提取 binding 字段，初始化 OutputBindings。
// 仅 string/number/boolean 类型的变量支持 binding。
// 配置格式兼容单个对象简写和数组多 binding。
func (nr *NodeResult) InitOutputBindings(outputs map[string]interface{}) {
	if outputs == nil {
		return
	}
	for varName, v := range outputs {
		def, ok := v.(map[string]interface{})
		if !ok {
			continue
		}

		// 仅 string/number/boolean 类型支持 binding
		varType, _ := def["type"].(string)
		if varType != "string" && varType != "number" && varType != "boolean" {
			continue
		}

		// 从变量定义中提取 binding 字段
		rawBinding, exists := def["binding"]
		if !exists {
			continue
		}

		// 值可以是数组（多个 binding）或单个对象（兼容简写）
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
				// 解析 timeout: {enabled: bool, seconds: int}
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

// SetOutputVarsFrom 带锁设置 OutputVars（直接从其他 NodeResult 复制）
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

// IncIteration 累计一次实际迭代。
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

// SnapshotVars 获取 local 和 output 变量的浅拷贝快照（RLock 保护）
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
