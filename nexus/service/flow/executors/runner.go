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
	"strings"
	"time"

	"github.com/google/uuid"
)

type Diagram struct {
	Nodes []*flow.Node `json:"nodes"`
	Edges []*flow.Edge `json:"edges"`
}

// legacyGlobalRefRe 工作流配置里的旧写法 {{ .global_vars.NAME }}。
var legacyGlobalRefRe = regexp.MustCompile(`\{\{-?\s*\.global_vars\.([A-Za-z_][A-Za-z0-9_]*)`)

// rejectLegacyGlobalRefs 拦下「节点配置里还在用 {{ .global_vars.X }}」的流程。
//
// 全局变量已改成节点输入的一种来源（config.inputs 的 source_type: "global"），
// 模板上下文不再注入 .global_vars。而 Go 模板遇到缺失键只会渲染成 <no value>、
// 不会报错 —— 不在这里拦，旧写法就会以「请求带着 <no value> 发出去」的形式
// 静默坏，比直接报错难查得多。报错里给出节点名与变量名，照着改即可。
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

// legacyGlobalRefIn 在节点配置里找旧写法（递归 map / slice / string）。
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

func NewFlow(Instance *models.Flow, args map[string]interface{}, reqID, runID, schema, userID string, ctx context.Context) (*flow.ExecContext, error) {
	if args == nil {
		args = make(map[string]interface{})
	}

	// 提取 session_id（校验并剥离，不进入业务参数）
	sessionID, args, err := extractSessionID(args)
	if err != nil {
		return nil, err
	}
	// 这里**不做**会话归属校验：agent 调 flow（local 工具 / rpc 节点）时会把调用方的
	// 会话一并传下来，让子 flow 与父级落在同一个 users/{uid}/sessions/{sid}/ 场地 ——
	// 那个会话的 owner 是父 agent，不是本 flow。归属校验只放在外部协议入口
	// （MCP runFlowTool / RPC runFlow），那里拿到的 session 才是不可信的客户端输入。

	// 调用深度：由上游（local 工具 / rpc 节点）注入，顶层为 0。剥离后不进业务参数。
	callDepth, args := extractCallDepth(args)
	// 调用链：与 _call_depth 同一套保留键机制，只用于环路检测，同样不能漏进业务变量。
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

	// 旧写法拦在运行之前：模板缺键只会渲染成 <no value>，不拦就是静默坏。
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

	// 运行期限额快照：启动时解析一次（Redis 缓存未命中则回源 DB），运行中不再查询配置。
	limits := flowconfig.Resolve(schema)

	stdCtx, cancel := context.WithCancel(ctx)
	execCtx := &flow.ExecContext{
		Flow:         f,
		RunEnvVars:   defEnvVars,
		RunFlowVars:  flow.InitFlowVars(defFlowVars),
		RunArguments: flow.InitArguments(args),
		StdCtx:       stdCtx,
		ReqID:        reqID,
		RunID:        runID,
		SessionID:    sessionID,
		UserID:       userID,
		CallDepth:    callDepth,
		CallChain:    callChain,
		Schema:       schema,
		Limits:       &limits,
		StartedAt:    time.Now(),

		ProgressChan:       make(chan flow.ProgressMsg, 100),
		ResultChan:         make(chan string, 100),
		ExecutedProcessMap: flow.NewProcessExecMap(),
		ExecutedNodeMap:    flow.NewNodeExecMap(),
		ProcessManager:     flow.NewProcessManager(),

		OutputBindingFire: fireOutputBinding,
	}
	execCtx.SetCancel(cancel)
	return execCtx, nil
}

func NewFlowExecContext(Instance *models.Flow, args map[string]interface{}, reqID, runID, schema, userID string, ctx context.Context) (*flow.ExecContext, error) {
	return NewFlow(Instance, args, reqID, runID, schema, userID, ctx)
}

