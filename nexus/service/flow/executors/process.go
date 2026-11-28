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

	// Execute the body under its scope, then join on every exit path: the end of a body is the join, whether the body
	// succeeded, failed, was cancelled or received a control signal.
	scope, bodyErr := runBody(node, ctx, childPID, entryNode)
	joinErr := finishBody(nr, scope)
	err = bodyErr
	if joinErr != nil && errors.Is(joinErr, SignalExit) {
		// A member's exit outranks a failure that happened in the same instant: stopping the whole run is the author's
		// decision, and every node keeps its own outcome either way (nothing here rewrites a failed record).
		err = joinErr
	} else if err == nil && joinErr != nil {
		// A member failed and the body's own chain did not: the join's aggregate code becomes the host's error, so a
		// member failure is visible without any wait node.
		err = joinErr
	}
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
		return failf(nr, code, nodeMessage(err))
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *ProcessExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{}, // empty: child-chain error codes are passed through, not validated
		// skip: the scope is optional and a failed member must not stop the main chain; handler: the script may rewrite the
		// error or turn it into a skip. No retry: re-running the whole body repeats every member's side
		// effects, which is never what "retry the failed member" means.
		Actions: []flow.ErrorAction{flow.ErrorActionSkip, flow.ErrorActionHandler},
		// post: the one way a skipped host can still leave a marker behind (ok = false), because a handler cannot write
		// outputs and the host's own outputs are otherwise whatever its members wrote before the failure.
		HasPost: true,
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
