package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flame/nexus/shared/models"
)

// stubSchemaTool 提供任意 schema 的桩工具，用于验证参数校验 / 别名归位。
type stubSchemaTool struct {
	meta ToolMeta
}

func (s *stubSchemaTool) Meta() ToolMeta { return s.meta }

func (s *stubSchemaTool) Execute(_ context.Context, _ *Context, _ map[string]any) (any, error) {
	return "ok", nil
}

func TestNormalizeArgs(t *testing.T) {
	meta := ToolMeta{
		Name: "read_skill",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string"},
				"skill_id": map[string]any{"type": "string"},
			},
			"required": []any{"name"},
		},
	}
	args := map[string]any{"skill_name": "english-joke-writing"}
	normalizeArgs(meta, args)

	if args["name"] != "english-joke-writing" {
		t.Fatalf("name = %v, want english-joke-writing", args["name"])
	}
	if _, still := args["skill_name"]; still {
		t.Errorf("alias key skill_name should be removed, got %#v", args)
	}
	if err := validateRequired(meta, args); err != nil {
		t.Errorf("validateRequired = %v, want nil", err)
	}
}

func TestNormalizeArgsDoesNotClobberCanonical(t *testing.T) {
	meta := ToolMeta{
		Name: "bind_skill",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string"},
				"skill": map[string]any{"type": "string"},
			},
			"required": []any{"agent", "skill"},
		},
	}
	// `agent` / `skill` 是 bind_skill 的规范名，绝不能被别名规则改掉。
	args := map[string]any{"agent": "poet", "skill": "english"}
	normalizeArgs(meta, args)

	if args["agent"] != "poet" || args["skill"] != "english" {
		t.Fatalf("canonical args mutated: %#v", args)
	}
}

func TestValidateRequiredMessage(t *testing.T) {
	meta := ToolMeta{
		Name: "read_skill",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"owner": map[string]any{"type": "string"},
			},
			"required": []any{"name"},
		},
	}
	err := validateRequired(meta, map[string]any{"skill_id": "x"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	want := `tool read_skill: missing required argument(s) ["name"]; received ["skill_id"]; expected params: ["name", "owner"]`
	if err.Error() != want {
		t.Errorf("msg = %q, want %q", err.Error(), want)
	}
}

func TestExecuteToolSkillNameAlias(t *testing.T) {
	Register(&stubSchemaTool{meta: ToolMeta{
		Name: "stub_read_skill",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []any{"name"},
		},
	}})

	if _, err := ExecuteTool(context.Background(), &Context{}, "stub_read_skill", map[string]any{"skill_name": "x"}); err != nil {
		t.Fatalf("ExecuteTool = %v, want nil", err)
	}
}

// 未授权工具必须被执行层拒绝（防文本形态调用绕过工具授权）。
func TestExecuteToolRejectsUngrantedTool(t *testing.T) {
	Register(&stubSchemaTool{meta: ToolMeta{
		Name: "stub_ungranted",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
			"required":   []any{"name"},
		},
	}})

	c := &Context{AgentName: "joker", AllowedTools: map[string]bool{"other_tool": true}}
	if _, err := ExecuteTool(context.Background(), c, "stub_ungranted", map[string]any{"name": "x"}); err == nil {
		t.Fatal("want error for ungranted tool")
	}

	// 空集（未配置授权）：任何工具都拒绝
	c.AllowedTools = map[string]bool{}
	if _, err := ExecuteTool(context.Background(), c, "stub_ungranted", map[string]any{"name": "x"}); err == nil {
		t.Fatal("want error when grant set is empty")
	}

	// 在集合内：放行
	c.AllowedTools = map[string]bool{"stub_ungranted": true}
	if _, err := ExecuteTool(context.Background(), c, "stub_ungranted", map[string]any{"name": "x"}); err != nil {
		t.Fatalf("ExecuteTool = %v, want nil", err)
	}
}

// 缺省工具集必须都是已注册的普通内置工具：防拼错名字、防把 super 类混进缺省授权。
func TestDefaultAgentToolsAreRegistered(t *testing.T) {
	if len(models.DefaultAgentTools) == 0 {
		t.Fatal("DefaultAgentTools 为空")
	}
	seen := map[string]bool{}
	for _, name := range models.DefaultAgentTools {
		if seen[name] {
			t.Errorf("缺省工具 %q 重复", name)
		}
		seen[name] = true
		if _, ok := Get(name); !ok {
			t.Errorf("缺省工具 %q 未注册", name)
		}
		if IsSupervisorOnlyTool(name) {
			t.Errorf("缺省工具 %q 是 super（仅超级智能体）工具，不能进缺省集", name)
		}
	}
}

