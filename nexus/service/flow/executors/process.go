package executors

import (
	"context"
	"errors"
	"fmt"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*ProcessExecutor)(nil)

type ProcessExecutor struct{}

func (e *ProcessExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	nr.ProcessResult = &flow.ProcessResult{}

	// 初始化输出变量
	if node.Data != nil && node.Data.Config != nil {
		if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
			nr.InitOutputVars(outputs)
			nr.InitOutputBindings(outputs)
		}
	}

	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// 初始化局部变量
	if locals, ok := node.Data.Config["locals"].(map[string]any); ok {
		nr.InitLocalVars(locals)
	} else {
		nr.InitLocalVars(nil)
	}

	// 查找入口节点
	entryNode := findEntryNode(ctx.Flow, node.ID)
	if entryNode == nil {
		// 与 loop 一致：缺入口不是「可选跳过」，而是整个子流程压根没执行。
		// 静默成功会让流程看起来走完了、实际少一大段。
		return failf(nr, flow.ErrCodeConfig,
			fmt.Sprintf("process node %s: body entry node not found", node.ID))
	}
	nr.AddLog("入口节点: " + entryNode.ID)

	// 创建子 process，隔离子流程变量并区分调试调用栈层级
	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, err.Error())
	}
	nr.AddLog("子流程 processID: " + childPID)

	// 执行子节点（depth 传 0 即可，实际深度由调试栈 CurrentDepth 决定）
	err = execute(entryNode, ctx, childPID, 0)
	if err != nil {

		if isControlSignal(err) {
			nr.Finish(flow.StatusSuccess, "", "")
			nr.AddLog("收到控制信号" + err.Error() + "，process 正常结束")
			return err
		}

		// 2. stdCtx 取消（超时/中断）：置当前节点 cancel，逐层退出
		if ctx.StdCtx.Err() != nil {
			errCode := flow.ErrCodeTimeout // context.DeadlineExceeded -> 超时
			if ctx.StdCtx.Err() == context.Canceled {
				errCode = flow.ErrCodeCancel // 用户主动取消
			}
			nr.Finish(flow.StatusCancelled, errCode, ctx.StdCtx.Err().Error())
			return ctx.StdCtx.Err()
		}

		// 3. 其他错误
		code := flow.ErrCodeInternal
		var ee *ExecError
		if errors.As(err, &ee) {
			code = ee.Code
		}
		return failf(nr, code, err.Error())
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *ProcessExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{}, // 空：透传子链错误码，不校验
		Actions:    []flow.ErrorAction{},
	}
}

// findEntryNode 从 flow.Nodes 中查找 parentNode == parentID 且 type == "entry" 的入口节点
func findEntryNode(f *flow.Flow, parentID string) *flow.Node {
	for _, n := range f.Nodes {
		if n.ParentNode == parentID && n.Type == "entry" {
			return n
		}
	}
	return nil
}

func NewProcessExecutor() *ProcessExecutor {
	return &ProcessExecutor{}
}

func init() {
	RegisterExecutor("process", NewProcessExecutor())
}
