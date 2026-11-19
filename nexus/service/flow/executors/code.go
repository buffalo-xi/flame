package executors

import (
	"flame/nexus/service/flow/core"
	"flame/tools/xjson"
)

var _ Executor = (*CodeExecutor)(nil)

type CodeExecutor struct{}

func (e *CodeExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	// 设置 CodeResult
	nr.CodeResult = &flow.CodeResult{}

	// 初始化输出变量（须在解析配置前就位：脚本返回值的落地依赖这些声明）
	if node.Data != nil && node.Data.Config != nil {
		if outputs, ok := node.Data.Config["outputs"].(map[string]interface{}); ok {
			nr.InitOutputVars(outputs)
			nr.InitOutputBindings(outputs)
		}
		if locals, ok := node.Data.Config["locals"].(map[string]interface{}); ok {
			nr.InitLocalVars(locals)
		}
	}

	cfg, err := parseScriptNode(node, ctx, processID, nr)
	if err != nil {
		return err
	}

	scriptResult, err := runScript(ctx, nr, cfg, false)
	if err != nil {
		return err
	}

	if scriptResult == nil {
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	mapResult, ok := scriptResult.(map[string]interface{})
	if !ok {
		return failf(nr, flow.ErrCodeScript, "unexpected return type: "+xjson.MarshalJSON(scriptResult))
	}

	if err := applyScriptResult(nr, mapResult); err != nil {
		return failf(nr, flow.ErrCodeVar, err.Error())
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *CodeExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeScript, flow.ErrCodeCapability, flow.ErrCodeVar, flow.ErrCodeTimeout, flow.ErrCodeBusy, flow.ErrCodeOther},
		Actions:    []flow.ErrorAction{flow.ErrorActionSkip, flow.ErrorActionRetry, flow.ErrorActionHandler},
		HasPost:    true,
	}
}

func NewCodeExecutor() *CodeExecutor {
	return &CodeExecutor{}
}

func init() {
	RegisterExecutor("code", NewCodeExecutor())
}