// 技能元工具的分类口径：私有三件套是普通工具（被授权即用，含非超级智能体），
// 共享三件套与指定归属三件套是 skill-admin（仅超级智能体），绑定类在 agent-admin。
// 旧的单工具名必须彻底消失。
// 分类是「谁能用哪个工具」的唯一判据，改错等于权限边界错，故在此显式锁死。
func TestSkillToolCategories(t *testing.T) {
	for _, name := range []string{"create_own_skill", "update_own_skill", "delete_own_skill"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("工具 %q 未注册", name)
		}
		if IsSupervisorOnlyTool(name) {
			t.Errorf("%q 不应仅超级智能体可用（否则 agent 无法自建私有 skill）", name)
		}
		// 与 builtin:skill 分家：skill 是「看技能」，skill-own 是「写自己的能力面」，
		// 风险档次不同（D20 特意不放进 DefaultAgentTools），放同一组没法分开授权。
		if got := tool.Meta().Category; got != CategorySkillOwn {
			t.Errorf("%q 分类 = %q, want %q", name, got, CategorySkillOwn)
		}
	}
	// 「看 / 用技能」三件套必须留在 skill 组 —— 它们跟着 core 常驻，不能混进 skill-own。
	// use_skill 与 read_skill 同组：前者激活、后者纯查看，都不产生写权限。
	for _, name := range []string{"list_skills", "read_skill", "use_skill"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("工具 %q 未注册", name)
		}
		if got := tool.Meta().Category; got != CategorySkill {
			t.Errorf("%q 分类 = %q, want %q", name, got, CategorySkill)
		}
	}
	for _, name := range []string{"create_shared_skill", "update_shared_skill", "delete_shared_skill"} {
		if _, ok := Get(name); !ok {
			t.Fatalf("工具 %q 未注册", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q 必须仅超级智能体可用（共享 skill 只有超级智能体能建改删）", name)
		}
	}
	// 指定归属三件套（替别的 agent 管它的私有 skill）：分类 skill-admin，仅超级智能体。
	// 与 own 三件套对称 —— 那两个是「你自己的私有」，这三个是「指定 agent 的私有」。
	for _, name := range []string{"create_agent_skill", "update_agent_skill", "delete_agent_skill"} {
		tool, ok := Get(name)
		if !ok {
			t.Fatalf("工具 %q 未注册", name)
		}
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q 必须仅超级智能体可用（替别的 agent 写私有技能等于改它的提示词）", name)
		}
		if got := tool.Meta().Category; got != CategorySkillAdmin {
			t.Errorf("%q 分类 = %q, want %q", name, got, CategorySkillAdmin)
		}
	}
	// 绑定类（指定「给哪些 agent 用」）仍专属超级智能体
	for _, name := range []string{"bind_skill", "unbind_skill"} {
		if !IsSupervisorOnlyTool(name) {
			t.Errorf("%q 必须仅超级智能体可用（绑定关系由超级智能体决定）", name)
		}
	}
	// 旧的单工具形态必须彻底消失（旧 ref 由 sql/skill_own_tool_rename.sql 迁移）
	for _, name := range []string{"create_skill", "update_skill", "delete_skill"} {
		if _, ok := Get(name); ok {
			t.Errorf("旧工具名 %q 仍注册，应已改名（私有加 _own_ 后缀、共享为 *_shared_skill）", name)
		}
	}
}

// 缺省授权项必须是 required 语义可承载的形态（item_type=tool / ref=builtin:<name>）。
func TestDefaultAgentToolGrantsShape(t *testing.T) {
	for _, it := range models.DefaultAgentToolGrants() {
		if it.ItemType != models.GrantItemTool {
			t.Errorf("item_type = %q, want %q", it.ItemType, models.GrantItemTool)
		}
		if !strings.HasPrefix(it.Ref, models.GrantRefBuiltinPrefix) {
			t.Errorf("ref = %q, 缺少前缀 %q", it.Ref, models.GrantRefBuiltinPrefix)
		}
	}
}

// 「仅超级智能体」的判据必须双向闭合：工具被判为 supervisor-only ⇔ 它的分类在 supervisorOnlyCategoryList 里，
// 且列表里每个分类都真的有工具（空分类等于列了个不存在的组，面板上会静默消失）。
// 漏一个分类的后果是提权，多一个空分类的后果是授权面板缺组 —— 两头都要拦。
func TestSupervisorOnlyCategoriesAreClosed(t *testing.T) {
	byCategory := map[string][]string{}
	for _, tool := range List() {
		m := tool.Meta()
		byCategory[m.Category] = append(byCategory[m.Category], m.Name)
		if got, want := IsSupervisorOnlyTool(m.Name), IsSupervisorOnlyCategory(m.Category); got != want {
			t.Errorf("%q 的 IsSupervisorOnlyTool=%v，但分类 %q 的 IsSupervisorOnlyCategory=%v（两者必须同源）",
				m.Name, got, m.Category, want)
		}
	}
	for _, category := range SupervisorOnlyCategories() {
		if len(byCategory[category]) == 0 {
			t.Errorf("supervisor-only 分类 %q 下没有任何工具（空组不会出现在授权面板里）", category)
		}
	}
	// 反向：任何不属于 supervisor-only 分类的工具都不该被判为 supervisor-only。
	if IsSupervisorOnlyCategory(CategorySuper) {
		t.Error("CategorySuper 是过渡别名，不应被当作 supervisor-only 分类")
	}
	if IsSupervisorOnlyCategory(CategorySkill) {
		t.Error("CategorySkill（看技能）对普通 agent 开放，不应是 supervisor-only")
	}
}

