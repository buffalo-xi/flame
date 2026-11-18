package models

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"flame/tools/xsql"
)

// ToolFileName 工具目录里的契约文件名。
//
// 为什么契约要落成文件（而不是只存在 custom_tools 表里）：
//   - 契约（description / parameters / output）与实现代码是**同一件事的两面**：
//     改 schema 不改代码、或反过来，都会让模型拿到与实际行为不符的工具；
//     放同一个目录才能在一个 commit 里改完、一起 review、一起回滚。
//   - 插件的做法就是「目录是实体、清单不落库」（service/plugin/registry.go 的注释），
//     工具向它对齐后，盘上的目录与平台认知不会再各说一套。
//   - 表里剩下的应该是**租户对它的策略**（status / configs / 运行态），
//     而不是「这个工具是什么」。
//
// 与插件的一个关键差别：插件的契约由**执行 tool.py** 得出（插件是平台方写的，可信）；
// 自定义工具是租户写的代码，绝不能为了读契约去执行它 —— 所以这里是声明式 JSON，
// 由 Go 直接解析。
const ToolFileName = "TOOL.json"

// toolFileSchemaVersion 当前契约文件版本。将来加字段时靠它做兼容判断。
const toolFileSchemaVersion = 1

// ToolFile 工具目录里的契约（TOOL.json）。
//
// 字段与 CustomTool 的「契约部分」一一对应；status / configs / 时间戳**不在**其中：
// 那些是租户策略与运行态，属于表，不属于工具本身。
type ToolFile struct {
	SchemaVersion int `json:"schema_version,omitempty"`
	// Name 必须与目录名一致；留空视为一致（写盘时总会写上）。
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description"`
	Version     string `json:"version,omitempty"`

	// Parameters 入参 JSON Schema（发给模型）。
	Parameters map[string]any `json:"parameters"`
	// Output 出参 JSON Schema（不发模型，仅校验与展示）。
	Output map[string]any `json:"output,omitempty"`

	Lang  string `json:"lang"`
	Entry string `json:"entry"`

	TimeoutMS      int  `json:"timeout_ms,omitempty"`
	MaxOutputBytes int  `json:"max_output_bytes,omitempty"`
	Egress         bool `json:"egress,omitempty"`

	ConfigFields []ScriptConfigField `json:"config_fields,omitempty"`

	// Implementations 预留位：语义等价的多个实现（插件那套 impl + fallback）。
	//
	// 现在**不消费**：自定义工具是租户自己的一条业务逻辑，多实现的两个典型价值
	// （多租户用不同后端、换供应商）已由「每租户一个目录」和配置项覆盖。
	// 留成可选数组是为了将来真要加时不用改契约格式、不用搬目录 ——
	// 缺省即单实现（顶层 lang / entry）。
	Implementations []ToolFileImpl `json:"implementations,omitempty"`
}

// ToolFileImpl 一个实现的声明（当前仅占位，见 ToolFile.Implementations 注释）。
type ToolFileImpl struct {
	Name         string              `json:"name"`
	DisplayName  string              `json:"display_name,omitempty"`
	Lang         string              `json:"lang"`
	Entry        string              `json:"entry"`
	ConfigFields []ScriptConfigField `json:"config_fields,omitempty"`
}

// ToolFileOf 由表行生成契约文件（迁移与写盘用）。
//
// 与 repository.Create 的缺省值保持同一口径：文件与行在任何时刻都不该因为
// 「谁先补的默认值」而不同。
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

// Normalize 补默认值（解析之后调用）：容忍手写的 TOOL.json 省略这些字段。
func (f *ToolFile) Normalize() {
	f.SchemaVersion = toolFileSchemaVersion
	f.Lang = normalizeToolLang(f.Lang)
	f.Entry = normalizeToolEntry(f.Entry, f.Lang)
	f.TimeoutMS = normalizeToolTimeout(f.TimeoutMS)
	f.MaxOutputBytes = normalizeToolMaxOutput(f.MaxOutputBytes)
	f.Parameters = paramSchemaOrEmpty(f.Parameters)
}

// paramSchemaOrEmpty 空 schema 落成「合法的空对象 schema」。
//
// 而不是留 null：TOOL.json 是给人看、给 git diff 的，`"parameters": null` 会让人
// 以为是漏写；写出 {"type":"object","properties":{}} 才是「这个工具没有参数」的明说。
func paramSchemaOrEmpty(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return schema
}

// Apply 把契约覆盖到表行。
//
// 只覆盖**契约字段**：status / configs / created_* / updated_at 一概不动 ——
// 那是策略与运行态，文件没有资格决定它们。
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
	// 文件里没写 config_fields 时保持表里的值。契约文件是「按需覆盖」：
	// 手写 / 老版本写出的 TOOL.json 常常没有这个键，若在这里置成 nil，
	// 落库时会写成 SQL NULL 并撞 NOT NULL 约束（保存直接失败）。
	if f.ConfigFields != nil {
		t.ConfigFields = xsql.JSONB{Data: f.ConfigFields}
	}
}

// Validate 校验契约本身的完整性（名字与目录一致、entry 不越界、语言受支持）。
//
// dirName 是工具目录名：契约里的 name 与它不一致时**不能静默接受** ——
// 那会让「目录叫 a、授权叫 b」，排查时谁都对不上。
func (f *ToolFile) Validate(dirName string) error {
	if n := strings.TrimSpace(f.Name); n != "" && n != dirName {
		return fmt.Errorf("TOOL.json 的 name %q 与目录名 %q 不一致", n, dirName)
	}
	if err := ValidateToolEntry(f.Entry); err != nil {
		return err
	}
	switch normalizeToolLang(f.Lang) {
	case "python", "js", "lua", "sh":
	default:
		return fmt.Errorf("不支持的语言 %q（python / js / lua / sh）", f.Lang)
	}
	return nil
}

// MarshalToolFile 稳定的文件内容：两空格缩进、末尾换行（diff 友好）。
func MarshalToolFile(f *ToolFile) ([]byte, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseToolFile 解析契约文件；解析后做归一化（调用方仍需 Validate 才可用）。
func ParseToolFile(data []byte) (*ToolFile, error) {
	var f ToolFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	f.Normalize()
	return &f, nil
}

// ValidateToolEntry 入口文件必须是工具目录内的相对路径（以 / 分隔、不含 ..）。
//
// 与沙箱挂载同一条判据的「声明侧」：文件里的 entry 越界，就等于声明了一个
// 挂载之外的路径，必须在读契约时就拒掉（而不是等执行时才发现）。
func ValidateToolEntry(entry string) error {
	e := strings.TrimSpace(entry)
	if e == "" {
		return fmt.Errorf("entry 不能为空")
	}
	if strings.ContainsRune(e, '\\') || path.IsAbs(e) {
		return fmt.Errorf("entry %q 必须是相对工具目录、以 / 分隔的路径", e)
	}
	clean := path.Clean(e)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("entry %q 不能跳出工具目录", e)
	}
	return nil
}

// normalizeToolLang 与 service/agent 的 defaultScriptLang 同口径（那里是创建时的归一化）。
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
