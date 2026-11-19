package tools

import (
	"context"
	"strings"
	"testing"
)

// stubPluginRunner records whether it was invoked (a stand-in for the plugin tools' execution channel).
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
		t.Fatal("the plugin runner was not invoked")
	}
	if stub.schema != "t0" || stub.tool != "web_search" {
		t.Errorf("forwarded values are wrong: schema=%q tool=%q", stub.schema, stub.tool)
	}
	if stub.args["query"] != "nexus" {
		t.Errorf("the arguments were not forwarded: %v", stub.args)
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("result type = %T", got)
	}
}

// The key negative case: an unauthorized plugin name must be blocked **before a process is started**.
// Otherwise one round of "the model invents a tool name" spawns a python process and burns the tenant's search quota.
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
		t.Fatal("an unauthorized call should be refused")
	}
	if !strings.Contains(err.Error(), "not granted") {
		t.Errorf("unexpected error: %v", err)
	}
	if stub.called {
		t.Fatal("an unauthorized call must not reach the plugin runner")
	}
}

// Without a runner injected, an unknown name is still ErrToolNotFound (it must not become a panic or an empty success).
func TestExecuteToolUnknownWithoutRunner(t *testing.T) {
	c := &Context{Schema: "t0", AgentName: "main"}
	_, err := ExecuteTool(context.Background(), c, "web_search", nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want ErrToolNotFound, got %v", err)
	}
}
