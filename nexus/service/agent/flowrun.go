package agent

import (
	"context"
	"errors"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
)

// RunFlowTestFunc 进程内试运行一个工作流，由 flow/executors 注册。
//
// 为什么又用注册点：executors 反向 import 本包（rpc 节点要直调 agent 端点，见
// executors/rpc_target.go），本包不能再 import 回去 —— 否则成环。与
// SetLocalEndpointRunner 同一套理由，区别只在「跑一个已对外开放端点」vs「跑一个按 id
// 指定的流程」：后者不要求端点、不校验 exposed，供 run_flow 工具试跑。
type RunFlowTestFunc func(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	flowID string,
	args map[string]any,
	sessionID string,
	callDepth int,
	followDrafts bool,
) (*tools.FlowRunResult, error)

var runFlowTest RunFlowTestFunc

// SetFlowTestRunner 由 flow/executors 在 init 中调用，进程级生效。
// 任何加载了 flow 的进程（main / websocket / rpc / mcp 四个入口都 import 该包）自动具备。
func SetFlowTestRunner(fn RunFlowTestFunc) { runFlowTest = fn }

// errFlowTestUnavailable 执行通道没装配：报清楚原因，而不是让工具「成功返回空结果」。
var errFlowTestUnavailable = errors.New(
	"试运行不可用：本进程未加载 flow 执行器（run_flow 依赖 /v1/code、沙箱与执行器注册）")
