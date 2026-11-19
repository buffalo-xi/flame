package models

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"flame/tools/xsql"
)

// ToolFileName is the contract file name inside a tool directory.
//
// Why the contract is a file (rather than living only in the custom_tools table):
//   - the contract (description / parameters / output) and the implementation code are **two sides of one thing**: changing the schema
//     without the code, or the other way round, hands the model a tool that does not match its behaviour; keeping them in one directory
//     means they change in one commit, get reviewed together and roll back together.
//   - that is exactly what plugins do ("the directory is the entity, the manifest is not stored in the database" - see the notes in
//     service/plugin/registry.go), and aligning the tools with it stops the on-disk directory and the platform's understanding from telling
//     two different stories.
//   - what remains in the table should be **the tenant's policy towards it** (status / configs / runtime state), not "what this tool is".
//
// One key difference from plugins: a plugin's contract is obtained by **executing tool.py** (plugins are written by the platform and are
// trusted); a custom tool is tenant-written code and must never be executed just to read its contract - hence declarative JSON here, parsed
// directly by Go.
const ToolFileName = "TOOL.json"

// toolFileSchemaVersion is the current contract file version. It is there to make compatibility decisions when fields are added later.
const toolFileSchemaVersion = 1

// ToolFile is the contract inside a tool directory (TOOL.json).
//
// The fields correspond one to one with CustomTool's "contract part"; status / configs / timestamps are **not** among them: those are tenant
// policy and runtime state, and belong to the table rather than to the tool itself.
type ToolFile struct {
	SchemaVersion int `json:"schema_version,omitempty"`
	// Name must match the directory name; empty counts as matching (it is always written when saving).
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description"`
	Version     string `json:"version,omitempty"`

	// Parameters is the input JSON Schema (sent to the model).
	Parameters map[string]any `json:"parameters"`
	// Output is the output JSON Schema (not sent to the model; verification and display only).
	Output map[string]any `json:"output,omitempty"`

	Lang  string `json:"lang"`
	Entry string `json:"entry"`

	TimeoutMS      int  `json:"timeout_ms,omitempty"`
	MaxOutputBytes int  `json:"max_output_bytes,omitempty"`
	Egress         bool `json:"egress,omitempty"`

	ConfigFields []ScriptConfigField `json:"config_fields,omitempty"`

	// Implementations is a reserved slot: several semantically equivalent implementations (the plugin impl + fallback scheme).
	//
	// It is **not consumed** today: a custom tool is one piece of business logic written by the tenant, and the two typical reasons for
	// multiple implementations (different backends per tenant, swapping vendors) are already covered by "one directory per tenant" and by
	// configuration entries.
	// It is left as an optional array so that adding it later needs neither a contract format change nor a directory move - the default is a
	// single implementation (the top-level lang / entry).
	Implementations []ToolFileImpl `json:"implementations,omitempty"`
}

// ToolFileImpl is the declaration of one implementation (currently a placeholder only, see the notes on ToolFile.Implementations).
type ToolFileImpl struct {
	Name         string              `json:"name"`
	DisplayName  string              `json:"display_name,omitempty"`
	Lang         string              `json:"lang"`
	Entry        string              `json:"entry"`
	ConfigFields []ScriptConfigField `json:"config_fields,omitempty"`
}

// ToolFileOf builds the contract file from a table row (used by migrations and when writing to disk).
//
// It keeps the same criterion as repository.Create's defaults: the file and the row must never differ just because of "who filled in the
// default first".
func ToolFileOf(t *CustomTool) *ToolFile {
	return &ToolFile{
		SchemaVersion:  toolFileSchemaVersion,
		Name:           t.Name,
		DisplayName:    t.DisplayName,
		Description:    t.Description,
		Parameters:     paramSchemaOrEmpty(t.ParamSchema()),
		Output:         t.OutputSchema(),
		Lang:           normalizeToolLang(t.Lang),
		Entry:          normalizeToolEntry(t.Entry, t.Lang),
		TimeoutMS:      normalizeToolTimeout(t.TimeoutMS),
		MaxOutputBytes: normalizeToolMaxOutput(t.MaxOutputBytes),
		Egress:         t.Egress,
		ConfigFields:   t.ConfigFieldList(),
	}
}

// Normalize fills in defaults (call it after parsing): it tolerates a hand-written TOOL.json omitting these fields.
func (f *ToolFile) Normalize() {
	f.SchemaVersion = toolFileSchemaVersion
	f.Lang = normalizeToolLang(f.Lang)
	f.Entry = normalizeToolEntry(f.Entry, f.Lang)
	f.TimeoutMS = normalizeToolTimeout(f.TimeoutMS)
	f.MaxOutputBytes = normalizeToolMaxOutput(f.MaxOutputBytes)
	f.Parameters = paramSchemaOrEmpty(f.Parameters)
}

