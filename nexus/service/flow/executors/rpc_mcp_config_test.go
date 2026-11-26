package executors

import (
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// Both remote-call nodes validate their config before looking anything up: every case below fails without a
// connection, a code client or a network call.
func TestRpcExecutorConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		wantMsg string
	}{
		{"no block", map[string]any{}, "rpc config not found"},
		{
			name: "template handler",
			config: map[string]any{"rpc": map[string]any{
				"handler": map[string]any{"lang": "template", "script": "{{ .input.x }}"},
			}},
			wantMsg: "does not support the template language",
		},
		{
			name:    "malformed arguments",
			config:  map[string]any{"rpc": map[string]any{"arguments": 42}},
			wantMsg: "the arguments config is malformed",
		},
		{
			name:    "missing connection",
			config:  map[string]any{"rpc": map[string]any{}},
			wantMsg: "rpc connection_id must not be empty",
		},
		{
			name:    "missing tool",
			config:  map[string]any{"rpc": map[string]any{"connection_id": "c1"}},
			wantMsg: "rpc tool_name must not be empty",
		},
		{
			name: "endpoint target without a name",
			config: map[string]any{"rpc": map[string]any{
				"target_type": "endpoint",
			}},
			wantMsg: "rpc endpoint is not configured",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "rpc", tc.config)
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			if err := (&RpcExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed rpc config")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}

	t.Run("no data at all", func(t *testing.T) {
		node := newExecNode("2", "rpc", nil)
		node.Data = nil
		ctx := newExecContext()
		nr := newExecResult(node, ctx)
		if err := (&RpcExecutor{}).Execute(node, ctx, "1", nr); err == nil {
			t.Fatal("Execute must fail without node data")
		}
		if nr.ErrCode != flow.ErrCodeConfig || !strings.Contains(nr.ErrMsg, "node data or config is nil") {
			t.Errorf("ErrCode/ErrMsg = %q/%q", nr.ErrCode, nr.ErrMsg)
		}
	})
}

// The mcp node checks the connection and the tool before the handler, so a node missing both reports the connection
// first - the order is the order the author fills the panel in.
func TestMcpExecutorConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		wantMsg string
	}{
		{"no block", map[string]any{}, "mcp config not found"},
		{
			name:    "missing connection",
			config:  map[string]any{"mcp": map[string]any{"handler": map[string]any{"lang": "template", "script": "{{"}}},
			wantMsg: "mcp connection_id must not be empty",
		},
		{
			name:    "missing tool",
			config:  map[string]any{"mcp": map[string]any{"connection_id": "c1"}},
			wantMsg: "mcp tool_name must not be empty",
		},
		{
			name: "template handler",
			config: map[string]any{"mcp": map[string]any{
				"connection_id": "c1",
				"tool_name":     "search",
				"handler":       map[string]any{"lang": "template", "script": "{{ .input.x }}"},
			}},
			wantMsg: "does not support the template language",
		},
		{
			name: "broken arguments template",
			config: map[string]any{"mcp": map[string]any{
				"connection_id": "c1",
				"tool_name":     "search",
				"arguments":     "{{",
			}},
			wantMsg: "failed to render the arguments template",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "mcp", tc.config)
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			if err := (&McpExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed mcp config")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}

	t.Run("no data at all", func(t *testing.T) {
		node := newExecNode("2", "mcp", nil)
		node.Data = nil
		ctx := newExecContext()
		nr := newExecResult(node, ctx)
		if err := (&McpExecutor{}).Execute(node, ctx, "1", nr); err == nil {
			t.Fatal("Execute must fail without node config")
		}
		if nr.ErrCode != flow.ErrCodeConfig {
			t.Errorf("ErrCode = %q, want config", nr.ErrCode)
		}
	})
}

// resolveInputVars is shared by llm / rpc / mcp / http: no inputs block means no arguments, a flow source reads the
// run's flow variables, and an unresolvable source fails the node instead of sending a half-empty request.
func TestResolveInputVars(t *testing.T) {
	t.Run("no inputs block", func(t *testing.T) {
		node := newExecNode("2", "http", map[string]any{})
		ctx := newExecContext()
		nr := newExecResult(node, ctx)
		got, err := resolveInputVars(node, ctx, "1", nr)
		if err != nil || len(got) != 0 {
			t.Fatalf("resolveInputVars = %v, %v, want an empty map", got, err)
		}
		if len(nr.InputVars) != 0 {
			t.Errorf("InputVars = %v, want them initialised empty", nr.InputVars)
		}
	})

	t.Run("flow source", func(t *testing.T) {
		node := newExecNode("2", "http", map[string]any{
			"inputs": map[string]any{
				"topic": map[string]any{"source_type": "flow", "source_key": "topic"},
			},
		})
		ctx := newExecContext()
		ctx.RunFlowVars = map[string]any{"topic": "billing"}
		nr := newExecResult(node, ctx)

		got, err := resolveInputVars(node, ctx, "1", nr)
		if err != nil {
			t.Fatalf("resolveInputVars = %v", err)
		}
		if got["topic"] != "billing" {
			t.Errorf("inputs = %v, want the flow variable", got)
		}
		if nr.InputVars["topic"] != "billing" {
			t.Errorf("InputVars = %v, want the same values on the node result", nr.InputVars)
		}
	})

	t.Run("unresolvable source", func(t *testing.T) {
		node := newExecNode("2", "http", map[string]any{
			"inputs": map[string]any{
				"topic": map[string]any{"source_type": "flow", "source_key": "missing"},
			},
		})
		ctx := newExecContext()
		ctx.RunFlowVars = map[string]any{}
		nr := newExecResult(node, ctx)

		if _, err := resolveInputVars(node, ctx, "1", nr); err == nil {
			t.Fatal("resolveInputVars must fail when the source does not resolve")
		}
	})
}
