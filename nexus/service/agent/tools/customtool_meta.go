package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/shared/capability"
)

// The meta tools for custom tools (super agent only, CategoryCustomTool).
//
// Why this layer is needed: the super agent can change files inside an **existing** tool (gate.go lets tools/{name} through as
// an entity), but two things it cannot do - create an entity root (the file gate refuses that outright, otherwise mkdir would
// produce orphans that "exist on disk but not in the database"), and change the custom_tools row (description / parameter
// schema / language / entry / timeout / egress / config). Without meta tools the super agent could only edit tools someone else
// created.
//
// The division of labour matches create_agent / create_shared_skill: **the meta tool builds the shell, the file tools fill
// it in**.
// create writes a minimal runnable skeleton (SkeletonForLang, the same source as the admin-side creation), and the super agent
// then overwrites tools/<name>/<entry> with write_file; by the time the file is written the entity exists with status=1, so the
// gate lets it through.

// CustomToolInput is the input for creating a custom tool (the fields map one to one onto the table).
type CustomToolInput struct {
	Name           string
	DisplayName    string
	Description    string
	Lang           string
	Entry          string
	Parameters     map[string]any
	Output         map[string]any
	TimeoutMS      int
	MaxOutputBytes int
	Egress         bool
	ConfigFields   []map[string]any
	Configs        map[string]any
}

// CustomToolUpdate is the input for updating a custom tool.
//
// The pointer semantics match SkillInput: nil = leave untouched; for Parameters/Output/ConfigFields/Configs nil means "leave
// untouched" and a non-nil empty value means "clear".
//
// There is no Name: renaming would silently break custom:<name> in agent_tool_grants, so a rename is equivalent to "delete and
// recreate", which the caller does explicitly in two steps.
type CustomToolUpdate struct {
	DisplayName    *string
	Description    *string
	Lang           *string
	Entry          *string
	Parameters     map[string]any
	Output         map[string]any
	TimeoutMS      *int
	MaxOutputBytes *int
	Egress         *bool
	ConfigFields   []map[string]any
	Configs        map[string]any
}

// CustomToolRefs lists the referrers reported when a delete is refused (empty = nobody references it).
type CustomToolRefs struct {
	Agents []string
	Skills []string
	Groups []string
}

// Total is the total number of references.
func (r *CustomToolRefs) Total() int {
	if r == nil {
		return 0
	}
	return len(r.Agents) + len(r.Skills) + len(r.Groups)
}

// CustomToolAdmin creates, updates and deletes custom tools (for the meta tools; injected by service/agent).
//
// It is separate from the read-only CustomToolSource: that one is the view on the execution hot path (List/Get/Status), while
// this one carries admin-only semantics and is only ever reached by the super agent.
type CustomToolAdmin interface {
	Create(ctx context.Context, in CustomToolInput) (*CustomToolSpec, error)
	Update(ctx context.Context, name string, in CustomToolUpdate) (*CustomToolSpec, error)
	// Delete removes the table row plus the directory on disk.
	//
	// With force=false, if it is still granted to an agent / required by a skill / referenced by a custom group, the call is
	// **refused** and the referrers are returned for the caller to show. Refusing rather than cleaning up silently: deleting a
	// referenced tool makes a tool vanish from those agents' catalogs with no error at all.
	//
	// The reference rows themselves are not cleaned up (not even with force): a ref pointing at a missing tool resolves to
	// nothing, so keeping it means "recreate after a mistaken delete" restores the grants immediately - the same rule as the
	// admin side.
	Delete(ctx context.Context, name string, force bool) (*CustomToolRefs, error)
	// ListAll lists every tool (locked / deprecated included), for the super agent's self-inspection.
	ListAll(ctx context.Context) ([]CustomToolSpec, error)
}

// ---- create_custom_tool ----

type createCustomToolTool struct{}

