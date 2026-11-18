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

// ProgressMsg 一条外发的流式进度。
// Category 与 agent/core 的 Category* 同值（thinking / text / tool_call / tool_result），
// 未显式指定类别的一律 text。这里没有「最终结果」档位：最终结果只认响应里的 result。
type ProgressMsg struct {
	Category string
	Message  string
}

type ExecContext struct {
	Flow         *Flow
	RunEnvVars   map[string]interface{}
	RunFlowVars  map[string]interface{}
	RunArguments map[string]interface{}
	StdCtx       context.Context //标准上下文，用于取消执行
	ReqID        string
	RunID        string
	SessionID    string
	UserID       string // 调用方用户（RPC/MCP 绑定解析、后台登录用户）
	// CallDepth 跨 agent / flow 的调用深度（顶层为 0，每跨一层 +1）。
	//
	// 由 NewFlow 从运行参数 _call_depth 提取并剥离，rpc 节点调端点时以 +1 继续下传。
	// 用途：拦住 agent → 本地 flow 端点 → rpc 节点 → agent 端点 → … 互相调用形成的环
	// （call_agent 有自己的深度计数，但 flow 这一段原本不在链上）。
	// 语义是「栈深度」不是「调用次数」：同一 flow 里的多个 rpc 节点读到的是同一个值，
	// 平级调用不累加（扇出由 flow 的 max_exec_count 管）。
	CallDepth int
	// CallChain 祖先链（见 shared/callchain）：由 NewFlow 从运行参数 _call_chain 提取并剥离。
	//
	// 与 CallDepth 互补：深度是「最多几层」，链是「谁重入就立刻拒」。链只记祖先，
	// 平级扇出不累加 —— 同一 flow 里两个并列 rpc 节点读到的是同一条链。
	CallChain []string
	Schema    string
	// Limits 本次运行冻结的限额快照：NewFlow 启动时解析一次，运行期不再查询配置。
	// 为 nil 时（如单测直接构造）由 limits() 兜底解析一次。
	Limits    *flowconfig.FlowLimits
	StartedAt time.Time // 本次运行启动时间（无节点执行时兜底 run.started_at）

	Result       string
	ProgressMsgs []string // 进度文本（落库用，不带类别）
	ResultMsgs   []string // 结果文本消息
	Error        string

	ProgressChan       chan ProgressMsg
	ResultChan         chan string
	ExecutedProcessMap *ProcessExecMap
	ExecutedNodeMap    *NodeExecMap

	// progressOverflow 进度通道是否已发生过溢出（只用于"首次丢弃记一条日志"，避免刷屏）
	progressOverflow atomic.Bool

	ProcessManager *ProcessManager

	monoWG sync.WaitGroup // 跟踪 mono 独立并发节点，Start 返回前 Wait

	VarsMu sync.RWMutex

	// OutputBindingFire 当 node 类型变量通过 SetVariableWithFire 写入后，
	// 自动触发对应 output binding。由 executors 包在初始化时注入。
	OutputBindingFire func(nr *NodeResult, varName string, value any) error

	// Debug 调试控制器（单步/暂停/断点）。为 nil 时执行引擎按原逻辑全速运行。
	Debug *DebugControl

	// DebugScope 调试会话作用域：决定「依赖的实体读草稿还是正式版本」。
	//
	// 只有编辑页调试器与 run_flow 试跑会设置；MCP / RPC / agent 的 local__ 工具等
	// 正式入口一律为 nil（= 读正式版本）。子调用通过 runEndpoint 继承它，
	// 跨进程的调用天然没有（进程内的参数，不进协议）。
	DebugScope *DebugScope

	// OnFlowDone 流程执行结束回调（无论成功/失败/被取消都会调用），
	// 由调试源（WS handler）注入，用于向客户端推送 finished 事件。
	OnFlowDone func(err error)

	// cancel 取消函数，用于 stop 调试时中断执行（由 NewFlow 通过 WithCancel 注入）。
	cancel context.CancelFunc
}

// Cancel 取消执行（调试 stop 时调用，使 StdCtx.Err() 非 nil 从而唤醒挂起点）。
func (e *ExecContext) Cancel() {
	if e.cancel != nil {
		e.cancel()
	}
}

// SetCancel 注入取消函数（由 NewFlow 在跨包构造时调用）。
func (e *ExecContext) SetCancel(c context.CancelFunc) {
	e.cancel = c
}

func (ctx *ExecContext) GetProgressChan() chan ProgressMsg {
	return ctx.ProgressChan
}

