package executors

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// RenderTemplate is the single render path shared by code / result / progress / binding transforms, so its
// custom functions are part of the product surface: a change here reaches every template in every flow.
func TestRenderTemplateFunctions(t *testing.T) {
	data := map[string]any{
		"input": map[string]any{
			"name":  " world ",
			"obj":   map[string]any{"a": 1},
			"text":  "abcdef",
			"empty": "",
			"zero":  0,
		},
	}
	cases := []struct {
		name string
		tmpl string
		want string
	}{
		{"plain interpolation", `hello{{ .input.name }}`, "hello world "},
		{"upper", `{{ upper .input.name }}`, " WORLD "},
		{"lower", `{{ lower "ABC" }}`, "abc"},
		{"trim", `{{ trim .input.name }}`, "world"},
		{"json", `{{ json .input.obj }}`, `{"a":1}`},
		{"truncate", `{{ truncate 3 .input.text }}`, "abc"},
		{"truncate keeps a shorter string", `{{ truncate 99 "ab" }}`, "ab"},
		// default fires on nil and on the zero value of a string / bool, not on 0 (a number is a value)
		{"default on missing key", `{{ default "fallback" .input.missing }}`, "fallback"},
		{"default on empty string", `{{ default "fallback" .input.empty }}`, "fallback"},
		{"default keeps a value", `{{ default "fallback" .input.name }}`, " world "},
		{"default keeps zero", `{{ default "fallback" .input.zero }}`, "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderTemplate(tc.tmpl, data)
			if err != nil {
				t.Fatalf("RenderTemplate(%q) = %v", tc.tmpl, err)
			}
			if got != tc.want {
				t.Errorf("RenderTemplate(%q) = %q, want %q", tc.tmpl, got, tc.want)
			}
		})
	}
}

// The cache is keyed by the template text: rendering the same template twice must give the same answer without
// re-parsing (and must not poison the second render).
func TestRenderTemplateIsCached(t *testing.T) {
	tmpl := `cached-{{ .input.n }}`
	first, err := RenderTemplate(tmpl, map[string]any{"input": map[string]any{"n": 1}})
	if err != nil {
		t.Fatalf("first render = %v", err)
	}
	second, err := RenderTemplate(tmpl, map[string]any{"input": map[string]any{"n": 2}})
	if err != nil {
		t.Fatalf("second render = %v", err)
	}
	if first != "cached-1" || second != "cached-2" {
		t.Errorf("renders = %q / %q, want cached-1 / cached-2", first, second)
	}
	if _, ok := tmplCache.Load(tmpl); !ok {
		t.Error("the parsed template is not in the cache, so every render parses again")
	}
}

func TestRenderTemplateParseError(t *testing.T) {
	if _, err := RenderTemplate("{{", map[string]any{}); err == nil {
		t.Fatal("RenderTemplate must fail for a template that does not parse")
	}
}

// The output ceiling is what stops "one render produces a huge string": Execute aborts as soon as a Write
// crosses it, and the sentinel is what the caller reports.
func TestRenderTemplateOutputLimit(t *testing.T) {
	chunk := strings.Repeat("x", 100)
	items := make([]any, (maxTemplateOutputBytes/len(chunk))+10)
	for i := range items {
		items[i] = chunk
	}
	_, err := RenderTemplate(`{{ range .input.items }}{{ . }}{{ end }}`, map[string]any{
		"input": map[string]any{"items": items},
	})
	if !errors.Is(err, errTemplateOutputTooLarge) {
		t.Fatalf("RenderTemplate = %v, want errTemplateOutputTooLarge", err)
	}
}

// checkTemplateData caps the input as well: the output limit alone would let a huge input cost the traversal.
func TestCheckTemplateData(t *testing.T) {
	if err := checkTemplateData(map[string]any{"a": 1, "b": []any{1, 2}}); err != nil {
		t.Errorf("a small payload was rejected: %v", err)
	}

	deep := map[string]any{}
	cur := deep
	for i := 0; i < maxTemplateDataDepth+2; i++ {
		next := map[string]any{}
		cur["n"] = next
		cur = next
	}
	if err := checkTemplateData(deep); err == nil || !strings.Contains(err.Error(), "nested too deep") {
		t.Errorf("deep payload = %v, want the depth error", err)
	}

	wide := make([]any, maxTemplateDataNodes+1)
	if err := checkTemplateData(wide); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("wide payload = %v, want the node-count error", err)
	}
}

// limitedWriter is the gate: it refuses the write that would cross the ceiling instead of truncating silently.
func TestLimitedWriter(t *testing.T) {
	w := &limitedWriter{limit: 5}
	if n, err := w.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("Write(abc) = %d, %v", n, err)
	}
	if n, err := w.Write([]byte("def")); err == nil || n != 0 {
		t.Fatalf("Write past the limit = %d, %v, want 0 and an error", n, err)
	}
	if !w.exceeded {
		t.Error("exceeded is not set, so RenderTemplate cannot report the size error")
	}
	if w.String() != "abc" {
		t.Errorf("String() = %q, want the content written before the limit", w.String())
	}
}

