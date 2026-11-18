package executors

import (
	"context"

	"flame/nexus/service/agent"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
)

// init 把「进程内跑一个端点」的能力交给 agent 包，供 local__<端点名> 工具调用。
//
// 依赖方向：executors → service/agent（rpc 节点要直调 agent 端点，早已如此），
// service/agent 不反向 import 本包 —— 环由这个注册点打破。任何加载了 flow 的进程
// （cmd/main、websocket、rpc、mcp 四个入口都 import 本包）自动具备该能力。
func init() { agent.SetLocalEndpointRunner(runEndpointForTool) }

// runEndpointForTool 供 agent 的 local__<端点> 工具调用。
//
// wantProgress 恒为 false：工具调用只回最终结果，没有进度通道；传 true 会让
// 端点白白收集一遍 agent 的思考/文本碎片。onProgress 传 nil 与此一致。
//
// callDepth 是调用方 agent 的当前深度（tools.Context.CallDepth），端点执行时以 +1
// 继续下传 —— agent → flow → agent 这条环靠它收敛。
//
// 调用链经 ctx 传来（agent 侧 Invoke 里 callchain.With），不为这个函数再加一个参数：
// 签名已有 8 个位置参数，链是纯控制信息、不进业务语义。
func runEndpointForTool(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	sessionID string,
	callDepth int,
	endpointName string, // 端点名（= 流程名 / 智能体名），不再是 id
	args map[string]any,
	wantProgress bool,
	followDrafts bool,
) (string, bool, error) {
	// followDrafts 来自调用方 agent 的读定义模式：调试中的 agent 调本地端点，
	// 被跑的 flow 也跟随草稿（否则调试链上会出现「一半草稿、一半线上」）。
	// 正式运行传 false = scope 为 nil = 读正式版本。
	var scope *flow.DebugScope
	if followDrafts {
		scope = flow.NewDebugScope(true)
	}
	return runEndpoint(ctx, authInfo, sessionID, callDepth, callchain.From(ctx), endpointName, args, wantProgress, scope, nil)
}
