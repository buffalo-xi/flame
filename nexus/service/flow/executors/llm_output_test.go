package executors

import (
	"testing"

	"flame/nexus/service/flow/core"
)

// 固定输出变量不能依赖前端是否配置：前端某模式漏配 reasoning_content 时，
// 执行器写入会得到 "output var reasoning_content not defined" 并把节点判失败。
func TestEnsureFixedOutputsFillsMissing(t *testing.T) {
	outputs := map[string]interface{}{
		"content": map[string]interface{}{"type": "string"},
		"answer":  map[string]interface{}{"type": "string"},
	}

	merged := ensureFixedOutputs(outputs, llmFixedOutputVars)

	for _, name := range []string{"content", "reasoning_content", "tool_calls"} {
		if _, ok := merged[name]; !ok {
			t.Errorf("merged 缺少固定变量 %s", name)
		}
	}
	if _, ok := merged["answer"]; !ok {
		t.Error("merged 丢了自定义变量 answer")
	}
	if _, ok := outputs["reasoning_content"]; ok {
		t.Error("ensureFixedOutputs 不应修改调用方传入的 map")
	}
}

// 脚本返回固定变量必须被忽略：content 是追加语义，执行器写过之后再拼一次
// 会变成两份内容，"最终结果"就没有唯一答案了。
func TestApplyHandlerAppendDeniesLLMFixedOutputs(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "llm"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	merged := ensureFixedOutputs(nil, llmFixedOutputVars)
	merged["answer"] = map[string]interface{}{"type": "string"}
	nr.InitOutputVars(merged)

	if err := applyHandlerAppend(nr, map[string]any{
		"content":           "脚本想追加的正文",
		"reasoning_content": "脚本想追加的推理",
		"answer":            "脚本加工结果",
	}, llmReservedOutputVars); err != nil {
		t.Fatalf("applyHandlerAppend 失败: %v", err)
	}

	if v, _ := nr.GetVariable("content"); v != "" {
		t.Errorf("content = %v，应保持空串（固定变量不许脚本写）", v)
	}
	if v, _ := nr.GetVariable("reasoning_content"); v != "" {
		t.Errorf("reasoning_content = %v，应保持空串（固定变量不许脚本写）", v)
	}
	if v, _ := nr.GetVariable("answer"); v != "脚本加工结果" {
		t.Errorf("answer = %v，want 脚本加工结果（自定义变量应可写）", v)
	}
}
