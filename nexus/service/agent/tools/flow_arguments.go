package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ===== update_flow_arguments =====
//
// Entry parameters (flows.arguments) are not a standalone entity: they are just one JSON field of the flows row, with no id
// and no table of their own. Adding and changing are the same upsert underneath, so this is not split into a
// create / update / delete trio - one "add city and mark it required" would cost three round trips with three tools, and the
// intermediate state would be "required points at a parameter that does not exist".
//
// Why this tool exists: without it, changing parameters would mean delete_flow + create_flow, and rebuilding replaces
// flows.id - service credential allowlists are granted per target entity id (svc_auth_binding_targets), so the old rows would
// silently stop matching; run records, version history and checkpoints would be wiped as well.

type updateFlowArgumentsTool struct{}

func (t *updateFlowArgumentsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_flow_arguments",
		Category: CategoryFlowAdmin,
		Description: "Add, change or remove a workflow's entry parameters (flows.arguments): set replaces a parameter's declaration wholesale by name (adds it when absent), " +
			"remove deletes by name. Declared parameters are the run_flow inputs keys and the input_schema of an exposed endpoint. " +
			"**Only entry parameters are touched**: name / description / diagram / limits are never changed (use the node tools to change structure). " +
			"Changes land in the working copy: run_flow test runs and the endpoint input_schema pick them up immediately; " +
			"but formal calls run the **released version**, so the new declaration only takes effect after you hit Publish in the editor. " +
			"Changing a type or adding a required parameter makes existing callers send wrong or missing values — call read_flow first to see the current state.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"flow": map[string]any{"type": "string", "description": flowRefHint},
				"set": map[string]any{
					"type": "object",
					"description": "Replace that parameter's declaration wholesale by name (adds it when the name is absent). The shape matches create_flow's arguments: " +
						`{"city":{"type":"string","description":"city name"}}. ` +
						"type supports string / number / boolean / object / array only (array must carry items.type; object may also carry properties / required).",
				},
				"remove": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "Parameter names to delete. They are dropped from required as well; node config is **not** changed, " +
						"so source_type=argument variables that still reference them silently resolve to nothing",
				},
				"required": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
					"description": "The required list, **replaced wholesale**: omit = keep the current state (deleted ones are dropped automatically); pass [] = make everything optional. " +
						"Every name must exist in set or in the existing parameters — call read_flow for the current state first instead of listing only this round's addition",
				},
			},
			"required": []any{"flow"},
		},
	}
}

func (t *updateFlowArgumentsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	ref := strings.TrimSpace(argString(args, "flow"))
	if ref == "" {
		return nil, errors.New("flow must not be empty (workflow id or name)")
	}
	if c.Flows == nil {
		return nil, errors.New("flow manager not available")
	}
	setRaw, _ := args["set"].(map[string]any)
	remove, _ := argStringSlice(args, "remove")
	rawRequired, hasRequired := args["required"]
	// Passing required alone is a valid edit too: adjust only the required list, leaving the declarations untouched
	if len(setRaw) == 0 && len(remove) == 0 && !hasRequired {
		return nil, errors.New("give at least one of set / remove / required; use read_flow to inspect the current state")
	}
	// The same name in both set and remove is incoherent: refuse instead of letting the execution order decide.
	for _, name := range remove {
		if _, both := setRaw[name]; both {
			return nil, fmt.Errorf("parameter %q is in both set and remove: change it or delete it in one call, not both", name)
		}
	}

	detail, err := c.Flows.Get(ctx, ref, ref)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("workflow %q does not exist: confirm the name with list_flows first", ref)
	}

	// Decode the whole document as is and then change a part of it: hand-written fields such as description / default must
	// not be washed away by one edit.
	doc := map[string]any{}
	if len(detail.Arguments) > 0 {
		if err := json.Unmarshal(detail.Arguments, &doc); err != nil {
			return nil, fmt.Errorf("the existing input arguments are not valid JSON: %w", err)
		}
	}
	doc["type"] = "object"
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}

	for _, name := range remove {
		delete(props, name)
	}
	// Validate set entry by entry: only the named parameters are rewritten, the rest keep their original text
	for name, spec := range setRaw {
		clean, err := validateFlowArgProps(map[string]any{name: spec}, "")
		if err != nil {
			return nil, err
		}
		props[name] = clean[name]
	}
	doc["properties"] = props

	// required: when passed it is replaced wholesale (read_flow first for the current state); when absent it is kept, with the
	// removed entries dropped
	if hasRequired && rawRequired != nil {
		req, err := flowArgRequired(rawRequired, props, "")
		if err != nil {
			return nil, err
		}
		doc["required"] = flowArgAnySlice(req)
	} else if cur, ok := doc["required"].([]any); ok {
		kept := make([]any, 0, len(cur))
		for _, item := range cur {
			if name, ok := item.(string); ok {
				if _, exists := props[name]; exists {
					kept = append(kept, name)
				}
			}
		}
		if len(kept) > 0 {
			doc["required"] = kept
		} else {
			delete(doc, "required")
		}
	}

	merged, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize input arguments: %w", err)
	}
	if _, err := c.Flows.UpdateArguments(ctx, ref, ref, json.RawMessage(merged)); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	changed := make(map[string]bool, len(setRaw)+len(remove))
	for name := range setRaw {
		changed[name] = true
	}
	for _, name := range remove {
		changed[name] = true
	}
	touched := make([]string, 0, len(changed))
	for name := range changed {
		touched = append(touched, name)
	}
	sort.Strings(touched)

	return map[string]any{
		"flow_id":   detail.ID,
		"flow_name": detail.Name,
		"arguments": names,
		"changed":   touched,
		"note":      "written to the working copy: run_flow picks it up immediately; the live entry point switches to the new declaration only after you release it in the editor",
	}, nil
}
