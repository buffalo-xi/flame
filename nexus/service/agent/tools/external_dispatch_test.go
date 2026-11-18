package tools

import (
	"context"
	"strings"
	"testing"
)

// stubExternalRunner 外部连接工具的执行通道替身（mcp__ / rpc__ 归它管）。
type stubExternalRunner struct {
	called    bool
	schema    string
	tool      string
	args      map[string]any
	maxOutput int
}

func (s *stubExternalRunner) Has(name string) bool {
	return strings.HasPrefix(name, ExternalPrefixMCP) || strings.HasPrefix(name, ExternalPrefixRPC)
}

func (s *stubExternalRunner) Invoke(_ context.Context, schema, tool string, args map[string]any, maxOutput int) (any, error) {
	s.called = true
	s.schema, s.tool, s.args, s.maxOutput = schema, tool, args, maxOutput
	return map[string]any{"content": "ok", "is_error": false}, nil
}

func TestExecuteToolDelegatesToExternal(t *testing.T) {
	stub := &stubExternalRunner{}
	c := &Context{
		Schema:       "t0",
		AgentName:    "main",
		AllowedTools: map[string]bool{"mcp__fs__read_file": true},
		Externals:    stub,
	}

	got, err := ExecuteTool(context.Background(), c, "mcp__fs__read_file", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatalf("ExecuteTool = %v", err)
	}
	if !stub.called {
		t.Fatal("外部工具执行器没被调用")
	}
	if stub.schema != "t0" || stub.tool != "mcp__fs__read_file" {
		t.Errorf("透传不对：schema=%q tool=%q", stub.schema, stub.tool)
	}
	if stub.args["path"] != "a.txt" {
		t.Errorf("入参没透传：%v", stub.args)
	}
	// 输出上限必须传下去：外部工具常直接吐整份文件，不截断会一次吃掉整个上下文。
	if stub.maxOutput != DefaultMaxOutput {
		t.Errorf("maxOutput = %d, want %d", stub.maxOutput, DefaultMaxOutput)
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("结果类型 = %T", got)
	}
}

// 未授权的外部工具名必须在查库/建连之前被挡掉（授权闸在分发之前）。
func TestExecuteToolRejectsUnauthorizedExternalBeforeInvoke(t *testing.T) {
	stub := &stubExternalRunner{}
	c := &Context{
		Schema:       "t0",
		AgentName:    "poet",
		AllowedTools: map[string]bool{"read_file": true},
		Externals:    stub,
	}

	_, err := ExecuteTool(context.Background(), c, "rpc__erp__create_order", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("未授权调用应被授权闸拒绝，实际 %v", err)
	}
	if stub.called {
		t.Fatal("未授权调用不该触达外部执行器")
	}
}

// 未注入 Endpoints 时 local__ 落到 not found：不能悄悄被当成 mcp/rpc 转出去
// （注入后的正常路径见 endpoint_dispatch_test.go）。
func TestExecuteToolLocalEndpointIsNotDelegated(t *testing.T) {
	stub := &stubExternalRunner{}
	c := &Context{
		Schema:       "t0",
		AgentName:    "main",
		AllowedTools: map[string]bool{"local__daily-report": true},
		Externals:    stub,
	}

	_, err := ExecuteTool(context.Background(), c, "local__daily-report", nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("期望 ErrToolNotFound，实际 %v", err)
	}
	if stub.called {
		t.Fatal("local__ 不该被外部执行器接管")
	}
}

// 没有注入执行器时，外部名字仍是 not found（不能 panic、不能空成功）。
func TestExecuteToolExternalWithoutRunner(t *testing.T) {
	c := &Context{Schema: "t0", AgentName: "main", AllowedTools: map[string]bool{"mcp__fs__read_file": true}}
	_, err := ExecuteTool(context.Background(), c, "mcp__fs__read_file", nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("期望 ErrToolNotFound，实际 %v", err)
	}
}
