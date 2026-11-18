package executors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/core"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/flowconfig"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	flows "flame/nexus/shared/repository/flows"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/conv"
	"flame/tools/xjson"

	"github.com/google/uuid"
)

// rpcEndpointProgress 本租户端点执行过程中的一条进度。
// Category 取自 agent/core 的 Category* 常量；Meta 仅工具类事件有值。
type rpcEndpointProgress struct {
	Message  string
	Category string
	Meta     map[string]interface{}
}

// findRpcEndpoint 按**端点名**定位本租户的 rpc 端点。
//
// 不用 id 的原因：端点名恒等于实体名，创建时已保证唯一、实体改名时 syncEndpoints
// 会同步端点名；而 id 在「导出 → 导入到另一个租户」后会重新生成，diagram 里的 id 就废了。
func findRpcEndpoint(authInfo *auth.AuthInfo, name string) (*models.SvcEndpoint, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	// 不过滤 exposed：本租户直调允许调用未对外开放的端点
	return svcendpoints.NewRepository().FindByName(authInfo, "rpc", name)
}

// runEndpoint 进程内执行本租户 RPC 端点（target_type=agent/flow），不校验 exposed
// （本租户直调允许调用仅租户内部可用的端点）。
//
// callDepth 是调用方所处的层数（顶层 0）：agent 走 tools.Context.CallDepth，
// rpc 节点走 ExecContext.CallDepth。实际执行以 callDepth+1 下传。
//
// wantProgress 来自节点配置（rpcCfg.progress，默认关闭）：为 false 时同步跑完只取最终结果，
// 不消费进度通道 —— 进程内虽然没有 wire，但「要不要这份进度」仍应由调用方显式表达，
// 否则每个节点调用都会白收一遍 agent 的思考/文本。
// 返回 (result, isError, err)：err != nil 表示节点异常；isError=true 表示业务错误。
//
// scope 是调试会话的草稿作用域：非 nil 且开启「跟随依赖的草稿」时，
// 被调 flow 读草稿并记进 scope；为 nil（正式入口）时读正式版本。
// 它是本次执行的内存属性，不进任何跨进程协议 —— 所以外部调用天然没有 scope。
func runEndpoint(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	sessionID string,
	callDepth int,
	callChain []string,
	endpointName string,
	arguments map[string]interface{},
	wantProgress bool,
	scope *flow.DebugScope,
	onProgress func(p rpcEndpointProgress),
) (string, bool, error) {
	// 端点一律按名字定位：名字在租户内唯一、改名会跟着变；id 在导出导入后会重新生成
	ep, err := findRpcEndpoint(authInfo, endpointName)
	if err != nil {
		return "", false, fmt.Errorf("查询 RPC 端点失败: %w", err)
	}
	if ep == nil {
		return "", false, fmt.Errorf("RPC 端点不存在: %s", endpointName)
	}

	// 调用深度护栏：agent → 本地 flow 端点 → rpc 节点 → agent 端点 → … 会成环，
	// 而每次跨 run 都会新建 ExecContext（执行计数各自归零），只能靠深度收敛。
	//
	// 上限取 flow 侧递归深度（flow_configs.max_recursion_depth，默认 10）：
	// 这条链的主要形态是 flow 编排，套 agent 的 3 层会误伤 A→B→C→D 的流水线。
	// agent 参与的链另有一道更严的闸（见 agent/localendpoint.go），各管各的语义。
	//
	// 配置成 -1 / 0（「不限制」）时**不认**，退回兜底值 —— 纯 flow 互相调用没有
	// 别的约束（agent 那道闸不在链上），这道闸被一次误配置关掉就等于敞开。
	next := callDepth + 1
	if maxDepth := resolveEndpointDepthLimit(authInfo.Schema); next > maxDepth {
		return "", false, fmt.Errorf(
			"调用深度超限（第 %d 层，上限 %d）：拒绝继续调用端点 %q，请检查 agent 与 flow 是否互相调用",
			next, maxDepth, ep.Name)
	}

	// 循环检测：目标实体已在祖先链里就立刻拒（f → a → f），不必等深度跑满。
	// 放在深度检查之后：层数超限更好理解，优先报它。
	// 平级扇出不受影响：同一 flow 的多个 rpc 节点读到的是同一条链，且都只含祖先。
	self := callchain.Key(ep.TargetType, ep.TargetID)
	if callchain.Contains(callChain, self) {
		return "", false, fmt.Errorf("检测到循环调用：%s 已在本调用链中（%s）", self, callchain.Format(callChain))
	}
	nextChain := callchain.Append(callChain, self)

	switch ep.TargetType {
	case "agent":
		// 调试会话跟随 agent 草稿并记账；正式运行读已发布版本（没有版本就拒绝）
		return runAgentEndpoint(ctx, authInfo, sessionID, next, nextChain, ep.TargetID, arguments, wantProgress, scope, onProgress)
	case "flow":
		return runFlowEndpoint(ctx, authInfo, sessionID, next, nextChain, ep.TargetID, arguments, wantProgress, scope, onProgress)
	default:
		return "", false, fmt.Errorf("不支持的 target_type %q", ep.TargetType)
	}
}

