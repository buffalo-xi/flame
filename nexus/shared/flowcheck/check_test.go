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

// A wrong-shape block is refused; a missing script is an error (the step cannot run), while a missing outputs block is
// only a warning (it runs, it just loses what the script returns). The messages stay what create_flow has always
// returned (the strings are part of the tool's output contract).
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
	if len(fs) != 2 {
		t.Fatalf("want one error and one warning, got %v", codes(fs))
	}
	if !has(fs, SeverityError, CodeIncomplete) || !has(fs, SeverityWarning, CodeOutputs) {
		t.Fatalf("a missing script must be an error and a missing outputs only a warning, got %v", codes(fs))
	}
	if !strings.Contains(fs[0].Message, "code.script") && !strings.Contains(fs[1].Message, "code.script") {
		t.Errorf("the script finding lost its message: %v", fs)
	}
}

// A node dropped on the canvas has no config at all, and that is not an exemption: a code node without a script cannot run
// whether the config block is missing or merely empty. A node type with nothing to configure stays silent.
func TestStepJudgesEmptyConfig(t *testing.T) {
	fs, err := Step("code", nil, nil)
	if err != nil {
		t.Fatalf("Step = %v", err)
	}
	if !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("a code node with no config has no script and must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("start", nil, nil); len(fs) != 0 {
		t.Fatalf("start has nothing to configure, got %v", codes(fs))
	}
}

// nil langs means "unknown", so the language check is skipped rather than guessed.
func TestStepLangUnknown(t *testing.T) {
	cfg := map[string]any{"code": map[string]any{"script": "return {}", "lang": "ruby"}}
	if fs, _ := Step("code", cfg, nil); has(fs, SeverityWarning, CodeUnsupportedLang) {
		t.Fatalf("unknown langs must skip the language check, got %v", codes(fs))
	}
	fs, _ := Step("code", cfg, []string{"lua", "python"})
	if !has(fs, SeverityError, CodeUnsupportedLang) {
		t.Fatalf("a language outside the list must be reported, got %v", codes(fs))
	}
}

// The text nodes are script nodes too: without config.code the executor fails before it can emit anything, so the missing
// key is an error even though the node has nothing else to configure.
func TestStepTextNodesNeedScript(t *testing.T) {
	for _, typ := range []string{"result", "progress"} {
		if fs, _ := Step(typ, nil, nil); !has(fs, SeverityError, CodeIncomplete) {
			t.Errorf("%s without config must be an error, got %v", typ, codes(fs))
		}
		if fs, _ := Step(typ, map[string]any{"code": map[string]any{}}, nil); !has(fs, SeverityError, CodeIncomplete) {
			t.Errorf("%s without a script must be an error, got %v", typ, codes(fs))
		}
		if fs, _ := Step(typ, map[string]any{"code": map[string]any{"script": "x"}}, nil); len(fs) != 0 {
			t.Errorf("%s with a script must be silent, got %v", typ, codes(fs))
		}
	}
}

