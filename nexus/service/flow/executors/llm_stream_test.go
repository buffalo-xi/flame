package executors

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"flame/nexus/service/flow/core"
	"flame/nexus/service/llm/gateway"
)

// A complete call writes the three fixed variables unconditionally: content always, reasoning_content only when the
// model returned one, tool_calls always.
func TestExecuteCompleteWritesFixedOutputs(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		generate: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (gateway.GenerateResponse, error) {
			return gateway.GenerateResponse{
				Content:          "hi",
				ReasoningContent: "because",
				ToolCalls:        []gateway.ToolCall{{ID: "call-1", Name: "search"}},
			}, nil
		},
	})

	nr, ctx := newLLMResult(t, llmOutputs())
	if err := (&LLMExecutor{}).executeComplete(ctx, nr, &gateway.ModelInput{GatewayModel: "m"}, nil, "", "", 30, false); err != nil {
		t.Fatalf("executeComplete = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
	if got, _ := nr.OutputVars["content"].(string); got != "hi" {
		t.Errorf("content = %v, want hi", nr.OutputVars["content"])
	}
	if got, _ := nr.OutputVars["reasoning_content"].(string); got != "because" {
		t.Errorf("reasoning_content = %v, want because", nr.OutputVars["reasoning_content"])
	}
	if calls := toSlice(nr.OutputVars["tool_calls"]); len(calls) != 1 {
		t.Errorf("tool_calls = %v, want one call", nr.OutputVars["tool_calls"])
	}
}

// A reasoning model may answer without reasoning content; that must not be an error, and the variable stays empty.
func TestExecuteCompleteWithoutReasoning(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		generate: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (gateway.GenerateResponse, error) {
			return gateway.GenerateResponse{Content: "hi"}, nil
		},
	})

	nr, ctx := newLLMResult(t, llmOutputs())
	if err := (&LLMExecutor{}).executeComplete(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err != nil {
		t.Fatalf("executeComplete = %v", err)
	}
	if got, _ := nr.OutputVars["reasoning_content"].(string); got != "" {
		t.Errorf("reasoning_content = %q, want empty", got)
	}
}

func TestExecuteCompleteGatewayError(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		generate: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (gateway.GenerateResponse, error) {
			return gateway.GenerateResponse{}, errors.New("dial tcp: refused")
		},
	})

	nr, ctx := newLLMResult(t, llmOutputs())
	if err := (&LLMExecutor{}).executeComplete(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err == nil {
		t.Fatal("executeComplete must fail when the gateway fails")
	}
	if nr.ErrCode != flow.ErrCodeConnection {
		t.Errorf("ErrCode = %q, want connection", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "LLM call failed") {
		t.Errorf("ErrMsg = %q, want it to name the call", nr.ErrMsg)
	}
}

// The fixed variables must be declared on the node; a missing declaration is a var error rather than a silent drop.
func TestExecuteCompleteUndeclaredFixedOutput(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		generate: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (gateway.GenerateResponse, error) {
			return gateway.GenerateResponse{Content: "hi"}, nil
		},
	})

	nr, ctx := newLLMResult(t, map[string]any{"custom": map[string]any{"type": "string"}})
	if err := (&LLMExecutor{}).executeComplete(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err == nil {
		t.Fatal("executeComplete must fail when content is not declared")
	}
	if nr.ErrCode != flow.ErrCodeVar {
		t.Errorf("ErrCode = %q, want var", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "failed to write content") {
		t.Errorf("ErrMsg = %q, want it to name the variable", nr.ErrMsg)
	}
}

// Streaming accumulates content frame by frame, and the Done frame carrying an empty tool_calls array must not wipe the
// snapshot from the frames before it.
func TestExecuteStreamAccumulatesAndKeepsToolCalls(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		stream: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (<-chan gateway.StreamChunk, error) {
			return chunkChan(
				gateway.StreamChunk{Content: "he", ToolCalls: []gateway.ToolCall{{ID: "c1", Name: "search"}}},
				gateway.StreamChunk{Content: "llo", ReasoningContent: "why"},
				gateway.StreamChunk{Done: true},
			), nil
		},
	})

	nr, ctx := newLLMResult(t, llmOutputs())
	if err := (&LLMExecutor{}).executeStream(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err != nil {
		t.Fatalf("executeStream = %v (code %q)", err, nr.ErrCode)
	}
	if got, _ := nr.OutputVars["content"].(string); got != "hello" {
		t.Errorf("content = %q, want the frames concatenated", got)
	}
	if got, _ := nr.OutputVars["reasoning_content"].(string); got != "why" {
		t.Errorf("reasoning_content = %q, want the frame's value", got)
	}
	if calls := toSlice(nr.OutputVars["tool_calls"]); len(calls) != 1 {
		t.Errorf("tool_calls = %v, want the snapshot kept across the empty Done frame", nr.OutputVars["tool_calls"])
	}
	if nr.Status != flow.StatusSuccess {
		t.Errorf("status = %v, want success", nr.Status)
	}
}