// paramSchemaOrEmpty turns an empty schema into a "valid empty object schema".
//
// Rather than leaving null: TOOL.json is for people to read and for git diffs, and `"parameters": null` reads as an omission, whereas writing
// {"type":"object","properties":{}} states outright that "this tool takes no arguments".
func paramSchemaOrEmpty(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return schema
}

// Apply writes the contract onto a table row.
//
// It overwrites **contract fields only**: status / configs / created_* / updated_at are untouched - those are policy and runtime state, and a
// file has no business deciding them.
func (f *ToolFile) Apply(t *CustomTool) {
	if name := strings.TrimSpace(f.Name); name != "" {
		t.Name = name
	}
	t.DisplayName = strings.TrimSpace(f.DisplayName)
	t.Description = strings.TrimSpace(f.Description)
	if f.Parameters != nil {
		t.Parameters = xsql.JSONB{Data: f.Parameters}
	}
	if f.Output != nil {
		t.Output = xsql.JSONB{Data: f.Output}
	}
	t.Lang = normalizeToolLang(f.Lang)
	t.Entry = normalizeToolEntry(f.Entry, t.Lang)
	t.TimeoutMS = normalizeToolTimeout(f.TimeoutMS)
	t.MaxOutputBytes = normalizeToolMaxOutput(f.MaxOutputBytes)
	t.Egress = f.Egress
	// When the file has no config_fields, keep the row's value. The contract file overwrites on demand: a hand-written / old-version TOOL.json
	// often lacks this key, and setting it to nil here would store SQL NULL and trip the NOT NULL constraint (the save fails outright).
	if f.ConfigFields != nil {
		t.ConfigFields = xsql.JSONB{Data: f.ConfigFields}
	}
}

// Validate checks the contract's own integrity (the name matches the directory, the entry stays inside it, the language is supported).
//
// dirName is the tool directory name: when the name in the contract differs, it must **not be accepted silently** - that would mean "the
// directory is called a and the grant is called b", and nobody can match them up when investigating.
func (f *ToolFile) Validate(dirName string) error {
	if n := strings.TrimSpace(f.Name); n != "" && n != dirName {
		return fmt.Errorf("the name %q in TOOL.json does not match the directory name %q", n, dirName)
	}
	if err := ValidateToolEntry(f.Entry); err != nil {
		return err
	}
	switch normalizeToolLang(f.Lang) {
	case "python", "js", "lua", "sh":
	default:
		return fmt.Errorf("unsupported language %q (python / js / lua / sh)", f.Lang)
	}
	return nil
}

// MarshalToolFile produces stable file content: two-space indent and a trailing newline (diff friendly).
func MarshalToolFile(f *ToolFile) ([]byte, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseToolFile parses a contract file; it normalises after parsing (the caller still has to Validate before using it).
func ParseToolFile(data []byte) (*ToolFile, error) {
	var f ToolFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	f.Normalize()
	return &f, nil
}

// ValidateToolEntry requires the entry file to be a relative path inside the tool directory (slash separated, no ..).
//
// It is the "declaration side" of the same criterion as the sandbox mount: an entry that escapes means a path outside the mount is declared,
// and it must be refused while reading the contract (not discovered at execution time).
func ValidateToolEntry(entry string) error {
	e := strings.TrimSpace(entry)
	if e == "" {
		return fmt.Errorf("entry must not be empty")
	}
	if strings.ContainsRune(e, '\\') || path.IsAbs(e) {
		return fmt.Errorf("entry %q must be a slash-separated path relative to the tool directory", e)
	}
	clean := path.Clean(e)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("entry %q must not escape the tool directory", e)
	}
	return nil
}

// normalizeToolLang uses the same criterion as service/agent's defaultScriptLang (which normalises at creation time).
func normalizeToolLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "js", "node":
		return "js"
	case "lua":
		return "lua"
	case "sh", "shell":
		return "sh"
	default:
		return "python"
	}
}

func normalizeToolEntry(entry, lang string) string {
	if e := strings.TrimSpace(entry); e != "" {
		return e
	}
	switch normalizeToolLang(lang) {
	case "js":
		return "main.js"
	case "lua":
		return "main.lua"
	case "sh":
		return "main.sh"
	}
	return "main.py"
}

func normalizeToolTimeout(ms int) int {
	if ms <= 0 {
		return 60000
	}
	return ms
}

func normalizeToolMaxOutput(n int) int {
	if n <= 0 {
		return 65536
	}
	return n
}
