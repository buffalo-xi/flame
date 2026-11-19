package models

import (
	"encoding/json"

	"flame/tools/xsql"
)

// CustomTool is a tenant's custom tool (the table {schema}.custom_tools).
//
// The boundary with built-in tools: built-in tools are registered in code and identical for every tenant; this table is **runtime data**, one
// set per tenant, maintained by the tenant administrator and made usable through tool grants.
//
// The boundary with plugins: a plugin is an implementation discovered on disk (one piece of code + one configuration per tenant), whereas a
// custom tool is the tenant's own code (the code belongs to that one tenant).
//
// In effect = status==1 && granted by agent_tool_grants && the platform has sandbox.exec on.
// Miss any of the three and the catalog entry is greyed out and the execution layer refuses outright - the criterion is held half by
// toolgrant and half by tools.
//
// Why sandbox.exec rather than sandbox.code: a custom tool goes sb.Start -> /v1/exec (a one-shot process, no pool), the same channel as the
// exec tool and skill scripts. sandbox.code is the gate of the /v1/code channel (workflow code nodes) - the criterion is the channel, not
// "is a script file being run".
type CustomTool struct {
	ID   string `json:"id"`
	Name string `json:"name"` // short name; the external tool name is custom__<name>, the grant ref is custom:<name>
	// DisplayName is the panel display name (falls back to name when empty).
	// It serves the UI only: the admin card title, the group heading in the tool grant catalog.
	// It **never enters the model context** (the model's tool name is always custom__<name> and its description always Description), nor the
	// ref / file path - consistent with agent / skill having no display_name.
	DisplayName string `json:"display_name"`
	Description string `json:"description"` // model-visible: decides when it gets called, so the purpose boundary must be spelled out

	// Parameters is the input JSON Schema (sent to the model).
	Parameters xsql.JSONB `json:"parameters"`
	// Output is the output JSON Schema (**not sent to the model** - OpenAI function calling has no such field).
	// It has exactly two uses: validating the script output at run time, and showing "what does this tool return" on the admin side.
	Output xsql.JSONB `json:"output"`

	// Lang is the language name (python / js / lua / sh), the same criterion as sandbox.Spec.Lang: the interpreter entry is resolved by
	// sandboxd for the current mode, and nexus does not hold the knowledge of "what it is called inside the image".
	Lang string `json:"lang"`
	// Entry is the entry file (relative to tools/<name>/), main.py by default.
	Entry string `json:"entry"`

	TimeoutMS      int  `json:"timeout_ms"`       // single-run timeout (still bounded by Context.Timeout)
	MaxOutputBytes int  `json:"max_output_bytes"` // stdout cap; **exceeding it fails** (truncation yields broken JSON)
	Egress         bool `json:"egress"`           // whether egress is allowed (the platform must also enable net.egress)

	// ConfigFields declares the configuration entries, from which the admin side renders the form and decides which ones to mask.
	ConfigFields xsql.JSONB `json:"config_fields"`
	// Configs holds the configuration values (plaintext, the same criterion as plugins.configs: masking happens on read-back only).
	Configs xsql.JSONB `json:"configs"`

	Status    int       `json:"status"` // 1 normal / 2 locked / 3 deprecated (models.StatusXxx)
	CreatedBy string    `json:"created_by"`
	CreatedAt xsql.Time `json:"created_at"`
	UpdatedAt xsql.Time `json:"updated_at"`
}

// ScriptConfigField is one configuration declaration (isomorphic to plugin.ConfigField, so the frontend can reuse one form).
type ScriptConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // string / int / bool
	Help     string `json:"help"`
	Secret   bool   `json:"secret"`   // true: masked on read-back, and writing the masked string back means "unchanged"
	Required bool   `json:"required"` // true: a missing value fails at run time with code=config
	Default  any    `json:"default,omitempty"`
}

// ParamSchema is the input schema; an unparsable one returns an empty object (an unparsable schema is as good as none).
func (t *CustomTool) ParamSchema() map[string]any { return scriptJSONMap(t.Parameters) }

// OutputSchema is the output schema; nil = no validation.
func (t *CustomTool) OutputSchema() map[string]any { return scriptJSONMap(t.Output) }

// ConfigMap returns the configuration values.
func (t *CustomTool) ConfigMap() map[string]any { return scriptJSONMap(t.Configs) }

// ConfigFieldList returns the configuration declarations; an unparsable value counts as an empty list.
func (t *CustomTool) ConfigFieldList() []ScriptConfigField {
	raw := jsonbRaw(t.ConfigFields)
	if raw == nil {
		return nil
	}
	var out []ScriptConfigField
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// Label is the human-readable short title: the display name, falling back to name.
// It is separate from Description: a description is a full sentence for the model and does not make a good list title.
func (t *CustomTool) Label() string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return t.Name
}

// scriptJSONMap decodes JSONB into a map; empty / invalid always returns nil, letting the caller treat it as "no schema".
func scriptJSONMap(v xsql.JSONB) map[string]any {
	raw := jsonbRaw(v)
	if raw == nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