// 超级智能体的 core 集必须是六个「组」ref，且都是已知的内置类别。
// 拼错组名的后果不是报错，而是超级智能体少一整类工具（静默降级），所以在这里拦。
func TestSupervisorCoreGrantRefsAreGroups(t *testing.T) {
	if len(models.SupervisorCoreGrantRefs) == 0 {
		t.Fatal("SupervisorCoreGrantRefs 为空")
	}
	cats := map[string]bool{}
	for _, tool := range List() {
		cats[tool.Meta().Category] = true
	}
	seen := map[string]bool{}
	for _, ref := range models.SupervisorCoreGrantRefs {
		if seen[ref] {
			t.Errorf("core 组 %q 重复", ref)
		}
		seen[ref] = true
		if !strings.HasPrefix(ref, models.GrantRefBuiltinPrefix) {
			t.Errorf("core 组 %q 缺少前缀 %q", ref, models.GrantRefBuiltinPrefix)
			continue
		}
		category := strings.TrimPrefix(ref, models.GrantRefBuiltinPrefix)
		if !cats[category] {
			t.Errorf("core 组 %q 不是已知的内置类别", ref)
		}
		// 每个 core 组下必须有工具，否则常驻了一个空组。
		found := false
		for _, tool := range List() {
			if tool.Meta().Category == category {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("core 组 %q 下没有任何已注册工具", ref)
		}
	}
}

// core 的取舍必须被钉住，否则「browser / 四个按需组不该常驻」的结论会被静默推翻。
// 这几条是「节约超级智能体上下文」的全部收益来源（37 → 20 个工具），改动前先想清楚。
func TestSupervisorCoreExcludesOnDemandGroups(t *testing.T) {
	core := map[string]bool{}
	for _, ref := range models.SupervisorCoreGrantRefs {
		core[ref] = true
	}
	// browser：自称低频 + schema 最大 + 唯一「出网 + 读任意页面 + 带登录态」组合。
	// 它不跟随任何技能，要就由管理员加一条 required（见 models.SupervisorCoreGrantRefs 的注释）。
	if core["builtin:browser"] {
		t.Error("builtin:browser 不应常驻 core")
	}
	// 五个管理组里只有 agent-admin 常驻（超级智能体的自举：能建 agent 才能拉起别的工作面）。
	// 其余四个跟随技能：use_skill 激活声明它们的技能后才出现（绑定本身不产生工具）。
	if !core["builtin:agent-admin"] {
		t.Error("builtin:agent-admin 必须常驻（超级智能体的自举能力）")
	}
	for _, ref := range []string{"builtin:skill-own", "builtin:skill-admin",
		"builtin:tool-grant", "builtin:custom-tool", "builtin:flow-admin"} {
		if core[ref] {
			t.Errorf("%q 不应常驻 core（跟随技能按需激活）", ref)
		}
	}
}

// SupervisorCoreGrants 的形态：item_type=group + builtin: 前缀（与 DefaultAgentToolGrants 的单工具不同）。
func TestSupervisorCoreGrantsShape(t *testing.T) {
	for _, it := range models.SupervisorCoreGrants() {
		if it.ItemType != models.GrantItemGroup {
			t.Errorf("item_type = %q, want %q", it.ItemType, models.GrantItemGroup)
		}
		if !strings.HasPrefix(it.Ref, models.GrantRefBuiltinPrefix) {
			t.Errorf("ref = %q, 缺少前缀 %q", it.Ref, models.GrantRefBuiltinPrefix)
		}
	}
}

// sql/main_core_tool_grants.sql 里的 refs 数组必须与 models.SupervisorCoreGrantRefs 逐项一致。
// 这份 SQL 是**硬编码**的，两边漂移的后果是「新租户 6 组、存量租户 5 组」这种只在老环境复现的差异。
func TestSupervisorCoreGrantRefsMatchSQL(t *testing.T) {
	path := filepath.Join("..", "..", "..", "sql", "main_core_tool_grants.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	src := string(raw)
	start := strings.Index(src, "refs text[] := ARRAY[")
	if start < 0 {
		t.Fatal("SQL 里找不到 refs 数组声明")
	}
	body := src[start:]
	if end := strings.Index(body, "];"); end >= 0 {
		body = body[:end]
	}
	// 数组声明里只有字符串字面量与注释，按单引号切分取奇数段即可。
	parts := strings.Split(body, "'")
	inSQL := map[string]bool{}
	for i := 1; i < len(parts); i += 2 {
		inSQL[parts[i]] = true
	}
	if len(inSQL) == 0 {
		t.Fatal("SQL 的 refs 数组解析为空（格式变了？）")
	}

	inGo := map[string]bool{}
	for _, ref := range models.SupervisorCoreGrantRefs {
		inGo[ref] = true
		if !inSQL[ref] {
			t.Errorf("SupervisorCoreGrantRefs 里的 %q 不在 SQL 的 refs 数组里", ref)
		}
	}
	for ref := range inSQL {
		if !inGo[ref] {
			t.Errorf("SQL 里的 %q 不在 SupervisorCoreGrantRefs 里", ref)
		}
	}
}
