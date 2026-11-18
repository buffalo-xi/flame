package executors

import (
	"context"
	"errors"
	"fmt"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*FunctionExecutor)(nil)

type FunctionExecutor struct{}

func (e *FunctionExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	// call 节点调用时会预创建 FunctionResult 并注入参数，此处保留不覆盖
	if nr.FunctionResult == nil {
		nr.FunctionResult = &flow.FunctionResult{}
	}

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

	// params 不在此初始化，由 call 节点执行时创建 FunctionResult 时注入

	// 查找入口节点
	entryNode := findEntryNode(ctx.Flow, node.ID)
	if entryNode == nil {
		// 与 loop / process 一致：缺入口不是「可选跳过」。
		// 静默成功会让 call 拿到一个「成功但没有任何输出」的结果，比直接报错难查得多。
		return failf(nr, flow.ErrCodeConfig,
			fmt.Sprintf("function node %s: body entry node not found", node.ID))
	}
	//nr.AddLog("入口节点: " + entryNode.ID)

	// 创建子 process 执行子链（function 子节点在子流程中，隔离子流程变量并区分调试调用栈）
	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, "create function process: "+err.Error())
	}

	err = execute(entryNode, ctx, childPID, 0)
	if err != nil {

		if errors.Is(err, SignalReturn) {
			nr.Finish(flow.StatusSuccess, "", "")
			nr.AddLog("收到 return 信号，function 正常返回")
			return nil
		}

		if errors.Is(err, SignalExit) {
			nr.Finish(flow.StatusSuccess, "", "")
			nr.AddLog("收到 exit 信号，退出整个工作流")
			return err // 传播 SignalExit 到上层
		}

		// break/continue 只在 loop 上下文有效，
		// 泄漏到 function 说明子链里存在 loop 外部的 break/continue 节点
		if errors.Is(err, SignalBreak) || errors.Is(err, SignalContinue) {
			return failf(nr, flow.ErrCodeConfig,
				fmt.Sprintf("break/continue 不能出现在 function(node %s) 子链的 loop 外部", node.ID))
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

func (e *FunctionExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{},
		Actions:    []flow.ErrorAction{},
	}
}

func NewFunctionExecutor() *FunctionExecutor {
	return &FunctionExecutor{}
}

func init() {
	RegisterExecutor("function", NewFunctionExecutor())
}
