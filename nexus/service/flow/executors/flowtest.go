package executors

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/tools"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/callchain"
	flows "flame/nexus/shared/repository/flows"

	"github.com/google/uuid"
)

// init hands "trial-run a flow by id" to the agent package, for the run_flow tool.
// The reasoning is the same as the registration point in localendpoint.go: it breaks the one-way
// executors -> agent dependency.
func init() { agent.SetFlowTestRunner(runFlowForTest) }

// runFlowForTest serves the run_flow tool's trial run of a workflow.
//
// It differs from runFlowEndpoint in only two ways: it does not require the endpoint to exist (the object
// of a trial run usually is not exposed yet), and it returns per-node outcomes instead of just a piece of
// text. The status rules still match a real run (a deprecated flow cannot run; flowsManager.RunTest
// intercepts that).
//
// It returns only the node outcomes and the logs of failing nodes, **not the node output variables**: an
// HTTP body or an LLM answer is tens of KB, which costs tokens in the model context and is barely readable;
// data belongs in the run records.
func runFlowForTest(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	flowID string,
	args map[string]any,
	sessionID string,
	callDepth int,
	followDrafts bool,
) (*tools.FlowRunResult, error) {
	instance, err := flows.NewRepository().GetFlow(authInfo, flowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get the flow: %w", err)
	}
	if instance == nil {
		return nil, fmt.Errorf("flow not found: %s", flowID)
	}

	// The arguments are copied and two reserved keys injected (NewFlow strips them, so business arguments
	// are not polluted):
	//   _call_depth  depth +1, which is how an agent node inside the flow that calls back into an agent
	//                converges (the same rule as runEndpoint)
	//   session_id   the same session as the caller, so artefacts land in the same users/{uid}/sessions/{sid}/
	input := make(map[string]any, len(args)+2)
	for k, v := range args {
		input[k] = v
	}
	input["_call_depth"] = callDepth + 1
	// The chain arrives through ctx (callchain.With in the run_flow tool): the flow being tried out is
	// itself an "entity about to execute", so it appends itself to the chain before passing it on —
	// otherwise, when an rpc / agent node inside it loops back to itself, the first inbound layer does not
	// see F and only a whole wasted round stops it.
	input[callchain.ArgumentKey] = callchain.Append(
		callchain.From(ctx),
		callchain.Key(callchain.KindFlow, instance.ID),
	)
	if sessionID != "" {
		input["session_id"] = sessionID
	}

	runID := uuid.Must(uuid.NewV7()).String()
	start := time.Now()
	execCtx, err := NewFlow(instance, input, "", runID, authInfo.Schema, authInfo.ID, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialise the flow run: %w", err)
	}
	// A trial run is debugging: this run reads the current working copy. The sub-flows it starts follow
	// drafts by default too — otherwise "change A, change B, look at both" would need B released first
	// (release means going live, see the comment on releaseFlowVersionTx). The caller may pass false to run
	// the sub-flows on their released versions, to verify the production chain.
	execCtx.DebugScope = flow.NewDebugScope(followDrafts)

	// Start runs to completion synchronously and stores the run record (run_type lands in task / call, like
	// any other direct call)
	runErr := Start(execCtx)

	res := &tools.FlowRunResult{
		FlowID:     execCtx.GetflowId(),
		FlowName:   execCtx.GetFlowName(),
		RunID:      runID,
		Status:     "success",
		Result:     execCtx.GetResult(),
		DurationMs: time.Since(start).Milliseconds(),
		Nodes:      collectNodeTraces(execCtx),
	}
	switch {
	case ctx.Err() != nil:
		// The tool call timed out (60s by default) or the caller cancelled: the flow was interrupted and the
		// node list stops at the interruption point. This case must come first: on cancellation GetError is
		// often set to the context error as well, and reporting failed would suggest the flow's own logic is
		// broken.
		res.Status = "cancelled"
		res.ErrMsg = ctx.Err().Error()
	case execCtx.GetError() != "":
		res.Status = "failed"
		res.ErrMsg = execCtx.GetError()
	case runErr != nil:
		res.Status = "failed"
		res.ErrMsg = runErr.Error()
	}
	res.ErrCode = firstFailureCode(res.Nodes)
	return res, nil
}

// collectNodeTraces takes the executed nodes, sorted by start time (execution order).
//
// Successful nodes are in the list too: on failure the most useful information is "the first steps ran, it
// stopped here". Logs are only given for non-successful nodes — a successful node's logs are mostly noise
// such as "executed successfully".
func collectNodeTraces(execCtx *flow.ExecContext) []tools.FlowNodeTrace {
	if execCtx == nil || execCtx.ExecutedNodeMap == nil {
		// An empty array rather than nil: null in JSON reads as "no idea what ran" to the model
		return []tools.FlowNodeTrace{}
	}
	// Range calls back while holding the lock, so the sort must happen outside it
	results := make([]*flow.NodeResult, 0, execCtx.ExecutedNodeMap.Len())
	execCtx.ExecutedNodeMap.Range(func(_ string, nr *flow.NodeResult) bool {
		if nr != nil {
			results = append(results, nr)
		}
		return true
	})
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].StartTime.Before(results[j].StartTime)
	})

	out := make([]tools.FlowNodeTrace, 0, len(results))
	for _, nr := range results {
		tr := tools.FlowNodeTrace{
			NodeID:     nr.NodeID,
			Type:       string(nr.NodeType),
			Status:     string(nr.Status),
			ErrCode:    nr.ErrCode,
			ErrMsg:     nr.ErrMsg,
			DurationMs: nr.DurationMs,
			// A failing post script does not change the node's main status, but it is a failure reason too,
			// so it is always included regardless of status
			PostError:  nr.PostError,
			RetryCount: nr.RetryCount,
		}
		// Only non-successful nodes carry logs and a stack: a successful node's logs are mostly noise such
		// as "executed successfully"
		if nr.Status != flow.StatusSuccess {
			if len(nr.RunLogs) > 0 {
				tr.Logs = strings.Join(nr.RunLogs, "\n")
			}
			tr.ErrStack = nr.ErrStack
		}
		out = append(out, tr)
	}
	return out
}

// firstFailureCode returns the error code of the earliest failing node: a flow has no error code field of
// its own, and that code is both what on_error matches and where troubleshooting starts.
func firstFailureCode(nodes []tools.FlowNodeTrace) string {
	for _, n := range nodes {
		if n.Status == string(flow.StatusFailed) && n.ErrCode != "" {
			return n.ErrCode
		}
	}
	return ""
}
