package executors

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// Everything before the model lookup is pure config: a missing block, an unsupported response mode, an empty message
// list. These fail without touching the model repository or the gateway.
func TestLLMExecutorConfigErrors(t *testing.T) {
	cases := []struct {
		name    string
		config  map[string]any
		wantMsg string
	}{
		{"no config", nil, "node data or config is nil"},
		{"no llm block", map[string]any{}, "llm config not found"},
		{
			name:    "unsupported response mode",
			config:  map[string]any{"llm": map[string]any{"response_mode": "chunked"}},
			wantMsg: "valid values are complete or sse_stream",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "llm", tc.config)
			if tc.config == nil {
				node.Data = nil
			}
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			if err := (&LLMExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed llm config")
			}
			if nr.ErrCode != flow.ErrCodeConfig {
				t.Errorf("ErrCode = %q, want config", nr.ErrCode)
			}
			if !strings.Contains(nr.ErrMsg, tc.wantMsg) {
				t.Errorf("ErrMsg = %q, want it to contain %q", nr.ErrMsg, tc.wantMsg)
			}
		})
	}
}

// The message list is rendered from templates; the role defaults to user, non-object entries are skipped, and a list
// that renders to nothing is an error (an empty request would otherwise reach the model).
func TestBuildLLMMessages(t *testing.T) {
	tmplCtx := map[string]any{"input": map[string]any{"topic": "billing"}}
	newNR := func() *flow.NodeResult {
		return newExecResult(newExecNode("2", "llm", map[string]any{}), newExecContext())
	}

	if _, err := buildLLMMessages(newNR(), map[string]any{}, map[string]any{}, tmplCtx); err == nil ||
		!strings.Contains(err.Error(), "messages cannot be empty") {
		t.Errorf("error = %v, want the empty-list error", err)
	}
	if _, err := buildLLMMessages(newNR(), map[string]any{},
		map[string]any{"messages": []any{"not an object"}}, tmplCtx); err == nil ||
		!strings.Contains(err.Error(), "messages are empty after rendering") {
		t.Errorf("error = %v, want the all-skipped error", err)
	}

	msgs, err := buildLLMMessages(newNR(), map[string]any{}, map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You help with {{ .input.topic }}."},
			map[string]any{"content": "and then?"},
			"skipped",
		},
	}, tmplCtx)
	if err != nil {
		t.Fatalf("buildLLMMessages = %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v, want 2 (the non-object is skipped)", msgs)
	}
	if msgs[0].Role != "system" || msgs[0].Content != "You help with billing." {
		t.Errorf("msg[0] = %#v, want the rendered system prompt", msgs[0])
	}
	if msgs[1].Role != "user" {
		t.Errorf("msg[1].Role = %q, want user as the default", msgs[1].Role)
	}

	if _, err := buildLLMMessages(newNR(), map[string]any{}, map[string]any{
		"messages": []any{map[string]any{"content": "{{"}},
	}, tmplCtx); err == nil || !strings.Contains(err.Error(), "failed to render the messages template") {
		t.Errorf("error = %v, want the render error", err)
	}
}

