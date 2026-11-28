package flowcheck

import (
	"encoding/json"
	"fmt"
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

// The condition text used to be unchecked: a bare expression (no return) and a name config.inputs never declares both
// passed with 0 findings and then failed the node on its first evaluation - lua reports a syntax error for the first, the
// undeclared name is nil in the comparison for the second.
func TestStepBranchConditionScripts(t *testing.T) {
	score := map[string]any{"score": map[string]any{"type": "number", "source_type": "flow", "source_key": "score"}}
	cases := []struct {
		name      string
		lang      string
		condition string
		inputs    map[string]any
		wantSev   string // "" = no condition finding at all
		wantCode  string
	}{
		{
			name: "lua bare expression", condition: "input.score > 90", inputs: score,
			wantSev: SeverityError, wantCode: CodeConditionReturn,
		},
		{
			name: "lua with return", condition: "return input.score > 90", inputs: score,
		},
		{
			// Valid js that returns nothing: the node runs, the condition is just never true.
			name: "js returns nothing", lang: "js", condition: "input.score > 90", inputs: score,
			wantSev: SeverityWarning, wantCode: CodeConditionReturn,
		},
		{
			name: "python __return__", lang: "python", condition: "__return__ = input.score > 90", inputs: score,
		},
		{
			name: "undeclared input", condition: "return input.missing > 0", inputs: score,
			wantSev: SeverityError, wantCode: CodeConditionInput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, err := Step("branch", map[string]any{
				"branch":   map[string]any{"lang": tc.lang},
				"children": []any{map[string]any{"id": "c1", "condition": tc.condition}},
				"inputs":   tc.inputs,
			}, nil)
			if err != nil {
				t.Fatalf("Step = %v", err)
			}
			if tc.wantCode == "" {
				if len(fs) != 0 {
					t.Fatalf("codes = %v, want no finding", codes(fs))
				}
				return
			}
			if !has(fs, tc.wantSev, tc.wantCode) {
				t.Fatalf("codes = %v, want %s:%s", codes(fs), tc.wantSev, tc.wantCode)
			}
		})
	}
}