// A failure in the middle of the stream is not a partial success: the node fails and the code says whether the cause was
// the deadline, a cancel, or the connection.
func TestExecuteStreamErrors(t *testing.T) {
	cases := []struct {
		name     string
		ctx      func() (context.Context, context.CancelFunc)
		wantCode string
	}{
		{"live connection", func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, flow.ErrCodeConnection},
		{"cancelled", func() (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(context.Background())
			cancel()
			return c, cancel
		}, flow.ErrCodeCancel},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, flow.ErrCodeTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useFakeGateway(t, &fakeGateway{
				stream: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (<-chan gateway.StreamChunk, error) {
					return chunkChan(
						gateway.StreamChunk{Content: "half"},
						gateway.StreamChunk{Error: errors.New("read: connection reset")},
					), nil
				},
			})

			nr, ctx := newLLMResult(t, llmOutputs())
			stdCtx, cancel := tc.ctx()
			defer cancel()
			ctx.StdCtx = stdCtx

			if err := (&LLMExecutor{}).executeStream(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err == nil {
				t.Fatal("executeStream must fail on a mid-stream error")
			}
			if nr.ErrCode != tc.wantCode {
				t.Errorf("ErrCode = %q, want %q", nr.ErrCode, tc.wantCode)
			}
			if !strings.Contains(nr.ErrMsg, "LLM streaming call interrupted") {
				t.Errorf("ErrMsg = %q, want it to name the interruption", nr.ErrMsg)
			}
			if nr.Status == flow.StatusSuccess {
				t.Error("a broken stream must not end as success")
			}
		})
	}
}

func TestExecuteStreamOpenError(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		stream: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (<-chan gateway.StreamChunk, error) {
			return nil, errors.New("502 bad gateway")
		},
	})

	nr, ctx := newLLMResult(t, llmOutputs())
	if err := (&LLMExecutor{}).executeStream(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err == nil {
		t.Fatal("executeStream must fail when the call cannot be opened")
	}
	if nr.ErrCode != flow.ErrCodeConnection {
		t.Errorf("ErrCode = %q, want connection", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "LLM streaming call failed") {
		t.Errorf("ErrMsg = %q, want it to name the call", nr.ErrMsg)
	}
}

func TestExecuteStreamUndeclaredFixedOutput(t *testing.T) {
	useFakeGateway(t, &fakeGateway{
		stream: func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (<-chan gateway.StreamChunk, error) {
			return chunkChan(gateway.StreamChunk{Content: "x"}), nil
		},
	})

	nr, ctx := newLLMResult(t, map[string]any{"custom": map[string]any{"type": "string"}})
	if err := (&LLMExecutor{}).executeStream(ctx, nr, &gateway.ModelInput{}, nil, "", "", 30, false); err == nil {
		t.Fatal("executeStream must fail when content is not declared")
	}
	if nr.ErrCode != flow.ErrCodeVar {
		t.Errorf("ErrCode = %q, want var", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "failed to append content") {
		t.Errorf("ErrMsg = %q, want it to name the variable", nr.ErrMsg)
	}
}

// fakeGateway implements gateway.Client; only the two call methods are used by the LLM executor, the rest is a stub so
// the interface is satisfied.
type fakeGateway struct {
	generate func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (gateway.GenerateResponse, error)
	stream   func(context.Context, gateway.ModelInput, []gateway.Message, []gateway.ToolDef) (<-chan gateway.StreamChunk, error)
}

func (f *fakeGateway) Provision(string, gateway.ModelInput, gateway.ProviderInput) (string, error) {
	return "", errors.New("not used")
}

func (f *fakeGateway) Deprovision(string, gateway.ModelInput, gateway.ProviderInput) error {
	return errors.New("not used")
}

func (f *fakeGateway) DeprovisionAll(string) (int, error) { return 0, errors.New("not used") }

func (f *fakeGateway) ListProvisioned(string) ([]gateway.ProvisionedModel, error) {
	return nil, errors.New("not used")
}

func (f *fakeGateway) Update(string, gateway.ModelInput, gateway.ProviderInput) error {
	return errors.New("not used")
}

func (f *fakeGateway) Generate(ctx context.Context, model gateway.ModelInput, messages []gateway.Message, tools []gateway.ToolDef) (gateway.GenerateResponse, error) {
	return f.generate(ctx, model, messages, tools)
}

func (f *fakeGateway) Stream(ctx context.Context, model gateway.ModelInput, messages []gateway.Message, tools []gateway.ToolDef) (<-chan gateway.StreamChunk, error) {
	return f.stream(ctx, model, messages, tools)
}

// useFakeGateway swaps the package-level gateway for one test and always puts the previous one back: the global is shared
// by the whole process, so these tests must not run in parallel.
func useFakeGateway(t *testing.T, fake gateway.Client) {
	t.Helper()
	previous := gateway.Current
	gateway.SetCurrent(fake)
	t.Cleanup(func() { gateway.SetCurrent(previous) })
}

// chunkChan delivers the frames and closes the channel, which is what the executor's range loop expects.
func chunkChan(chunks ...gateway.StreamChunk) <-chan gateway.StreamChunk {
	ch := make(chan gateway.StreamChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch
}

// newLLMResult builds a node result that declares the given output variables.
func newLLMResult(t *testing.T, outputs map[string]any) (*flow.NodeResult, *flow.ExecContext) {
	t.Helper()
	ctx := newExecContext()
	nr := newExecResult(newExecNode("2", "llm", map[string]any{}), ctx)
	nr.InitOutputVars(outputs)
	return nr, ctx
}

// llmOutputs is the fixed variable declaration the executor expects, as a config would carry it.
func llmOutputs() map[string]any {
	out := make(map[string]any, len(llmFixedOutputVars))
	for name, typ := range llmFixedOutputVars {
		out[name] = map[string]any{"type": typ}
	}
	return out
}
