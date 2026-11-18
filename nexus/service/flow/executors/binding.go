package executors

import (
	"encoding/json"
	"flame/nexus/service/flow/core"
	"fmt"
	"strings"
)

// fireOutputBinding 检查并触发指定变量的所有 binding，任一 binding 出错立即返回
func fireOutputBinding(nr *flow.NodeResult, varName string, value interface{}) error {
	if nr.OutputBindings == nil {
		return nil
	}
	bindings, ok := nr.OutputBindings[varName]
	if !ok || len(bindings) == 0 {
		return nil
	}
	// 遍历该变量的所有 binding（可同时绑 result + progress）
	for _, binding := range bindings {
		if err := fireSingleBinding(nr, binding, varName, value); err != nil {
			return err
		}
	}
	return nil
}

// fireSingleBinding 触发单个 binding
func fireSingleBinding(nr *flow.NodeResult, binding *flow.OutputBinding, varName string, value interface{}) error {
	if binding.Channel == "" {
		return nil
	}

	// 计算输出字符串
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
	// 与 result / progress 节点同语义：先累积（最终结果 + 落库消息），再尽力外发。
	// 通道满或无人读（非流式调用方从不读通道）只影响流式通知，不影响最终结果。
	// 这里 channel 是用户在编辑器里显式选择的输出意图，所以 result 一律按"外发"处理。
	switch binding.Channel {
	case "result":
		nr.Ctx.EmitResult(output, binding.StreamEnabled())
	case "progress":
		nr.Ctx.SendProgressAs(binding.CategoryOrText(), output)
	}
	return nil
}

// fireOutputBindings 批量触发（用于 wf_write_outputs），任一变量出错立即返回
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

// executeBindingTransform 执行 binding 的 transform 脚本（受限模式：禁止写变量回调）
func executeBindingTransform(nr *flow.NodeResult, binding *flow.OutputBinding,
	varName string, value interface{}) (string, error) {

	timeout := -1 // -1 表示不限时
	if binding.Transform.Timeout != nil && binding.Transform.Timeout.Enabled {
		timeout = binding.Transform.Timeout.Seconds
		if timeout <= 0 {
			timeout = 30
		}
	}

	// 1. 解析 transform 的 inputs（与 code 节点 inputs 同机制）
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

	// 2. 用输出变量的实际名字注入值（如 inputs["response"] = "Hello"）
	inputs[varName] = value

	// 脚本是纯「输入 → 返回」：没有回调，也无法写变量 —— 「禁止写」由模型本身保证，
	// 不再需要受限回调白名单（脚本根本没有写变量、再触发 binding 的通道）。
	// 重算超时
	timeout = nr.Ctx.ResolveScriptTimeout(binding.Transform.Lang, timeout)

	// 3. template 直接渲染为字符串，跳过 JSON decode（binding 最终只需字符串）
	if binding.Transform.Lang == "template" {
		ctxData := BuildScriptTemplateContext(nr, inputs)
		return RenderTemplate(binding.Transform.Script, ctxData)
	}

	// 4. 调用 ExecuteScript
	result, err := ExecuteScript(nr.Ctx.StdCtx, binding.Transform.Lang,
		binding.Transform.Script, inputs, nr, timeout)
	if err != nil {
		return "", err
	}

	return resultToString(result), nil
}

// resultToString 将脚本返回值转为字符串
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

// valueToString 将变量值转为字符串（无 transform 时使用）
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
