package tools

import (
	"context"
	"errors"
	"sort"
	"strings"

	globalvarsrepo "flame/nexus/shared/repository/globalvars"
)

// ===== list_global_vars =====
//
// Why this tool exists: global variables are referenced in workflows through node inputs (source_type: "global") and in
// AGENT.md / SKILL.md as `${global_vars.X}`, but **only a human can tell the model their names**. Without this tool the model
// can only guess, and a wrong guess is a hard failure at run time (the error does carry the variable name, but only after
// a run has failed). The skill docs teach "how to use them"; this tool supplies "which ones exist".
//
// Security stance:
//   - **the value is never returned**, only name / kind / description / has_value. The value has no business in the model
//     context - references go by name, and once a value enters the context it lands in session records and SSE, which is
//     exactly the leak we have been closing;
//   - the category is flow-admin (super agent only): the consumer is the identity that writes workflows and docs, and an
//     ordinary agent has no use for the list of names anyway.

type listGlobalVarsTool struct{}

func (t *listGlobalVarsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "list_global_vars",
		Category: CategoryFlowAdmin,
		Description: "List this tenant's global variable names and types (**values are not included**). Check it before writing node config so the names you use are exact. " +
			"Usage in workflows: add an entry to the node's config.inputs — " +
			"`{\"source_type\": \"global\", \"source_key\": \"<name>\"}` — and reference it in templates as `{{ .input.<name> }}`, " +
			"not `{{.global_vars.<name>}}` (that form is retired; leaving it in a config makes the flow fail to load). " +
			"In AGENT.md / SKILL.md keep using `${global_vars.<name>}`. " +
			"kind has four values: text / number / bool / secret. " +
			"**Never put secret variables into prompts**: using them in llm messages or an intent system_prompt is not blocked but logs a warning, " +
			"and the value ends up in the model context and run records; secrets belong in connection / tool config (http headers, mcp/rpc connection params). " +
			"A misspelled name fails hard at run time — it does not silently run empty.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Optional: only list one kind — text / number / bool / secret. Omit for all",
				},
				"keyword": map[string]any{
					"type":        "string",
					"description": "Optional: case-insensitive substring filter on name or description",
				},
			},
			"required": []any{},
		},
	}
}

func (t *listGlobalVarsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c == nil || c.Schema == "" {
		return nil, errors.New("missing tenant context (schema is empty)")
	}

	kind := strings.ToLower(strings.TrimSpace(argString(args, "kind")))
	keyword := strings.ToLower(strings.TrimSpace(argString(args, "keyword")))

	rows, err := globalvarsrepo.NewRepository().List(c.Schema)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if kind != "" && strings.ToLower(r.Kind) != kind {
			continue
		}
		if keyword != "" &&
			!strings.Contains(strings.ToLower(r.Name), keyword) &&
			!strings.Contains(strings.ToLower(r.Description), keyword) {
			continue
		}
		items = append(items, map[string]any{
			"name":        r.Name,
			"kind":        r.Kind,
			"description": r.Description,
			// Only "does it have a value": the model does not need the value itself (references go by name), and a value
			// in the context is a leak
			"has_value": r.Value != "",
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	notes := []string{
		"Values are not returned here: references go by name, and the value is resolved per tenant at run time",
		"Workflows: write {\"source_type\": \"global\", \"source_key\": \"<name>\"} into the node's config.inputs, and `{{ .input.<name> }}` in templates; " +
			"AGENT.md / SKILL.md use `${global_vars.<name>}`",
	}
	if len(items) == 0 {
		notes = append(notes, "This tenant has no global variables yet (create them under Settings → Global variables); for configuration use environment variables or ask the user to create one first")
	}

	return map[string]any{
		"total": len(items),
		"items": items,
		"notes": notes,
	}, nil
}

func init() {
	Register(&listGlobalVarsTool{})
}
