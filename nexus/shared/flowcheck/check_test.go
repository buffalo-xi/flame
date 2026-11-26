package flowcheck

import (
	"encoding/json"
	"strings"
	"testing"
)

func codes(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Severity+":"+f.Code)
	}
	return out
}

func has(fs []Finding, severity, code string) bool {
	for _, f := range fs {
		if f.Severity == severity && f.Code == code {
			return true
		}
	}
	return false
}

// A wrong-shape block is refused; a missing required key is only a warning, and the message stays what create_flow has
// always returned (the strings are part of the tool's output contract).
func TestStepSplit(t *testing.T) {
	if _, err := Step("code", map[string]any{"code": "nope"}, nil); err == nil {
		t.Fatal("a non-object behaviour block must be refused")
	}
	if _, err := Step("loop", map[string]any{"loop": map[string]any{"type": "until"}}, nil); err == nil {
		t.Fatal("an unknown loop.type must be refused")
	}

	fs, err := Step("code", map[string]any{"code": map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("Step = %v", err)
	}
	if len(fs) != 2 || !has(fs, SeverityWarning, CodeIncomplete) {
		t.Fatalf("want two completeness warnings, got %v", codes(fs))
	}
	if !strings.Contains(fs[0].Message, "code.script") && !strings.Contains(fs[1].Message, "code.script") {
		t.Errorf("the script warning lost its message: %v", fs)
	}
}

// nil langs means "unknown", so the language check is skipped rather than guessed.
func TestStepLangUnknown(t *testing.T) {
	cfg := map[string]any{"code": map[string]any{"script": "return {}", "lang": "ruby"}}
	if fs, _ := Step("code", cfg, nil); has(fs, SeverityWarning, CodeUnsupportedLang) {
		t.Fatalf("unknown langs must skip the language check, got %v", codes(fs))
	}
	fs, _ := Step("code", cfg, []string{"lua", "python"})
	if !has(fs, SeverityWarning, CodeUnsupportedLang) {
		t.Fatalf("a language outside the list must be reported, got %v", codes(fs))
	}
}

func checkDiagram(t *testing.T, body string) []Finding {
	t.Helper()
	fs, err := Diagram(json.RawMessage(body), nil)
	if err != nil {
		t.Fatalf("Diagram = %v", err)
	}
	return fs
}

// The canvas saves a diagram as a JSON string; both shapes must reach the same checks.
func TestDiagramAcceptsBothShapes(t *testing.T) {
	obj := `{"nodes":[{"id":"1","type":"start"}],"edges":[]}`
	str, _ := json.Marshal(obj)
	if fs := checkDiagram(t, obj); len(fs) != 0 {
		t.Fatalf("clean object: %v", codes(fs))
	}
	if fs := checkDiagram(t, string(str)); len(fs) != 0 {
		t.Fatalf("clean string: %v", codes(fs))
	}
}

// Without an entry child a container reports "body entry node not found" only at run time.
func TestContainerNeedsEntry(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{"loop":{"type":"count","total_times":2}}}}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`)
	if !has(fs, SeverityError, CodeContainerEntry) {
		t.Fatalf("a loop without an entry must be an error, got %v", codes(fs))
	}
}

// A second outgoing edge on an ordinary node is a run-time config error; a branch may have several.
func TestMultiOutgoing(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"code","data":{"config":{"code":{"script":"return {}"}}}},
		{"id":"3","type":"code","data":{"config":{"code":{"script":"return {}"}}}},
		{"id":"4","type":"result","data":{"config":{"code":{"lang":"template","script":"x"}}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"2","target":"3"},
		{"id":"e3","source":"2","target":"4"}
	]}`)
	if !has(fs, SeverityError, CodeMultiOutgoing) {
		t.Fatalf("two outgoing edges on a code node must be an error, got %v", codes(fs))
	}
}

// An edge that crosses a container boundary is rejected by the canvas as well.
func TestCrossParentEdge(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"process","data":{"config":{}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","data":{"config":{"code":{"script":"return {}"}}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e3","source":"3","target":"4"}
	]}`)
	if !has(fs, SeverityError, CodeCrossParent) {
		t.Fatalf("an edge leaving a container must be an error, got %v", codes(fs))
	}
}

// A label is a valid reference form, so call.target written as a label must resolve.
func TestCallTargetAcceptsLabelAndRejectsNonFunction(t *testing.T) {
	ok := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"label":"add"}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"call","data":{"label":"use","config":{"target":"add"}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"2","target":"4"}
	]}`)
	if has(ok, SeverityError, CodeCallTarget) {
		t.Fatalf("a label pointing at a function must resolve, got %v", codes(ok))
	}

	// Two edges out of start would also be reported; only the call finding matters here.
	bad := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"code","data":{"label":"fetch","config":{"code":{"script":"return {}"}}}},
		{"id":"4","type":"call","data":{"config":{"target":"fetch"}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e3","source":"2","target":"4"}
	]}`)
	if !has(bad, SeverityError, CodeCallTarget) {
		t.Fatalf("calling a code node must be an error, got %v", codes(bad))
	}
}

// A dangling source_ref silently yields an empty value instead of failing loudly.
func TestDanglingSourceRef(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"code","data":{"config":{
			"code":{"script":"return {}"},
			"inputs":{"x":{"type":"string","source_type":"node","source_ref":"99","source_key":"body"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`)
	if !has(fs, SeverityError, CodeDanglingRef) {
		t.Fatalf("source_ref 99 must be reported, got %v", codes(fs))
	}
	for _, f := range fs {
		if f.Code == CodeDanglingRef && f.Path != "inputs.x" {
			t.Errorf("path = %q, want inputs.x", f.Path)
		}
	}
}

