package executors

import (
	"context"
	"errors"
	"flame/nexus/service/flow/core"
	"fmt"
	"strings"
)

var _ Executor = (*BranchExecutor)(nil)

type BranchExecutor struct{}

// branchConfig is the config of one branch
type branchConfig struct {
	id        string // unique id, matching childId in an edge's source_handle
	label     string // display name (for logs, not guaranteed unique)
	condition string
	exclusive bool // exclusive: once it matches and completes, further branches and the default are not tried
}

func (e *BranchExecutor) Execute(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult) error {

	nr.BranchResult = &flow.BranchResult{}

	// ── 1. Parse the config ──
	config := node.Data.Config
	if config == nil {
		return failf(nr, flow.ErrCodeConfig, "branch node is missing its configuration")
	}

	// The script-related config lives in one config.branch block: {lang, timeout}
	lang := "lua"
	timeout := -1
	if branchCfg, ok := config["branch"].(map[string]interface{}); ok {
		if s, _ := branchCfg["lang"].(string); s != "" {
			lang = s
		}
		timeout = parseTimeoutConfig(branchCfg["timeout"])
	}
	timeout = ctx.ResolveScriptTimeout(lang, timeout)

	children, ok := config["children"].([]interface{})
	if !ok || len(children) == 0 {
		return failf(nr, flow.ErrCodeConfig, "branch node configures no branch")
	}

	branches, err := parseBranchChildren(children)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, "failed to parse the branch config: "+err.Error())
	}

	// Read the input variables (top level of the config, shared by every branch)
	inputVars := make(map[string]interface{})
	if inputsConfig, ok := config["inputs"].(map[string]interface{}); ok {
		vars, err := ctx.GetVariables(inputsConfig, processID)
		if err != nil {
			// One unresolvable row fails the whole batch. Carrying on with an empty map turns the real cause ("variable
			// source is not selected" / "unsupported source type" / "node result not found") into a condition error on
			// line 1 ("attempt to perform arithmetic on a nil value"); every other executor fails here.
			return failf(nr, flow.ErrCodeConfig, "failed to read the input variables: "+err.Error())
		}
		inputVars = vars
	}
	nr.SetInputVars(inputVars)

	nr.AddLogf("%d plain branch(es) in total", len(branches))

	// ── 2. Walk the plain branches ──
	for _, branch := range branches {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		if branch.condition == "" {
			nr.AddLogf("branch %s(%s) has no condition script, skipped", branch.label, branch.id)
			continue
		}

		matched, evalErr := evaluateBranchCondition(ctx, nr, branch, lang, timeout, inputVars)
		if evalErr != nil {
			nr.AddLogf("branch %s(%s) condition failed: %v", branch.label, branch.id, evalErr)
			return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("branch %s(%s) condition failed: %v", branch.label, branch.id, evalErr))
		}
		if !matched {
			nr.AddLogf("branch %s(%s) condition not met", branch.label, branch.id)
			continue
		}

		// The condition matched, run the branch chain
		result := e.executeBranch(node, ctx, processID, nr, branch.id, branch.label)
		if result != nil {
			return result
		}

		// An exclusive branch: once it matched and completed, no further branch or default is tried and
		// the branch node finishes
		if branch.exclusive {
			nr.AddLogf("branch %s(%s) matched and completed (exclusive), no further branches are tried", branch.label, branch.id)
			nr.Finish(flow.StatusSuccess, "", "")
			return nil
		}

		nr.AddLogf("branch %s(%s) completed normally, checking the next branch", branch.label, branch.id)
	}

	// ── 3. The default branch ──
	if err := ctx.StdCtx.Err(); err != nil {
		nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
		return err
	}

	defaultEdge := findBranchEdge(node, node.ID)
	if defaultEdge != nil {
		nr.AddLog("running the default branch")
		result := e.executeBranch(node, ctx, processID, nr, node.ID, "default")
		if result != nil {
			return result
		}
		nr.AddLog("the default branch completed normally")
	} else {
		nr.AddLog("no outgoing edge from the default branch")
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// executeBranch runs one branch chain; nil means it completed normally, non-nil must be propagated
func (e *BranchExecutor) executeBranch(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, edgeKey string, label string) error {

	targetEdge := findBranchEdge(node, edgeKey)
	if targetEdge == nil {
		// Reached only for a *matched* child: both callers check the default edge before calling. A child whose condition
		// matched but that has no outgoing edge cannot run, and skipping it quietly would report success having done nothing -
		// the static check already calls this shape an error (branch_edge), so the run agrees with it.
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf(
			"branch %s matched but has no outgoing edge (source%s), so there is nothing to run", label, edgeKey))
	}

	targetNode := GetNodeByID(ctx.Flow, targetEdge.Target)
	if targetNode == nil {
		nr.AddLogf("branch %s points at a missing target node %s, returning an error", label, targetEdge.Target)
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("branch %s points at a missing target node %s, returning an error", label, targetEdge.Target))
	}

	// Record the executed branch id
	nr.BranchResult.AddExecutedBranch(edgeKey)

	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, fmt.Sprintf("failed to start the child run for branch %s: %v", label, err))
	}

	nr.AddLogf("running branch %s → node %s(%s)", label, targetNode.Data.Label, targetNode.ID)
	execErr := execute(targetNode, ctx, childPID, 0)

	// Completed normally
	if execErr == nil {
		return nil
	}

	// A control signal (break/continue/return/exit) -> the node succeeded, propagate it upwards
	if isControlSignal(execErr) {
		nr.AddLogf("branch %s returned control signal %s, node succeeded", label, execErr.Error())
		nr.Finish(flow.StatusSuccess, "", "")
		return execErr
	}

	// The context was cancelled
	if ctx.StdCtx.Err() != nil {
		errCode := flow.ErrCodeTimeout
		if ctx.StdCtx.Err() == context.Canceled {
			errCode = flow.ErrCodeCancel
		}
		nr.Finish(flow.StatusCancelled, errCode, ctx.StdCtx.Err().Error())
		return ctx.StdCtx.Err()
	}

	// A child-chain error -> the container node itself is not a failure (the condition evaluation /
	// intent classification already succeeded): record Success and propagate upwards, since the runner
	// bypasses a container node's on_error for any status other than Failed, and the error code travels
	// through ExecError layer by layer to the ancestor / top level.
	// (Errors that happen before execute — missing target node, failed child process creation — already
	//  finished as StatusFailed above, so they still belong to the container node and go through its
	//  on_error.)
	code := flow.ErrCodeOther
	var ee *ExecError
	if errors.As(execErr, &ee) {
		code = ee.Code
	}
	nr.AddLogf("branch %s child chain failed, propagating the error: %v", label, execErr)
	nr.Finish(flow.StatusSuccess, "", "")
	return newExecError(code, fmt.Sprintf("branch %s failed: %v", label, execErr))
}

