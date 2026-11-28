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
	// A call node pre-creates FunctionResult and injects the arguments before invoking this; keep it and
	// do not overwrite
	if nr.FunctionResult == nil {
		nr.FunctionResult = &flow.FunctionResult{}
	}

	// Initialise the output variables
	if node.Data != nil && node.Data.Config != nil {
		if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
			nr.InitOutputVars(outputs)
			nr.InitOutputBindings(outputs)
		}
	}

	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// Initialise the local variables
	if locals, ok := node.Data.Config["locals"].(map[string]any); ok {
		nr.InitLocalVars(locals)
	} else {
		nr.InitLocalVars(nil)
	}

	// params are not initialised here: a call node injects them when it creates FunctionResult

	// Look up the entry node
	entryNode := findEntryNode(ctx.Flow, node.ID)
	if entryNode == nil {
		// Same as loop / process: a missing entry is not "optionally skipped".
		// Succeeding silently would hand call a "succeeded with no output" result, which is much harder
		// to trace than an outright error.
		return failf(nr, flow.ErrCodeConfig,
			fmt.Sprintf("function node %s: body entry node not found", node.ID))
	}
	//nr.AddLog("entry node: " + entryNode.ID)

	// Create a child process to run the child chain (function children live in the sub-flow, isolating
	// the sub-flow variables and distinguishing the debug stack level)
	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, "create function process: "+err.Error())
	}

	err = execute(entryNode, ctx, childPID, 0)
	if err != nil {

		if errors.Is(err, SignalReturn) {
			nr.Finish(flow.StatusSuccess, "", "")
			nr.AddLog("received the return signal, the function returns normally")
			return nil
		}

		if errors.Is(err, SignalExit) {
			nr.Finish(flow.StatusSuccess, "", "")
			nr.AddLog("received the exit signal, exiting the whole flow")
			return err // propagate SignalExit upwards
		}

		// break/continue only make sense inside a loop. Safety net: the node itself already refuses to fire when no loop can
		// consume it (control.go checkSignalScope), so a signal arriving here means it bypassed that check - keep a
		// classified error rather than a bare string.
		if errors.Is(err, SignalBreak) || errors.Is(err, SignalContinue) {
			return failf(nr, flow.ErrCodeConfig,
				fmt.Sprintf("function node %s (%s) received a break / continue signal: a function is an isolation scope, so the loop has to be declared inside its body",
					node.ID, node.Label()))
		}

		// 2. stdCtx cancelled (timeout / interruption): mark this node cancelled and unwind layer by layer
		if ctx.StdCtx.Err() != nil {
			errCode := flow.ErrCodeTimeout // context.DeadlineExceeded -> timeout
			if ctx.StdCtx.Err() == context.Canceled {
				errCode = flow.ErrCodeCancel // the user cancelled it
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
