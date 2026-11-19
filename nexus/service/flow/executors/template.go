package executors

import (
	"bytes"
	"encoding/json"
	"errors"
	"flame/nexus/service/flow/core"
	"flame/nexus/shared/globalvars"
	"fmt"
	"strings"
	"sync"
	"text/template"
)

// tmplCache is the template cache, so a template is not parsed again
var tmplCache sync.Map

// tmplFuncs is the set of custom template functions
var tmplFuncs = template.FuncMap{
	"json": func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	},
	"truncate": func(n int, s string) string {
		r := []rune(s)
		if len(r) > n {
			return string(r[:n])
		}
		return s
	},
	"default": func(def, v any) any {
		if v == nil {
			return def
		}
		switch val := v.(type) {
		case string:
			if val == "" {
				return def
			}
		case bool:
			if !val {
				return def
			}
		}
		return v
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	"trim":  strings.TrimSpace,
}

const (
	// maxTemplateOutputBytes is the output ceiling for one template render.
	// text/template's Execute writes incrementally and a Write returning an error aborts the render
	// immediately, so this gate really does stop "one render produces a huge string and blows up
	// memory" (a timeout cannot, because Go template rendering in this process cannot be interrupted).
	maxTemplateOutputBytes = 1 << 20 // 1 MiB
	// maxTemplateDataNodes is the ceiling on the total number of data nodes visible to a template (map /
	// slice elements plus scalars).
	// The {{range}} iteration ceiling is about the element count, so capping the input size caps the
	// source of the render size.
	maxTemplateDataNodes = 50000
	// maxTemplateDataDepth is the nesting-depth ceiling, so traversal / rendering cannot be amplified by
	// an extremely deep structure.
	maxTemplateDataDepth = 32
)

// errTemplateOutputTooLarge is the sentinel error for an over-limit output.
var errTemplateOutputTooLarge = errors.New("template output exceeds size limit")

// limitedWriter is an io.Writer with a ceiling: once the accumulated writes exceed limit it returns an
// error.
type limitedWriter struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		w.exceeded = true
		return 0, errTemplateOutputTooLarge
	}
	return w.buf.Write(p)
}

func (w *limitedWriter) String() string { return w.buf.String() }

