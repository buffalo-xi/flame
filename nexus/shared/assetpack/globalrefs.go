package assetpack

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"flame/nexus/shared/storage"
)

// Syntax of a global variable reference, one form per runtime injection point:
//
//	config.inputs: {source_type: "global", source_key: NAME} — workflows (structured, current)
//	${global_vars.NAME}                                     — agent / skill documents
//	{{.global_vars.NAME}}                                   — the old workflow form (retired, only used to spot leftovers)
//
// Three places share these regexes (here / the preserve decision in secrets.go / the requirement
// list scan); keeping one copy avoids "the syntax changed and only one side was updated".
var (
	refTemplateRe = regexp.MustCompile(`\{\{-?\s*\.global_vars\.([A-Za-z_][A-Za-z0-9_]*)`)
	refDocRe      = regexp.MustCompile(`\$\{global_vars\.([A-Za-z_][A-Za-z0-9_]*)`)
	// refInputRe matches a node input reference {{ .input.NAME }}: the value is not in the package
	// (it comes from an input variable source), so redaction must keep it as a reference, for the
	// same reason as a global_vars reference.
	refInputRe = regexp.MustCompile(`\{\{-?\s*\.input\.([A-Za-z_][A-Za-z0-9_]*)`)
	// globalRefExactRe matches a string that is exactly one reference (not a mixed form such as
	// "Bearer xxx").
	globalRefExactRe = regexp.MustCompile(
		`^(?:\{\{-?\s*\.global_vars\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}` +
			`|\$\{global_vars\.[A-Za-z_][A-Za-z0-9_]*\}` +
			`|\{\{-?\s*\.input\.[A-Za-z_][A-Za-z0-9_]*\s*\}\})$`)
)

// GlobalVarRefsInText collects the global variable names referenced by a piece of text
// (deduplicated, sorted).
func GlobalVarRefsInText(text string) []string {
	if text == "" {
		return nil
	}
	out := make([]string, 0, 2)
	for _, m := range refTemplateRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	for _, m := range refDocRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// GlobalVarRefsInValue recursively scans a JSON-shaped value (map / []any / string).
//
// Packing uses it on the payload: a workflow's env_vars and diagram both live in there.
func GlobalVarRefsInValue(v any) []string {
	switch val := v.(type) {
	case string:
		return GlobalVarRefsInText(val)
	case map[string]any:
		var out []string
		for _, item := range val {
			out = append(out, GlobalVarRefsInValue(item)...)
		}
		return out
	case []any:
		var out []string
		for _, item := range val {
			out = append(out, GlobalVarRefsInValue(item)...)
		}
		return out
	default:
		return nil
	}
}

// GlobalVarRefsInNodeInputs reads global variable sources from node input variables:
// {source_type: "global", source_key: "name"}.
//
// Regexes alone cannot cover this — that form has no {{ }} / ${}, only a type plus a name. It
// recurses looking for the key "inputs": a workflow has them at
// diagram.nodes[].data.config.inputs, other node-level configs share the shape, so anything found
// counts, and one extra dependency only adds one extra line to the pre-check.
func GlobalVarRefsInNodeInputs(v any) []string {
	var out []string
	var walk func(any)
	walk = func(val any) {
		switch item := val.(type) {
		case map[string]any:
			if inputs, ok := item["inputs"].(map[string]any); ok {
				out = append(out, globalRefsOfInputs(inputs)...)
			}
			for _, sub := range item {
				walk(sub)
			}
		case []any:
			for _, sub := range item {
				walk(sub)
			}
		}
	}
	walk(v)
	return out
}

// globalRefsOfInputs picks the names "whose source is a global variable" out of one inputs table.
func globalRefsOfInputs(inputs map[string]any) []string {
	var out []string
	for _, raw := range inputs {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if sourceType, _ := item["source_type"].(string); sourceType != "global" {
			continue
		}
		if name, _ := item["source_key"].(string); strings.TrimSpace(name) != "" {
			out = append(out, strings.TrimSpace(name))
		}
	}
	return out
}

// CollectGlobalVarRefs sums up an asset's dependency on global variables.
//
// Three sources are scanned:
//  1. structured references in node input variables (source_type: "global") — the current workflow
//     form;
//  2. references in the payload text (the document form and the old workflow form, mainly for
//     legacy packages);
//  3. ${global_vars.X} in the definition-domain documents (*.md, AGENT.md / SKILL.md).
//
// Documents are scanned, scripts are not: scripts run in the sandbox and are not text-substituted at
// runtime, so scanning them would invent dependencies that the list claims and the run never reads.
//
// When st / dir are empty step 3 is skipped (a workflow has no definition-domain directory).
// An unreadable directory is not an error: the dependency list is best-effort information and must
// not fail the packing.
func CollectGlobalVarRefs(ctx context.Context, st storage.Storage, dir string,
	payload map[string]any) []string {

	// In production the jsonb columns inside the payload are raw bytes (json.RawMessage). Normalise
	// them to map / []any before scanning: without that, both steps silently produce an empty list
	// and an import never warns about "which variable is missing in the target tenant".
	norm := deepCopyMap(payload)
	all := make([]string, 0, 4)
	all = append(all, GlobalVarRefsInNodeInputs(norm)...)
	all = append(all, GlobalVarRefsInValue(norm)...)
	if st != nil && dir != "" {
		all = append(all, collectDocRefs(ctx, st, dir, 0)...)
	}

	seen := make(map[string]struct{}, len(all))
	out := make([]string, 0, len(all))
	for _, name := range all {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Bounds of the document scan: the definition-domain directory is freely edited by the user, so
// both depth and file count need a ceiling, otherwise one deep directory could stall listing.
const (
	docScanMaxDepth = 3
	docScanMaxFiles = 64
)

func collectDocRefs(ctx context.Context, st storage.Storage, dir string, depth int) []string {
	if depth > docScanMaxDepth {
		return nil
	}
	entries, err := st.List(ctx, dir)
	if err != nil {
		return nil
	}

	out := make([]string, 0, 2)
	files := 0
	for _, e := range entries {
		if files >= docScanMaxFiles {
			break
		}
		child := dir + "/" + e.Name
		if e.IsDir {
			out = append(out, collectDocRefs(ctx, st, child, depth+1)...)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name), ".md") {
			continue
		}
		content, rerr := st.ReadFile(ctx, child)
		if rerr != nil {
			continue
		}
		files++
		out = append(out, GlobalVarRefsInText(string(content))...)
	}
	return out
}
