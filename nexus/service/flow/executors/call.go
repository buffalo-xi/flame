package executors

import (
	"fmt"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*CallExecutor)(nil)

type CallExecutor struct{}

// Execute 执行 call 节点：解析实参 → 创建子流程 → 预注入参数到 FunctionResult →
// 复用 FunctionExecutor 在子流程中执行目标 function 子链 → 镜像输出与状态。
func (e *CallExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	// 1. 校验配置
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 初始化输出变量
	if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	targetID, _ := node.Data.Config["target"].(string)
	if targetID == "" {
		return failf(nr, flow.ErrCodeConfig, "call node missing config.target")
	}

	// 2. 找到目标 function 节点
	funcNode := GetNodeByID(ctx.Flow, targetID)
	if funcNode == nil {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("target function node %s not found", targetID))
	}
	if funcNode.Type != "function" {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("target node %s is not a function node", targetID))
	}

	// 3. 校验实参与目标函数的形参声明一致（多传 / 漏传必填都在这里报错）
	argsConfig, _ := node.Data.Config["args"].(map[string]interface{})
	var paramsConfig map[string]interface{}
	if funcNode.Data != nil {
		paramsConfig, _ = funcNode.Data.Config["params"].(map[string]interface{})
	}
	for name, rawDef := range paramsConfig {
		def, _ := rawDef.(map[string]interface{})
		required := true
		if v, ok := def["required"].(bool); ok {
			required = v
		}
		if _, ok := argsConfig[name]; !ok && required {
			return failf(nr, flow.ErrCodeConfig,
				fmt.Sprintf("missing required arg %q for function %s", name, targetID))
		}
	}
	for name := range argsConfig {
		if _, ok := paramsConfig[name]; !ok {
			return failf(nr, flow.ErrCodeConfig,
				fmt.Sprintf("unknown arg %q for function %s", name, targetID))
		}
	}

	// 4. 解析实参（在父流程上下文中解析变量来源，再注入子流程）
	paramValues, err := ctx.GetVariables(argsConfig, processID)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, "resolve args failed: "+err.Error())
	}

	// 5. 检查递归深度（在创建子进程之前）
	maxDepth := ctx.ResolveRecursionDepth()
	if maxDepth > 0 {
		currentDepth := ctx.ProcessManager.GetCallDepth(processID)
		if currentDepth >= maxDepth {
			return failf(nr, flow.ErrCodeInternal,
				fmt.Sprintf("exceeded max recursion depth: %d (limit: %d)", currentDepth, maxDepth))
		}
	}

	// 6. 创建子流程，标记为 call_func 用于递归深度追踪
	childPID, err := ctx.ProcessManager.NewProcess(processID, map[string]interface{}{
		"call_func": true,
	})
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, "create call child process: "+err.Error())
	}

	// 7. 为 function 节点创建 funcNR 并预注入参数
	funcNR := flow.NewNodeResult(funcNode, childPID, ctx)
	funcNR.FunctionResult = &flow.FunctionResult{}
	for k, v := range paramValues {
		if err := funcNR.FunctionResult.SetParamVar(k, v); err != nil {
			return failf(nr, flow.ErrCodeConfig, "inject param "+k+" failed: "+err.Error())
		}
	}
	if err := ctx.RecordNodeExecution(childPID, funcNode.ID, funcNR); err != nil {
		return failf(nr, flow.ErrCodeInternal, "记录节点执行失败: "+err.Error())
	}

	label := targetID
	if funcNode.Data != nil && funcNode.Data.Label != "" {
		label = funcNode.Data.Label
	}
	nr.AddLog(fmt.Sprintf("调用函数 %s(%s)", label, funcNode.ID))

	// 8. 复用 FunctionExecutor 执行（function.go 已改为 FunctionResult 为 nil 时才创建，
	//    因此这里预注入的参数不会被覆盖）
	funcExec := NewFunctionExecutor()
	err = funcExec.Execute(funcNode, ctx, childPID, funcNR)

	// 9. 复制输出到 call 节点
	nr.SetOutputVarsFrom(funcNR.OutputVars)

	// 10. 镜像 funcNR 状态（FunctionExecutor 已 Finish funcNR）
	if err != nil {
		nr.Finish(funcNR.Status, funcNR.ErrCode, funcNR.ErrMsg)
		return err
	}
	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *CallExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: nil, // 未定义：使用全局错误码，透传子链所有错误码
		Actions:    []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost:    true,
	}
}

func NewCallExecutor() *CallExecutor {
	return &CallExecutor{}
}

func init() {
	RegisterExecutor("call", NewCallExecutor())
}
