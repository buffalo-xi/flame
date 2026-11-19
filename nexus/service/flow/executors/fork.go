package executors

import (
	"flame/nexus/service/flow/core"
	"fmt"
)

var _ Executor = (*ForkExecutor)(nil)

type ForkExecutor struct{}

func (e *ForkExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	// 获取最大并发数，默认为 -1（不限制）
	maxConcurrency := -1
	if node.Data != nil && node.Data.Config != nil {
		if val, ok := node.Data.Config["max_concurrency"]; ok && val != nil {
			switch v := val.(type) {
			case int:
				maxConcurrency = v
			case float64:
				maxConcurrency = int(v)
			default:
				// 静默退回 -1 会让用户配的上限彻底失效（并发不受限），而界面看不出任何异常。
				// 前端存的是 number，走到这里基本是手改 diagram 或资产包导入的类型错。
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
