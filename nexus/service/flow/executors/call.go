package executors

import (
	"fmt"

	"flame/nexus/service/flow/core"
)

var _ Executor = (*CallExecutor)(nil)

type CallExecutor struct{}

// Execute runs a call node: resolve the arguments -> create a child process -> pre-inject the arguments
// into FunctionResult -> reuse FunctionExecutor to run the target function's child chain in the
// sub-flow -> mirror the outputs and status.
func (e *CallExecutor) Execute(node *flow.Node, ctx *flow.ExecContext, processID string, nr *flow.NodeResult) error {
	// 1. Validate the config
	if node.Data == nil || node.Data.Config == nil {
		return failf(nr, flow.ErrCodeConfig, "node data or config is nil")
	}

	// Initialise the output variables
	if outputs, ok := node.Data.Config["outputs"].(map[string]any); ok {
		nr.InitOutputVars(outputs)
		nr.InitOutputBindings(outputs)
	}

	targetID, _ := node.Data.Config["target"].(string)
	if targetID == "" {
		return failf(nr, flow.ErrCodeConfig, "call node missing config.target")
	}

	// 2. Find the target function node
	funcNode := GetNodeByID(ctx.Flow, targetID)
	if funcNode == nil {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("target function node %s not found", targetID))
	}
	if funcNode.Type != "function" {
		return failf(nr, flow.ErrCodeConfig, fmt.Sprintf("target node %s is not a function node", targetID))
	}

	// 3. Check the arguments against the target function's parameter declarations (an extra or a missing
	// required argument is reported here)
	argsConfig, _ := node.Data.Config["args"].(map[string]interface{})
	var paramsConfig map[string]interface{}
	if funcNode.Data != nil {
		paramsConfig, _ = funcNode.Data.Config["params"].(map[string]interface{})
	}
	for name, rawDef := range paramsConfig {
		def, _ := rawDef.(map[string]interface{})
		required := true
		if v, ok := def["required"].(bool); ok {
			required = v
		}
		if _, ok := argsConfig[name]; !ok && required {
			return failf(nr, flow.ErrCodeConfig,
				fmt.Sprintf("missing required arg %q for function %s", name, targetID))
		}
	}
	for name := range argsConfig {
		if _, ok := paramsConfig[name]; !ok {
			return failf(nr, flow.ErrCodeConfig,
				fmt.Sprintf("unknown arg %q for function %s", name, targetID))
		}
	}

	// 4. Resolve the arguments (resolve the variable sources in the parent flow context, then inject into
	// the sub-flow)
	paramValues, err := ctx.GetVariables(argsConfig, processID)
	if err != nil {
		return failf(nr, flow.ErrCodeConfig, "resolve args failed: "+err.Error())
	}

	// 5. Check the recursion depth (before creating the child process)
	maxDepth := ctx.ResolveRecursionDepth()
	if maxDepth > 0 {
		currentDepth := ctx.ProcessManager.GetCallDepth(processID)
		if currentDepth >= maxDepth {
			return failf(nr, flow.ErrCodeInternal,
				fmt.Sprintf("exceeded max recursion depth: %d (limit: %d)", currentDepth, maxDepth))
		}
	}

	// 6. Create the child process, flagged call_func for recursion depth tracking
	childPID, err := ctx.ProcessManager.NewProcess(processID, map[string]interface{}{
		"call_func": true,
	})
	if err != nil {
		return failf(nr, flow.ErrCodeInternal, "create call child process: "+err.Error())
	}

	// 7. Create funcNR for the function node and pre-inject the arguments
	funcNR := flow.NewNodeResult(funcNode, childPID, ctx)
	funcNR.FunctionResult = &flow.FunctionResult{}
	for k, v := range paramValues {
		if err := funcNR.FunctionResult.SetParamVar(k, v); err != nil {
			return failf(nr, flow.ErrCodeConfig, "inject param "+k+" failed: "+err.Error())
		}
	}
	if err := ctx.RecordNodeExecution(childPID, funcNode.ID, funcNR); err != nil {
		return failf(nr, flow.ErrCodeInternal, "failed to record the node run: "+err.Error())
	}

	label := targetID
	if funcNode.Data != nil && funcNode.Data.Label != "" {
		label = funcNode.Data.Label
	}
	nr.AddLog(fmt.Sprintf("calling function %s(%s)", label, funcNode.ID))

	// 8. Reuse FunctionExecutor to run it (function.go only creates FunctionResult when it is nil, so the
	//    arguments pre-injected here are not overwritten)
	funcExec := NewFunctionExecutor()
	err = funcExec.Execute(funcNode, ctx, childPID, funcNR)

	// 9. Copy the outputs onto the call node
	nr.SetOutputVarsFrom(funcNR.OutputVars)

	// 10. Mirror funcNR's status (FunctionExecutor already finished it)
	if err != nil {
		nr.Finish(funcNR.Status, funcNR.ErrCode, funcNR.ErrMsg)
		return err
	}
	nr.Finish(flow.StatusSuccess, "", "")
	return nil
}

func (e *CallExecutor) Capabilities() NodeCapabilities {
	return NodeCapabilities{
		ErrorCodes: nil, // undeclared: the global error codes apply, and every child-chain code passes through
		Actions:    []flow.ErrorAction{flow.ErrorActionRetry, flow.ErrorActionSkip, flow.ErrorActionHandler},
		HasPost:    true,
	}
}

func NewCallExecutor() *CallExecutor {
	return &CallExecutor{}
}

func init() {
	RegisterExecutor("call", NewCallExecutor())
}