// GetVariables resolves the whole input block at once, so one row that cannot be resolved fails the step before it runs:
// the runner never reaches "input.score + 1" with a nil, it fails with "variable source is not selected". Judged in the
// diagram check (validate_flow / pre-save), not in Step - a bare create_node fills placeholder rows on purpose.
func TestDiagramInputSourceFindings(t *testing.T) {
	cases := []struct {
		name    string
		row     string // raw JSON for inputs.x
		wantErr bool
	}{
		{name: "not an object", row: `"text"`, wantErr: true},
		{name: "no source_type", row: `{"type":"number"}`, wantErr: true},
		{name: "unknown source_type", row: `{"source_type":"vault","source_key":"k"}`, wantErr: true},
		{name: "no source_key", row: `{"source_type":"flow"}`, wantErr: true},
		{name: "unknown builtin", row: `{"source_type":"builtin","source_key":"now"}`, wantErr: true},
		{name: "node without ref", row: `{"source_type":"node","source_key":"out"}`, wantErr: true},
		{name: "complete row", row: `{"source_type":"builtin","source_key":"run_id"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := checkDiagram(t, fmt.Sprintf(`{
				"nodes": [
					{"id": "1", "type": "start", "data": {"label": "开始"}},
					{"id": "2", "type": "code", "data": {"label": "算", "config": {
						"code": {"script": "return 1", "lang": "lua"},
						"inputs": {"x": %s}
					}}}
				],
				"edges": [
					{"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
				]
			}`, tc.row))
			if got := has(fs, SeverityError, CodeInputSource); got != tc.wantErr {
				t.Fatalf("codes = %v, want input_source reported = %v", codes(fs), tc.wantErr)
			}
		})
	}
}

// The entry-argument declaration is documentation until something compares it with the diagram: a node reading a name the
// declaration does not have fails at run time with "var path X not found", and one declared but left out of required fails
// whenever a caller omits the value (nothing rejects that call at the boundary).
func TestArgumentsFindings(t *testing.T) {
	diagram := json.RawMessage(`{
		"nodes": [
			{"id": "1", "type": "start", "data": {"label": "开始"}},
			{"id": "2", "type": "code", "data": {"label": "答", "config": {
				"code": {"script": "return input.question", "lang": "lua"},
				"inputs": {"question": {"type": "string", "source_type": "argument", "source_key": "question"}}
			}}}
		],
		"edges": []
	}`)
	cases := []struct {
		name      string
		arguments string
		wantErr   bool
		wantWarn  bool
	}{
		{
			name:      "not declared",
			arguments: `{"type":"object","properties":{"other":{"type":"string"}},"required":["other"]}`,
			wantErr:   true,
		},
		{
			name:      "declared but optional",
			arguments: `{"type":"object","properties":{"question":{"type":"string"}}}`,
			wantWarn:  true,
		},
		{
			name:      "declared and required",
			arguments: `{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := Arguments(diagram, json.RawMessage(tc.arguments))
			if got := has(fs, SeverityError, CodeArgumentUndeclared); got != tc.wantErr {
				t.Fatalf("findings = %v, want argument_undeclared reported = %v", fs, tc.wantErr)
			}
			if got := has(fs, SeverityWarning, CodeArgumentOptional); got != tc.wantWarn {
				t.Fatalf("findings = %v, want argument_optional reported = %v", fs, tc.wantWarn)
			}
		})
	}
	// No declaration at all judges like an empty one: the row has nowhere to come from.
	if fs := Arguments(diagram, nil); !has(fs, SeverityError, CodeArgumentUndeclared) {
		t.Fatalf("findings = %v, want argument_undeclared when nothing is declared", fs)
	}
	// A diagram that reads no argument is left alone.
	quiet := json.RawMessage(`{"nodes":[{"id":"1","type":"start","data":{"label":"开始"}}],"edges":[]}`)
	if fs := Arguments(quiet, nil); len(fs) != 0 {
		t.Fatalf("findings = %v, want none when nothing reads an argument", fs)
	}
}

// An intent classification at a sampled temperature is not reproducible: the same input can route differently between runs.
// Judged in the diagram check (a definition about to run), not in Step, so a bare create_node stays quiet.
func TestDiagramIntentTemperature(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   bool
	}{
		{name: "unset", params: `{}`, want: true},
		{name: "zero", params: `{"temperature":0}`, want: false},
		{name: "sampled", params: `{"temperature":0.7}`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := checkDiagram(t, fmt.Sprintf(`{
				"nodes": [
					{"id": "1", "type": "start", "data": {"label": "开始"}},
					{"id": "2", "type": "intent", "data": {"label": "分流", "config": {
						"llm": {"params": %s},
						"children": [{"id": "a1", "label": "查单", "description": "check the order"}]
					}}}
				],
				"edges": [
					{"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
				]
			}`, tc.params))
			if got := has(fs, SeverityWarning, CodeIntentTemperature); got != tc.want {
				t.Fatalf("codes = %v, want intent_temperature = %v", codes(fs), tc.want)
			}
		})
	}
}

