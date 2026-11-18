package executors

import (
	"flame/nexus/service/flow/core"
)

var _ Executor = (*ResultExecutor)(nil)

type ResultExecutor struct{}

func (e *ResultExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	cfg, err := parseScriptNode(node, ctx, processID, nr)
	if err != nil {
		return err
	}

	// 是否作为流式结果逐条外发（缺省 false：只累积，结束时一次性返回）
	stream, _ := node.Data.Config["stream"].(bool)

	raw, err := runScript(ctx, nr, cfg, true)
	if err != nil {
		return err
	}

	// 结果 → 累积（必要时同时外发到结果流）
	msg := resultToString(raw)
	ctx.EmitResult(msg, stream)
	nr.SetOutputVarsFrom(map[string]interface{}{"output": msg})

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *ResultExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeScript, flow.ErrCodeCapability, flow.ErrCodeTimeout, flow.ErrCodeBusy, flow.ErrCodeOther},
		HasPost:    false,
	}
}

func NewResultExecutor() *ResultExecutor {
	return &ResultExecutor{}
}

func init() {
	RegisterExecutor("result", NewResultExecutor())
}