func (t *createCustomToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_custom_tool",
		Category: CategoryCustomTool,
		Description: "Create a tenant-level **custom tool** (supervisor only). A custom tool is the tenant's own code: " +
			"the model supplies arguments and gets a structured result; at run time it uses the one-shot /v1/exec process (requires the tenant to have " + capability.Exec + " enabled). " +
			"This tool only creates the entity (DB row + tools/<name>/ directory + a minimal runnable skeleton). Write the code afterwards with " +
			"write_file to /tools/<name>/<entry> (**the leading / means the tenant root**; without it the file lands in the session workspace and the tool cannot read it), " +
			"then grant the ref `custom:<name>` to the target agent with grant_agent_tool. " +
			"The tool name cannot be changed after creation (it would break existing grants).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Tool short name: letters/digits/hyphens, must not start with a hyphen, must not contain a double underscore or a dot",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Purpose shown to the model (decides when it gets called — state the boundaries clearly)",
				},
				"display_name": map[string]any{"type": "string", "description": "Admin-facing display name (not part of the model context; may be empty)"},
				"lang": map[string]any{
					"type":        "string",
					"description": "python (default) / js / lua / sh",
					"enum":        []any{"python", "js", "lua", "sh"},
				},
				"entry": map[string]any{
					"type":        "string",
					"description": "Entry file (relative to tools/<name>/, must stay inside that directory); defaults to main.py/js/lua/sh by language",
				},
				"parameters": map[string]any{
					"type":        "object",
					"description": "Input JSON Schema (sent to the model), e.g. {\"type\":\"object\",\"properties\":{...}}",
				},
				"output": map[string]any{
					"type":        "object",
					"description": "Output JSON Schema (not sent to the model; used only to validate the script output)",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"description": "Per-run timeout in milliseconds (default 60000, max 600000)",
				},
				"max_output_bytes": map[string]any{
					"type":        "integer",
					"description": "stdout cap in bytes (default 65536, max 1048576); exceeding it fails the run",
				},
				"egress": map[string]any{
					"type":        "boolean",
					"description": "Whether the script may reach the internet; requires " + capability.Egress + " to be enabled by the platform, otherwise this tool refuses",
				},
				"config_fields": map[string]any{
					"type":        "array",
					"description": "Config field declarations ([{key,label,type,help,secret,required,default}]); drives the rendered form and decides which values are masked",
				},
				"configs": map[string]any{
					"type":        "object",
					"description": "Config values (plaintext; secret entries are only injected at run time — never hardcode them in the script)",
				},
			},
			"required": []any{"name", "description"},
		},
	}
}

func (t *createCustomToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("custom tool management is not available")
	}
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateCustomToolName(name); err != nil {
		return nil, err
	}
	entry := strings.TrimSpace(argString(args, "entry"))
	if err := validateScriptEntry(entry); err != nil {
		return nil, err
	}
	egress := argBool(args, "egress")
	if err := requireCustomToolCaps(c, egress); err != nil {
		return nil, err
	}

	in := CustomToolInput{
		Name:        name,
		DisplayName: strings.TrimSpace(argString(args, "display_name")),
		Description: strings.TrimSpace(argString(args, "description")),
		Lang:        argString(args, "lang"),
		Entry:       entry,
		TimeoutMS:   argInt(args, "timeout_ms"),
		Egress:      egress,
	}
	in.Parameters, _ = args["parameters"].(map[string]any)
	in.Output, _ = args["output"].(map[string]any)
	in.Configs, _ = args["configs"].(map[string]any)
	in.ConfigFields = argMapSlice(args, "config_fields")
	if v := argInt(args, "max_output_bytes"); v > 0 {
		if v > maxCustomToolOutput {
			return nil, fmt.Errorf("max_output_bytes is at most %d", maxCustomToolOutput)
		}
		in.MaxOutputBytes = v
	}
	if in.TimeoutMS > maxCustomToolTimeoutMS {
		return nil, fmt.Errorf("timeout_ms is at most %d", maxCustomToolTimeoutMS)
	}

	spec, err := c.CustomToolsAdmin.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tool": spec,
		"next": []string{
			// The path must carry the leading "/" (= counted from the tenant root). Without it the path is treated as
			// workspace-relative, the file lands in users/{uid}/sessions|runs/... while the tool still reads the skeleton under
			// tools/<name>/ - the change has no effect.
			"write_file /tools/" + name + "/" + entryOrDefault(entry, in.Lang),
			"grant_agent_tool ref=custom:" + name,
		},
	}, nil
}

// ---- update_custom_tool ----

type updateCustomToolTool struct{}

