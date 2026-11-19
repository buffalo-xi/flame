package executors

import (
	"flame/nexus/service/flow/core"
	"fmt"
)

var _ Executor = (*WaitExecutor)(nil)

type WaitExecutor struct{}

func (e *WaitExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	nr.WaitResult = &flow.WaitResult{}

	// Read the id of the fork node this node waits for
	forkNodeID := ""
	if node.Data != nil && node.Data.Config != nil {
		if val, ok := node.Data.Config["fork_node_id"].(string); ok {
			forkNodeID = val
		}
	}

	if forkNodeID == "" {
		return failf(nr, flow.ErrCodeConfig, "fork_node_id not configured")
	}

	// Look up the fork node's NodeResult
	forkNR := ctx.FindNodeResultByProcessChain(forkNodeID, processID)
	if forkNR == nil || forkNR.ForkResult == nil {
		// fork_node_id points at the wrong place (a fork that does not exist, or one that is not on
		// this chain): a graph configuration error, the same class as "not configured" above — reporting
		// internal here would send people off to investigate the platform.
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("fork node result not found: %s", forkNodeID))
	}

	// Read the timeout from this node's config (the same rule as other nodes: off means no node-level
	// timeout)
	timeout := -1
	if node.Data != nil && node.Data.Config != nil {
		timeout = parseTimeoutConfig(node.Data.Config["timeout"])
	}
	timeout = ctx.ResolveTimeout(timeout)

	nr.WaitResult.ForkNodeID = forkNodeID
	nr.AddLogf("waiting for concurrent group %s, timeout %ds", forkNodeID, timeout)
	completed, err := forkNR.ForkResult.WaitTimeoutCtx(ctx.StdCtx, timeout)
	if err != nil {
		// ctx cancelled or timed out: let the caller handle it as a cancellation rather than a wait node
		// failure
		if ctxErr := ctx.StdCtx.Err(); ctxErr != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), ctxErr.Error())
			return ctxErr
		}
		// A state error: the fork has already been waited on (a duplicate wait)
		nr.AddLog(fmt.Sprintf("wait failed: %v", err))
		// Waiting on the same fork twice is a graph structure error (not a platform fault)
		return failf(nr, flow.ErrCodeConfig, err.Error())
	}
	if completed {
		nr.AddLog("wait finished: every concurrent task completed")
		// Scan the statuses of the fork group members: any failure fails the wait (fail-fast)
		// Note: it scans node statuses, not output variables, so it does not violate the "do not
		// reference concurrent branch results after wait" rule
		failed := scanForkMembersFailed(ctx, forkNR.ForkResult)
		if failed != "" {
			nr.AddLog("concurrent branch failed: " + failed)
			return failf(nr, flow.ErrCodeOther, "concurrent branch failed: "+failed)
		}
	} else {
		// A real timeout: some concurrent tasks did not finish, leaving skip/retry/terminate to the
		// onError rules
		nr.AddLog("wait timed out: some concurrent tasks did not finish")
		return failf(nr, flow.ErrCodeTimeout, "wait timed out: some concurrent tasks did not finish")
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// scanForkMembersFailed scans the node statuses of the fork group members and returns a description of
// the first failing member (an empty string means no failure)
func scanForkMembersFailed(ctx *flow.ExecContext, fr *flow.ForkResult) string {
	errMsg := ""
	for _, m := range fr.Members() {
		key := m.ProcessID + "_" + m.NodeID
		memberNR, ok := ctx.ExecutedNodeMap.Get(key)
		if !ok || memberNR == nil {
			continue
		}
		if memberNR.Status == flow.StatusFailed {
			errMsg = errMsg + "\n" + fmt.Sprintf("node failed %s (process %s): %s", m.NodeID, m.ProcessID, memberNR.ErrMsg)
		}
		if memberNR.Status == flow.StatusCancelled {
			errMsg = errMsg + "\n" + fmt.Sprintf("node canceled %s (process %s): %s", m.NodeID, m.ProcessID, memberNR.ErrMsg)
		}
	}
	return errMsg
}

func (e *WaitExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		// config: fork_node_id missing or wrong, and the same fork waited on twice, are all graph
		// configuration errors
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeTimeout, flow.ErrCodeOther},
		Actions:    []flow.ErrorAction{flow.ErrorActionSkip},
		HasPost:    false,
	}
}

func NewWaitExecutor() *WaitExecutor {
	return &WaitExecutor{}
}

func init() {
	RegisterExecutor("wait", NewWaitExecutor())
}
