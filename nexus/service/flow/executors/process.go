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

	// Look up the entry node
	entryNode := findEntryNode(ctx.Flow, node.ID)
	if entryNode == nil {
		// Same as loop: a missing entry is not "optionally skipped", it means the whole sub-flow never
		// ran. Succeeding silently would make the flow look complete while a large part of it is missing.
		return failf(nr, flow.ErrCodeConfig,
			fmt.Sprintf("process node %s: body entry node not found", node.ID))
	}
	nr.AddLog("entry node: " + entryNode.ID)

	// Create a child process, isolating the sub-flow variables and distinguishing the debug stack level
	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, err.Error())
	}
	nr.AddLog("child processID: " + childPID)

	// Execute the child node (passing 0 for depth is fine; the real depth comes from the debug stack's
	// CurrentDepth)
	err = execute(entryNode, ctx, childPID, 0)
	if err != nil {

		if isControlSignal(err) {
			nr.Finish(flow.StatusSuccess, "", "")
			nr.AddLog("received control signal " + err.Error() + ", the process finished normally")
			return err
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

		// 3. Other errors
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
		ErrorCodes: []string{}, // empty: child-chain error codes are passed through, not validated
		Actions:    []flow.ErrorAction{},
	}
}

// findEntryNode finds the entry node in flow.Nodes whose parentNode == parentID and type == "entry"
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