// The connection blocks are required before any network happens: an empty id is a config error at run time, not
// "no data yet", so it belongs to the save-time check.
func TestStepMcpAndRpcConnections(t *testing.T) {
	if fs, _ := Step("mcp", nil, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Errorf("mcp without config.mcp must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("mcp", map[string]any{"mcp": map[string]any{}}, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Errorf("mcp without a connection must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("mcp", map[string]any{"mcp": map[string]any{"connection_id": "c", "tool_name": "t"}}, nil); len(fs) != 0 {
		t.Errorf("a complete mcp config must be silent, got %v", codes(fs))
	}

	// rpc switches on target_type; an empty value is the legacy "connection" mode, so only "endpoint" reads rpc.endpoint.
	if fs, _ := Step("rpc", map[string]any{"rpc": map[string]any{"target_type": "endpoint"}}, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Errorf("an endpoint rpc without rpc.endpoint must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("rpc", map[string]any{"rpc": map[string]any{"target_type": "endpoint", "endpoint": "e"}}, nil); len(fs) != 0 {
		t.Errorf("a complete endpoint rpc must be silent, got %v", codes(fs))
	}
	if fs, _ := Step("rpc", map[string]any{"rpc": map[string]any{"endpoint": "e"}}, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Errorf("an empty target_type means connection, so a bare endpoint is not enough, got %v", codes(fs))
	}
}

// An assign with no assignments reports success and writes nothing: a warning with its own code, because the author has
// to be told the step is a no-op rather than that a key is missing.
func TestStepAssignNoOp(t *testing.T) {
	fs, _ := Step("assign", map[string]any{"assignments": []any{}}, nil)
	if !has(fs, SeverityWarning, CodeNoOp) {
		t.Fatalf("an assign with no assignments must be a warning, got %v", codes(fs))
	}
	if fs, _ := Step("assign", map[string]any{"assignments": []any{
		map[string]any{"kind": "output", "target": "x", "value": "1"},
	}}, nil); len(fs) != 0 {
		t.Fatalf("a real assignment must be silent, got %v", codes(fs))
	}
}

// Both executors read response_mode on the path to the call: an unknown value is a guaranteed failure, and in the http
// case the request has already been sent by the time it is read.
func TestStepResponseMode(t *testing.T) {
	fs, _ := Step("http", map[string]any{
		"http": map[string]any{"url": "https://example.com", "response_mode": "stream"},
	}, nil)
	if !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("an unknown http response_mode must be an error, got %v", codes(fs))
	}

	llm := func(mode string) map[string]any {
		return map[string]any{"llm": map[string]any{
			"messages":      []any{map[string]any{"role": "user", "content": "x"}},
			"response_mode": mode,
		}}
	}
	if fs, _ := Step("llm", llm("chunked"), nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("an unknown llm response_mode must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("llm", llm("sse_stream"), nil); len(fs) != 0 {
		t.Fatalf("a valid response_mode must be silent, got %v", codes(fs))
	}
}

// An llm step without messages fails before the model is resolved, so it is knowable without a database.
func TestStepLLMNeedsMessages(t *testing.T) {
	fs, _ := Step("llm", map[string]any{"llm": map[string]any{}}, nil)
	if !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("an llm step without messages must be an error, got %v", codes(fs))
	}
	for _, f := range fs {
		if f.Code == CodeIncomplete && f.Path != "llm.messages" {
			t.Errorf("path = %q, want llm.messages", f.Path)
		}
	}
}

// The loop dispatches on loop.type (absent means "unknown loop type") and refuses a non-numeric count before the first
// iteration; a missing count is legal and means the system ceiling.
func TestStepLoopTypeAndCount(t *testing.T) {
	if fs, _ := Step("loop", map[string]any{"loop": map[string]any{}}, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("a loop without loop.type must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("loop", map[string]any{"loop": map[string]any{"type": "count", "total_times": "5"}}, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("a string total_times must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("loop", map[string]any{"loop": map[string]any{"type": "count"}}, nil); len(fs) != 0 {
		t.Fatalf("a missing total_times means the system ceiling, got %v", codes(fs))
	}
	if fs, _ := Step("loop", map[string]any{"loop": map[string]any{"type": "while", "max_times": "x"}}, nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("a string max_times must be an error, got %v", codes(fs))
	}
}

// An assign list is all-or-nothing: one malformed entry is refused before any assignment lands.
func TestStepAssignItems(t *testing.T) {
	one := func(item any) map[string]any {
		return map[string]any{"assignments": []any{item}}
	}
	if fs, _ := Step("assign", one(map[string]any{"kind": "variable", "target": "x", "value": "1"}), nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("an unknown kind must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("assign", one(map[string]any{"kind": "output"}), nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("a missing target must be an error, got %v", codes(fs))
	}
	if fs, _ := Step("assign", one("x"), nil); !has(fs, SeverityError, CodeIncomplete) {
		t.Fatalf("a non-object assignment must be an error, got %v", codes(fs))
	}
}

// The runner skips a branch child without a condition, so that path never runs: a warning, not an error. Intent children
// are classified by label / description instead and must stay silent.
func TestStepBranchChildNeedsCondition(t *testing.T) {
	fs, _ := Step("branch", map[string]any{"children": []any{
		map[string]any{"id": "c1", "condition": "return true"},
		map[string]any{"id": "c2"},
	}}, nil)
	if !has(fs, SeverityWarning, CodeBranchCondition) {
		t.Fatalf("a branch child without a condition must be a warning, got %v", codes(fs))
	}
	for _, f := range fs {
		if f.Code == CodeBranchCondition && f.Path != "children.c2.condition" {
			t.Errorf("path = %q, want children.c2.condition", f.Path)
		}
	}
	if fs, _ := Step("intent", map[string]any{"children": []any{
		map[string]any{"id": "i1", "label": "refund", "description": "a refund request"},
	}}, nil); has(fs, SeverityWarning, CodeBranchCondition) {
		t.Fatalf("intent children carry no condition and must not be reported: %v", codes(fs))
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

// A break / continue is consumed by the innermost loop. Outside one the run fails, and a function body counts as outside
// even when the call site sits in a loop, because the function is a separate invocation. A process forwards the signal,
// so its body stays in scope.
func TestControlSignalScope(t *testing.T) {
	top := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"break"}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`)
	if !has(top, SeverityError, CodeControlScope) {
		t.Fatalf("a top-level break must be an error, got %v", codes(top))
	}

	inLoop := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{"loop":{"type":"count","total_times":2}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"continue","parent_node":"2"}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"}]}`)
	if has(inLoop, SeverityError, CodeControlScope) {
		t.Fatalf("a continue inside a loop body must pass, got %v", codes(inLoop))
	}

	inProcess := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{"loop":{"type":"count","total_times":2}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"process","parent_node":"2"},
		{"id":"5","type":"entry","parent_node":"4"},
		{"id":"6","type":"break","parent_node":"4"}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"3","target":"4"},
		{"id":"e3","source":"5","target":"6"}
	]}`)
	if has(inProcess, SeverityError, CodeControlScope) {
		t.Fatalf("a process forwards the signal to its loop and must pass, got %v", codes(inProcess))
	}

	inFunction := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function"},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"break","parent_node":"2"}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"}]}`)
	if !has(inFunction, SeverityError, CodeControlScope) {
		t.Fatalf("a break in a function body must be an error, got %v", codes(inFunction))
	}

	nested := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function"},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"loop","parent_node":"2","data":{"config":{"loop":{"type":"count","total_times":2}}}},
		{"id":"5","type":"entry","parent_node":"4"},
		{"id":"6","type":"break","parent_node":"4"}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"3","target":"4"},
		{"id":"e3","source":"5","target":"6"}
	]}`)
	if has(nested, SeverityError, CodeControlScope) {
		t.Fatalf("a loop inside a function owns its own break and must pass, got %v", codes(nested))
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
	if !has(fs, SeverityWarning, CodeOnErrorCodeUnused) {
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
