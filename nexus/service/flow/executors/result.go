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

	// Whether to emit it as a streamed result item by item (default false: accumulate and return the
	// whole thing at the end)
	stream, _ := node.Data.Config["stream"].(bool)

	raw, err := runScript(ctx, nr, cfg, true)
	if err != nil {
		return err
	}

	// result -> accumulate (and emit onto the result stream when asked)
	msg := resultToString(raw)
	ctx.EmitResult(msg, stream)
	nr.SetOutputVarsFrom(map[string]interface{}{"output": msg})

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *ResultExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes:   []string{flow.ErrCodeScript, flow.ErrCodeCapability, flow.ErrCodeTimeout, flow.ErrCodeBusy, flow.ErrCodeOther},
		HasPost:      false,
		FixedOutputs: []string{"output"},
	}
}

func NewResultExecutor() *ResultExecutor {
	return &ResultExecutor{}
}

func init() {
	RegisterExecutor("result", NewResultExecutor())
}