// A while / do_while condition reads a wider set than branch: the runner merges inputs -> loop vars -> outputs -> locals
// before every check, so a name that only exists in outputs must not be reported.
func TestStepLoopConditionScripts(t *testing.T) {
	fs, err := Step("loop", map[string]any{
		"loop": map[string]any{"type": "while", "condition": "input.total < 10"},
	}, nil)
	if err != nil {
		t.Fatalf("Step = %v", err)
	}
	if !has(fs, SeverityError, CodeConditionReturn) || !has(fs, SeverityError, CodeConditionInput) {
		t.Fatalf("codes = %v, want both a missing return and an undeclared input", codes(fs))
	}

	fs, err = Step("loop", map[string]any{
		"loop":    map[string]any{"type": "while", "lang": "python", "condition": "__return__ = input.total < 10"},
		"outputs": map[string]any{"total": map[string]any{"type": "number"}},
	}, nil)
	if err != nil {
		t.Fatalf("Step = %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("codes = %v, want no finding: total is declared in outputs", codes(fs))
	}
}

// A declared child with no outgoing edge used to be a warning while the run reported success having executed nothing: it is
// an error now, so validate_flow's error_count and the canvas badge both say "cannot run as written".
func TestDiagramBranchChildWithoutEdgeIsError(t *testing.T) {
	fs := checkDiagram(t, `{
		"nodes": [
			{"id": "1", "type": "start", "data": {"label": "开始"}},
			{"id": "2", "type": "branch", "data": {"label": "判断", "config": {
				"children": [{"id": "c1", "label": "是", "condition": "return true"}]
			}}}
		],
		"edges": [
			{"id": "e1", "source": "1", "target": "2", "source_handle": "source1right", "target_handle": "target2left"}
		]
	}`)
	if !has(fs, SeverityError, CodeBranchEdge) {
		t.Fatalf("codes = %v, want an error for a child with no outgoing edge", codes(fs))
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

// call.args and the target function's params must agree: the executor refuses an unknown arg and a missing required one
// before the child process starts, and `required` defaults to true.
func TestCallArgsMatchParams(t *testing.T) {
	fn := &node{ID: "2", Type: "function"}
	fn.Data.Config = map[string]any{"params": map[string]any{
		"a": map[string]any{"type": "number"},
		"b": map[string]any{"type": "number", "required": false},
	}}
	call := &node{ID: "4", Type: "call"}
	collect := func() []Finding {
		var out []Finding
		checkCallArgs(call, fn, func(severity, code, nodeID, nodeType, path, msg, hint string) {
			out = append(out, Finding{Severity: severity, Code: code, NodeID: nodeID, NodeType: nodeType, Path: path, Message: msg, Hint: hint})
		})
		return out
	}

	call.Data.Config = map[string]any{"args": map[string]any{"a": map[string]any{}}}
	if fs := collect(); len(fs) != 0 {
		t.Fatalf("a complete call must pass, got %v", codes(fs))
	}

	call.Data.Config = map[string]any{"args": map[string]any{"b": map[string]any{}}}
	fs := collect()
	if !has(fs, SeverityError, CodeCallArgs) {
		t.Fatalf("a missing required arg must be an error, got %v", codes(fs))
	}
	for _, f := range fs {
		if f.Path != "args.a" {
			t.Errorf("path = %q, want args.a", f.Path)
		}
	}

	call.Data.Config = map[string]any{"args": map[string]any{"a": map[string]any{}, "c": map[string]any{}}}
	if fs := collect(); !has(fs, SeverityError, CodeCallArgs) {
		t.Fatalf("an unknown arg must be an error, got %v", codes(fs))
	}
}

// The diagram check reaches the same comparison, so a saved flow reports it without a run.
func TestCallArgsInDiagram(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"params":{"a":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"call","data":{"config":{"target":"2"}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"2","target":"4"}]}`)
	if !has(fs, SeverityError, CodeCallArgs) {
		t.Fatalf("a call without the required arg must be reported, got %v", codes(fs))
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

// A source_key must be an output the source node really produces: a typo yields an empty value with no error at all.
// Fixed outputs (llm.content, http.body, intent.index ...) count as declared, a sub-path only needs its root, and a call
// node is read through the target function's outputs because that is what it mirrors.
func TestSourceKeyMustBeDeclared(t *testing.T) {
	bad := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"llm","data":{"config":{"llm":{"messages":[{"role":"user","content":"hi"}]}}}},
		{"id":"3","type":"code","data":{"config":{
			"code":{"script":"return {}"},
			"outputs":{"total":{"type":"number"}},
			"inputs":{"x":{"type":"string","source_type":"node","source_ref":"2","source_key":"contnet"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"2","target":"3"}]}`)
	if !has(bad, SeverityError, CodeSourceKey) {
		t.Fatalf("a misspelled source_key must be an error, got %v", codes(bad))
	}
	for _, f := range bad {
		if f.Code == CodeSourceKey && f.Path != "inputs.x.source_key" {
			t.Errorf("path = %q, want inputs.x.source_key", f.Path)
		}
	}

	ok := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"llm","data":{"config":{"llm":{"messages":[{"role":"user","content":"hi"}]}}}},
		{"id":"3","type":"code","data":{"config":{
			"code":{"script":"return {}"},
			"outputs":{"a":{"type":"string"},"b":{"type":"string"},"c":{"type":"string"}},
			"inputs":{
				"a":{"type":"string","source_type":"node","source_ref":"2","source_key":"content"},
				"b":{"type":"string","source_type":"node","source_ref":"2","source_key":"content.parts[0]"},
				"c":{"type":"string","source_type":"node","source_ref":"1","source_key":"anything"}
			}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"2","target":"3"}]}`)
	if has(ok, SeverityError, CodeSourceKey) {
		t.Fatalf("a fixed output, a sub-path and a visual node must pass, got %v", codes(ok))
	}

	call := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"outputs":{"total":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"call","data":{"config":{"target":"2"}}},
		{"id":"5","type":"code","data":{"config":{
			"code":{"script":"return {}"},
			"outputs":{"a":{"type":"number"}},
			"inputs":{"a":{"type":"number","source_type":"node","source_ref":"4","source_key":"total"}}
		}}}
	],"edges":[
		{"id":"e1","source":"1","target":"2"},
		{"id":"e2","source":"2","target":"4"},
		{"id":"e3","source":"4","target":"5"}
	]}`)
	if has(call, SeverityError, CodeSourceKey) {
		t.Fatalf("a call node exposes the function's outputs, got %v", codes(call))
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
		{"id":"2","type":"call","data":{"config":{"target":"ghost"}}},
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
	if !has(first, SeverityError, CodeCallTarget) || !has(first, SeverityError, CodeUnknownType) {
		t.Fatalf("want call_target + unknown_type, got %v", codes(first))
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
		case CodeOnErrorAction, CodeOnErrorCode, CodeOnErrorPost, CodeOnErrorShadowed:
			t.Fatalf("a valid on_error block must not be reported: %v", f)
		}
	}
}

