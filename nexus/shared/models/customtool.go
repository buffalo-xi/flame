package models

import (
	"encoding/json"

	"flame/tools/xsql"
)

// CustomTool 租户自定义的自定义工具（表 {schema}.custom_tools）。
//
// 与内置工具的边界：内置工具在代码里注册、对所有租户一致；本表是**运行期数据**，
// 一个租户一份，由租户管理员维护、由工具授权决定谁能用。
//
// 与 plugins 的边界：插件是磁盘上发现的实现（一份代码 + 每租户一份配置），
// 自定义工具是租户自己的代码（代码只属于这一个租户）。
//
// 生效 = status==1 && 被 agent_tool_grants 授权 && 平台开通 sandbox.exec。
// 三者缺一，目录置灰、执行层直接拒 —— 判据在 toolgrant 与 tools 两层各自握一半。
//
// 为什么是 sandbox.exec 而不是 sandbox.code：自定义工具走 sb.Start → /v1/exec
// （一次性进程、无池），与 exec 工具、技能脚本同一条通道。sandbox.code 是
// /v1/code 通道（工作流 code 节点）的闸 —— 判据是通道，不是「跑的是脚本文件」。
type CustomTool struct {
	ID   string `json:"id"`
	Name string `json:"name"` // 短名；对外工具名 custom__<name>，授权 ref custom:<name>
	// DisplayName 面板展示名（空则用 name）。
	// 只服务 UI：管理端卡片标题、工具授权目录的组标题。
	// **不进模型上下文**（模型的工具名恒为 custom__<name>、描述恒为 Description），
	// 也不进 ref / 文件路径 —— 与 agent / skill 不设 display_name 的分工一致。
	DisplayName string `json:"display_name"`
	Description string `json:"description"` // 模型可见：决定它何时被调用，必须写清用途边界

	// Parameters 入参 JSON Schema（发给模型）。
	Parameters xsql.JSONB `json:"parameters"`
	// Output 出参 JSON Schema（**不发给模型** —— OpenAI function calling 没有该字段）。
	// 用途只有两个：运行时校验脚本输出、管理端展示"这个工具返回什么"。
	Output xsql.JSONB `json:"output"`

	// Lang 语言名（python / js / lua / sh），与 sandbox.Spec.Lang 同口径：
	// 解释器入口由 sandboxd 按当前 mode 解析，nexus 不持有"镜像里叫什么"这份认知。
	Lang string `json:"lang"`
	// Entry 入口文件（相对 tools/<name>/），默认 main.py。
	Entry string `json:"entry"`

	TimeoutMS      int  `json:"timeout_ms"`       // 单次执行超时（仍受 Context.Timeout 上限约束）
	MaxOutputBytes int  `json:"max_output_bytes"` // stdout 上限；**超限即失败**（截断会得到坏 JSON）
	Egress         bool `json:"egress"`           // 是否允许出网（还需平台开通 net.egress）

	// ConfigFields 配置项声明，管理端据此渲染表单并决定哪几项打码。
	ConfigFields xsql.JSONB `json:"config_fields"`
	// Configs 配置值（明文，与 plugins.configs 同口径：打码只发生在回显）。
	Configs xsql.JSONB `json:"configs"`

	Status    int       `json:"status"` // 1 正常 / 2 锁定 / 3 废弃（models.StatusXxx）
	CreatedBy string    `json:"created_by"`
	CreatedAt xsql.Time `json:"created_at"`
	UpdatedAt xsql.Time `json:"updated_at"`
}

// ScriptConfigField 一项配置声明（与 plugin.ConfigField 同构，便于前端复用同一套表单）。
type ScriptConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"` // string / int / bool
	Help     string `json:"help"`
	Secret   bool   `json:"secret"`   // true：回显打码，写回时打码串 = 不改
	Required bool   `json:"required"` // true：执行时缺它直接报 code=config
	Default  any    `json:"default,omitempty"`
}

// ParamSchema 入参 schema；解析失败返回空对象（不可解析的 schema 等于没有 schema）。
func (t *CustomTool) ParamSchema() map[string]any { return scriptJSONMap(t.Parameters) }

// OutputSchema 出参 schema；nil = 不校验。
func (t *CustomTool) OutputSchema() map[string]any { return scriptJSONMap(t.Output) }

// ConfigMap 配置值。
func (t *CustomTool) ConfigMap() map[string]any { return scriptJSONMap(t.Configs) }

// ConfigFieldList 配置声明；解析失败按空表处理。
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

// Label 人类可读的短标题：展示名缺省回落为 name。
// 与 Description 分开：描述是给模型看的完整句子，不适合当列表标题。
func (t *CustomTool) Label() string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return t.Name
}

// scriptJSONMap 把 JSONB 解成 map；空 / 非法一律返回 nil，让调用方按"没有 schema"处理。
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