// SendProgress 累积进度（落库），并尽力外发到进度通道（类别固定 text）。
//
// 先累积再外发：落库与最终结果不依赖通道有没有人读。
// 外发非阻塞：进度只是通知，消费端慢或不在（非流式调用方从不读该通道）时不能拖死执行；
// 缓冲满时丢弃这一帧，只在首次丢弃时记一条日志（避免刷屏）。
// 与 binding.go 里 fireSingleBinding 对同一通道的写法保持一致。
func (ctx *ExecContext) SendProgress(progress string) {
	ctx.SendProgressAs("text", progress)
}

// SendProgressAs 以指定类别外发进度。category 取值与 agent/core 的 Category*
// 保持一致（thinking / text / tool_call / tool_result）；这里只是字符串，
// 具体映射由 executors 层的 binding 决定。
func (ctx *ExecContext) SendProgressAs(category, progress string) {
	ctx.AppendProgressMsg(progress)
	if ctx.ProgressChan == nil {
		return
	}
	select {
	case ctx.ProgressChan <- ProgressMsg{Category: category, Message: progress}:
	default:
		if ctx.progressOverflow.CompareAndSwap(false, true) {
			ctx.LogInfo("进度通道缓冲已满，后续进度通知将被丢弃（落库记录不受影响）")
		}
	}
}

func (ctx *ExecContext) GetResultChan() chan string {
	return ctx.ResultChan
}

// EmitResult 累积结果（拼进最终 Result + 记入 ResultMsgs 用于落库），并按需作为流式结果外发。
//
// stream=false 时只累积不外发：调用方在结束时从 GetResult 取完整结果（result 节点的缺省）。
// 外发用非阻塞写：流只是通知，消费端慢或压根不在（非流式调用方从不读该通道）时，
// 不能反过来把执行拖死；丢弃的帧不影响最终结果，因为累积已经先做完了。
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

// AppendProgressMsg 追加一条 progress 消息。
func (ctx *ExecContext) AppendProgressMsg(msg string) {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	ctx.ProgressMsgs = append(ctx.ProgressMsgs, msg)
}

// AppendResultMsg 追加一条 result 消息。
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

// MonoWGAdd 增加 mono 独立并发节点计数
func (ctx *ExecContext) MonoWGAdd(delta int) {
	ctx.monoWG.Add(delta)
}

// MonoWGDone 标记一个 mono 独立并发节点完成
func (ctx *ExecContext) MonoWGDone() {
	ctx.monoWG.Done()
}

// MonoWGWait 等待所有 mono 独立并发节点完成
func (ctx *ExecContext) MonoWGWait() {
	ctx.monoWG.Wait()
}

// limits 取本次运行冻结的限额快照；未注入时兜底解析一次（仅单测/异常构造会走到）。
func (ctx *ExecContext) limits() flowconfig.FlowLimits {
	if ctx.Limits != nil {
		return *ctx.Limits
	}
	return flowconfig.Resolve(ctx.Schema)
}

// ResolveMaxLoopTimes 返回最终生效的最大循环次数（供 loop 执行器使用）。
func (ctx *ExecContext) ResolveMaxLoopTimes() int {
	return ctx.limits().MaxLoopTimes
}

// ResolveTimeout 获取超时时间（秒），参数是调用方传入的整数值
// 查找链：参数 -> Instance.Timeout -> config.MaxTimeout
// 规则：链上所有非-1的值中取最小值，最终与 MaxTimeout 比较取小
func (ctx *ExecContext) ResolveTimeout(timeout int) int {
	limits := ctx.limits()
	result := timeout
	// 如果参数未设置（<=0），从Instance.Timeout查找
	if result <= 0 {
		if ctx.Flow != nil && ctx.Flow.Instance != nil {
			result = ctx.Flow.Instance.Timeout.Int()
		}
	} else {
		// 参数已设置，但Instance.Timeout也设置了（>0），取较小值
		if ctx.Flow != nil && ctx.Flow.Instance != nil {
			insTimeout := ctx.Flow.Instance.Timeout.Int()
			if insTimeout > 0 && insTimeout < result {
				result = insTimeout
			}
		}
	}

	// 仍未设置，使用 max_timeout 兜底
	if result <= 0 {
		return limits.MaxTimeout
	}

	// 与 MaxTimeout 比较，取较小值
	sysMax := limits.MaxTimeout
	if sysMax > 0 && result > sysMax {
		return sysMax
	}
	return result
}

// GetFlowTimeout 获取流程级别的超时时间（秒），无参数
// 查找链：Instance.Timeout -> config.MaxTimeout
// 与 MaxTimeout 取较小者
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

