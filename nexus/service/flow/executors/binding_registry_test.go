package executors

import (
	"encoding/json"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// An output binding is what makes a variable reach the caller: the result channel accumulates (and streams when
// asked), the progress channel notifies. These cases pin that split.
func TestFireOutputBindingResultChannel(t *testing.T) {
	cases := []struct {
		name       string
		stream     *bool
		wantStream bool
	}{
		{"stream defaults to on", nil, true},
		{"stream off accumulates only", boolPtr(false), false},
		{"stream on explicitly", boolPtr(true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := &flow.OutputBinding{Channel: "result", Stream: tc.stream}
			nr, ctx := newBindingResult(map[string][]*flow.OutputBinding{"output": {binding}})
			ctx.ResultChan = make(chan string, 1)

			if err := fireOutputBinding(nr, "output", "hello"); err != nil {
				t.Fatalf("fireOutputBinding = %v", err)
			}
			if len(ctx.ResultMsgs) != 1 || ctx.ResultMsgs[0] != "hello" {
				t.Errorf("ResultMsgs = %v, want one entry", ctx.ResultMsgs)
			}
			if got := ctx.GetResult(); got != "hello" {
				t.Errorf("Result = %q, want hello", got)
			}
			select {
			case got := <-ctx.ResultChan:
				if !tc.wantStream {
					t.Errorf("streamed %q although stream is off", got)
				}
				if got != "hello" {
					t.Errorf("streamed %q, want hello", got)
				}
			default:
				if tc.wantStream {
					t.Error("stream is on but nothing was pushed onto the channel")
				}
			}
		})
	}
}

func TestFireOutputBindingProgressChannel(t *testing.T) {
	binding := &flow.OutputBinding{Channel: "progress", Category: "thinking"}
	nr, ctx := newBindingResult(map[string][]*flow.OutputBinding{"output": {binding}})
	ctx.ProgressChan = make(chan flow.ProgressMsg, 1)

	if err := fireOutputBinding(nr, "output", "pondering"); err != nil {
		t.Fatalf("fireOutputBinding = %v", err)
	}
	if len(ctx.ProgressMsgs) != 1 || ctx.ProgressMsgs[0] != "pondering" {
		t.Errorf("ProgressMsgs = %v, want one entry", ctx.ProgressMsgs)
	}
	if len(ctx.ResultMsgs) != 0 {
		t.Errorf("ResultMsgs = %v, want none: progress is not result text", ctx.ResultMsgs)
	}
	select {
	case msg := <-ctx.ProgressChan:
		if msg.Category != "thinking" || msg.Message != "pondering" {
			t.Errorf("progress frame = %#v, want the configured category", msg)
		}
	default:
		t.Error("nothing was pushed onto the progress channel")
	}
}

// A transform is the "how it looks" step: template renders straight to a string, and the value is visible under
// the variable's own name.
func TestFireOutputBindingTemplateTransform(t *testing.T) {
	binding := &flow.OutputBinding{
		Channel:   "result",
		Transform: &flow.BindingTransform{Lang: "template", Script: "<{{ .input.output }}>"},
	}
	nr, ctx := newBindingResult(map[string][]*flow.OutputBinding{"output": {binding}})

	if err := fireOutputBinding(nr, "output", "x"); err != nil {
		t.Fatalf("fireOutputBinding = %v", err)
	}
	if len(ctx.ResultMsgs) != 1 || ctx.ResultMsgs[0] != "<x>" {
		t.Errorf("ResultMsgs = %v, want the transformed text", ctx.ResultMsgs)
	}
}

// A transform that fails must fail the write (and be logged), not silently fall back to the raw value.
func TestFireOutputBindingTransformFailure(t *testing.T) {
	binding := &flow.OutputBinding{
		Channel:   "result",
		Transform: &flow.BindingTransform{Lang: "template", Script: "{{"},
	}
	nr, ctx := newBindingResult(map[string][]*flow.OutputBinding{"output": {binding}})

	if err := fireOutputBinding(nr, "output", "x"); err == nil {
		t.Fatal("fireOutputBinding must return the transform error")
	}
	if len(ctx.ResultMsgs) != 0 {
		t.Errorf("ResultMsgs = %v, want none when the transform failed", ctx.ResultMsgs)
	}
	if !strings.Contains(strings.Join(nr.RunLogs, "\n"), "binding transform error") {
		t.Errorf("logs = %v, want the failure to be logged", nr.RunLogs)
	}
}

// One variable may be bound to both channels at once: both fire, in declaration order.
func TestFireOutputBindingMultipleChannels(t *testing.T) {
	bindings := []*flow.OutputBinding{
		{Channel: "result"},
		{Channel: "progress"},
	}
	nr, ctx := newBindingResult(map[string][]*flow.OutputBinding{"output": bindings})

	if err := fireOutputBinding(nr, "output", "both"); err != nil {
		t.Fatalf("fireOutputBinding = %v", err)
	}
	if len(ctx.ResultMsgs) != 1 || len(ctx.ProgressMsgs) != 1 {
		t.Errorf("result = %v progress = %v, want one entry on each", ctx.ResultMsgs, ctx.ProgressMsgs)
	}
}

func TestFireOutputBindingNoBinding(t *testing.T) {
	nr, ctx := newBindingResult(nil)
	if err := fireOutputBinding(nr, "output", "x"); err != nil {
		t.Fatalf("fireOutputBinding without bindings = %v", err)
	}
	if len(ctx.ResultMsgs) != 0 || len(ctx.ProgressMsgs) != 0 {
		t.Errorf("result = %v progress = %v, want nothing emitted", ctx.ResultMsgs, ctx.ProgressMsgs)
	}

	// A declared but empty list, and an unknown channel, are both no-ops.
	nr, ctx = newBindingResult(map[string][]*flow.OutputBinding{
		"empty":   {},
		"unknown": {{Channel: "nowhere"}},
	})
	if err := fireOutputBinding(nr, "empty", "x"); err != nil {
		t.Fatalf("empty binding list = %v", err)
	}
	if err := fireOutputBinding(nr, "unknown", "x"); err != nil {
		t.Fatalf("unknown channel = %v", err)
	}
	if len(ctx.ResultMsgs) != 0 || len(ctx.ProgressMsgs) != 0 {
		t.Errorf("result = %v progress = %v, want nothing emitted", ctx.ResultMsgs, ctx.ProgressMsgs)
	}
}

// The batch form fires by root variable name: a write to "response.meta.title" fires the binding declared on
// "response".
func TestFireOutputBindingsUsesRootName(t *testing.T) {
	nr, ctx := newBindingResult(map[string][]*flow.OutputBinding{
		"response": {{Channel: "result"}},
	})
	if err := fireOutputBindings(nr, map[string]any{"response.meta.title": "hi"}); err != nil {
		t.Fatalf("fireOutputBindings = %v", err)
	}
	if len(ctx.ResultMsgs) != 1 || ctx.ResultMsgs[0] != "hi" {
		t.Errorf("ResultMsgs = %v, want the batch value", ctx.ResultMsgs)
	}
}

func TestValueToString(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"string passes through", "x", "x"},
		{"nil is empty", nil, ""},
		{"json.Number keeps its digits", json.Number("42"), "42"},
		{"numbers are formatted", 42, "42"},
		{"maps are marshalled", map[string]any{"a": 1}, `{"a":1}`},
		{"slices are marshalled", []any{1, 2}, "[1,2]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valueToString(tc.value); got != tc.want {
				t.Errorf("valueToString(%#v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestResultToString(t *testing.T) {
	if got := resultToString("x"); got != "x" {
		t.Errorf("resultToString(string) = %q", got)
	}
	if got := resultToString(nil); got != "" {
		t.Errorf("resultToString(nil) = %q, want empty", got)
	}
	if got := resultToString(json.Number("1")); got != "1" {
		t.Errorf("resultToString(number) = %q, want 1", got)
	}
}

// The registry is the palette's source of truth: every shipped node type must be resolvable, and the capability
// dump must carry the global error codes next to the per-type entries.
func TestRegistry(t *testing.T) {
	types := RegisteredTypes()
	if len(types) == 0 {
		t.Fatal("the registry is empty: init() did not register anything")
	}
	for i := 1; i < len(types); i++ {
		if types[i-1] > types[i] {
			t.Fatalf("RegisteredTypes is not sorted: %v", types)
		}
	}

	for _, want := range []string{"code", "result", "progress", "branch", "loop", "fork", "wait", "assign", "exit"} {
		exec, ok := GetExecutor(want)
		if !ok || exec == nil {
			t.Errorf("the node type %q has no executor", want)
		}
	}
	if exec, ok := GetExecutor("no-such-type"); ok || exec != nil {
		t.Errorf("GetExecutor(no-such-type) = %v, %v, want nil, false", exec, ok)
	}

	caps := GetNodeCapabilities()
	if _, ok := caps["all_error_codes"]; !ok {
		t.Error("the capability dump is missing all_error_codes")
	}
	for _, typ := range types {
		if _, ok := caps[typ].(NodeCapabilities); !ok {
			t.Errorf("the capability dump for %q is %T, want NodeCapabilities", typ, caps[typ])
		}
	}
}

func TestOutputNames(t *testing.T) {
	got := outputNames(map[string]string{"b": "string", "a": "number"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("outputNames = %v, want a sorted pair", got)
	}
	if got := outputNames(nil); len(got) != 0 {
		t.Errorf("outputNames(nil) = %v, want empty", got)
	}
}

func newBindingResult(bindings map[string][]*flow.OutputBinding) (*flow.NodeResult, *flow.ExecContext) {
	ctx := newExecContext()
	nr := newExecResult(newExecNode("2", "code", map[string]any{}), ctx)
	nr.OutputBindings = bindings
	return nr, ctx
}

func boolPtr(v bool) *bool { return &v }