// resolveEndpointDepthLimit 端点调用链的深度上限（恒为正数）。
func resolveEndpointDepthLimit(schema string) int {
	return effectiveEndpointDepthLimit(
		flowconfig.Resolve(schema).MaxRecursionDepth,
		flowconfig.Fallback().MaxRecursionDepth,
	)
}

// effectiveEndpointDepthLimit 把配置值收敛成正数上限。
//
// 配置语义里 -1 表示「不限制」，但这里刻意不认：纯 flow 的环只有这一道闸
// （agent 参与的链另有 max_call_depth），允许它被关掉等于把防线交给一次误配置。
// 因此非正数一律退回 fallback；fallback 本身异常时再取 10，绝不放行无上限。
func effectiveEndpointDepthLimit(configured, fallback int) int {
	if configured > 0 {
		return configured
	}
	if fallback > 0 {
		return fallback
	}
	return 10
}

// runFlowEndpoint 执行 flow 类型端点。
//
// sessionID 与 runAgentEndpoint 同口径透传：子 flow 与父级落在同一个
// users/{uid}/sessions/{sid}/ 场地。原先这里漏了 —— agent→flow 与 flow→agent
// 都共享会话，唯独 flow→flow 不共享，子 flow 的产物全落进 runs/，
// 随调用结束被删，事后从会话里找不回来。
//
// wantProgress 为 true 时边跑边回调进度；为 false 时同步执行，只返回最终结果。
// 跨进程的 MCP / RPC 端点上「要不要进度」由 params._meta.progressToken 表达，
// 这里是进程内直调，等价语义落在节点配置上（rpcCfg.progress）。
func runFlowEndpoint(ctx context.Context, authInfo *auth.AuthInfo, sessionID string, callDepth int, callChain []string, flowId string, input map[string]interface{}, wantProgress bool, scope *flow.DebugScope, onProgress func(p rpcEndpointProgress)) (string, bool, error) {
	repo := flows.NewRepository()

	// 先读实体行：状态与名字从这里来；定义列再按「草稿 or 正式版本」选。
	Instance, err := repo.GetFlow(authInfo, flowId)
	if err != nil {
		return "", false, fmt.Errorf("获取 flow 实例失败: %w", err)
	}
	// 废弃的 flow 不可运行：内部调用（RPC 节点的本租户直调）走的正是这里，
	// 而 runEndpoint 明确**不校验 exposed**，所以这是唯一的无条件拦截点。
	if !models.IsRunnable(int(Instance.Status)) {
		return "", false, fmt.Errorf("flow %q 已废弃，不可调用", Instance.Name)
	}

	// 「调试跑草稿、正式跑正式版」唯一的分叉点：
	//   - 有 scope（这次执行源自编辑页调试器 / run_flow 试跑）：读工作副本，并记账；
	//   - 没有 scope（MCP / RPC / agent 的 local__ 等正式入口）：必须读正式版本，
	//     没有正式版本就拒绝 —— 不能悄悄退回草稿，否则「正式」二字就没了。
	if scope.Enabled() {
		scope.RecordDraft("flow", flowId, Instance.Name, "调试会话跟随草稿")
	} else {
		released, perr := repo.GetCurrentFlowVersion(authInfo, flowId)
		if errors.Is(perr, flows.ErrNoReleasedVersion) {
			return "", false, fmt.Errorf(
				"flow %q 尚未发布正式版本：正式调用只运行已发布的版本，请在编辑页发布后再试", Instance.Name)
		}
		if perr != nil {
			return "", false, fmt.Errorf("获取 flow %q 正式版本失败: %w", Instance.Name, perr)
		}
		Instance = released
	}

	// 深度随 flow 下传：复制入参后注入 _call_depth（NewFlow 会剥离，不污染业务参数）。
	// 必须复制而不是直接改 input：那是调用方（rpc 节点 / 工具参数）持有的 map。
	flowInput := input
	if flowInput == nil {
		flowInput = make(map[string]interface{}, 1)
	} else {
		cp := make(map[string]interface{}, len(flowInput)+1)
		for k, v := range flowInput {
			cp[k] = v
		}
		flowInput = cp
	}
	flowInput["_call_depth"] = callDepth
	// 链随 flow 下传：NewFlow 的 extractCallChain 会剥离它，不污染业务参数
	if len(callChain) > 0 {
		flowInput[callchain.ArgumentKey] = callChain
	}
	// 会话随 flow 下传：NewFlow 的 extractSessionID 会把它剥离，不污染业务参数
	if sessionID != "" {
		flowInput["session_id"] = sessionID
	}

	execCtx, err := NewFlow(Instance, flowInput, conv.ToString(nil), uuid.Must(uuid.NewV7()).String(), authInfo.Schema, authInfo.ID, ctx)
	if err != nil {
		return "", false, fmt.Errorf("初始化 flow 执行失败: %w", err)
	}
	// 作用域继续往下传：子流程里的 rpc 节点要按同一个 scope 决定它的子调用读哪份
	execCtx.DebugScope = scope

	if !wantProgress {
		// 同步执行：不消费 progress/result 通道（无人读时内层写入是非阻塞的）
		Start(execCtx)
		return endpointFlowResult(execCtx)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- Start(execCtx)
		execCtx.CloseChans()
	}()

	progressCh := execCtx.GetProgressChan()
	resultCh := execCtx.GetResultChan()

loop:
	for progressCh != nil || resultCh != nil {
		select {
		case chunk, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			if chunk.Message != "" && onProgress != nil {
				onProgress(rpcEndpointProgress{Message: xjson.SanitizeJSON(core.CleanProgressMarkers(chunk.Message)), Category: chunk.Category})
			}
		case chunk, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			if chunk != "" && onProgress != nil {
				onProgress(rpcEndpointProgress{Message: xjson.SanitizeJSON(chunk), Category: core.CategoryText})
			}
		case <-errCh:
			break loop
		case <-ctx.Done():
			execCtx.CloseChans()
			return "", false, ctx.Err()
		}
	}

	return endpointFlowResult(execCtx)
}