func Start(execCtx *flow.ExecContext) (err error) {
	// 运行台账：开跑即登记、结束注销 —— 「租户里正在跑什么」才包含工作流。
	// 注意调试运行的执行体在下面的 goroutine 里（Start 会立刻返回），
	// 注销必须由它收尾，不能在 Start 里 defer。
	unregister := registerActiveRun(execCtx)

	// 调试模式下：执行体放到独立 goroutine，由 DebugControl 驱动挂起/恢复，
	// Start 立即返回，执行结果/错误通过 WS 推回前端。
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
	// 流程结束后主动取消 ctx：fork 组因超时被放弃等待（ForkStateAbandoned）时，
	// 后台分支仍在跑 LLM / 沙箱调用，而那一刻台账已注销、用户已经看到「结束」。
	// 放在 runFlow 之后（defer 在本函数返回时执行），落库不依赖 StdCtx，不受影响。
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

// registerActiveRun 让这次 flow 运行同时进「跨实例台账」和「本进程停止表」，
// 返回幂等注销函数。调试运行、试跑、正式调用都在这里登记（run_type 区分），
// 子流程也会各自登记一条。
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

// ensureSession 工作流运行结束后，若有 session_id 则确保 sessions 表存在对应会话记录。
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

// extractSessionID 从运行参数里提取并校验 session_id。
// 规则：键不存在 → 不启用（返回空）；值非 string → 报错；trim 后为空 → 视为不存在；
// trim 后非空但非合法 UUID → 报错；合法 UUID → 返回并剥离出业务参数。
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

// extractCallDepth 从运行参数里取出调用深度并剥离（不修改原 map）。
//
// 值可能来自 JSON 反序列化（float64）或进程内直调（int），两种都认；
// 非法值一律按 0 处理 —— 深度只用于限流，不能因为脏数据把正常调用拒掉。
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

// extractCallChain 从运行参数里取出调用链并剥离（不修改原 map）。
//
// 与 _call_depth 同一处保留键、同一套理由：链只是控制信息，
// 漏进 RunArguments 就会变成流程变量（{{_call_chain}}），也会随会话记忆落库。
// 非法元素由 callchain.Normalize 丢弃 —— 不因脏数据拒掉正常调用。
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

// persistSessionMemory 运行结束后自动记录会话记忆。
// 仅当存在合法 session_id 且工作流允许记忆（ResolveMaxSessionRecords()>0）时写入。
//
// runErr 写进 status/error：失败的一轮如果只留一行空 output，历史里看起来与
// 「正常完成、没有输出」完全一样，无法区分是工作流报错还是真的没说话。
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

	// 与 agent 侧同口径：被主动取消 → stopped（「被停」与「失败」是两件事，
	// 前者不是 bug，不该在历史里显示成红色错误），其它错误 → failed。
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

// runFlow 执行体：超时控制 + 主流程 + 并发节点等待。
func runFlow(execCtx *flow.ExecContext) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v", r)
			execCtx.LogError(fmt.Sprintf("flow panic: %v", r))
		}
	}()

	// 启用流程级别的超时控制
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
			err = nil // return/exit 信号：正常流程终止
		} else if errors.Is(err, SignalBreak) {
			// break 泄漏到顶层说明全程没有 loop 消费它，属于配置错误
			msg := "a break node outside any loop is a config error"
			execCtx.LogError(msg)
			err = errors.New(msg)
		} else if errors.Is(err, SignalContinue) {
			// continue 泄漏到顶层说明全程没有 loop 消费它，属于配置错误
			msg := "a continue node outside any loop is a config error"
			execCtx.LogError(msg)
			err = errors.New(msg)
		} else {
			execCtx.LogError(fmt.Sprintf("flow execution error: %v", err))
		}
	}

	// 等待所有 mono 独立并发节点完成，防止孤儿 goroutine（无论成败）
	execCtx.MonoWGWait()

	return err
}

