package models

import (
	"strings"
	"testing"

	"flame/tools/xsql"
)

func sampleTool() *CustomTool {
	return &CustomTool{
		Name:        "interest-final-amount",
		DisplayName: "利息终值计算",
		Description: "按本金/年数/年利率算终值",
		Parameters: xsql.JSONB{Data: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"principal": map[string]any{"type": "number"},
			},
			"required": []any{"principal"},
		}},
		Output: xsql.JSONB{Data: map[string]any{
			"type":     "object",
			"required": []any{"final_amount"},
		}},
		Lang:           "python",
		Entry:          "main.py",
		TimeoutMS:      60000,
		MaxOutputBytes: 65536,
		Egress:         true,
		ConfigFields: xsql.JSONB{Data: []any{
			map[string]any{"key": "rate_source", "label": "利率来源", "type": "string", "required": true},
		}},
		Status:  StatusNormal,
		Configs: xsql.JSONB{Data: map[string]any{"rate_source": "pboc"}},
	}
}

// The contract must round-trip losslessly: the file is the only source of truth, and a field lost on serialisation means every read changes
// the contract.
func TestToolFileRoundTrip(t *testing.T) {
	src := sampleTool()
	f := ToolFileOf(src)

	data, err := MarshalToolFile(f)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	back, err := ParseToolFile(data)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if back.Name != src.Name || back.Description != src.Description {
		t.Errorf("basic fields were lost: %+v", back)
	}
	if back.Lang != "python" || back.Entry != "main.py" {
		t.Errorf("language / entry do not match: %s %s", back.Lang, back.Entry)
	}
	if back.TimeoutMS != 60000 || back.MaxOutputBytes != 65536 || !back.Egress {
		t.Errorf("execution parameters were lost: %+v", back)
	}
	if got := back.Parameters["type"]; got != "object" {
		t.Errorf("the input schema was lost: %#v", back.Parameters)
	}
	if len(back.ConfigFields) != 1 || back.ConfigFields[0].Key != "rate_source" {
		t.Errorf("the configuration declaration was lost: %#v", back.ConfigFields)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("the file should end with a newline (diff friendly)")
	}
}

// Apply overwrites contract fields only: policy (status) and configuration values (configs) must stay with the table and the tenant.
func TestToolFileApplyKeepsPolicy(t *testing.T) {
	tool := &CustomTool{
		Name:        "demo",
		Description: "old description",
		Status:      StatusLocked,
		Configs:     xsql.JSONB{Data: map[string]any{"api_key": "k-1234567"}},
	}
	f := &ToolFile{
		Name:        "demo",
		Description: "new description",
		Lang:        "js",
		Entry:       "main.js",
		ConfigFields: []ScriptConfigField{
			{Key: "api_key", Secret: true},
		},
	}
	f.Normalize()
	f.Apply(tool)

	if tool.Description != "new description" || tool.Lang != "js" || tool.Entry != "main.js" {
		t.Errorf("contract fields were not applied: %+v", tool)
	}
	if tool.Status != StatusLocked {
		t.Errorf("the contract must not change status: %d", tool.Status)
	}
	if tool.ConfigMap()["api_key"] != "k-1234567" {
		t.Errorf("the contract must not change configs: %#v", tool.ConfigMap())
	}
}

// An old / hand-written TOOL.json often lacks the config_fields key: that must not clear the table's declaration to nil - it would be stored
// as NULL and the NOT NULL column fails outright.
func TestToolFileApplyKeepsConfigFieldsWhenAbsent(t *testing.T) {
	tool := &CustomTool{
		Name:         "demo",
		ConfigFields: xsql.JSONB{Data: []any{map[string]any{"key": "api_key"}}},
	}
	f := &ToolFile{Name: "demo", Lang: "python", Entry: "main.py"}
	f.Normalize()
	f.Apply(tool)
	if tool.ConfigFields.Data == nil {
		t.Fatal("a file without config_fields should keep the table's value, not set nil")
	}

	// An explicit empty array = clear the declaration (the same "overwrite on demand" semantics as Parameters / Output)
	empty := &ToolFile{Name: "demo", Lang: "python", Entry: "main.py", ConfigFields: []ScriptConfigField{}}
	empty.Normalize()
	empty.Apply(tool)
	if tool.ConfigFields.Data == nil {
		t.Fatal("an explicit empty array should be written as [], not nil")
	}
}

// A name that does not match the directory must be refused: otherwise "the directory is called a and the grant is called b", and nobody can
// match them up when investigating.
func TestToolFileValidateRejectsMismatch(t *testing.T) {
	f := &ToolFile{Name: "other", Lang: "python", Entry: "main.py"}
	f.Normalize()
	if err := f.Validate("demo"); err == nil || !strings.Contains(err.Error(), "does not match the directory name") {
		t.Fatalf("it should report the name mismatch, got %v", err)
	}

	ok := &ToolFile{Lang: "python", Entry: "main.py"}
	ok.Normalize()
	if err := ok.Validate("demo"); err != nil {
		t.Fatalf("an empty name should count as matching: %v", err)
	}
}

func TestValidateToolEntry(t *testing.T) {
	bad := []string{"", "../x.py", "lib/../../x.py", "/abs/main.py", `lib\main.py`}
	for _, e := range bad {
		if err := ValidateToolEntry(e); err == nil {
			t.Errorf("entry %q should be refused", e)
		}
	}
	for _, e := range []string{"main.py", "lib/util.py", "./main.py"} {
		if err := ValidateToolEntry(e); err != nil {
			t.Errorf("entry %q should pass, got %v", e, err)
		}
	}
}

// Parsing fills in defaults: a hand-written TOOL.json often only has description + parameters.
func TestParseToolFileFillsDefaults(t *testing.T) {
	f, err := ParseToolFile([]byte(`{"description":"d","parameters":{"type":"object"}}`))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if f.Lang != "python" || f.Entry != "main.py" || f.TimeoutMS != 60000 || f.MaxOutputBytes != 65536 {
		t.Fatalf("defaults were not filled in: %+v", f)
	}
}
