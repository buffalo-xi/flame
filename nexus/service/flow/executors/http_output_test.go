package executors

import (
	"fmt"
	"testing"

	"flame/nexus/service/flow/core"
)

// 老节点可能没配全 HTTP 固定变量（例如缺少 stream_finished），
// 执行器写入前必须补齐，否则写入直接失败、节点判失败。
func TestEnsureFixedOutputsFillsHTTPVars(t *testing.T) {
	merged := ensureFixedOutputs(map[string]interface{}{
		"body": map[string]interface{}{"type": "string"},
	}, httpFixedOutputVars)

	for _, name := range []string{"mode", "status", "headers", "body", "sse_events", "stream_finished"} {
		if _, ok := merged[name]; !ok {
			t.Errorf("merged 缺少固定变量 %s", name)
		}
	}
}

// 脚本返回固定变量必须被忽略：body 是追加语义，执行器写过之后再拼一次会变成两份。
func TestApplyHandlerAppendDeniesHTTPFixedOutputs(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "http"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	merged := ensureFixedOutputs(nil, httpFixedOutputVars)
	merged["text"] = map[string]interface{}{"type": "string"}
	nr.InitOutputVars(merged)

	if err := applyHandlerAppend(nr, map[string]any{
		"body":   "脚本想追加的响应体",
		"status": float64(500),
		"text":   "脚本加工结果",
	}, httpReservedOutputVars); err != nil {
		t.Fatalf("applyHandlerAppend 失败: %v", err)
	}

	if v, _ := nr.GetVariable("body"); v != "" {
		t.Errorf("body = %v，应保持空串（固定变量不许脚本写）", v)
	}
	if v, _ := nr.GetVariable("status"); fmt.Sprint(v) != "0" {
		t.Errorf("status = %v，应保持初始值 0（固定变量不许脚本写）", v)
	}
	if v, _ := nr.GetVariable("text"); v != "脚本加工结果" {
		t.Errorf("text = %v，want 脚本加工结果（自定义变量应可写）", v)
	}
}
