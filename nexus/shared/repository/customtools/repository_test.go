package customtools

import (
	"testing"

	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// nil 的 JSONB 会被写成 SQL NULL，直接撞 NOT NULL 约束。
// 这里保证写库前一定补成合法空值，且不覆盖已有值。
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
			t.Fatalf("%s 序列化失败: %v", name, err)
		}
		if v == nil {
			t.Fatalf("%s 仍是 NULL：保存会撞 NOT NULL 约束", name)
		}
	}

	had := &models.CustomTool{
		ConfigFields: xsql.JSONB{Data: []any{map[string]any{"key": "api_key"}}},
	}
	normalizeRequiredJSON(had)
	if had.ConfigFields.Data == nil {
		t.Fatal("已有值不应被覆盖")
	}
}