func execute(node *flow.Node, execCtx *flow.ExecContext, processID string, depth int) error {
	// 维护调试调用栈：进入子 process 时压栈，退出时弹栈，depth 即当前嵌套层级
	if execCtx.Debug != nil && execCtx.Debug.Enabled {
		execCtx.Debug.PushStack(processID)
		defer execCtx.Debug.PopStack()
		depth = execCtx.Debug.CurrentDepth()
	}

	currentNode := node
	f := execCtx.Flow
	for currentNode != nil {
		select {
		case <-execCtx.StdCtx.Done():
			return execCtx.StdCtx.Err()
		default:
		}

		// 调试挂起钩子：节点执行前判断是否暂停/单步/断点命中
		if execCtx.Debug != nil && execCtx.Debug.Enabled {
			execCtx.Debug.SuspendIfNeeded(currentNode, processID, depth)
			// 全停屏障：若有其他节点已触发挂起，本节点也阻塞，实现 all-stop
			execCtx.Debug.WaitIfGloballySuspended()
			// stop 通过 cancel 唤醒；再次检查是否已取消
			if execCtx.StdCtx.Err() != nil {
				return execCtx.StdCtx.Err()
			}
		}

		// 并发节点：派发到 goroutine + 子 process，主流程不执行、不沿出边继续
		if concurrency := getConcurrency(currentNode); concurrency != nil {
			if err := dispatchConcurrent(currentNode, execCtx, processID, concurrency); err != nil {
				execCtx.LogError(fmt.Sprintf("failed to dispatch the concurrent node: %s: %v", currentNode.ID, err))
				return err
			}
			execCtx.LogInfo("dispatching concurrent node: " + currentNode.ID)
		} else {
			err := executeNode(currentNode, execCtx, processID)
			if err != nil {
				return err
			}
		}

		if len(currentNode.OutEdges) == 0 {
			break
		}

		// 分支节点：所有出边都是分支边，已由 BranchExecutor 在子 process 中处理
		// 意图节点同分支：出边均为分支出边，由 IntentExecutor 在子 process 中处理
		if currentNode.Type == string(flow.NodeTypeBranch) || currentNode.Type == string(flow.NodeTypeIntent) {
			break
		}

		// 除分支 / 意图节点外，出边只能有一条：多条时只会走 OutEdges[0]，
		// 其余的连线看着有效、实际永远不执行 —— 这是最难查的一类故障
		// （流程看起来走完了，结果少一段，日志里一个字都没有）。
		// 界面按 handle 基名去重（同一节点的 right / bottom 归成同一个基名），
		// 正常连线造不出多条，所以这里主要拦导入的资产包与直接调 API 保存的 diagram。
		if len(currentNode.OutEdges) > 1 {
			return newExecError(flow.ErrCodeConfig, fmt.Sprintf(
				"node %s(%s) has %d outgoing edges; this type allows only one - remove the extra edges",
				currentNode.ID, currentNode.Type, len(currentNode.OutEdges)))
		}

		// 确定下一个节点
		var nextNodeID string
		nextNodeID = currentNode.OutEdges[0].Target

		currentNode = GetNodeByID(f, nextNodeID)
	}
	return nil
}

// concurrencyConfig 节点的并发配置：
//   - 缺省：串行节点
//   - {mode: "mono"}：独立并发（不属于任何 fork 组）
//   - {mode: "fork", fork_node_id: "<id>"}：加入指定 fork 组
type concurrencyConfig struct {
	Mode       string
	ForkNodeID string
}

// getConcurrency 解析节点的并发配置；缺省或非法配置返回 nil（按串行处理）。
func getConcurrency(node *flow.Node) *concurrencyConfig {
	if node == nil || node.Data == nil || node.Data.Config == nil {
		return nil
	}
	raw, ok := node.Data.Config["concurrency"].(map[string]interface{})
	if !ok {
		return nil
	}
	mode, _ := raw["mode"].(string)
	switch mode {
	case "mono":
		return &concurrencyConfig{Mode: mode}
	case "fork":
		forkNodeID, _ := raw["fork_node_id"].(string)
		return &concurrencyConfig{Mode: mode, ForkNodeID: forkNodeID}
	default:
		return nil
	}
}

