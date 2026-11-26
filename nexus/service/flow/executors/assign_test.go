package executors

import (
	"encoding/json"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// assign renders each value as a Go template and lands it on a declared variable, converting the text by the
// declaration's type - all in pure Go, so the executor needs no sandbox at all.
func TestAssignExecutorWritesOutputs(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"outputs": map[string]any{
			"greeting": map[string]any{"type": "string"},
			"count":    map[string]any{"type": "number"},
			"done":     map[string]any{"type": "boolean"},
		},
		"assignments": []any{
			map[string]any{"kind": "output", "target": "greeting", "value": "hello"},
			map[string]any{"kind": "output", "target": "count", "value": "42"},
			map[string]any{"kind": "output", "target": "done", "value": "true"},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if nr.Status != flow.StatusSuccess {
		t.Fatalf("status = %v, want success", nr.Status)
	}
	if got, _ := nr.OutputVars["greeting"].(string); got != "hello" {
		t.Errorf("greeting = %v, want hello", nr.OutputVars["greeting"])
	}
	if got, _ := nr.OutputVars["count"].(json.Number); string(got) != "42" {
		t.Errorf("count = %#v, want json.Number 42", nr.OutputVars["count"])
	}
	if got, _ := nr.OutputVars["done"].(bool); !got {
		t.Errorf("done = %#v, want true", nr.OutputVars["done"])
	}
}

// A sub-path goes through SetVarByPath, so the declaration's type decides the separator: an object gets
// "target.path", an array gets "target[0]".
func TestAssignExecutorWritesOutputSubPath(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"outputs": map[string]any{"response": map[string]any{"type": "object"}},
		"assignments": []any{
			map[string]any{"kind": "output", "target": "response", "path": "meta.title", "value": "hi"},
		},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	meta, _ := nr.OutputVars["response"].(map[string]any)["meta"].(map[string]any)
	if got, _ := meta["title"].(string); got != "hi" {
		t.Errorf("response.meta.title = %#v, want hi", nr.OutputVars["response"])
	}
}

// A target writes a variable outside the node; the sub-path is appended to the target's source_key rather than
// to the name, because the target definition already carries the base path.
func TestAssignExecutorWritesTargetSubPath(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"targets": map[string]any{
			"report": map[string]any{"source_type": "flow", "source_key": "report"},
		},
		"assignments": []any{
			map[string]any{"kind": "target", "target": "report", "path": "title", "value": "quarterly"},
		},
	})
	ctx := newExecContext()
	ctx.RunFlowVars = map[string]any{}
	ctx.Flow = &flow.Flow{NodeMap: map[string]*flow.Node{"2": node}}
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	report, _ := ctx.RunFlowVars["report"].(map[string]any)
	if got, _ := report["title"].(string); got != "quarterly" {
		t.Errorf("flow var report = %#v, want title quarterly", ctx.RunFlowVars["report"])
	}
}

// No assignments block (or an empty one) is a legal no-op: the node succeeds and writes nothing.
func TestAssignExecutorWithoutAssignmentsSucceeds(t *testing.T) {
	for _, cfg := range []map[string]any{
		{},
		{"assignments": []any{}},
	} {
		node := newExecNode("2", "assign", cfg)
		ctx := newExecContext()
		nr := newExecResult(node, ctx)
		if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err != nil {
			t.Fatalf("Execute(%v) = %v (code %q)", cfg, err, nr.ErrCode)
		}
		if nr.Status != flow.StatusSuccess {
			t.Errorf("Execute(%v) status = %v, want success", cfg, nr.Status)
		}
	}
}

func TestAssignExecutorMissingConfigFails(t *testing.T) {
	node := &flow.Node{ID: "2", Type: "assign"}
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail without node data")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
}