// ResolveRecursionDepth 解析最终生效的最大递归深度。
// 规则：
//  1. Instance.MaxRecursionDepth 为 -1（或未设置）→ 使用 system_max_recursion_depth
//  2. 两者都设置了正值 → 取 min(Instance, system)
//  3. 都 <=0 → 返回 -1（无限制，兼容旧行为）
func (ctx *ExecContext) ResolveRecursionDepth() int {
	instanceDepth := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceDepth = ctx.Flow.Instance.MaxRecursionDepth.Int()
	}

	sysDepth := ctx.limits().MaxRecursionDepth

	// 都未设置 / 都为 -1：无限
	if instanceDepth <= 0 && sysDepth <= 0 {
		return -1
	}

	// 实例为 -1：只用系统上限
	if instanceDepth <= 0 {
		return sysDepth
	}

	// 系统为 -1：只用实例上限
	if sysDepth <= 0 {
		return instanceDepth
	}

	// 两者都设置：取较小值
	if instanceDepth < sysDepth {
		return instanceDepth
	}
	return sysDepth
}

// ResolveMaxNodeCount 解析最终生效的最大节点数。
// 规则同 ResolveRecursionDepth：
//  1. Instance.MaxNodeCount 为 -1（或未设置）→ 使用 system_max_node_count
//  2. 两者都设置了正值 → 取 min(Instance, system)
//  3. 都 <=0 → 返回 -1（无限制，兼容旧行为）
func (ctx *ExecContext) ResolveMaxNodeCount() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxNodeCount.Int()
	}

	sysMax := ctx.limits().MaxNodeCount

	// 都未设置 / 都为 -1：无限
	if instanceMax <= 0 && sysMax <= 0 {
		return -1
	}

	// 实例为 -1：只用系统上限
	if instanceMax <= 0 {
		return sysMax
	}

	// 系统为 -1：只用实例上限
	if sysMax <= 0 {
		return instanceMax
	}

	// 两者都设置：取较小值
	if instanceMax < sysMax {
		return instanceMax
	}
	return sysMax
}

// ResolveScriptTimeout 获取脚本执行的超时时间（秒）
// lang: 脚本语言 (lua/python/js)，timeout: 调用方设置（节点上的「超时控制」）
//
// 节点未设超时控制（timeout<=0）时，不再有 nexus 本地每语言缺省值 —— 直接交给
// ResolveTimeout 走通用兜底链：实例 Instance.Timeout → flow_configs.max_timeout
// （平台端按租户配置，缺省 300）→ 代码兜底 300。
//
// ⚠️ 这里算出来的只是「希望的上限」，**它下面还有一道沙箱侧的硬闸**：
// sandboxd 的 code_max_sec（handle_code.go 的 codeDeadline），
// 也就是说脚本执行的实际上限 = min(本函数结果, code_max_sec，缺省 120)。
func (ctx *ExecContext) ResolveScriptTimeout(lang string, timeout int) int {
	return ctx.ResolveTimeout(timeout)
}

// ResolveMaxExecCount 解析最终生效的最大执行次数。
// 规则同 ResolveRecursionDepth：
//  1. Instance.MaxExecCount 为 -1（或未设置）→ 使用 system_max_exec_count
//  2. 两者都设置了正值 → 取 min(Instance, system)
//  3. 都 <=0 → 返回 -1（无限制，兼容旧行为）
func (ctx *ExecContext) ResolveMaxExecCount() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxExecCount.Int()
	}

	sysMax := ctx.limits().MaxExecCount

	// 都未设置 / 都为 -1：无限
	if instanceMax <= 0 && sysMax <= 0 {
		return -1
	}

	// 实例为 -1：只用系统上限
	if instanceMax <= 0 {
		return sysMax
	}

	// 系统为 -1：只用实例上限
	if sysMax <= 0 {
		return instanceMax
	}

	// 两者都设置：取较小值
	if instanceMax < sysMax {
		return instanceMax
	}
	return sysMax
}

// ResolveMaxRunRecords 解析最终生效的运行记录保留数。
// 工作流级 max_run_records 三态语义：
//   - 0：不存储（不保留任何运行记录）
//   - -1：未设置/不限制 → 使用系统级 MaxRunRecords
//   - 正数 N：保留 N 条，受系统级硬顶约束（取 min）
func (ctx *ExecContext) ResolveMaxRunRecords() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxRunRecords.Int()
	}

	// 0 = 不存储
	if instanceMax == 0 {
		return 0
	}

	sysMax := ctx.limits().MaxRunRecords

	// 实例为 -1（未设置/不限制）→ 用系统上限
	if instanceMax < 0 {
		return sysMax
	}

	// 实例为正数 → 受系统顶约束取 min
	if sysMax > 0 && instanceMax > sysMax {
		return sysMax
	}
	return instanceMax
}