// dispatchConcurrent 派发一个并发节点到独立 goroutine + 子 process 执行。
// 主流程只负责派发，不执行节点本身，也不沿出边继续该节点。
func dispatchConcurrent(node *flow.Node, ctx *flow.ExecContext, parentProcessID string, cc *concurrencyConfig) error {
	if cc.Mode == "fork" && cc.ForkNodeID == "" {
		return newExecError(flow.ErrCodeConfig,
			fmt.Sprintf("concurrent node %s: fork_node_id is required when mode is fork", node.ID))
	}

	// 创建子 process
	childProcessID, err := ctx.ProcessManager.NewProcess(parentProcessID, nil)
	if err != nil {
		return newExecError(flow.ErrCodeInternal,
			fmt.Sprintf("create child process for concurrent node %s: %v", node.ID, err))
	}

	var forkNR *flow.NodeResult
	if cc.Mode == "fork" {
		// 查找 fork 节点的 NodeResult（在父 process 链上）
		forkNR = ctx.FindNodeResultByProcessChain(cc.ForkNodeID, parentProcessID)
		if forkNR == nil || forkNR.ForkResult == nil {
			return newExecError(flow.ErrCodeConfig,
				fmt.Sprintf("fork node result not found for concurrent node %s: %s", node.ID, cc.ForkNodeID))
		}
		// 加入 fork 组（阻塞获取信号量，实现 maxConcurrency 限流；ctx 取消则中断流程）
		member := flow.ForkMember{ProcessID: childProcessID, NodeID: node.ID}
		if err := forkNR.ForkResult.Add(1, ctx.StdCtx, member); err != nil {
			// 子 process 已创建但没能加入并发组：回收掉，避免留下悬空记录
			ctx.ProcessManager.RemoveProcess(childProcessID)
			// ctx 取消：不包装，直接返回，让上层走 StatusCancelled 路径
			if ctx.StdCtx.Err() != nil {
				return ctx.StdCtx.Err()
			}
			return newExecError(flow.ErrCodeInternal,
				fmt.Sprintf("fork add failed for node %s: %v", node.ID, err))
		}
	} else {
		ctx.MonoWGAdd(1)
	}

	// 派发 goroutine，只执行该节点本身，不沿出边继续
	go func(n *flow.Node, c *flow.ExecContext, pid string, fnr *flow.NodeResult) {
		defer func() {
			if r := recover(); r != nil {
				c.LogError(fmt.Sprintf("concurrent node %s panic: %v", n.ID, r))
			}
			if fnr != nil && fnr.ForkResult != nil {
				fnr.ForkResult.Done()
			} else {
				c.MonoWGDone()
			}
		}()

		// 调试：并发节点执行前也要做断点/单步挂起 + 全停屏障（与串行节点一致）。
		// 压入子 process 得到正确 depth，挂起/全停检查完成后弹栈。
		if c.Debug != nil && c.Debug.Enabled {
			c.Debug.PushStack(pid)
			c.Debug.SuspendIfNeeded(n, pid, c.Debug.CurrentDepth())
			c.Debug.WaitIfGloballySuspended()
			c.Debug.PopStack()
		}

		// 并发节点错误记录在自身 NodeResult，不传播到主流程
		_ = executeNode(n, c, pid)
		//println("并发节点运行完成：" + n.Data.Label)
	}(node, ctx, childProcessID, forkNR)

	return nil
}

// shouldBypassOnError 判断错误是否应绕过 on_error 处理
// （控制信号 / ctx 取消 / 内部错误 / 配置错误 / 非 Failed 状态）。
// 返回 (true, err) 时调用方直接返回 err；返回 (false, nil) 时继续 on_error 流程。
// 首次执行与重试执行共用此判断，保证两条路径语义一致。
func shouldBypassOnError(err error, nr *flow.NodeResult, ctx *flow.ExecContext) (bool, error) {
	// 控制信号：不走 on_error，原样返回
	if isControlSignal(err) {
		return true, err
	}
	// ctx 取消：不走 on_error，直接返回
	// 注意：nr 的状态应由 executor.Execute 内部设置，此处不再重复调用 nr.Finish()
	if ctx.StdCtx.Err() != nil {
		return true, ctx.StdCtx.Err()
	}
	// 系统内部错误：不走 on_error，直接返回
	var ee *ExecError
	if errors.As(err, &ee) && ee.Code == flow.ErrCodeInternal {
		return true, err
	}
	// 系统配置错误：不走 on_error，直接返回
	if errors.As(err, &ee) && ee.Code == flow.ErrCodeConfig {
		return true, err
	}
	if nr.Status != flow.StatusFailed {
		return true, err
	}
	return false, nil
}

