package tools

import (
	"context"
	"strings"
	"testing"
)

// stubPluginRunner 记录是否被调用（插件工具的执行通道替身）。
type stubPluginRunner struct {
	called bool
	schema string
	tool   string
	args   map[string]any
}

func (s *stubPluginRunner) Has(name string) bool { return name == "web_search" }

func (s *stubPluginRunner) Invoke(_ context.Context, schema, tool string, args map[string]any) (any, error) {
	s.called = true
	s.schema = schema
	s.tool = tool
	s.args = args
	return map[string]any{"results": []any{}}, nil
}

func TestExecuteToolDelegatesToPlugin(t *testing.T) {
	stub := &stubPluginRunner{}
	c := &Context{
		Schema:       "t0",
		AgentName:    "main",
		AllowedTools: map[string]bool{"web_search": true},
		Plugins:      stub,
	}

	got, err := ExecuteTool(context.Background(), c, "web_search", map[string]any{"query": "nexus"})
	if err != nil {
		t.Fatalf("ExecuteTool = %v", err)
	}
	if !stub.called {
		t.Fatal("插件执行器没被调用")
	}
	if stub.schema != "t0" || stub.tool != "web_search" {
		t.Errorf("透传不对：schema=%q tool=%q", stub.schema, stub.tool)
	}
	if stub.args["query"] != "nexus" {
		t.Errorf("入参没透传：%v", stub.args)
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("结果类型 = %T", got)
	}
}

// 关键负向用例：未授权的插件名字必须**在起进程之前**被挡掉。
// 否则一次「模型瞎写工具名」就会起一个 python 进程、花掉租户的检索配额。
func TestExecuteToolRejectsUnauthorizedPluginBeforeInvoke(t *testing.T) {
	stub := &stubPluginRunner{}
	c := &Context{
		Schema:       "t0",
		AgentName:    "poet",
		AllowedTools: map[string]bool{"read_file": true},
		Plugins:      stub,
	}

	_, err := ExecuteTool(context.Background(), c, "web_search", map[string]any{"query": "x"})
	if err == nil {
		t.Fatal("未授权调用应当被拒")
	}
	if !strings.Contains(err.Error(), "not granted") {
		t.Errorf("错误信息不对：%v", err)
	}
	if stub.called {
		t.Fatal("未授权调用不该触达插件执行器")
	}
}

// 没有注入执行器时，未知名字仍是 ErrToolNotFound（不能变成 panic 或空成功）。
func TestExecuteToolUnknownWithoutRunner(t *testing.T) {
	c := &Context{Schema: "t0", AgentName: "main"}
	_, err := ExecuteTool(context.Background(), c, "web_search", nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("期望 ErrToolNotFound，实际 %v", err)
	}
}
