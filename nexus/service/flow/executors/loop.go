package executors

import (
	"context"
	"errors"
	"flame/nexus/service/flow/core"
	"fmt"
)

var _ Executor = (*LoopExecutor)(nil)

type LoopExecutor struct{}

func (e *LoopExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	nr.LoopResult = &flow.LoopResult{}

	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// Initialise the locals
	if locals, ok := node.Data.Config["locals"].(map[string]any); ok {
		nr.InitLocalVars(locals)
	}

	// Initialise the outputs
	if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	// Initialise the loop variables
	if loopVars, ok := node.Data.Config["loop_vars"].(map[string]any); ok {
		nr.LoopResult.LoopVars = flow.InitLoopVars(loopVars)
	}

	// Resolve inputs uniformly (top level of the config) into NodeResult.InputVars. count / each keep this snapshot for
	// the whole loop (an iterated array must not change under its own iteration); while / do_while re-resolve it before
	// every condition - see evaluateCondition.
	inputsCfg, _ := node.Data.Config["inputs"].(map[string]interface{})
	if inputsCfg != nil {
		inputVars, err := ctx.GetVariables(inputsCfg, processID)
		if err != nil {
			return failf(nr, flow.ErrCodeConfig, err.Error())
		}
		nr.SetInputVars(inputVars)
	}

	lc, _ := node.Data.Config["loop"].(map[string]interface{})
	if lc == nil {
		return failf(nr, flow.ErrCodeConfig, "loop config not found")
	}

	loopType, _ := lc["type"].(string)
	nr.LoopResult.LoopConfig = lc

	switch loopType {
	case "count":
		totalTimes, set, terr := parseCountInt(lc, "total_times")
		if terr != nil {
			return failf(nr, flow.ErrCodeConfig, terr.Error())
		}
		if set && totalTimes <= 0 {
			// An explicit 0 means "no round": falling back to the system ceiling would turn "do not run" into ten
			// thousand executions. Only a missing key keeps the documented system-ceiling default.
			nr.AddLogf("total_times is %d, the loop runs no round", totalTimes)
			nr.Finish(flow.StatusSuccess, "", "")
			return nil
		}
		return e.executeLoopCount(node, ctx, processID, nr, totalTimes)

	case "each":
		breakOnEmpty, _ := lc["break_on_empty"].(bool)
		sourceKey, _ := lc["source_key"].(string)
		return e.executeLoopEach(node, ctx, processID, nr, sourceKey, breakOnEmpty)

	case "while":
		return e.executeLoopWhile(node, ctx, processID, nr, lc)

	case "do_while":
		return e.executeLoopDoWhile(node, ctx, processID, nr, lc)

	default:
		return failf(nr, flow.ErrCodeConfig, "unknown loop type: "+loopType)
	}
}

func (e *LoopExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: []string{},
		// skip / handler, no retry: the same trade-off as process - a retried body would repeat every member's side effects.
		Actions: []flow.ErrorAction{flow.ErrorActionSkip, flow.ErrorActionHandler},
		// iterations is the readable round count (NodeResult.GetVariable resolves it); a loop declares no other output, so
		// this is the one name a downstream step can take from the loop node itself.
		FixedOutputs: []string{"iterations"},
		// post: the same as process - a skipped loop can leave a marker (ok = false) that a handler cannot write.
		HasPost: true,
	}
}

// ========== the four loop kinds ==========

