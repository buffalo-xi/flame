package flow

import (
	"strings"
	"testing"
)

// builtin 的键集是引擎契约：脚本里一旦写了 builtin.run_id，改名就是破坏性变更。
// 这条用例把当前键集钉住 —— 增删键时必须同步 desktop 的 builtinVars.js（选择器）
// 并复查本函数。
func TestBuiltinVarsKeys(t *testing.T) {
	ctx := &ExecContext{
		SessionID: "sess-1",
		RunID:     "run-1",
		UserID:    "user-1",
		Schema:    "t0",
	}
	want := []string{"session_id", "run_id", "user_id", "schema", "flow_id"}
	got := ctx.BuiltinVars()
	if len(got) != len(want) {
		t.Fatalf("key 数量 = %d, want %d（增删键必须同步前端选择器）", len(got), len(want))
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("缺少 key %q（当前：%v）", k, got)
		}
	}
	// 无 flow 实例时 flow_id 退空串，而不是 panic
	if got["flow_id"] != "" {
		t.Errorf("flow_id = %#v, want 空串", got["flow_id"])
	}
}

// 声明了就取得到；空值恒为 ""（不是 nil）—— 脚本侧要能跨语言统一判空。
func TestGetVariablesBuiltin(t *testing.T) {
	ctx := &ExecContext{RunID: "run-1", UserID: "user-1", Schema: "t0"}
	defs := map[string]interface{}{
		"sid": map[string]interface{}{
			"source_type": "builtin",
			"source_key":  "session_id",
			"source_ref":  "",
		},
		"rid": map[string]interface{}{
			"source_type": "builtin",
			"source_key":  "run_id",
		},
	}

	values, err := ctx.GetVariables(defs, "")
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if v, ok := values["sid"]; !ok || v != "" {
		t.Errorf("无会话时 sid = %#v, want 空串", v)
	}
	if values["rid"] != "run-1" {
		t.Errorf("rid = %#v, want run-1", values["rid"])
	}

	ctx.SessionID = "sess-1"
	values, err = ctx.GetVariables(defs, "")
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if values["sid"] != "sess-1" {
		t.Errorf("sid = %#v, want sess-1", values["sid"])
	}
}

// 拼错 key 必须显式报错。静默返回 nil 会让脚本拿到空值继续跑，
// 运行日志里看起来只是「这次没拿到值」，几乎无法归因。
func TestGetVariablesBuiltinUnknownKey(t *testing.T) {
	ctx := &ExecContext{}
	_, err := ctx.GetVariables(map[string]interface{}{
		"x": map[string]interface{}{
			"source_type": "builtin",
			"source_key":  "sessionid", // 少了下划线
		},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "unknown builtin variable") {
		t.Fatalf("err = %v, want 含 unknown builtin variable", err)
	}
}

// 未被节点声明的 builtin 不应出现在结果里 —— 这是「显式引入」的前提：
// 大多数脚本不需要运行上下文，它们拿到的 input 里不该多出任何键。
func TestGetVariablesWithoutBuiltinDeclaration(t *testing.T) {
	ctx := &ExecContext{SessionID: "sess-1", RunFlowVars: map[string]interface{}{"present": 1}}
	values, err := ctx.GetVariables(map[string]interface{}{
		"a": map[string]interface{}{
			"source_type": "flow",
			"source_key":  "present",
		},
	}, "")
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if _, ok := values["session_id"]; ok {
		t.Errorf("未声明却出现 session_id：%v", values)
	}
	if len(values) != 1 {
		t.Errorf("结果键数 = %d, want 1（%v）", len(values), values)
	}
}