// parseBranchChildren parses the children config into branchConfig values (plain branches only)
func parseBranchChildren(children []interface{}) ([]branchConfig, error) {
	branches := make([]branchConfig, 0, len(children))
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			continue
		}

		id, _ := childMap["id"].(string)
		if id == "" {
			continue
		}

		label, _ := childMap["label"].(string)
		if label == "" {
			label = id
		}

		condition, _ := childMap["condition"].(string)

		// Parse exclusive: default true (exclusive: the first match ends this node).
		// Only an explicit false / "false" / "0" / "no" makes it non-exclusive; anything else (missing
		// key, invalid value) counts as true.
		exclusive := true
		switch v := childMap["exclusive"].(type) {
		case bool:
			exclusive = v
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "false", "0", "no":
				exclusive = false
			}
		}

		branches = append(branches, branchConfig{
			id:        id,
			label:     label,
			condition: condition,
			exclusive: exclusive,
		})
	}

	if len(branches) == 0 {
		return nil, fmt.Errorf("no valid branch configuration")
	}

	return branches, nil
}

// evaluateBranchCondition evaluates a branch condition script and reports whether it matched
func evaluateBranchCondition(ctx *flow.ExecContext, nr *flow.NodeResult, branch branchConfig,
	lang string, timeout int, inputVars map[string]interface{}) (bool, error) {

	result, err := ExecuteScript(ctx.StdCtx, lang, branch.condition, inputVars, nr, timeout)
	if err != nil {
		return false, fmt.Errorf("failed to run the condition script: %v", err)
	}

	return isConditionTrue(result), nil
}

// findBranchEdge returns the outgoing edge of one sub-branch (when branchID is a child id) or of the default branch (when it
// is the node's own id). The owner is resolved exactly, not by prefix: with children "cA" and "cA2", a prefix match sent cA
// down cA2's chain whenever cA2's edge happened to come first in OutEdges.
func findBranchEdge(node *flow.Node, branchID string) *flow.Edge {
	for _, edge := range node.OutEdges {
		if owner, ok := handleOwner(edge.SourceHandle); ok && owner == branchID {
			return edge
		}
	}
	return nil
}

// handleOwner resolves the id a source_handle names: the handle is source<id><left|right|top|bottom>, so exactly one position
// suffix is stripped and the rest is the owner - the same rule flowcheck (handleOwner there) and the authoring tools
// (splitHandle) apply, otherwise the check and the run disagree about which outlet an edge belongs to.
//
// A handle without any position suffix keeps the whole remainder as the owner, so a hand-written diagram still resolves.
func handleOwner(handle string) (string, bool) {
	body, ok := strings.CutPrefix(handle, "source")
	if !ok || body == "" {
		return "", false
	}
	for _, pos := range []string{"left", "right", "top", "bottom"} {
		if owner, cut := strings.CutSuffix(body, pos); cut && owner != "" {
			return owner, true
		}
	}
	return body, true
}

// isConditionTrue reports whether the condition script returned true
func isConditionTrue(result interface{}) bool {
	if result == nil {
		return false
	}

	switch v := result.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "True" || v == "TRUE"
	case int, int8, int16, int32, int64:
		return v != 0
	case float64:
		return v != 0
	case float32:
		return v != 0
	default:
		return false
	}
}

func (e *BranchExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{flow.ErrCodeConfig, flow.ErrCodeScript},
		Actions:    []flow.ErrorAction{},
	}
}

func NewBranchExecutor() *BranchExecutor {
	return &BranchExecutor{}
}

func init() {
	RegisterExecutor("branch", NewBranchExecutor())
}
