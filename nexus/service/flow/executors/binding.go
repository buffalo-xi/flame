package executors

import (
	"encoding/json"
	"flame/nexus/service/flow/core"
	"fmt"
	"strings"
)

// fireOutputBinding checks and fires every binding of the given variable, returning at the first failure
func fireOutputBinding(nr *flow.NodeResult, varName string, value interface{}) error {
	if nr.OutputBindings == nil {
		return nil
	}
	bindings, ok := nr.OutputBindings[varName]
	if !ok || len(bindings) == 0 {
		return nil
	}
	// Walk every binding of this variable (one variable may be bound to result and progress at once)
	for _, binding := range bindings {
		if err := fireSingleBinding(nr, binding, varName, value); err != nil {
			return err
		}
	}
	return nil
}

// fireSingleBinding fires one binding
func fireSingleBinding(nr *flow.NodeResult, binding *flow.OutputBinding, varName string, value interface{}) error {
	if binding.Channel == "" {
		return nil
	}

	// Compute the output string
	var output string
	if binding.Transform != nil && binding.Transform.Script != "" {
		result, err := executeBindingTransform(nr, binding, varName, value)
		if err != nil {
			nr.AddLogf("binding transform error [%s→%s]: %v", varName, binding.Channel, err)
			return err
		}
		output = result
	} else {
		output = valueToString(value)
	}

	if nr.Ctx == nil {
		return nil
	}
	// Same semantics as the result / progress nodes: accumulate first (final result plus stored
	// message), then emit best-effort. A full channel or no reader (a non-streaming caller never reads
	// the channel) only affects streamed notifications, not the final result.
	// Here the channel is an output intent the user picked explicitly in the editor, so result always
	// counts as "emit".
	switch binding.Channel {
	case "result":
		nr.Ctx.EmitResult(output, binding.StreamEnabled())
	case "progress":
		nr.Ctx.SendProgressAs(binding.CategoryOrText(), output)
	}
	return nil
}

// fireOutputBindings fires a batch (used by wf_write_outputs), returning at the first failing variable
func fireOutputBindings(nr *flow.NodeResult, vars map[string]interface{}) error {
	if nr.OutputBindings == nil {
		return nil
	}
	for varName, value := range vars {
		rootName := strings.Split(strings.Split(varName, ".")[0], "[")[0]
		if err := fireOutputBinding(nr, rootName, value); err != nil {
			return err
		}
	}
	return nil
}

// executeBindingTransform runs a binding's transform script (restricted mode: no variable-writing callback)
func executeBindingTransform(nr *flow.NodeResult, binding *flow.OutputBinding,
	varName string, value interface{}) (string, error) {

	timeout := -1 // -1 means no time limit
	if binding.Transform.Timeout != nil && binding.Transform.Timeout.Enabled {
		timeout = binding.Transform.Timeout.Seconds
		if timeout <= 0 {
			timeout = 30
		}
	}

	// 1. Resolve the transform's inputs (the same mechanism as the code node's inputs)
	var inputs map[string]any
	if binding.Transform.Inputs != nil {
		var err error
		inputs, err = nr.Ctx.GetVariables(binding.Transform.Inputs, nr.ProcessID)
		if err != nil {
			return "", fmt.Errorf("resolve binding inputs: %w", err)
		}
	} else {
		inputs = make(map[string]any)
	}

	// 2. Inject the value under the output variable's real name (e.g. inputs["response"] = "Hello")
	inputs[varName] = value

	// The script is a pure "input -> return": it has no callback and cannot write variables — "cannot
	// write" is guaranteed by the model itself, so a restricted callback allowlist is no longer needed
	// (the script has no channel to write a variable and fire the binding again).
	// Recompute the timeout
	timeout = nr.Ctx.ResolveScriptTimeout(binding.Transform.Lang, timeout)

	// 3. A template renders straight to a string, skipping the JSON decode (a binding only needs a string)
	if binding.Transform.Lang == "template" {
		ctxData := BuildScriptTemplateContext(nr, inputs)
		return RenderTemplate(binding.Transform.Script, ctxData)
	}

	// 4. Call ExecuteScript
	result, err := ExecuteScript(nr.Ctx.StdCtx, binding.Transform.Lang,
		binding.Transform.Script, inputs, nr, timeout)
	if err != nil {
		return "", err
	}

	return resultToString(result), nil
}

// resultToString turns a script return value into a string
func resultToString(result any) string {
	switch v := result.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// valueToString turns a variable value into a string (used when there is no transform)
func valueToString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	case json.Number:
		return v.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