func executeNode(node *flow.Node, ctx *flow.ExecContext, processID string) error {
	// start / entry / layout 都是结构节点，没有执行器：
	// start 是流程根入口，entry 是容器内入口，layout 是纯占位（不执行，沿出边继续）。
	if node.Type == string(flow.NodeTypeStart) ||
		node.Type == string(flow.NodeTypeEntry) ||
		node.Type == string(flow.NodeTypeLayout) {
		return nil
	}

	// 预防性检查：如果 ctx 已经取消/超时，创建 nr 并记录错误状态
	if err := ctx.StdCtx.Err(); err != nil {
		nr := flow.NewNodeResult(node, processID, ctx)

		if recordErr := ctx.RecordNodeExecution(processID, node.ID, nr); recordErr != nil {
			return failf(nr, flow.ErrCodeInternal, "failed to record the node run: "+recordErr.Error())
		}

		// 根据取消类型设置正确的状态和错误码
		if err == context.Canceled {
			nr.Finish(flow.StatusCancelled, flow.ErrCodeCancel, err.Error())
		} else {
			nr.Finish(flow.StatusFailed, flow.ErrCodeTimeout, err.Error())
		}
		return err
	}

	// 首次执行
	nr := flow.NewNodeResult(node, processID, ctx)

	if err := ctx.RecordNodeExecution(processID, node.ID, nr); err != nil {
		return failf(nr, flow.ErrCodeInternal, "failed to record the node run: "+err.Error())
	}

	executor, exists := GetExecutor(node.Type)
	if !exists {
		// 归 config 而不是 internal：走到这里几乎总是「流程图里还留着已下线的节点类型」
		// （如 knowledge，前端只为渲染老图保留）。报 internal 会让人以为是服务坏了，
		// 而这是用户能改的配置问题。
		return failf(nr, flow.ErrCodeConfig,
			fmt.Sprintf("node type %q has no executor: the type may have been retired (for example knowledge); replace the node and try again", node.Type))
	}
	currentErr := executor.Execute(node, ctx, processID, nr)
	if currentErr == nil {
		return nil
	}

	// 兜底检查：如果 executor 没有正确设置 nr.Status，这里补充设置
	if nr.Status == flow.StatusRunning {
		nr.Finish(flow.StatusFailed, flow.ErrCodeInternal,
			fmt.Sprintf("the executor of node %s(%s) did not set the node status: %v", node.Label(), node.ID, currentErr))
	}

	// 控制信号 / ctx 取消 / 内部错误 / 配置错误 / 非 Failed 状态：不走 on_error，直接返回
	if bypass, retErr := shouldBypassOnError(currentErr, nr, ctx); bypass {
		return retErr
	}

	// 校验 errCode 合法性（脚本设的同样受约束）
	// 空 ErrorCodes 表示不校验（容器节点透传子链错误码）；非空才校验
	caps := executor.Capabilities()
	if len(caps.ErrorCodes) > 0 && !containsString(caps.ErrorCodes, nr.ErrCode) {
		nr.AddLogf("warning: errCode %s is not declared by node %s, downgraded to other", nr.ErrCode, node.Type)
		// 把原始码写进文案：否则用户只看到 other，不知道节点本来报的是什么，
		// 而他们按原始码配的 on_error 一定匹配不上。
		nr.ErrMsg = fmt.Sprintf("%s (original error code %s)", nr.ErrMsg, nr.ErrCode)
		nr.ErrCode = flow.ErrCodeOther
	}

	cfg := parseOnErrorConfig(node)
	// 节点未声明的动作一律按未配置处理，与 HasPost 同源：
	// 前端面板按 Capabilities.Actions 渲染，后端若照单全收，手写 diagram 就能绕过面板限制
	// （例如给 intent 配 skip、给 branch 配 retry），声明也就失去意义。
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
	// 节点未声明 HasPost 时，忽略配置中的 post，保证前后端语义一致
	if !caps.HasPost {
		cfg.Post = nil
	}
	currentCode := nr.ErrCode
	currentMsg := nr.ErrMsg
	retryCount := 0
	// activeNR 是「当前这次尝试」的记录：首次是 nr，重试之后换成 retryNR。
	//
	// 为什么必须区分：RecordNodeExecution 用 processID_nodeID 做 key，重试会覆盖上一次，
	// 所以 ExecutedNodeMap（以及下游与界面读到的最终状态）指向的是**最后一次**。
	// 若 skip / post 仍作用在首次的 nr 上，就会出现「节点显示 failed、流程却继续走了」
	// 这种自相矛盾，post 设置的输出也会写进一条没人读的记录里。
	activeNR := nr

	for {
		// 1. retry
		if cfg.Retry != nil && matchCodes(cfg.Retry.Codes, currentCode, currentMsg, cfg.Retry.Patterns) {
			if retryCount < cfg.Retry.Max {
				retryCount++
				if cfg.Retry.DelayMs > 0 {
					// 记在当前这次尝试上（首次或上一次重试），不要永远记在第一条记录里
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
					return nil // 重试成功
				}

				//nr.RetryCount = retryCount

				// 重试失败也需 bypass 检查：控制信号 / 超时 / 内部错误等不走 on_error
				if bypass, retErr := shouldBypassOnError(retryErr, retryNR, ctx); bypass {
					return retErr
				}

				// 校验 errCode 合法性（与首次执行一致）
				if len(caps.ErrorCodes) > 0 && !containsString(caps.ErrorCodes, retryNR.ErrCode) {
					retryNR.AddLogf("warning: retry errCode %s is not declared by node %s, downgraded to other", retryNR.ErrCode, node.Type)
					retryNR.ErrCode = flow.ErrCodeOther
				}

				// 更新错误信息，回到链顶重新匹配
				activeNR = retryNR
				currentErr = retryErr
				currentCode = retryNR.ErrCode
				currentMsg = retryNR.ErrMsg
				continue
			}
		}

		// 2. skip
		// 注意下面三处都用 activeNR：命中的是「当前这次」的错误码，
		// 要改的状态与输出也必须落在同一条记录上（见 activeNR 的说明）。
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
				activeNR.AddLogf("failed to run the handler script: %v", handlerErr)
				//applyPost(activeNR, cfg.Post)
				return newExecError(activeNR.ErrCode, activeNR.ErrMsg)
			}
			if action == "skip" {
				activeNR.ErrorAction = flow.ErrorActionSkip
				activeNR.Skip()
				applyPost(activeNR, cfg.Post)
				return nil
			}
			applyPost(activeNR, cfg.Post)
			// 用记录上的值而不是循环开头的 locals：handler 脚本可以在返回值里改
			// error_code / error_message，那些改动落在 activeNR 上，用 locals 会丢掉。
			return newExecError(activeNR.ErrCode, activeNR.ErrMsg)
		}

		// 4. 都没命中 → fail
		applyPost(activeNR, cfg.Post)
		return newExecError(activeNR.ErrCode, activeNR.ErrMsg)
	}
}

