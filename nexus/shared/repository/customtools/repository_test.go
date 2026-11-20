package customtools

import (
	"testing"

	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// A nil JSONB is written as SQL NULL and trips the NOT NULL constraint directly.
// This guarantees a legal empty value is filled in before writing, without overwriting an existing one.
func TestNormalizeRequiredJSON(t *testing.T) {
	tool := &models.CustomTool{}
	normalizeRequiredJSON(tool)

	for name, col := range map[string]xsql.JSONB{
		"parameters":    tool.Parameters,
		"config_fields": tool.ConfigFields,
		"configs":       tool.Configs,
	} {
		v, err := col.Value()
		if err != nil {
			t.Fatalf("%s failed to serialise: %v", name, err)
		}
		if v == nil {
			t.Fatalf("%s is still NULL: saving would trip the NOT NULL constraint", name)
		}
	}

	had := &models.CustomTool{
		ConfigFields: xsql.JSONB{Data: []any{map[string]any{"key": "api_key"}}},
	}
	normalizeRequiredJSON(had)
	if had.ConfigFields.Data == nil {
		t.Fatal("an existing value must not be overwritten")
	}
}