func (t *updateCustomToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_custom_tool",
		Category: CategoryCustomTool,
		Description: "Update a custom tool's **metadata** (description / parameter schema / language / entry / timeout / egress / config; supervisor only). " +
			"Pass only the fields you want to change; everything else stays as it is. Name and status cannot be changed (renaming breaks grants, status switches go through the admin UI only). " +
			"Change the code itself with write_file to tools/<name>/<entry>.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":             map[string]any{"type": "string", "description": "Short name of the tool to update"},
				"display_name":     map[string]any{"type": "string", "description": "Admin-facing display name (pass an empty string to clear)"},
				"description":      map[string]any{"type": "string", "description": "Purpose shown to the model"},
				"lang":             map[string]any{"type": "string", "enum": []any{"python", "js", "lua", "sh"}},
				"entry":            map[string]any{"type": "string", "description": "Entry file (relative to the tool directory)"},
				"parameters":       map[string]any{"type": "object", "description": "Input JSON Schema (replaced wholesale)"},
				"output":           map[string]any{"type": "object", "description": "Output JSON Schema (replaced wholesale)"},
				"timeout_ms":       map[string]any{"type": "integer", "description": "Per-run timeout in milliseconds"},
				"max_output_bytes": map[string]any{"type": "integer", "description": "stdout cap in bytes"},
				"egress":           map[string]any{"type": "boolean", "description": "Whether the script may reach the internet (requires " + capability.Egress + ")"},
				"config_fields":    map[string]any{"type": "array", "description": "Config field declarations (replaced wholesale)"},
				"configs":          map[string]any{"type": "object", "description": "Config values (merged by key; keys you omit keep their current value)"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *updateCustomToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("custom tool management is not available")
	}
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateCustomToolName(name); err != nil {
		return nil, err
	}

	var in CustomToolUpdate
	if v, ok := args["display_name"].(string); ok {
		in.DisplayName = &v
	}
	if v, ok := args["description"].(string); ok {
		in.Description = &v
	}
	if v, ok := args["lang"].(string); ok {
		in.Lang = &v
	}
	if v, ok := args["entry"].(string); ok {
		if err := validateScriptEntry(v); err != nil {
			return nil, err
		}
		in.Entry = &v
	}
	if v, ok := args["parameters"].(map[string]any); ok {
		in.Parameters = v
	}
	if v, ok := args["output"].(map[string]any); ok {
		in.Output = v
	}
	if v := argInt(args, "timeout_ms"); v > 0 {
		if v > maxCustomToolTimeoutMS {
			return nil, fmt.Errorf("timeout_ms is at most %d", maxCustomToolTimeoutMS)
		}
		in.TimeoutMS = &v
	}
	if v := argInt(args, "max_output_bytes"); v > 0 {
		if v > maxCustomToolOutput {
			return nil, fmt.Errorf("max_output_bytes is at most %d", maxCustomToolOutput)
		}
		in.MaxOutputBytes = &v
	}
	if v, ok := args["egress"].(bool); ok {
		if err := requireCustomToolCaps(c, v); err != nil {
			return nil, err
		}
		in.Egress = &v
	}
	if _, ok := args["config_fields"].([]any); ok {
		in.ConfigFields = argMapSlice(args, "config_fields")
	}
	if v, ok := args["configs"].(map[string]any); ok {
		in.Configs = v
	}

	spec, err := c.CustomToolsAdmin.Update(ctx, name, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tool": spec}, nil
}

// ---- delete_custom_tool ----

type deleteCustomToolTool struct{}

func (t *deleteCustomToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "delete_custom_tool",
		Category: CategoryCustomTool,
		Description: "Delete a custom tool and its code directory (supervisor only). If it is still granted to an agent, required by a skill, or " +
			"referenced by a tool group, the call is **refused** by default and the referrers are listed; pass force=true once you are sure. " +
			"Grant rows are not cleaned up, so recreating a tool with the same name restores the grants automatically.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "description": "Short name of the tool to delete"},
				"force": map[string]any{"type": "boolean", "description": "Delete anyway, ignoring references"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *deleteCustomToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("custom tool management is not available")
	}
	name := strings.TrimSpace(argString(args, "name"))
	if err := validateCustomToolName(name); err != nil {
		return nil, err
	}
	force := argBool(args, "force")
	refs, err := c.CustomToolsAdmin.Delete(ctx, name, force)
	// Not forced and still referenced: at the service layer "refused" is reported as (refs, nil) (nothing was deleted), and
	// only a forced call both deletes and returns the referrers, so the criterion must include !force - otherwise a forced
	// delete would be misreported as a refusal.
	if err == nil && !force && refs.Total() > 0 {
		return nil, fmt.Errorf("custom tool %q is still referenced (agents=%v skills=%v groups=%v); pass force=true to delete it anyway",
			name, refs.Agents, refs.Skills, refs.Groups)
	}
	if err != nil {
		if refs.Total() > 0 {
			return nil, fmt.Errorf("%w (referenced by agents=%v skills=%v groups=%v; pass force=true to delete it anyway)",
				err, refs.Agents, refs.Skills, refs.Groups)
		}
		return nil, err
	}
	out := map[string]any{"name": name, "deleted": true}
	if refs.Total() > 0 {
		// The reference rows that were kept: a hint that recreating a tool of the same name restores the grants automatically.
		out["kept_refs"] = map[string]any{
			"agents": refs.Agents, "skills": refs.Skills, "groups": refs.Groups,
		}
	}
	return out, nil
}

// ---- list_custom_tools ----

type listCustomToolsTool struct{}

func (t *listCustomToolsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "list_custom_tools",
		Category: CategoryCustomTool,
		Description: "List every custom tool in this tenant (including locked / deprecated ones; supervisor only). " +
			"The model only sees granted custom__* tools in its catalog and can never see ungranted or locked ones — this tool covers that blind spot.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listCustomToolsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.CustomToolsAdmin == nil {
		return nil, errors.New("custom tool management is not available")
	}
	list, err := c.CustomToolsAdmin.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "tools": list}, nil
}

