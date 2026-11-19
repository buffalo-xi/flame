package executors

import (
	"flame/nexus/service/flow/core"
	"fmt"
)

var _ Executor = (*ForkExecutor)(nil)

type ForkExecutor struct{}

func (e *ForkExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	// Read the maximum concurrency, defaulting to -1 (unlimited)
	maxConcurrency := -1
	if node.Data != nil && node.Data.Config != nil {
		if val, ok := node.Data.Config["max_concurrency"]; ok && val != nil {
			switch v := val.(type) {
			case int:
				maxConcurrency = v
			case float64:
				maxConcurrency = int(v)
			default:
				// Silently falling back to -1 would void the limit the user configured (concurrency
				// would be unbounded) while the UI shows nothing wrong. The frontend stores a number,
				// so reaching this point means a hand-edited diagram or a type error in an imported
				// asset package.
				return failf(nr, flow.ErrCodeConfig,
					fmt.Sprintf("max_concurrency must be a number, got %T", val))
			}
		}
	}

	nr.ForkResult = flow.NewForkResult(maxConcurrency)
	nr.AddLog(fmt.Sprintf("max_concurrency: %d", maxConcurrency))
	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *ForkExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeOther},
		Actions:    []flow.ErrorAction{},
	}
}

func NewForkExecutor() *ForkExecutor {
	return &ForkExecutor{}
}

func init() {
	RegisterExecutor("fork", NewForkExecutor())
}