// runAgentEndpoint 执行 agent 类型端点；wantProgress 为 true 时边跑边回调进度，否则只取最终答案。
func runAgentEndpoint(ctx context.Context, authInfo *auth.AuthInfo, sessionID string, callDepth int, callChain []string, agentID string, input map[string]interface{}, wantProgress bool, scope *flow.DebugScope, onProgress func(p rpcEndpointProgress)) (string, bool, error) {
	agentInst, err := agents.NewRepository().GetAgent(authInfo, agentID)
	if err != nil {
		return "", false, fmt.Errorf("获取 agent 实例失败: %w", err)
	}
	// 废弃的 agent 不可运行（同 runFlowEndpoint：本租户直调不校验 exposed）
	if !models.IsRunnable(int(agentInst.Status)) {
		return "", false, fmt.Errorf("agent %q 已废弃，不可调用", agentInst.Name)
	}
	// 超级智能体照常可被 flow 调起，不在这里拦：它的能力不该因调用者而打折
	// （管理面就是改草稿，读发布版等于让它看不见自己刚建的东西）。
	// 代价是这一格不受 scope 控制 —— resolveDefinitionMode 让 supervisor 恒读草稿，
	// 所以「正式 flow → supervisor」这条分支的结果不可复现，排查时以 supervisor 的草稿为准。
	agentArguments := map[string]interface{}{
		"args":      input,
		"_run_type": "flow",
	}
	// 深度与 user/session 一并透传：被调 agent 的 NewAgent 读 _call_depth，
	// 它再调 local 工具时由 agent 侧的上限继续拦。
	if callDepth > 0 {
		agentArguments["_call_depth"] = callDepth
	}
	// 链转给 agent：它再通过 rpc__ 出站时会把整条链带进 _meta.call_chain。
	// 放顶层保留键而不是 args 内部 —— args 是模型可见的业务入参。
	if len(callChain) > 0 {
		agentArguments[callchain.ArgumentKey] = callChain
	}
	// flow 的 user 与 session 一并透传：agent 节点与 flow 共用同一场地
	if authInfo.ID != "" {
		agentArguments["user_id"] = authInfo.ID
	}
	if sessionID != "" {
		agentArguments["session_id"] = sessionID
	}
	// 调试会话跟随 agent 草稿并记账（与 flow 的草稿跟随同一条口径）；
	// 正式运行读已发布的当前版本，没有版本直接报错。
	//
	// _follow_drafts 显式注入而不是靠子 agent 的缺省值：
	// 草稿是否继续往它调起的 agent / flow 扩散，应当由这里的 scope 决定，
	// 而不是由「那边缺省是 true」这条隐式约定兜住（缺省将来一改就是静默行为漂移）。
	if scope.Enabled() {
		scope.RecordDraft("agent", agentID, agentInst.Name, "调试会话跟随草稿")
		agent.SetDraftMode(agentArguments)
		agent.SetFollowDrafts(agentArguments, true)
	} else {
		agent.SetReleaseMode(agentArguments)
		agent.SetFollowDrafts(agentArguments, false)
	}
	inst, err := agent.NewAgent(agentInst, authInfo.Schema, agentArguments)
	if err != nil {
		return "", false, fmt.Errorf("初始化 agent 失败: %w", err)
	}
	if inst == nil {
		return "", false, fmt.Errorf("agent %s 不支持或不存在", agentID)
	}
	defer inst.Close()

	if !wantProgress {
		// 同步执行：不消费事件通道，只取最终答案
		if runErr := inst.Stream(context.Background()); runErr != nil {
			return "", false, fmt.Errorf("运行 agent 失败: %w", runErr)
		}
		inst.CloseChans()
		return endpointAgentResult(inst)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- inst.Stream(context.Background())
		inst.CloseChans()
	}()

	// 消费结构化事件 channel：思考与工具调用只在这里出现。
	eventCh := inst.GetEventChan()

loop:
	for eventCh != nil {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			if onProgress == nil {
				continue
			}
			if item, has := ev.ToProgress(); has {
				onProgress(rpcEndpointProgress{
					Message:  xjson.SanitizeJSON(item.Message),
					Category: item.Category,
					Meta:     item.Meta,
				})
			}
		case <-errCh:
			break loop
		case <-ctx.Done():
			inst.CloseChans()
			return "", false, ctx.Err()
		}
	}

	return endpointAgentResult(inst)
}

// endpointFlowResult 从 flow ExecContext 提取结果：业务错误走 isError=true。
func endpointFlowResult(execCtx *flow.ExecContext) (string, bool, error) {
	if e := execCtx.GetError(); e != "" {
		return e, true, nil
	}
	return execCtx.GetResult(), false, nil
}

// endpointAgentResult 从 agent 实例提取结果：业务错误走 isError=true。
func endpointAgentResult(inst core.Agent) (string, bool, error) {
	if e := inst.GetError(); e != "" {
		return e, true, nil
	}
	return inst.GetResult(), false, nil
}