// each-mode reads the array from config.inputs by name; a typo is a run-time failure.
func TestLoopEachSourceKey(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{"loop":{"type":"each","source_key":"rows"},"inputs":{}}}},
		{"id":"3","type":"entry","parent_node":"2"}
	],"edges":[
		{"id":"e1","source":"1","target":"2"}
	]}`)
	if !has(fs, SeverityError, CodeLoopSource) {
		t.Fatalf("an undeclared source_key must be an error, got %v", codes(fs))
	}
}

// Findings are ordered so two runs over the same diagram report identically.
func TestFindingsAreStable(t *testing.T) {
	body := `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"wait","data":{"config":{"fork_node_id":"ghost"}}},
		{"id":"3","type":"mystery"}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`
	first := checkDiagram(t, body)
	second := checkDiagram(t, body)
	if len(first) != len(second) {
		t.Fatalf("len = %d, want %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("finding %d differs between runs: %v vs %v", i, first[i], second[i])
		}
	}
	if !has(first, SeverityError, CodeWaitFork) || !has(first, SeverityError, CodeUnknownType) {
		t.Fatalf("want wait_fork + unknown_type, got %v", codes(first))
	}
}

// The runner drops an action the node does not declare, with one log line - so on_error looks configured and does
// nothing. intent declares retry only.
func TestOnErrorActionNotDeclared(t *testing.T) {
	fs, _ := Step("intent", map[string]any{
		"children": []any{map[string]any{"id": "a"}},
		"on_error": map[string]any{"skip": map[string]any{"codes": []any{"timeout"}}},
	}, nil)
	if !has(fs, SeverityWarning, CodeOnErrorAction) {
		t.Fatalf("skip on intent must be reported, got %v", codes(fs))
	}
}

// A code that does not exist can never match: an error, not a style note.
func TestOnErrorUnknownCode(t *testing.T) {
	fs, _ := Step("http", map[string]any{
		"http":     map[string]any{"url": "https://example.com"},
		"on_error": map[string]any{"retry": map[string]any{"codes": []any{"timeuot"}}},
	}, nil)
	if !has(fs, SeverityError, CodeOnErrorCode) {
		t.Fatalf("a misspelled code must be an error, got %v", codes(fs))
	}
}

// A real code the node type cannot produce is legal config that simply never fires.
func TestOnErrorCodeNotProduced(t *testing.T) {
	fs, _ := Step("code", map[string]any{
		"code":     map[string]any{"script": "return {}"},
		"outputs":  map[string]any{},
		"on_error": map[string]any{"retry": map[string]any{"codes": []any{"auth"}}},
	}, nil)
	if !has(fs, SeverityWarning, CodeOnErrorCode) {
		t.Fatalf("auth on a code node must be reported, got %v", codes(fs))
	}
}

func TestOnErrorPostUnsupported(t *testing.T) {
	fs, _ := Step("result", map[string]any{
		"code":     map[string]any{"lang": "template", "script": "x"},
		"on_error": map[string]any{"post": map[string]any{"set_outputs": map[string]any{}}},
	}, nil)
	if !has(fs, SeverityWarning, CodeOnErrorPost) {
		t.Fatalf("post on result must be reported, got %v", codes(fs))
	}
}

// A correct block produces nothing: false positives are what teach an author to ignore the checker.
func TestOnErrorValidPasses(t *testing.T) {
	fs, _ := Step("http", map[string]any{
		"http": map[string]any{
			"url":       "https://example.com",
			"handler":   map[string]any{"script": "return {}"},
			"timeout":   map[string]any{"enabled": true, "seconds": 5},
			"method":    "GET",
			"body":      "",
			"params":    []any{},
			"headers":   []any{},
			"body_type": "json",
		},
		"on_error": map[string]any{
			"retry": map[string]any{"codes": []any{"timeout", "connection"}, "max": float64(3)},
			"post":  map[string]any{"set_outputs": map[string]any{}},
		},
	}, nil)
	for _, f := range fs {
		switch f.Code {
		case CodeOnErrorAction, CodeOnErrorCode, CodeOnErrorPost:
			t.Fatalf("a valid on_error block must not be reported: %v", f)
		}
	}
}

func TestEmptyDiagramIsAnError(t *testing.T) {
	if _, err := Diagram(nil, nil); err == nil {
		t.Fatal("an empty diagram must be refused, not reported as clean")
	}
}
