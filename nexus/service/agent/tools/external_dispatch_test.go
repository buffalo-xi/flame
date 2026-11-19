package tools

import (
	"context"
	"strings"
	"testing"
)

// stubExternalRunner is a stand-in for the external-connection execution channel (it owns mcp__ / rpc__).
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
		t.Fatal("the external runner was not invoked")
	}
	if stub.schema != "t0" || stub.tool != "mcp__fs__read_file" {
		t.Errorf("forwarded values are wrong: schema=%q tool=%q", stub.schema, stub.tool)
	}
	if stub.args["path"] != "a.txt" {
		t.Errorf("the arguments were not forwarded: %v", stub.args)
	}
	// The output limit must be passed down: external tools often return a whole file, and without truncation that eats the entire
	// context in one go.
	if stub.maxOutput != DefaultMaxOutput {
		t.Errorf("maxOutput = %d, want %d", stub.maxOutput, DefaultMaxOutput)
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("result type = %T", got)
	}
}

// An unauthorized external tool name must be blocked before any lookup or connection is made (the grant gate runs before dispatch).
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
		t.Fatalf("an unauthorized call should be refused by the grant gate, got %v", err)
	}
	if stub.called {
		t.Fatal("an unauthorized call must not reach the external runner")
	}
}

// Without Endpoints injected, local__ falls through to not found: it must not be quietly forwarded as mcp/rpc
// (the normal path once injected is covered by endpoint_dispatch_test.go).
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
		t.Fatalf("want ErrToolNotFound, got %v", err)
	}
	if stub.called {
		t.Fatal("local__ must not be taken over by the external runner")
	}
}

// Without a runner injected, an external name is still not found (no panic, no empty success).
func TestExecuteToolExternalWithoutRunner(t *testing.T) {
	c := &Context{Schema: "t0", AgentName: "main", AllowedTools: map[string]bool{"mcp__fs__read_file": true}}
	_, err := ExecuteTool(context.Background(), c, "mcp__fs__read_file", nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want ErrToolNotFound, got %v", err)
	}
}