// The node-level params override the model defaults field by field, and extra_params merge key by key - an explicit
// null clears the model's extra params.
func TestBuildModelInput(t *testing.T) {
	model := &models.LLMModel{
		GatewayModel: "gpt-x",
		GatewayID:    "gw-1",
		BaseParams:   xsql.JSONB{Data: map[string]any{"max_tokens": 100, "temperature": 0.7, "top_p": 0.9}},
		ExtraParams:  xsql.JSONB{Data: map[string]any{"seed": 1, "vendor": "a"}},
	}

	t.Run("model defaults", func(t *testing.T) {
		mi := buildModelInput(map[string]any{}, model)
		if mi.GatewayModel != "gpt-x" || mi.GatewayID != "gw-1" {
			t.Errorf("identity = %q/%q, want the model's", mi.GatewayModel, mi.GatewayID)
		}
		if mi.MaxTokens != 100 || mi.Temperature != 0.7 || mi.TopP != 0.9 {
			t.Errorf("params = %d/%v/%v, want the model defaults", mi.MaxTokens, mi.Temperature, mi.TopP)
		}
		if mi.ExtraParams["seed"] != float64(1) || mi.ExtraParams["vendor"] != "a" {
			t.Errorf("extra = %#v, want the model's", mi.ExtraParams)
		}
	})

	t.Run("node overrides merge", func(t *testing.T) {
		mi := buildModelInput(map[string]any{
			"params": map[string]any{
				"max_tokens":   50,
				"temperature":  0.1,
				"extra_params": map[string]any{"seed": 2, "extra": true},
			},
		}, model)
		if mi.MaxTokens != 50 || mi.Temperature != 0.1 {
			t.Errorf("params = %d/%v, want the node's overrides", mi.MaxTokens, mi.Temperature)
		}
		if mi.TopP != 0.9 {
			t.Errorf("TopP = %v, want the model default kept", mi.TopP)
		}
		if mi.ExtraParams["seed"] != 2 || mi.ExtraParams["vendor"] != "a" || mi.ExtraParams["extra"] != true {
			t.Errorf("extra = %#v, want a per-key merge", mi.ExtraParams)
		}
	})

	t.Run("null extra_params clears the model's", func(t *testing.T) {
		mi := buildModelInput(map[string]any{"params": map[string]any{"extra_params": nil}}, model)
		if len(mi.ExtraParams) != 0 {
			t.Errorf("extra = %#v, want it cleared", mi.ExtraParams)
		}
	})

	t.Run("a model without defaults", func(t *testing.T) {
		mi := buildModelInput(map[string]any{}, &models.LLMModel{GatewayModel: "m", GatewayID: "g"})
		if mi.MaxTokens != 0 || mi.Temperature != 0 || len(mi.ExtraParams) != 0 {
			t.Errorf("params = %d/%v/%v, want zeroes", mi.MaxTokens, mi.Temperature, mi.ExtraParams)
		}
	})

	t.Run("unparsable model defaults are ignored", func(t *testing.T) {
		bad := &models.LLMModel{
			GatewayModel: "m",
			GatewayID:    "g",
			BaseParams:   xsql.JSONB{Data: "not json"},
			ExtraParams:  xsql.JSONB{Data: "not json either"},
		}
		mi := buildModelInput(map[string]any{}, bad)
		if mi.MaxTokens != 0 || len(mi.ExtraParams) != 0 {
			t.Errorf("params = %d extra = %#v, want the broken defaults ignored", mi.MaxTokens, mi.ExtraParams)
		}
	})
}

// Numeric parameters may arrive as any JSON/Go number shape, and a numeric string is accepted too; anything else
// counts as "not configured" rather than as zero.
func TestBpFloat(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]any
		want float64
		ok   bool
	}{
		{"nil map", nil, 0, false},
		{"missing", map[string]any{}, 0, false},
		{"nil value", map[string]any{"n": nil}, 0, false},
		{"float64", map[string]any{"n": 1.5}, 1.5, true},
		{"int", map[string]any{"n": 2}, 2, true},
		{"int64", map[string]any{"n": int64(3)}, 3, true},
		{"json.Number", map[string]any{"n": json.Number("4.5")}, 4.5, true},
		{"invalid json.Number", map[string]any{"n": json.Number("x")}, 0, false},
		{"numeric string", map[string]any{"n": "6"}, 6, true},
		{"non-numeric string", map[string]any{"n": "many"}, 0, false},
		{"bool", map[string]any{"n": true}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bpFloat(tc.m, "n")
			if got != tc.want || ok != tc.ok {
				t.Errorf("bpFloat = %v, %v, want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// A broken stream is classified by the run's context: deadline is a timeout, an explicit stop is a cancel, and a
// dropped connection is neither.
func TestLLMStreamErrCode(t *testing.T) {
	ctx := newExecContext()
	if got := llmStreamErrCode(ctx); got != flow.ErrCodeConnection {
		t.Errorf("live context = %q, want connection", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ctx.StdCtx = cancelled
	if got := llmStreamErrCode(ctx); got != flow.ErrCodeCancel {
		t.Errorf("cancelled = %q, want cancel", got)
	}

	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	ctx.StdCtx = expired
	if got := llmStreamErrCode(ctx); got != flow.ErrCodeTimeout {
		t.Errorf("expired = %q, want timeout", got)
	}
}
