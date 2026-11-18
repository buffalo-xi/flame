package tools

import (
	"context"
	"strings"
	"testing"
)

// stubEndpointRunner 本地端点工具的执行通道替身（local__ 归它管）。
type stubEndpointRunner struct {
	called bool
	in     LocalCall
}

func (s *stubEndpointRunner) Has(name string) bool {
	return strings.HasPrefix(name, ExternalPrefixLocal)
}

func (s *stubEndpointRunner) Invoke(_ context.Context, in LocalCall) (any, error) {
	s.called = true
	s.in = in
	return map[string]any{"content": "ok", "is_error": false}, nil
}

// local__<端点> 必须交给 EndpointRunner，而不是 externalToolRunner ——
// 后者只认 mcp__ / rpc__，转过去只会得到「不是可执行的外部连接工具」。
func TestExecuteToolDelegatesToEndpointRunner(t *testing.T) {
	ext := &stubExternalRunner{}
	ep := &stubEndpointRunner{}
	c := &Context{
		Schema: "t0", AgentName: "poet", AgentID: "agent-1",
		UserID: "user-1", SessionID: "sess-1", RunID: "run-1",
		CallDepth:    2,
		AllowedTools: map[string]bool{"local__daily-report": true},
		Externals:    ext,
		Endpoints:    ep,
	}

	got, err := ExecuteTool(context.Background(), c, "local__daily-report", map[string]any{"topic": "x"})
	if err != nil {
		t.Fatalf("ExecuteTool = %v", err)
	}
	if !ep.called {
		t.Fatal("本地端点执行器没被调用")
	}
	if ext.called {
		t.Fatal("local__ 不该被外部连接执行器接管")
	}
	// 身份必须透传：被调 flow 要落在调用方的租户 / 会话场地上。
	if ep.in.Schema != "t0" || ep.in.UserID != "user-1" || ep.in.SessionID != "sess-1" || ep.in.RunID != "run-1" {
		t.Errorf("身份透传不对：%+v", ep.in)
	}
	if ep.in.AgentID != "agent-1" || ep.in.ToolName != "local__daily-report" {
		t.Errorf("工具上下文透传不对：%+v", ep.in)
	}
	if ep.in.Args["topic"] != "x" {
		t.Errorf("入参没透传：%v", ep.in.Args)
	}
	// 栈深度必须透传：本地端点以深度 +1 执行，agent → flow → agent 的环靠它收敛。
	// 漏了这一项，执行侧永远看到 0，护栏等于不存在。
	if ep.in.CallDepth != 2 {
		t.Errorf("CallDepth = %d, want 2", ep.in.CallDepth)
	}
	// 输出上限与外部工具同口径：端点可能直接吐出整份报告。
	if ep.in.MaxOutput != DefaultMaxOutput {
		t.Errorf("maxOutput = %d, want %d", ep.in.MaxOutput, DefaultMaxOutput)
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("结果类型 = %T", got)
	}
}

// 未授权仍在分发之前挡掉：连端点表都不会查（与外部连接同理）。
func TestExecuteToolRejectsUnauthorizedLocalBeforeInvoke(t *testing.T) {
	ep := &stubEndpointRunner{}
	c := &Context{
		Schema: "t0", AgentName: "poet",
		AllowedTools: map[string]bool{"read_file": true},
		Endpoints:    ep,
	}

	_, err := ExecuteTool(context.Background(), c, "local__daily-report", nil)
	if err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("未授权调用应被授权闸拒绝，实际 %v", err)
	}
	if ep.called {
		t.Fatal("未授权调用不该触达端点执行器")
	}
}