// ExecuteTemplateScript is the structured variant (the code node): the rendered text is decoded, numbers stay
// json.Number so they match what lua returns.
func TestExecuteTemplateScript(t *testing.T) {
	got, err := ExecuteTemplateScript(`{"n": 1, "s": "x"}`, map[string]any{"input": map[string]any{}})
	if err != nil {
		t.Fatalf("ExecuteTemplateScript = %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("result = %T, want a map", got)
	}
	if n, ok := m["n"].(json.Number); !ok || n.String() != "1" {
		t.Errorf("n = %#v, want json.Number 1", m["n"])
	}

	// Render errors and decode errors are distinguished by the wrapping text.
	if _, err := ExecuteTemplateScript("{{", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "template render") {
		t.Errorf("broken template = %v, want the render error", err)
	}
	if _, err := ExecuteTemplateScript("not json", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "json decode") {
		t.Errorf("invalid json = %v, want the decode error", err)
	}
}

// The script context is a snapshot: a later write to the node's variables must not change what a running
// template already sees, and vice versa.
func TestBuildScriptTemplateContext(t *testing.T) {
	node := newExecNode("2", "code", map[string]any{
		"outputs": map[string]any{"out": map[string]any{"type": "string"}},
		"locals":  map[string]any{"tmp": map[string]any{"type": "string"}},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	nr.InitLocalVars(map[string]any{"tmp": map[string]any{"type": "string"}})
	if err := nr.SetLocalVar("tmp", "before"); err != nil {
		t.Fatalf("SetLocalVar = %v", err)
	}

	data := BuildScriptTemplateContext(nr, map[string]any{"k": "v"})
	if got := data["input"].(map[string]any)["k"]; got != "v" {
		t.Errorf("input.k = %v, want v", got)
	}
	if got := data["local"].(map[string]any)["tmp"]; got != "before" {
		t.Errorf("local.tmp = %v, want before", got)
	}
	if _, ok := data["output"].(map[string]any)["out"]; !ok {
		t.Errorf("output = %v, want the declared variable to be present", data["output"])
	}

	// The snapshot does not follow later writes.
	if err := nr.SetLocalVar("tmp", "after"); err != nil {
		t.Fatalf("SetLocalVar = %v", err)
	}
	if got := data["local"].(map[string]any)["tmp"]; got != "before" {
		t.Errorf("local.tmp = %v after a later write, want the snapshot value", got)
	}
}

// Topping the declaration up is covered per executor (see TestEnsureFixedOutputsFillsHTTPVars); what this pins
// is the merge rule: an existing declaration always wins, and the caller's map is never modified.
func TestEnsureFixedOutputsKeepsExistingDeclarations(t *testing.T) {
	declared := map[string]any{
		"custom": map[string]any{"type": "string", "description": "kept"},
		"output": map[string]any{"type": "object"},
	}
	merged := ensureFixedOutputs(declared, map[string]string{"output": "string", "extra": "number"})

	if got, _ := merged["output"].(map[string]any)["type"]; got != "object" {
		t.Errorf("output type = %v, want the existing declaration to win", got)
	}
	if got, _ := merged["custom"].(map[string]any)["description"]; got != "kept" {
		t.Errorf("custom = %v, want the caller's declaration untouched", merged["custom"])
	}
	if got, _ := merged["extra"].(map[string]any)["type"]; got != "number" {
		t.Errorf("extra = %v, want the missing fixed variable to be added", merged["extra"])
	}
	if _, exists := declared["extra"]; exists {
		t.Error("the caller's map was modified")
	}

	if got := ensureFixedOutputs(nil, nil); len(got) != 0 {
		t.Errorf("ensureFixedOutputs(nil, nil) = %v, want an empty map", got)
	}
}

// A result node with `lang: template` goes through RenderTemplate (no JSON decode) while a code node goes
// through ExecuteTemplateScript; this pins that the shared context feeds both the same way.
func TestTemplateContextReachesScripts(t *testing.T) {
	node := newExecNode("2", "result", map[string]any{
		"code": map[string]any{"lang": "template", "script": `hi {{ .local.tmp }}`},
	})
	ctx := newExecContext()
	nr := newExecResult(node, ctx)
	nr.InitLocalVars(map[string]any{"tmp": map[string]any{"type": "string"}})
	if err := nr.SetLocalVar("tmp", "there"); err != nil {
		t.Fatalf("SetLocalVar = %v", err)
	}
	if err := (&ResultExecutor{}).Execute(node, ctx, "1", nr); err != nil {
		t.Fatalf("Execute = %v (code %q)", err, nr.ErrCode)
	}
	if got, _ := nr.OutputVars["output"].(string); got != "hi there" {
		t.Errorf("output = %v, want the rendered local variable", nr.OutputVars["output"])
	}
}
