package executors

import (
	"flame/nexus/service/flow/core"
)

var _ Executor = (*ProgressExecutor)(nil)

type ProgressExecutor struct{}

func (e *ProgressExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	cfg, err := parseScriptNode(node, ctx, processID, nr)
	if err != nil {
		return err
	}

	raw, err := runScript(ctx, nr, cfg, true)
	if err != nil {
		return err
	}

	// result -> ProgressChan (the category comes from the node config: text | thinking)
	msg := resultToString(raw)
	ctx.SendProgressAs(cfg.Category, msg)
	nr.SetOutputVarsFrom(map[string]interface{}{"output": msg})

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *ProgressExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeScript, flow.ErrCodeCapability, flow.ErrCodeTimeout, flow.ErrCodeBusy, flow.ErrCodeOther},
		HasPost:    false,
	}
}

func NewProgressExecutor() *ProgressExecutor {
	return &ProgressExecutor{}
}

func init() {
	RegisterExecutor("progress", NewProgressExecutor())
}