// ---- 错误处理配置解析 ----

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

// ---- 匹配逻辑 ----

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

// containsAction 判断节点能力声明里是否包含某个 on_error 动作。
func containsAction(actions []flow.ErrorAction, a flow.ErrorAction) bool {
	for _, item := range actions {
		if item == a {
			return true
		}
	}
	return false
}

// ---- Handler 脚本执行 ----

func runHandlerScript(ctx *flow.ExecContext, cfg *HandlerRule,
	node *flow.Node, nr *flow.NodeResult) (string, error) {

	inputs, _ := ctx.GetVariables(cfg.Inputs, nr.ProcessID)
	if inputs == nil {
		inputs = make(map[string]interface{})
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

	// return 解析：string 兼容旧写法（"skip"/"fail"）；
	// map 可携带 action + error_code / error_message（替代原 wf_set_error）。
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

// ---- Post 处理 ----

func applyPost(nr *flow.NodeResult, post *PostConfig) {
	if post == nil {
		return
	}
	for path, value := range post.SetOutputs {
		nr.SetOutputVarWithFire(path, value)
	}
	if post.Handler != nil {
		inputs, _ := nr.Ctx.GetVariables(post.Handler.Inputs, nr.ProcessID)
		if inputs == nil {
			inputs = make(map[string]interface{})
		}
		inputs["error_code"] = nr.ErrCode
		inputs["error_message"] = nr.ErrMsg
		inputs["retry_count"] = nr.RetryCount
		result, err := ExecuteScript(nr.Ctx.StdCtx, post.Handler.Lang, post.Handler.Script, inputs, nr, 10)
		if err != nil {
			nr.AddLogf("failed to run the post-processing script: %v", err)
			nr.PostError = err.Error()
		} else if result != nil {
			if aerr := applyScriptResult(nr, result); aerr != nil {
				nr.AddLogf("failed to write the post-processing script result: %v", aerr)
				nr.PostError = aerr.Error()
			}
		}
	}
}

func GetNodeByID(f *flow.Flow, id string) *flow.Node {
	if f.NodeMap == nil {
		return nil
	}
	return f.NodeMap[id]
}

// GetStartNode 按节点类型查找工作流开始节点。
// id == "1" 只是前端/工具生成时的约定，优先命中以保持既有流程的行为；
// 缺失时按 type 兜底，避免把「开始节点」的身份绑死在字面量 id 上。
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