func TestAssignExecutorInvalidAssignmentsFail(t *testing.T) {
	cases := []struct {
		name        string
		assignments []any
		wantMsg     string
	}{
		{"not a map", []any{"oops"}, "not a map"},
		{"missing kind", []any{map[string]any{"target": "a", "value": "1"}}, "kind and target are required"},
		{"invalid kind", []any{map[string]any{"kind": "local", "target": "a", "value": "1"}}, "invalid kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newExecNode("2", "assign", map[string]any{
				"outputs":     map[string]any{"a": map[string]any{"type": "string"}},
				"assignments": tc.assignments,
			})
			ctx := newExecContext()
			nr := newExecResult(node, ctx)
			if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err == nil {
				t.Fatal("Execute must fail for a malformed assignment")
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

// A broken template is a script error, not a config error: the assignment itself is well formed, the value
// just does not render.
func TestAssignExecutorTemplateErrorFails(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"outputs":     map[string]any{"a": map[string]any{"type": "string"}},
		"assignments": []any{map[string]any{"kind": "output", "target": "a", "value": "{{"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for a template that does not parse")
	}
	if nr.ErrCode != flow.ErrCodeScript {
		t.Errorf("ErrCode = %q, want script", nr.ErrCode)
	}
}

// Writing a variable the node never declared is a var error: the engine refuses to invent output variables at
// run time.
func TestAssignExecutorUndeclaredOutputFails(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"outputs":     map[string]any{"a": map[string]any{"type": "string"}},
		"assignments": []any{map[string]any{"kind": "output", "target": "b", "value": "x"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail for an undeclared output")
	}
	if nr.ErrCode != flow.ErrCodeVar {
		t.Errorf("ErrCode = %q, want var", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "output var b not defined") {
		t.Errorf("ErrMsg = %q, want it to name the variable", nr.ErrMsg)
	}
}

// A target requires the flow's node map (the definition lives in the node config), which a bare executor test
// does not have; the reader reports that as a config error rather than panicking.
func TestAssignExecutorTargetWithoutFlowFails(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"targets":     map[string]any{"rate": map[string]any{"source_type": "flow", "source_key": "rate"}},
		"assignments": []any{map[string]any{"kind": "target", "target": "rate", "value": "1"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	if err := (&AssignExecutor{}).Execute(node, ctx, "1", nr); err == nil {
		t.Fatal("Execute must fail when the flow node map is missing")
	}
	if nr.ErrCode != flow.ErrCodeConfig {
		t.Errorf("ErrCode = %q, want config", nr.ErrCode)
	}
	if !strings.Contains(nr.ErrMsg, "target rate") {
		t.Errorf("ErrMsg = %q, want it to name the target", nr.ErrMsg)
	}
}

// convertByType follows the declaration, not the rendered text: a number declaration that does not parse
// still becomes a json.Number (the write is what decides), and an unparsable boolean stays the raw string so
// the author sees it rather than a silent false.
func TestConvertByType(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"outputs": map[string]any{
			"num":  map[string]any{"type": "number"},
			"flag": map[string]any{"type": "boolean"},
			"text": map[string]any{"type": "string"},
		},
	})
	cases := []struct {
		target   string
		rendered string
		want     any
	}{
		{"num", "42", json.Number("42")},
		{"num", "not-a-number", json.Number("not-a-number")},
		{"flag", "TRUE", true},
		{"flag", "1", true},
		{"flag", "0", false},
		{"flag", "", false},
		{"flag", "maybe", "maybe"},
		{"text", "plain", "plain"},
	}
	for _, tc := range cases {
		got := convertByType(tc.rendered, Assignment{Kind: "output", Target: tc.target}, node)
		if got != tc.want {
			t.Errorf("convertByType(%q, %s) = %#v, want %#v", tc.rendered, tc.target, got, tc.want)
		}
	}
}

func TestBuildFullPath(t *testing.T) {
	node := newExecNode("2", "assign", map[string]any{
		"outputs": map[string]any{
			"items": map[string]any{"type": "array[string]"},
			"obj":   map[string]any{"type": "object"},
		},
	})
	cases := []struct {
		name string
		a    Assignment
		want string
	}{
		{"no path", Assignment{Kind: "output", Target: "obj"}, "obj"},
		{"object path", Assignment{Kind: "output", Target: "obj", Path: "meta.title"}, "obj.meta.title"},
		{"array by declaration", Assignment{Kind: "output", Target: "items", Path: "[0]"}, "items[0]"},
		{"array by path prefix", Assignment{Kind: "target", Target: "items", Path: "[0].name"}, "items[0].name"},
		{"target object path", Assignment{Kind: "target", Target: "obj", Path: "title"}, "obj.title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildFullPath(tc.a, node); got != tc.want {
				t.Errorf("buildFullPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// parseAssignments is the config contract: an entry without kind/target, or with a kind outside
// output/target, is rejected by index so the author can find it.
func TestParseAssignments(t *testing.T) {
	got, err := parseAssignments([]any{
		map[string]any{"kind": "output", "target": "a", "path": "x", "value": "1"},
	})
	if err != nil {
		t.Fatalf("parseAssignments = %v", err)
	}
	want := Assignment{Kind: "output", Target: "a", Path: "x", Value: "1"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("parseAssignments = %#v, want %#v", got, []Assignment{want})
	}

	if _, err := parseAssignments([]any{map[string]any{"kind": "output"}}); err == nil ||
		!strings.Contains(err.Error(), "assignment[0]") {
		t.Errorf("parseAssignments error = %v, want it to name the index", err)
	}
}