// ResolveMaxSessionRecords 解析最终生效的会话记忆保留数。
// 工作流级 max_session_records 语义（与 max_run_records 不同，注意区分）：
//   - -1：不做会话记忆（开关关闭）→ 返回 0
//   - 0：非法值，按不做记忆处理
//   - 正数 N：保留 N 条，受系统级硬顶约束（取 min）
func (ctx *ExecContext) ResolveMaxSessionRecords() int {
	instanceMax := -1
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		instanceMax = ctx.Flow.Instance.MaxSessionRecords.Int()
	}

	// -1 或 0：不做会话记忆
	if instanceMax <= 0 {
		return 0
	}

	// 实例为正数 → 受系统顶约束取 min
	sysMax := ctx.limits().MaxSessionRecords
	if sysMax > 0 && instanceMax > sysMax {
		return sysMax
	}
	return instanceMax
}

// ResolveRunLimits 返回本次运行实际生效的限额（三级 Resolve 后的最终值）。
// 用于落库 run_limits 与调试 finished 帧，保证两处数据一致。
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

// RecordNodeExecution 原子记录节点执行：追加到 ExecutedProcessMap 并设置到 ExecutedNodeMap。
// 调用前检查总执行次数是否超过 ResolveMaxExecCount，超过则标记 nr 为 internal 错误并返回错误。
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

// GetflowId 返回当前工作流实例的 id（取自 flow.Instance，安全处理 nil）。
func (ctx *ExecContext) GetflowId() string {
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		return ctx.Flow.Instance.ID
	}
	return ""
}

// GetFlowName 返回当前工作流实例的名字（取自 flow.Instance，安全处理 nil）。
func (ctx *ExecContext) GetFlowName() string {
	if ctx.Flow != nil && ctx.Flow.Instance != nil {
		return ctx.Flow.Instance.Name
	}
	return ""
}

// BuiltinVars 本次运行的只读上下文，供变量源 "builtin" 取用。
//
// 刻意不做成默认输入：session / run 这类信息大多数脚本用不上，只有节点在 inputs 里
// 显式声明 source_type="builtin" 时才会进入脚本（见 getVariableNoLock）。这也让它与
// argument / env / flow / node 四个数据源平级，而不是一种隐式全局。
//
// 选材标准是「给程序用的，不给眼睛看的」：全部不可变、字符安全、能当键。
// 于是刻意不含 user_name（可改、含特殊字符）、不含 req_id（只用于日志串联、
// 可能为空且不唯一）、不含 env（可能含密钥）。
//
// 每次调用现构造，不缓存：Debug 控制器是在 NewFlow 之后由调试入口注入的，
// 缓存会拿到旧值；何况这几个字段都是字符串，构造成本可忽略。
// 不取锁 —— 调用方 getVariableNoLock 已持有 VarsMu 读锁，重入有死锁风险。
func (ctx *ExecContext) BuiltinVars() map[string]interface{} {
	return map[string]interface{}{
		"session_id": ctx.SessionID, // 无状态调用时为空串
		"run_id":     ctx.RunID,
		"user_id":    ctx.UserID, // 未认证调用时为空串
		"schema":     ctx.Schema,
		"flow_id":    ctx.GetflowId(),
	}
}