// ---- Shared validation ----

const (
	maxCustomToolTimeoutMS = 600_000
	maxCustomToolOutput    = 1 << 20
)

// validateCustomToolName validates the short name: the same rule set as the admin creation API.
//
// Both a double underscore and the custom__ prefix must be refused: the outward name is custom__<name>, so a further
// underscore inside the name garbles the prefix and shows the model names like custom__a__b that cannot be split back.
func validateCustomToolName(name string) error {
	if name == "" {
		return errors.New("name must not be empty")
	}
	if !ValidName(name) {
		return fmt.Errorf("tool name %q may only contain letters, digits and hyphens, and must not start with a hyphen", name)
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("tool name %q must not contain consecutive underscores (the exposed name is custom__<name>)", name)
	}
	if strings.HasPrefix(name, CustomToolPrefix) {
		return fmt.Errorf("tool name %q must not start with %q (that is the internal namespace)", name, CustomToolPrefix)
	}
	return nil
}

// validateScriptEntry requires the entry file to be a relative path inside the tool directory.
//
// The same reason as run_skill_script's escape check: entry comes from the model, and one ../ would let the sandbox execute a
// file outside the tool directory.
func validateScriptEntry(entry string) error {
	if entry == "" {
		return nil // the service layer fills in the default for the language
	}
	if strings.ContainsRune(entry, '\\') {
		return fmt.Errorf("entry %q must be separated with /", entry)
	}
	if path.IsAbs(entry) {
		return fmt.Errorf("entry %q must be a path relative to the tool directory", entry)
	}
	clean := path.Clean(entry)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("entry %q must not escape the tool directory", entry)
	}
	return nil
}

// requireCustomToolCaps runs a capability health check before a tool is created.
//
// Why block at creation: if the tool is created but the tenant has no sandbox.exec, the "capability not enabled" error only
// appears when the model calls it - by then it is already granted and written into the system prompt, so triage costs far more
// than an error here.
// egress additionally requires net.egress (it is a cross-channel egress policy and does not come with exec).
func requireCustomToolCaps(c *Context, egress bool) error {
	if c.Caps == nil {
		return nil // a caller with no tenant semantics (unit tests / internal), nothing is gated by capability
	}
	if egress && !c.Caps[capability.Egress] {
		return fmt.Errorf("egress=true requires the platform to grant %s (custom tools run on the exec channel, and egress is a separate grant)", capability.Egress)
	}
	if !c.Caps[capability.Exec] {
		return fmt.Errorf("running custom tools requires the platform to grant %s, which this tenant does not have", capability.Exec)
	}
	return nil
}

// argMapSlice takes an argument in []map[string]any shape.
//
// It cannot be asserted directly: a JSON-decoded array is []any and only the elements are map[string]any.
// nil means "not passed"; an empty array returns a non-nil empty slice (= clear).
func argMapSlice(args map[string]any, key string) []map[string]any {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// entryOrDefault is only used to hint the model's next step; the real default is applied by the service layer.
func entryOrDefault(entry, lang string) string {
	if entry != "" {
		return entry
	}
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "js", "node":
		return "main.js"
	case "lua":
		return "main.lua"
	case "sh", "shell":
		return "main.sh"
	}
	return "main.py"
}