// checkTemplateData caps the node count and nesting depth of the data visible to a template.
//
// It only bounds the size and offers no hard time limit: Go template rendering in this process cannot be
// interrupted, so the wall-clock time of a malformed template can only be constrained by process-level
// isolation.
func checkTemplateData(data any) error {
	n := 0
	var walk func(v any, depth int) error
	walk = func(v any, depth int) error {
		if depth > maxTemplateDataDepth {
			return fmt.Errorf("template data nested too deep (> %d)", maxTemplateDataDepth)
		}
		n++
		if n > maxTemplateDataNodes {
			return fmt.Errorf("template data too large (> %d nodes)", maxTemplateDataNodes)
		}
		switch t := v.(type) {
		case map[string]any:
			for _, e := range t {
				if err := walk(e, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, e := range t {
				if err := walk(e, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(data, 0)
}

// RenderTemplate renders a template string and returns the string directly (no JSON decode), using the
// cache so it is not parsed again.
//
// Two size guards apply (Code / Result / Progress / binding transform all share this function):
//   - input: checkTemplateData caps the node count and nesting depth of the visible data;
//   - output: limitedWriter aborts the render once it passes maxTemplateOutputBytes.
func RenderTemplate(tmplStr string, data map[string]any) (string, error) {
	if err := checkTemplateData(data); err != nil {
		return "", err
	}
	t, err := getOrParseTemplate(tmplStr)
	if err != nil {
		return "", err
	}
	w := &limitedWriter{limit: maxTemplateOutputBytes}
	if err := t.Execute(w, data); err != nil {
		if w.exceeded {
			return "", errTemplateOutputTooLarge
		}
		return "", err
	}
	return w.String(), nil
}

// getOrParseTemplate takes a template from the cache or parses it
func getOrParseTemplate(tmplStr string) (*template.Template, error) {
	if cached, ok := tmplCache.Load(tmplStr); ok {
		return cached.(*template.Template), nil
	}
	t, err := template.New("tmpl").Funcs(tmplFuncs).Parse(tmplStr)
	if err != nil {
		return nil, err
	}
	tmplCache.Store(tmplStr, t)
	return t, nil
}

// BuildScriptTemplateContext builds the three-part context of a template script:
//
//	.input  — node inputs (global variables are here too: they are one source of inputs)
//	.local  — local variables (a shallow snapshot from SnapshotVars)
//	.output — output variables (a shallow snapshot from SnapshotVars)
//
// Global variables no longer get a section of their own (it used to be .global_vars): like argument / env
// / flow / node / builtin, they are one source of node inputs and are resolved in GetVariables. One
// section less is one less hidden channel through which a template can quietly reach a global value —
// the node config and the input variable table now describe the same thing.
func BuildScriptTemplateContext(nr *flow.NodeResult, inputs map[string]any) map[string]any {
	localCopy, outputCopy := nr.SnapshotVars()
	return map[string]any{
		"input":  inputs,
		"local":  localCopy,
		"output": outputCopy,
	}
}

// WarnSecretPromptRefs logs a warning when a prompt references a global variable of secret kind (it does
// not block).
//
// The decision looks at the **source**, not the text: global variables now arrive through node inputs
// ({{ .input.xxx }}) and the prompt no longer contains the variable name. So it first maps back from the
// config.inputs entries with source_type == "global" which keys come from a variable of kind == secret,
// then checks whether the prompt references those keys.
//
// Why only prompts are flagged: using a secret in a node config is legitimate (the http Authorization
// header), while the prompt is the extra leak surface — the rendered text reaches the model context,
// session_records and SSE.
func WarnSecretPromptRefs(nr *flow.NodeResult, cfg map[string]any, texts ...string) {
	if nr == nil || nr.Ctx == nil || nr.Ctx.Schema == "" {
		return
	}
	inputs, _ := cfg["inputs"].(map[string]any)
	if len(inputs) == 0 || len(texts) == 0 {
		return
	}

	// First collect the input names that come from a global variable; when the secret list cannot be read
	// nothing is reported at all: this is an advisory warning and must not break a node because a query
	// failed.
	fromGlobal := make(map[string]string, len(inputs))
	for name, raw := range inputs {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if sourceType, _ := item["source_type"].(string); sourceType != "global" {
			continue
		}
		if key, _ := item["source_key"].(string); key != "" {
			fromGlobal[name] = key
		}
	}
	if len(fromGlobal) == 0 {
		return
	}

	secrets, err := globalvars.SecretNames(nr.Ctx.Schema)
	if err != nil {
		return
	}
	for inputName, globalName := range fromGlobal {
		if !secrets[globalName] {
			continue
		}
		for _, text := range texts {
			if !refersToInput(text, inputName) {
				continue
			}
			nr.WarnOnce("secret-prompt:"+inputName,
				"the prompt references the secret global variable %s (input variable %s): the rendered text reaches the model context and the run records, so secrets should only be used in connection / tool config",
				globalName, inputName)
			break
		}
	}
}

// refersToInput reports whether a template text references {{ .input.<name> }}.
//
// It does not use one big regex: a template may carry a pipeline ({{ .input.token | upper }}) or an
// attribute access ({{ .input.token.len }}), so requiring only "not an identifier character" after the
// name covers them all. A false positive costs one warning while a false negative is the real problem, so
// the test leans wide.
func refersToInput(text, name string) bool {
	needle := ".input." + name
	for i := 0; ; {
		idx := strings.Index(text[i:], needle)
		if idx < 0 {
			return false
		}
		end := i + idx + len(needle)
		if end >= len(text) {
			return true
		}
		if c := text[end]; c != '_' &&
			(c < '0' || c > '9') &&
			(c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') {
			return true
		}
		i = end
	}
}

// ExecuteTemplateScript runs a template script, with data built by the caller (the three-part context)
func ExecuteTemplateScript(script string, data map[string]any) (any, error) {
	rendered, err := RenderTemplate(script, data)
	if err != nil {
		return nil, fmt.Errorf("template render: %w", err)
	}
	// UseNumber keeps numbers as json.Number, consistent with lua
	dec := json.NewDecoder(strings.NewReader(rendered))
	dec.UseNumber()
	var result any
	if err := dec.Decode(&result); err != nil {
		return nil, fmt.Errorf("template result json decode: %w", err)
	}
	return result, nil
}
