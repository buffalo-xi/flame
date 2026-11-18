package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"flame/nexus/service/agent/toolgrant"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/agentconfig"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	"flame/nexus/shared/models"
	svcendpoints "flame/nexus/shared/repository/svcendpoints"
	"flame/tools/xlog"
)

// RunLocalEndpointFunc 进程内执行一个本地端点，由 flow/executors 注册。
//
// 为什么用注册点而不是直接 import：executors 反向 import 本包（rpc 节点要直调
// agent 端点，见 executors/rpc_target.go），本包再 import 回去就成环了。
// 注册函数等价于把「跑 flow」这件事倒置出去 —— 依赖方向保持单向：
// executors → service/agent → tools。
//
// 签名与 executors.runEndpoint 对齐，去掉 onProgress：工具调用只有最终结果一条
// 通道，订阅进度等于白收一遍 flow 的碎事件（与 rpc 节点 progress 默认关闭同口径）。
type RunLocalEndpointFunc func(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	sessionID string,
	callDepth int,
	endpointID string,
	args map[string]any,
	wantProgress bool,
	followDrafts bool,
) (content string, isError bool, err error)

var runLocalEndpoint RunLocalEndpointFunc

// SetLocalEndpointRunner 由 flow/executors 在 init 中调用，进程级生效。
func SetLocalEndpointRunner(fn RunLocalEndpointFunc) { runLocalEndpoint = fn }

// localEndpointRunner 把 local__<端点名> 翻译成一次进程内的端点执行。
//
// 只覆盖本地端点（local__）；mcp__ / rpc__ 归 externalToolRunner，两者前缀不重叠。
//
// 端点目录按运行实例缓存（eps）：一次 agent 循环里可能连着调好几次 local__，
// 每次 ListAll 一遍 svc_endpoints 是纯浪费；而同一轮里端点表不会变，
// 快照语义与「本轮工具目录在开跑时冻结」一致。
type localEndpointRunner struct {
	mu  sync.Mutex
	eps []models.SvcEndpoint
}

var _ tools.EndpointRunner = (*localEndpointRunner)(nil)

func (*localEndpointRunner) Has(name string) bool {
	return strings.HasPrefix(name, tools.ExternalPrefixLocal)
}

func (r *localEndpointRunner) Invoke(ctx context.Context, in tools.LocalCall) (any, error) {
	if in.Schema == "" {
		return nil, fmt.Errorf("工具 %s 属于本地端点，但缺少租户上下文", in.ToolName)
	}
	epName := strings.TrimPrefix(in.ToolName, tools.ExternalPrefixLocal)
	if epName == "" || epName == in.ToolName {
		return nil, fmt.Errorf("%w: %s", tools.ErrToolNotFound, in.ToolName)
	}

	// agent 参与的链单独用 agent 的深度上限（platform.agent_configs.max_call_depth，
	// 默认 3，与 call_agent 同源）：每回到一次 agent 就要多跑一整轮 LLM，比纯 flow
	// 编排贵得多，不能跟 flow 侧的 max_recursion_depth（默认 10）共用。
	// 放在查端点之前：超限时连库都不用查。
	if maxDepth := agentconfig.Resolve(in.Schema).MaxCallDepth; maxDepth > 0 && in.CallDepth >= maxDepth {
		return nil, fmt.Errorf("agent 调用深度超限（已达 %d 层，上限 %d）：拒绝继续调用 %s",
			in.CallDepth, maxDepth, in.ToolName)
	}

	// 执行层再收一次口径：只认 protocol=rpc 且 target_type=flow。
	// 目录侧已经收窄（Catalog.localRPCConns），这里是纵深防御 —— 否则一条手工写入的
	// local:rpc:<agent名> 授权行就能直调 agent 端点，绕过 agent_relations 与
	// 「main 不可被其它 agent 调用」两道闸（见 toolgrant/ref.go 的说明）。
	ep, err := r.findEndpoint(in.Schema, epName)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, fmt.Errorf("%w: %s（本地端点不存在，或它不是 rpc 协议的工作流端点）",
			tools.ErrToolNotFound, in.ToolName)
	}
	if runLocalEndpoint == nil {
		return nil, fmt.Errorf("工具 %s 无法执行：本地端点执行通道未装配（本进程未加载 flow/executors）", in.ToolName)
	}

	// 会话透传：让被调 flow 与调用方落在同一个 users/{uid}/sessions/{sid}/ 场地。
	// session_id 由 NewFlow 的 extractSessionID 剥离，不会污染 flow 的业务入参。
	args := in.Args
	if in.SessionID != "" {
		args = make(map[string]any, len(in.Args)+1)
		for k, v := range in.Args {
			args[k] = v
		}
		args["session_id"] = in.SessionID
	}

	authInfo := &auth.AuthInfo{Schema: in.Schema, ID: in.UserID}
	// 祖先链经 ctx 交给执行层：端点直调会在那边做循环检测并以「链 + 目标」继续下传。
	// 不走函数参数：RunLocalEndpointFunc 已有 8 个位置参数，链是纯控制信息。
	ctx = callchain.With(ctx, in.CallChain)
	// runLocalEndpoint 按端点名定位（名字唯一、改名同步，id 在导出导入后会变）：这里刚按名字查到 ep
	content, isError, err := runLocalEndpoint(ctx, authInfo, in.SessionID, in.CallDepth, ep.Name, args, false,
		!in.DefinitionMode.IsCurrent())
	if err != nil {
		return nil, fmt.Errorf("本地端点 %s 执行失败: %w", ep.Name, err)
	}
	xlog.Info("本地端点工具调用 schema=%s agent=%s endpoint=%s depth=%d is_error=%v",
		in.Schema, in.AgentName, ep.Name, in.CallDepth, isError)
	// 业务错误与外部工具同口径：is_error=true 仍然把正文交给模型，不上升为 Go error
	// （模型看到错误正文才有机会自我纠正；连接/装配失败才走 err）。
	return externalResult(content, nil, isError, in.MaxOutput), nil
}

// findEndpoint 精确按名找「本租户 protocol=rpc 且目标是工作流」的端点。
//
// 不能用 ListAll 的 Name 过滤：endpointWhere 对 name 是 ILIKE 模糊匹配
// （e.name ILIKE '%name%'），"poet" 会命中 "poet-2"，模型按名字调到的可能是另一个端点。
func (r *localEndpointRunner) findEndpoint(schema, name string) (*models.SvcEndpoint, error) {
	list, err := r.endpoints(schema)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// endpoints 返回本次运行的端点目录快照（首次调用查库，之后复用）。
//
// 只取 protocol=rpc 且 target=flow 的行：这是 local__ 唯一允许调的目标形态
// （见 Invoke 里的纵深防御）。
func (r *localEndpointRunner) endpoints(schema string) ([]models.SvcEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eps != nil {
		return r.eps, nil
	}
	list, err := svcendpoints.NewRepository().ListAll(
		&auth.AuthInfo{Schema: schema},
		&svcendpoints.ListSvcEndpointsRequest{
			Protocol:   toolgrant.LocalProtocolRPC,
			TargetType: toolgrant.LocalTargetFlow,
		})
	if err != nil {
		return nil, fmt.Errorf("查询本地端点失败: %w", err)
	}
	r.eps = list
	return list, nil
}