// skip runs before handler, so a code both rules list is a handler that never runs.
func TestOnErrorShadowedCode(t *testing.T) {
	fs, _ := Step("http", map[string]any{
		"http": map[string]any{"url": "https://example.com"},
		"on_error": map[string]any{
			"skip":    map[string]any{"codes": []any{"not_found"}},
			"handler": map[string]any{"codes": []any{"not_found", "timeout"}, "script": "return 'skip'"},
		},
	}, nil)
	if !has(fs, SeverityWarning, CodeOnErrorShadowed) {
		t.Fatalf("a code claimed by skip must be reported, got %v", codes(fs))
	}
}

// `other` without skip-side patterns means "every other message", so the handler has nothing left there either.
func TestOnErrorShadowedBareOther(t *testing.T) {
	fs, _ := Step("http", map[string]any{
		"http": map[string]any{"url": "https://example.com"},
		"on_error": map[string]any{
			"skip":    map[string]any{"codes": []any{"other"}},
			"handler": map[string]any{"codes": []any{"other"}, "script": "return 'skip'"},
		},
	}, nil)
	if !has(fs, SeverityWarning, CodeOnErrorShadowed) {
		t.Fatalf("bare `other` on skip must be reported, got %v", codes(fs))
	}
}

// Patterns narrow only `other`: disjoint patterns leave each rule its own messages, so reporting them would be a false
// positive - and a checker that cries wolf is a checker authors learn to skip.
func TestOnErrorShadowedPatternOnlyWhenOverlapping(t *testing.T) {
	disjoint, _ := Step("http", map[string]any{
		"http": map[string]any{"url": "https://example.com"},
		"on_error": map[string]any{
			"skip":    map[string]any{"codes": []any{"other"}, "patterns": []any{"%rate limit%"}},
			"handler": map[string]any{"codes": []any{"other"}, "patterns": []any{"%quota%"}, "script": "return 'skip'"},
		},
	}, nil)
	if has(disjoint, SeverityWarning, CodeOnErrorShadowed) {
		t.Fatalf("disjoint patterns must not be reported, got %v", codes(disjoint))
	}

	overlapping, _ := Step("http", map[string]any{
		"http": map[string]any{"url": "https://example.com"},
		"on_error": map[string]any{
			"skip":    map[string]any{"codes": []any{"other"}, "patterns": []any{"%timeout%"}},
			"handler": map[string]any{"codes": []any{"other"}, "patterns": []any{"%timeout after 3 retries%"}, "script": "return 'skip'"},
		},
	}, nil)
	if !has(overlapping, SeverityWarning, CodeOnErrorShadowed) {
		t.Fatalf("overlapping patterns must be reported, got %v", codes(overlapping))
	}
}

