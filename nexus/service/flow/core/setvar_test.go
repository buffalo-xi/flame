package flow

import (
	"strings"
	"testing"
)

// 运行上下文变量（builtin / env / argument）是本次运行的只读输入，
// 节点不能把它们当赋值目标 —— 必须明确拒绝，而不是落到 unsupported target type。
func TestSetVariableRejectsReadOnlySources(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{}}

	for _, src := range []string{"builtin", "env", "argument"} {
		err := ctx.SetVariable(map[string]interface{}{
			"source_type": src,
			"source_key":  "session_id",
		}, "改写", "")
		if err == nil {
			t.Fatalf("%s 源写变量应被拒绝", src)
		}
		if !strings.Contains(err.Error(), "只读") {
			t.Errorf("%s 的错误信息应说明只读，得到: %v", src, err)
		}
	}
}

// 目标未选择时给出明确提示，而不是空 source_type 的 obscure error。
func TestSetVariableRejectsEmptyTargetType(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{}}
	err := ctx.SetVariable(map[string]interface{}{"source_key": "somewhere"}, "v", "")
	if err == nil || !strings.Contains(err.Error(), "未选择") {
		t.Fatalf("err = %v，want 含「未选择」", err)
	}
}

// 流程变量是运行期可写的，不应被上面两条规则误伤。
func TestSetVariableWritesFlowVar(t *testing.T) {
	ctx := &ExecContext{RunFlowVars: map[string]interface{}{"k": "old"}}
	if err := ctx.SetVariable(map[string]interface{}{
		"source_type": "flow",
		"source_key":  "k",
	}, "new", ""); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	if v, _ := GetVarByPath(ctx.RunFlowVars, "k"); v != "new" {
		t.Errorf("k = %v，want new", v)
	}
}