func (e *LoopExecutor) executeLoopCount(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, totalTimes int) error {

	totalTimes = resolveMaxTimes(ctx, totalTimes)
	for i := 0; i < totalTimes; i++ {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		nr.LoopResult.IncIteration()
		// count publishes index (0-based round) and total (how many rounds are planned): "round 3 of 10" needs both, and a
		// single number cannot express it.
		nr.LoopResult.SetLoopVar("index", i)
		nr.LoopResult.SetLoopVar("total", totalTimes)

		err := runIteration(node, ctx, processID, nr)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *LoopExecutor) executeLoopEach(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, sourceKey string, breakOnEmpty bool) error {

	// Take the array to iterate from the shared inputs by source_key
	inputVars := nr.GetInputVars()
	arrayData, exists := inputVars[sourceKey]
	if !exists {
		return failf(nr, flow.ErrCodeConfig, "the each source is not in inputs: "+sourceKey)
	}

	items := toSlice(arrayData)
	if items == nil && arrayData != nil {
		return failf(nr, flow.ErrCodeConfig, "the each source is not an array: "+sourceKey)
	}
	if sysMax := ctx.ResolveMaxLoopTimes(); sysMax > 0 && len(items) > sysMax {
		nr.AddLogf("array length %d exceeds the system limit %d, truncated", len(items), sysMax)
		items = items[:sysMax]
	}
	if breakOnEmpty && len(items) == 0 {
		nr.AddLog("the data source is empty, skipping the loop")
		nr.Finish(flow.StatusSuccess, "", "")
		return nil
	}

	for i, item := range items {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		if breakOnEmpty && item == nil {
			nr.AddLogf("item %d is empty, aborting the loop", i)
			break
		}

		nr.LoopResult.IncIteration()
		nr.LoopResult.SetLoopVar("index", i)
		nr.LoopResult.SetLoopVar("item", item)
		nr.LoopResult.SetLoopVar("total", len(items))

		err := runIteration(node, ctx, processID, nr)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *LoopExecutor) executeLoopWhile(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, lc map[string]interface{}) error {

	inputsCfg, _ := node.Data.Config["inputs"].(map[string]interface{})
	maxTimes, _, terr := parseCountInt(lc, "max_times")
	if terr != nil {
		return failf(nr, flow.ErrCodeConfig, terr.Error())
	}
	maxTimes = resolveMaxTimes(ctx, maxTimes)

	for i := 0; i < maxTimes; i++ {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		// Check the condition first. index is written before the check, because the condition is the only reader of a loop
		// variable in this mode: "index < 4" must mean "four more rounds", so it sees the round about to run.
		nr.LoopResult.SetLoopVar("index", i)

		ok, errScript := evaluateCondition(ctx, nr, lc, inputsCfg, processID)
		if errScript != nil {
			nr.Finish(flow.StatusFailed, flow.ErrCodeScript, "failed to run the condition script: "+errScript.Error())
			return errScript
		}
		if !ok {
			// i rounds have run by now: this check happens before round i, so the count is i, not i+1.
			nr.AddLogf("condition not met, loop finished after %d round(s)", i)
			break
		}

		nr.LoopResult.IncIteration()
		err := runIteration(node, ctx, processID, nr)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			continue
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *LoopExecutor) executeLoopDoWhile(node *flow.Node, ctx *flow.ExecContext,
	processID string, nr *flow.NodeResult, lc map[string]interface{}) error {

	inputsCfg, _ := node.Data.Config["inputs"].(map[string]interface{})
	maxTimes, _, terr := parseCountInt(lc, "max_times")
	if terr != nil {
		return failf(nr, flow.ErrCodeConfig, terr.Error())
	}
	maxTimes = resolveMaxTimes(ctx, maxTimes)

	for i := 0; i < maxTimes; i++ {
		if err := ctx.StdCtx.Err(); err != nil {
			nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), err.Error())
			return err
		}

		// Run first (at least one round). index is the round being run, and the condition afterwards sees that same number,
		// so "index < 2" on a do_while yields three rounds (0, 1, 2) - the at-least-one semantics of this mode.
		nr.LoopResult.SetLoopVar("index", i)
		nr.LoopResult.IncIteration()
		err := runIteration(node, ctx, processID, nr)
		if err != nil {
			stop, retErr := handleIterationError(ctx, nr, i, err)
			if retErr != nil {
				return retErr
			}
			if stop {
				break
			}
			// Only a continue gets here, and it must fall through to the check below: the signal ends this round, not the
			// exit test. Skipping it would let the loop run to max_times and still report success - "continue" would read
			// as "ignore the condition".
		}

		// Then check the condition
		ok, errScript := evaluateCondition(ctx, nr, lc, inputsCfg, processID)
		if errScript != nil {
			nr.Finish(flow.StatusFailed, flow.ErrCodeScript, "failed to run the condition script: "+errScript.Error())
			return errScript
		}
		if !ok {
			nr.AddLogf("condition not met, loop finished after %d round(s)", i+1)
			break
		}
	}

	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

// ========== helpers ==========

// handleIterationError handles one iteration's error, translating control signals into loop semantics.
//
// All four loop kinds (count / each / while / do-while) share it: the control signal rules must agree,
// otherwise break / return behave differently depending on the loop.
//   - stop=true    the loop should end (break / return / exit / cancellation / failure)
//   - retErr!=nil  the caller returns that error directly
func handleIterationError(ctx *flow.ExecContext, nr *flow.NodeResult, i int, err error) (stop bool, retErr error) {
	if errors.Is(err, SignalBreak) {
		nr.AddLogf("break on round %d", i)
		return true, nil
	}
	if errors.Is(err, SignalContinue) {
		nr.AddLogf("continue on round %d", i)
		return false, nil
	}
	if errors.Is(err, SignalReturn) {
		nr.Finish(flow.StatusSuccess, "", "")
		return true, err
	}
	if errors.Is(err, SignalExit) {
		nr.AddLogf("exit on round %d, terminating the loop", i)
		nr.Finish(flow.StatusSuccess, "", "")
		return true, err
	}
	if ctx.StdCtx.Err() != nil {
		nr.Finish(flow.StatusCancelled, getCancelErrCode(ctx), ctx.StdCtx.Err().Error())
		return true, ctx.StdCtx.Err()
	}
	return true, failIteration(nr, i, err)
}

// runIteration runs one iteration: create a child process, execute the child node chain under that iteration's scope, and
// join it. A loop body is a scope owner, so a `concurrent` member inside it is dispatched and collected per round.
func runIteration(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	childPID, err := ctx.ProcessManager.NewProcess(processID, nil)
	if err != nil {
		return newExecError(flow.ErrCodeInternal, "create iteration process: "+err.Error())
	}

	entryNode := findEntryNode(ctx.Flow, node.ID)
	if entryNode == nil {
		// Skipping silently would spin the loop N rounds and still report success, with no clue in the
		// logs. A missing loop body entry is a flow structure error and has to surface at runtime.
		return newExecError(flow.ErrCodeConfig,
			fmt.Sprintf("loop node %s: body entry node not found", node.ID))
	}

	scope, bodyErr := runBody(node, ctx, childPID, entryNode)
	joinErr := finishLoopBody(nr, scope)
	if joinErr != nil && errors.Is(joinErr, SignalExit) {
		// Same precedence as a process host: a member's exit ends the run, even if the chain or a sibling failed in the
		// same instant. handleIterationError turns it into "loop succeeded, signal upwards".
		return joinErr
	}
	if bodyErr != nil {
		// The chain's own failure wins over a member failure, the same precedence a process host applies.
		return bodyErr
	}
	return joinErr
}

// conditionInputs assembles what a while / do_while condition script can read.
//
// config.inputs is re-resolved on every call instead of reading the startup snapshot: a condition such as "total < 3"
// reads a value the body writes back into the loop's outputs, and a frozen snapshot would never move (the loop would
// either stop before the first round or run to max_times). The snapshot itself is left alone, so the run record still
// shows what was handed in when the node started.
//
// Merge order, later wins: resolved inputs -> loop vars -> the loop's own outputs / locals (a body writes them through
// targets, so a condition can read a name it never declared). Outputs before loop vars matches NodeResult.GetVariable, so
// "input.index" means the same thing in a condition and inside the body.
func conditionInputs(ctx *flow.ExecContext, nr *flow.NodeResult,
	inputsCfg map[string]interface{}, processID string) (map[string]interface{}, error) {

	inputs := make(map[string]interface{})
	if inputsCfg != nil {
		resolved, err := ctx.GetVariables(inputsCfg, processID)
		if err != nil {
			return nil, newExecError(flow.ErrCodeConfig, err.Error())
		}
		for k, v := range resolved {
			inputs[k] = v
		}
	} else if src := nr.GetInputVars(); src != nil {
		for k, v := range src {
			inputs[k] = v
		}
	}

	if nr.LoopResult != nil && nr.LoopResult.LoopVars != nil {
		for k, v := range nr.LoopResult.LoopVars {
			inputs[k] = v
		}
	}
	for k, v := range nr.OutputVars {
		inputs[k] = v
	}
	for k, v := range nr.LocalVars {
		inputs[k] = v
	}
	return inputs, nil
}

// evaluateCondition evaluates a while/do_while condition script. What it can read is assembled by conditionInputs,
// which re-resolves config.inputs on every round.
func evaluateCondition(ctx *flow.ExecContext,
	nr *flow.NodeResult, lc map[string]interface{},
	inputsCfg map[string]interface{}, processID string) (bool, error) {

	lang, _ := lc["lang"].(string)
	if lang == "" {
		lang = "lua"
	}
	// The condition script timeout follows the same rule as code/branch: config.loop.timeout, with no
	// node-level timeout when it is off.
	timeout := ctx.ResolveScriptTimeout(lang, parseTimeoutConfig(lc["timeout"]))
	condition, _ := lc["condition"].(string)

	inputs, err := conditionInputs(ctx, nr, inputsCfg, processID)
	if err != nil {
		return false, err
	}

	result, err := ExecuteScript(ctx.StdCtx, lang, condition, inputs, nr, timeout)
	if err != nil {
		nr.AddLogf("condition evaluation failed: %v", err)
		// Classified as script rather than internal: a wrong condition script is something the user can
		// fix, while internal is aborted outright by shouldBypassOnError and an on_error keyed on it would
		// never match.
		return false, newExecError(flow.ErrCodeScript, "evaluate condition: "+err.Error())
	}

	b, ok := result.(bool)
	return ok && b, nil
}

// parseCountInt parses an integer field of the loop config (count's total_times, while / do_while's
// max_times) and reports whether the field was set at all.
//
// The two facts must not be conflated: an absent key means "take the system ceiling" (the documented
// semantics), while an explicit 0 is a real value - a count loop with total_times 0 runs no round.
// And a **value that is not a number** must be an error: silently treating it as 0 means "the user
// thought they set 5 rounds, it actually ran the system ceiling of 10000" — one mistyped value
// amplified into ten thousand executions, far more expensive than a plain failure.
func parseCountInt(lc map[string]interface{}, key string) (n int, set bool, err error) {
	raw, ok := lc[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch v := raw.(type) {
	case float64:
		return int(v), true, nil
	case int:
		return v, true, nil
	case int64:
		return int(v), true, nil
	default:
		return 0, false, newExecError(flow.ErrCodeConfig,
			fmt.Sprintf("loop config %s must be a number, got %T", key, raw))
	}
}

func resolveMaxTimes(ctx *flow.ExecContext, maxTimes int) int {
	sysMax := ctx.ResolveMaxLoopTimes()
	if maxTimes <= 0 {
		return sysMax
	}
	if maxTimes > sysMax {
		return sysMax
	}
	return maxTimes
}

func failIteration(nr *flow.NodeResult, iteration int, err error) error {
	code := flow.ErrCodeOther
	var ee *ExecError
	if errors.As(err, &ee) {
		code = ee.Code
	}
	return failf(nr, code, fmt.Sprintf("iteration %d failed: %s", iteration, nodeMessage(err)))
}

func toSlice(v interface{}) []interface{} {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []interface{}:
		return arr
	case []string:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	case []float64:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	case []bool:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	case []map[string]interface{}:
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = v
		}
		return result
	default:
		return nil
	}
}

// getCancelErrCode returns the error code matching ctx.StdCtx.Err()
func getCancelErrCode(ctx *flow.ExecContext) string {
	if ctx.StdCtx.Err() == context.Canceled {
		return flow.ErrCodeCancel
	}
	return flow.ErrCodeTimeout
}

func NewLoopExecutor() *LoopExecutor {
	return &LoopExecutor{}
}

func init() {
	RegisterExecutor("loop", NewLoopExecutor())
}