func (ctx *ExecContext) LogInfo(message string) {
	xlog.Info("req：%s run：%s flow：%s(%s) info：%s",
		ctx.GetReqID(), ctx.GetRunID(), ctx.GetFlowName(), ctx.GetflowId(), message)
}
func (ctx *ExecContext) LogError(message string) {
	ctx.SetError(message)
	xlog.Error("req：%s run：%s flow：%s(%s) err：%s",
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
			// 重复关闭（多个出口都会 CloseChans）：属于预期内的幂等调用，
			// 记在日志里而不是打到 stderr。
			xlog.Error("req：%s run：%s 关闭进度通道异常: %v", c.GetReqID(), c.GetRunID(), r)
		}
	}()
	if c.ProgressChan != nil {
		close(c.ProgressChan)
	}
}
func (c *ExecContext) CloseResultChan() {
	defer func() {
		if r := recover(); r != nil {
			xlog.Error("req：%s run：%s 关闭结果通道异常: %v", c.GetReqID(), c.GetRunID(), r)
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

// getVariableNoLock 不加锁版本，供内部批量调用
func (ctx *ExecContext) getVariableNoLock(source map[string]interface{}, processID string) (interface{}, error) {
	if source == nil {
		return nil, fmt.Errorf("variable source is nil")
	}

	sourceType, _ := source["source_type"].(string)
	sourceKey, _ := source["source_key"].(string)
	// 空引用不能静默返回 nil：UI 新建变量但没选来源时必须显式报错，
	// 否则脚本/模板会拿着空值继续跑，运行时无从定位。
	if sourceType == "" || strings.TrimSpace(sourceKey) == "" {
		return nil, fmt.Errorf("variable source is not selected")
	}

	switch sourceType {
	case "argument":
		return GetVarByPath(ctx.RunArguments, sourceKey)
	case "env":
		return GetEnvVar(ctx.RunEnvVars, sourceKey)
	case "global":
		// 租户级全局变量（{schema}.global_vars）：与环境变量平级的输入来源。
		// 名字拼错 / 变量不存在必须硬失败，理由同 builtin 那条注释 —— 静默 nil
		// 会让脚本拿着空值继续跑，而运行日志里看起来只是「这次没拿到值」。
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
		// 运行上下文：与拓扑无关，任意位置的节点都能引用
		v, ok := ctx.BuiltinVars()[sourceKey]
		if !ok {
			// 拼错 key 必须显式报错。静默返回 nil 会让脚本拿到空值继续跑，
			// 而这类错误在运行日志里几乎无法定位（看起来只是"这次没拿到值"）。
			return nil, fmt.Errorf("unknown builtin variable %q", sourceKey)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported source type %s", sourceType)
	}
}

// GetVariables 根据变量定义映射获取多个变量值
// 参数 variables 定义示例: {"arg1": {"type": "string", "source_key": "input", "source_ref": "", "source_type": "argument"}}
func (ctx *ExecContext) GetVariables(variables map[string]interface{}, processID string) (map[string]interface{}, error) {
	ctx.VarsMu.RLock()
	defer ctx.VarsMu.RUnlock()

	results := make(map[string]interface{})

	for key, value := range variables {
		if sourceDef, ok := value.(map[string]interface{}); ok {
			// 创建符合GetVariable方法要求的source map
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

			// 使用GetVariable方法获取变量值
			varValue, err := ctx.getVariableNoLock(source, processID)
			if err != nil {
				// 如果获取单个变量失败，直接返回错误
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

// setFlowVarNoLock 不加锁版本，供内部批量调用
func (ctx *ExecContext) setFlowVarNoLock(path string, value interface{}) error {
	return SetVarByPath(ctx.RunFlowVars, path, value)
}

// SetVariable 设置变量值，支持流程变量和节点局部变量两种类型
// 参数 target 定义示例: {"source_type": "flow", "source_key": "result", "source_ref": ""}
// 或 {"source_type": "node", "source_key": "myVar", "source_ref": "nodeID"}
func (ctx *ExecContext) SetVariable(target map[string]interface{}, value interface{}, processID string) error {
	ctx.VarsMu.Lock()
	defer ctx.VarsMu.Unlock()
	return ctx.setVariableNoLock(target, value, processID)
}

// setVariableNoLock 不加锁版本，供内部批量调用
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
		// 运行上下文变量（会话/运行/租户标识、环境变量、入口参数）是本次运行的只读输入，
		// 节点只能写自己的输出/局部变量或流程变量。
		return fmt.Errorf("运行上下文变量（%s 源）只读，不能作为赋值目标: %s", targetType, targetKey)
	case "":
		return fmt.Errorf("赋值目标未选择（source_type 为空）")
	default:
		return fmt.Errorf("unsupported target type %s", targetType)
	}
}

// SetVariables 批量设置变量值
// 参数 targets 定义示例: {"w1": {"type": "string", "source_key": "result", "source_ref": "", "source_type": "flow"}}
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

// SetVariableWithFire 写变量 + 自动触发 output binding。
// 仅 source_type="node" 且 OutputBindingFire hook 已注入时才触发。
// 适用于 executor（HTTP 等非脚本节点）与脚本返回值的 targets 落地。
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

// FindNodeResultByProcessChain 通过 processID 和 nodeID 在 ExecutedNodeMap 中查找 NodeResult
// 如果当前 processID 找不到，则查找其父 processID，递归向上直到找到或没有父节点
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