// A skip without a handler cannot shadow anything, and a handler without a script is already on_error_script: the new
// rule must stay silent in both cases instead of restating a finding that is already there.
func TestOnErrorShadowedNeedsBothScripts(t *testing.T) {
	fs, _ := Step("http", map[string]any{
		"http": map[string]any{"url": "https://example.com"},
		"on_error": map[string]any{
			"skip":    map[string]any{"codes": []any{"timeout"}},
			"handler": map[string]any{"codes": []any{"timeout"}},
		},
	}, nil)
	if has(fs, SeverityWarning, CodeOnErrorShadowed) {
		t.Fatalf("a handler without a script must not also be reported as shadowed, got %v", codes(fs))
	}
}

func TestEmptyDiagramIsAnError(t *testing.T) {
	if _, err := Diagram(nil, nil); err == nil {
		t.Fatal("an empty diagram must be refused, not reported as clean")
	}
}

// A function node is entered by a call node's target, not by an edge, so it must not be reported as unreachable; an
// ordinary node with no incoming edge still is. Reporting the function would mean every flow with a function carries a
// warning it can never clear.
func TestUnreachableSkipsCalledNodes(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"code","data":{"label":"body","config":{"code":{"lang":"lua","script":"return 1"},"outputs":{"x":{"type":"string"}}}}},
		{"id":"3","type":"function","data":{"label":"fn"}},
		{"id":"4","type":"entry","parent_node":"3"},
		{"id":"5","type":"code","data":{"label":"lonely","config":{"code":{"lang":"lua","script":"return 1"},"outputs":{"x":{"type":"string"}}}}}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`)

	unreachable := map[string]bool{}
	for _, f := range fs {
		if f.Code == CodeUnreachable {
			unreachable[f.NodeID] = true
		}
	}
	if unreachable["3"] {
		t.Errorf("the function node was reported as unreachable: %v", codes(fs))
	}
	if !unreachable["5"] {
		t.Errorf("an unwired code node must still be reported, got %v", codes(fs))
	}
}

// A code step that writes into an ancestor container through targets declares its variables there, so it is complete: only
// a step declaring none of outputs / targets / locals throws away what its script returns.
func TestCodeStepWithTargetsIsComplete(t *testing.T) {
	fs, err := Step("code", map[string]any{
		"code":    map[string]any{"script": "return { sum = 1 }"},
		"targets": map[string]any{"sum": map[string]any{"type": "number", "source_type": "node", "source_ref": "2"}},
	}, nil)
	if err != nil {
		t.Fatalf("Step = %v", err)
	}
	if has(fs, SeverityWarning, CodeOutputs) {
		t.Fatalf("a step writing through targets must not be warned, got %v", codes(fs))
	}
}

// A function body reads its parameters through the function node itself, so a param is a valid source_key inside the body.
// From outside the params do not exist while the caller runs, so that finding has to stay.
func TestFunctionParamsReadableInsideBody(t *testing.T) {
	inside := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"params":{"a":{"type":"number"}},"outputs":{"sum":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{
			"code":{"script":"return { sum = input.a }"},
			"inputs":{"a":{"type":"number","source_type":"node","source_ref":"2","source_key":"a"}},
			"targets":{"sum":{"type":"number","source_type":"node","source_ref":"2"}}
		}}}
	],"edges":[{"id":"e1","source":"3","target":"4"}]}`)
	if has(inside, SeverityError, CodeSourceKey) {
		t.Fatalf("a param read inside the function body must pass, got %v", codes(inside))
	}

	outside := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"params":{"a":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","data":{"config":{
			"code":{"script":"return {}"},
			"outputs":{"x":{"type":"number"}},
			"inputs":{"a":{"type":"number","source_type":"node","source_ref":"2","source_key":"a"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"4"}]}`)
	if !has(outside, SeverityError, CodeSourceKey) {
		t.Fatalf("reading a param from outside the body must be reported, got %v", codes(outside))
	}
}

// A loop body reads the round's variables (index / item / total) through the loop node itself, exactly as a function body
// reads its params. Outside the body they only hold the last round, so that reference stays an error.
func TestLoopVarsReadableInsideBody(t *testing.T) {
	inside := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{
			"loop":{"type":"count","total_times":3},
			"loop_vars":{"index":{"type":"number"},"total":{"type":"number"}}
		}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{
			"code":{"script":"return { i = input.i }"},
			"inputs":{"i":{"type":"number","source_type":"node","source_ref":"2","source_key":"index"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"}]}`)
	if has(inside, SeverityError, CodeSourceKey) {
		t.Fatalf("a loop variable read inside the body must pass, got %v", codes(inside))
	}

	outside := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{
			"loop":{"type":"count","total_times":3},
			"loop_vars":{"index":{"type":"number"}}
		}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","data":{"config":{
			"code":{"script":"return { i = input.i }"},
			"inputs":{"i":{"type":"number","source_type":"node","source_ref":"2","source_key":"index"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"2","target":"4"}]}`)
	if !has(outside, SeverityError, CodeSourceKey) {
		t.Fatalf("reading a loop variable from outside the body must be reported, got %v", codes(outside))
	}
}

// A node never reads itself: inputs are resolved before it runs, so its own outputs / locals are still empty and
// GetVariables fails the whole batch at run time.
func TestSelfReferenceInInputsIsReported(t *testing.T) {
	self := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"code","data":{"config":{
			"code":{"script":"return { acc = 1 }"},
			"outputs":{"acc":{"type":"number"}},
			"inputs":{"x":{"type":"number","source_type":"node","source_ref":"2","source_key":"acc"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`)
	if !has(self, SeverityError, CodeSelfRef) {
		t.Fatalf("a self reference must be reported, got %v", codes(self))
	}

	// The same read from an upstream node stays legal: that is the shape the rule points authors at.
	upstream := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"3","type":"code","data":{"config":{"code":{"script":"return { acc = 1 }"},"outputs":{"acc":{"type":"number"}}}}},
		{"id":"2","type":"code","data":{"config":{
			"code":{"script":"return { b = input.x }"},
			"outputs":{"b":{"type":"number"}},
			"inputs":{"x":{"type":"number","source_type":"node","source_ref":"3","source_key":"acc"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"3"},{"id":"e2","source":"3","target":"2"}]}`)
	if has(upstream, SeverityError, CodeSelfRef) {
		t.Fatalf("reading an upstream node must pass, got %v", codes(upstream))
	}
}

// A while / do_while condition re-resolves config.inputs before every check (loop.go conditionInputs), so reading the
// loop's own outputs is the documented accumulate pattern (references/nodes/loop.md) and the definition runs. Reporting it
// as self_ref made a runnable flow look unrunnable, which is what this guards. count / each keep the startup snapshot, so
// the same read there stays an error.
func TestWhileConditionMayReadOwnValues(t *testing.T) {
	const graph = `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{
			"loop":{"type":"%s","condition":"return input.total < 3","total_times":3},
			"outputs":{"total":{"type":"number"}},
			"inputs":{"total":{"type":"number","source_type":"node","source_ref":"2","source_key":"total"}}
		}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{
			"code":{"script":"return { total = 1 }"},
			"targets":{"total":{"source_type":"node","source_ref":"2","source_key":"total"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"}]}`

	for _, mode := range []string{"while", "do_while"} {
		fs := checkDiagram(t, fmt.Sprintf(graph, mode))
		for _, code := range []string{CodeSelfRef, CodeSourceKey} {
			if has(fs, SeverityError, code) {
				t.Errorf("%s: %s must not fire when the condition reads the loop's own output, got %v", mode, code, codes(fs))
			}
		}
	}

	// count takes its inputs once, before the first round: the same read still cannot see what the body writes back.
	if fs := checkDiagram(t, fmt.Sprintf(graph, "count")); !has(fs, SeverityError, CodeSelfRef) {
		t.Errorf("count: a self read must stay an error, got %v", codes(fs))
	}
}

// The same rule covers the names the loop keeps in locals / loop_vars: conditionInputs merges all of them, so a typo-free
// read of one is not a source_key mistake.
func TestWhileConditionMayReadOwnLocals(t *testing.T) {
	const graph = `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{
			"loop":{"type":"while","condition":"return input.%s < 3"},
			"locals":{"acc":{"type":"number"}},
			"loop_vars":{"index":{"type":"number"}},
			"inputs":{"%s":{"type":"number","source_type":"node","source_ref":"2","source_key":"%s"}}
		}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{"code":{"script":"return {}"}}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"}]}`

	for _, name := range []string{"acc", "index"} {
		fs := checkDiagram(t, fmt.Sprintf(graph, name, name, name))
		for _, code := range []string{CodeSelfRef, CodeSourceKey} {
			if has(fs, SeverityError, code) {
				t.Errorf("%s: %s must not fire for a while condition reading the loop's own %s, got %v",
					name, code, name, codes(fs))
			}
		}
	}
}

// A loop's iterations is a readable name: NodeResult.GetVariable resolves it and the run record stores it as the node's
// extra, so source_key: iterations must pass. Otherwise loop.md's "iterations records the actual round count" would be true
// only for a human reading the run record, and a flow could not act on the count at all.
func TestLoopIterationsIsReadable(t *testing.T) {
	fs := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{"loop":{"type":"count","total_times":3}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{"code":{"script":"return {}"}}}},
		{"id":"5","type":"code","data":{"config":{
			"code":{"script":"return { n = input.n }"},
			"outputs":{"n":{"type":"number"}},
			"inputs":{"n":{"type":"number","source_type":"node","source_ref":"2","source_key":"iterations"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"},{"id":"e3","source":"2","target":"5"}]}`)
	if has(fs, SeverityError, CodeSourceKey) {
		t.Fatalf("iterations is a readable loop output, got %v", codes(fs))
	}
}

// A call node reads its arguments from config.args, so the same reference rules apply there: no self reference, no
// dangling node, and a function is not a value producer outside its own body.
func TestCallArgsReferenceRules(t *testing.T) {
	const graph = `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"7","type":"function","data":{"config":{"params":{"a":{"type":"number"}},"outputs":{"r":{"type":"number"}}}}},
		{"id":"8","type":"entry","parent_node":"7"},
		{"id":"3","type":"code","data":{"config":{"code":{"script":"return { v = 1 }"},"outputs":{"v":{"type":"number"}}}}},
		{"id":"2","type":"call","data":{"config":{"target":"7","args":{"a":{"type":"number","source_type":"node","source_ref":"%s","source_key":"%s"}}}}}
	],"edges":[{"id":"e1","source":"1","target":"3"},{"id":"e2","source":"3","target":"2"}]}`

	for _, tc := range []struct {
		name string
		ref  string
		key  string
		want string
	}{
		{"self reference", "2", "r", CodeSelfRef},
		{"dangling node", "999", "r", CodeDanglingRef},
		{"function from outside", "7", "r", CodeFunctionRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := checkDiagram(t, fmt.Sprintf(graph, tc.ref, tc.key))
			if !has(fs, SeverityError, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, codes(fs))
			}
		})
	}

	// The upstream read stays legal: that is what args is for.
	ok := checkDiagram(t, fmt.Sprintf(graph, "3", "v"))
	for _, code := range []string{CodeSelfRef, CodeDanglingRef, CodeFunctionRef} {
		if has(ok, SeverityError, code) {
			t.Fatalf("reading an upstream node must pass, got %v", codes(ok))
		}
	}
}

// Outside a function body only a call node may name a function. Every other reference reads an empty value at run time (or
// writes into a variable nobody reads); a call node is unaffected because it reaches the function through config.target.
func TestFunctionRefOutsideBody(t *testing.T) {
	bad := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"params":{"a":{"type":"number"}},"outputs":{"sum":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","data":{"config":{
			"code":{"script":"return { x = 1 }"},
			"outputs":{"x":{"type":"number"}},
			"inputs":{"a":{"type":"number","source_type":"node","source_ref":"2","source_key":"a"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"4"}]}`)
	if !has(bad, SeverityError, CodeFunctionRef) {
		t.Fatalf("an outside reference to a function node must be reported, got %v", codes(bad))
	}
	for _, f := range bad {
		if f.Code == CodeFunctionRef && f.Path != "inputs.a.source_ref" {
			t.Errorf("path = %q, want inputs.a.source_ref", f.Path)
		}
	}

	ok := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"params":{"a":{"type":"number"}},"outputs":{"sum":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{
			"code":{"script":"return { sum = 1 }"},
			"targets":{"sum":{"type":"number","source_type":"node","source_ref":"2"}}
		}}},
		{"id":"5","type":"call","data":{"config":{"target":"2","args":{"a":{"type":"number","source_type":"flow","source_key":"k"}}}}},
		{"id":"6","type":"code","data":{"config":{
			"code":{"script":"return { y = 1 }"},
			"outputs":{"y":{"type":"number"}},
			"inputs":{"s":{"type":"number","source_type":"node","source_ref":"5","source_key":"sum"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"5"},{"id":"e2","source":"5","target":"6"},{"id":"e3","source":"3","target":"4"}]}`)
	if has(ok, SeverityError, CodeFunctionRef) {
		t.Fatalf("a call node and an in-body reference must pass, got %v", codes(ok))
	}
}

// Three function shapes that used to pass validation: an edge touching the function, a function nested in another
// container, and a targets entry writing a variable the container never declares.
func TestFunctionShapeRules(t *testing.T) {
	edge := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"outputs":{"sum":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{
			"code":{"script":"return { sum = 1 }"},
			"targets":{"sum":{"type":"number","source_type":"node","source_ref":"2"}}
		}}}
	],"edges":[{"id":"e1","source":"1","target":"2"},{"id":"e2","source":"3","target":"4"}]}`)
	if !has(edge, SeverityError, CodeFunctionEdge) {
		t.Fatalf("an edge into a function must be reported, got %v", codes(edge))
	}

	nested := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"loop","data":{"config":{"loop":{"type":"count","total_times":2}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"function","parent_node":"2","data":{"config":{"outputs":{"sum":{"type":"number"}}}}}
	],"edges":[{"id":"e1","source":"1","target":"2"}]}`)
	if !has(nested, SeverityError, CodeNestedFunction) {
		t.Fatalf("a nested function must be reported, got %v", codes(nested))
	}

	written := checkDiagram(t, `{"nodes":[
		{"id":"1","type":"start"},
		{"id":"2","type":"function","data":{"config":{"outputs":{"sum":{"type":"number"}}}}},
		{"id":"3","type":"entry","parent_node":"2"},
		{"id":"4","type":"code","parent_node":"2","data":{"config":{
			"code":{"script":"return { total = 1 }"},
			"targets":{"total":{"type":"number","source_type":"node","source_ref":"2"}}
		}}},
		{"id":"5","type":"call","data":{"config":{"target":"2"}}}
	],"edges":[{"id":"e1","source":"1","target":"5"},{"id":"e2","source":"3","target":"4"}]}`)
	if !has(written, SeverityError, CodeTargetVar) {
		t.Fatalf("writing an undeclared container variable must be reported, got %v", codes(written))
	}
	for _, f := range written {
		if f.Code == CodeTargetVar && f.Path != "targets.total.source_key" {
			t.Errorf("path = %q, want targets.total.source_key", f.Path)
		}
	}
}
