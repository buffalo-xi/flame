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

// 契约必须能无损往返：文件是唯一真源，序列化丢字段等于每次读都在改契约。
func TestToolFileRoundTrip(t *testing.T) {
	src := sampleTool()
	f := ToolFileOf(src)

	data, err := MarshalToolFile(f)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	back, err := ParseToolFile(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if back.Name != src.Name || back.Description != src.Description {
		t.Errorf("基础字段丢失: %+v", back)
	}
	if back.Lang != "python" || back.Entry != "main.py" {
		t.Errorf("语言/入口不一致: %s %s", back.Lang, back.Entry)
	}
	if back.TimeoutMS != 60000 || back.MaxOutputBytes != 65536 || !back.Egress {
		t.Errorf("执行参数丢失: %+v", back)
	}
	if got := back.Parameters["type"]; got != "object" {
		t.Errorf("入参 schema 丢失: %#v", back.Parameters)
	}
	if len(back.ConfigFields) != 1 || back.ConfigFields[0].Key != "rate_source" {
		t.Errorf("配置声明丢失: %#v", back.ConfigFields)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("文件应以换行结尾（diff 友好）")
	}
}

// Apply 只覆盖契约字段：策略（status）与配置值（configs）必须由表和租户说了算。
func TestToolFileApplyKeepsPolicy(t *testing.T) {
	tool := &CustomTool{
		Name:        "demo",
		Description: "旧描述",
		Status:      StatusLocked,
		Configs:     xsql.JSONB{Data: map[string]any{"api_key": "k-1234567"}},
	}
	f := &ToolFile{
		Name:        "demo",
		Description: "新描述",
		Lang:        "js",
		Entry:       "main.js",
		ConfigFields: []ScriptConfigField{
			{Key: "api_key", Secret: true},
		},
	}
	f.Normalize()
	f.Apply(tool)

	if tool.Description != "新描述" || tool.Lang != "js" || tool.Entry != "main.js" {
		t.Errorf("契约字段未覆盖: %+v", tool)
	}
	if tool.Status != StatusLocked {
		t.Errorf("status 不该被契约改动: %d", tool.Status)
	}
	if tool.ConfigMap()["api_key"] != "k-1234567" {
		t.Errorf("configs 不该被契约改动: %#v", tool.ConfigMap())
	}
}

// 老 / 手写的 TOOL.json 常没有 config_fields 这个键：
// 不能因此把表里的声明清成 nil —— 落库时是 NULL，NOT NULL 列会直接报错。
func TestToolFileApplyKeepsConfigFieldsWhenAbsent(t *testing.T) {
	tool := &CustomTool{
		Name:         "demo",
		ConfigFields: xsql.JSONB{Data: []any{map[string]any{"key": "api_key"}}},
	}
	f := &ToolFile{Name: "demo", Lang: "python", Entry: "main.py"}
	f.Normalize()
	f.Apply(tool)
	if tool.ConfigFields.Data == nil {
		t.Fatal("文件没写 config_fields 时应保留表里的值，不能置成 nil")
	}

	// 显式空数组 = 清空声明（与 Parameters / Output 同一套「按需覆盖」语义）
	empty := &ToolFile{Name: "demo", Lang: "python", Entry: "main.py", ConfigFields: []ScriptConfigField{}}
	empty.Normalize()
	empty.Apply(tool)
	if tool.ConfigFields.Data == nil {
		t.Fatal("显式空数组应写成 []，而不是 nil")
	}
}

// 名字与目录不一致必须拒绝：否则「目录叫 a、授权叫 b」，排查时对不上。
func TestToolFileValidateRejectsMismatch(t *testing.T) {
	f := &ToolFile{Name: "other", Lang: "python", Entry: "main.py"}
	f.Normalize()
	if err := f.Validate("demo"); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("应报名字不一致，实际 %v", err)
	}

	ok := &ToolFile{Lang: "python", Entry: "main.py"}
	ok.Normalize()
	if err := ok.Validate("demo"); err != nil {
		t.Fatalf("留空 name 应视为一致: %v", err)
	}
}

func TestValidateToolEntry(t *testing.T) {
	bad := []string{"", "../x.py", "lib/../../x.py", "/abs/main.py", `lib\main.py`}
	for _, e := range bad {
		if err := ValidateToolEntry(e); err == nil {
			t.Errorf("entry %q 应被拒绝", e)
		}
	}
	for _, e := range []string{"main.py", "lib/util.py", "./main.py"} {
		if err := ValidateToolEntry(e); err != nil {
			t.Errorf("entry %q 应通过，实际 %v", e, err)
		}
	}
}

// 解析时要补默认值：手写的 TOOL.json 常常只写 description + parameters。
func TestParseToolFileFillsDefaults(t *testing.T) {
	f, err := ParseToolFile([]byte(`{"description":"d","parameters":{"type":"object"}}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if f.Lang != "python" || f.Entry != "main.py" || f.TimeoutMS != 60000 || f.MaxOutputBytes != 65536 {
		t.Fatalf("默认值未补齐: %+v", f)
	}
}
