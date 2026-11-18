package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"flame/nexus/shared/db"
	"flame/nexus/shared/models"
	"flame/nexus/shared/versioning"
	"flame/nexus/shared/versions"
)

// ===== 元工具：create_agent / update_agent / delete_agent / create_own_skill / update_own_skill / delete_own_skill / create_shared_skill / update_shared_skill / delete_shared_skill / bind_skill / unbind_skill / list_agents / list_skills / read_agent / read_skill / use_skill =====
//
// read_skill 与 use_skill 的分工：前者只查看，后者「用」—— 返回正文并激活该技能声明的工具依赖。
// 绑定关系只决定它出现在 system prompt 的清单里，不再直接产生工具候选。

// skillRefFrom 从工具入参构造 skill 定位：优先 skill_id；否则按 name + space 走空间定向。
func skillRefFrom(args map[string]any, space string) SkillRef { return skillRefIn(args, space, "") }

// skillRefIn 同上，额外指定私有空间的**归属 agent**（*_agent_skill 系列用；空 = 当前 agent）。
//
// space 由**工具本身**决定（own 系列 = agent / shared 系列 = tenant），不接受模型传入 ——
// 工具名即口径，避免模型在 scope 参数上误选；也避免"拿可见口径定位"把共享 skill 当成自己的。
// ownerID 同理：只有 *_agent_skill 系列会填，且已由工具层解析校验过（目标 agent 须存在且可编辑）。
func skillRefIn(args map[string]any, space, ownerID string) SkillRef {
	return SkillRef{
		ID:      strings.TrimSpace(argString(args, "skill_id")),
		Name:    argString(args, "name"),
		Scope:   space,
		OwnerID: ownerID,
	}
}

// skillIDDefaultHint 追加到每个 skill_id 描述末尾的「默认别传」。
//
// name 与 skill_id 并列时，模型容易两个都填、或者干脆不知道填哪个；而 skill_id 只服务于
// 「按全局唯一 id 跨归属定位」一个场景（且无名空间定向时仅超级智能体可用）。
// 把默认值直接说成「不要传」比解释优先级更省事，也更少歧义。
const skillIDDefaultHint = "；默认不要传 —— 用 name 定位即可，只有已从 list_skills 拿到确切 id 时才填"

// rejectScopeArg 拒绝旧接口遗留的 scope 参数。
//
// 历史形态是「单个 create_skill + scope 参数」；拆分后工具名即口径，模型若按旧习惯传
// scope 必须显式纠正 —— 静默忽略会让它"以为建了共享、其实建了私有"。
// counterpart 为对偶工具名，用于给出可执行的纠正指引。
func rejectScopeArg(args map[string]any, tool, counterpart string) error {
	if _, ok := args["scope"]; !ok {
		return nil
	}
	return fmt.Errorf("%s 不接受 scope 参数（工具名即口径）；建另一个空间的 skill 请改用 %s", tool, counterpart)
}

// skillToolDeps 解析工具的 tool_deps 入参。
//   - 键不存在 → (nil, nil)：不改动；
//   - 键存在但工具不支持（own 系列）→ 显式报错：静默忽略会让模型以为"声明了依赖"，
//     其实什么都没发生 —— 与 rejectScopeArg 同样的考虑；
//   - 形态无法识别 → 报错（不猜、也不静默清空）。
func skillToolDeps(args map[string]any, tool string, allowDeps bool) (*[]string, error) {
	if _, present := args["tool_deps"]; !present {
		return nil, nil
	}
	if !allowDeps {
		return nil, fmt.Errorf("%s 不接受 tool_deps 参数：技能的工具依赖属授权面，只有超级智能体可用 *_agent_skill / *_shared_skill 声明", tool)
	}
	refs, ok := argStringSlice(args, "tool_deps")
	if !ok {
		return nil, fmt.Errorf("%s 的 tool_deps 必须是字符串数组（ref 形如 builtin:exec）", tool)
	}
	return &refs, nil
}

// withToolDeps 给技能工具补上 tool_deps 参数（仅超级智能体的 *_agent_skill / *_shared_skill 用）。
//
// own 系列刻意不给：技能依赖是 allow 授权的**生效触发器**，让非超级智能体能写就等于把
// "何时启用管理员授权的工具"交给它自己决定（自建 skill 提权）。与 skill 侧不允许
// 引用自定义组是同一个考虑。
func withToolDeps(m ToolMeta) ToolMeta {
	props, ok := m.Parameters["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		m.Parameters["properties"] = props
	}
	props["tool_deps"] = map[string]any{
		"type": "array", "items": map[string]any{"type": "string"},
		"description": "该技能声明需要的工具 ref 列表（如 builtin:exec、builtin:file）；全量替换，传 [] 清空，不传则不改。仅当宿主 agent 已被管理员以 allow 授权这些工具时才会真正生效",
	}
	return m
}

// skillRestricted 解析 restricted 入参（可见性开关）。
//   - 键不存在 → (nil, nil)：不改动；
//   - 键存在但空间不支持（私有 skill）→ 显式报错：静默忽略会让模型以为改了可见性，
//     与 rejectScopeArg / skillToolDeps 同样的考虑；
//   - 形态不对 → 报错（不猜）。
func skillRestricted(args map[string]any, tool string, allowRestricted bool) (*bool, error) {
	v, present := args["restricted"]
	if !present {
		return nil, nil
	}
	if !allowRestricted {
		return nil, fmt.Errorf("%s 不接受 restricted 参数：可见性只对租户级共享 skill 有意义，请改用 update_shared_skill", tool)
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%s 的 restricted 必须是布尔值", tool)
	}
	return &b, nil
}

// withRestricted 给技能工具补上 restricted 参数（仅 update_shared_skill 用）。
//
// 共享 skill 建出来默认就是收窄的（restricted=true + 白名单只含创建者，见 skillManager.Create），
// 所以这里只提供"开关"：false = 放开为全员可见，true = 收回白名单模式（白名单数据保留）。
// 白名单成员由 bind_skill / unbind_skill 维护，本参数不碰它。
func withRestricted(m ToolMeta) ToolMeta {
	props, ok := m.Parameters["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		m.Parameters["properties"] = props
	}
	props["restricted"] = map[string]any{
		"type": "boolean",
		"description": "可见性开关（不传则不改）：true = 仅白名单内 agent 可见（默认），false = 全员可见。" +
			"白名单成员用 bind_skill / unbind_skill 增删，本参数只切开关",
	}
	return m
}

// createAgentTool 创建新 agent（写 AGENT.md + 注册实例）。
type createAgentTool struct{}

func (t *createAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "create_agent",
		Category:    CategoryAgentAdmin,
		Description: "创建一个新的 agent 实例：写 AGENT.md 并注册到系统。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "唯一名，只含字母数字横线，即定义域目录名"},
				"description": map[string]any{"type": "string", "description": "agent 描述"},
				"agent_md":    map[string]any{"type": "string", "description": "AGENT.md 全文"},
				"is_supervisor": map[string]any{"type": "boolean", "description": "是否直接建成超级权限 agent（可选，默认 false）。" +
					"为 true 时会一并物化起步套件：core 工具组（含 builtin:agent-admin）、不限制工具、可发现未绑定技能、" +
					"绑定 agent-authoring / skill-authoring / custom-tool-authoring / flow-authoring"},
			},
			"required": []any{"name", "agent_md"},
		},
	}
}

func (t *createAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q (只允许字母数字横线)", name)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	exists, err := c.Agents.Exists(ctx, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("agent %q already exists", name)
	}

	agentMd := argString(args, "agent_md")
	description := argString(args, "description")

	// 写 AGENT.md（storage 会自动建目录）
	if err := c.Storage.WriteFile(ctx, path.Join(AgentsKey(c.Schema, name), agentEntryFile), []byte(agentMd)); err != nil {
		return nil, err
	}
	// 注册实例（工具由「工具配置」授权体系决定，config 不在此写入）
	id, err := c.Agents.Create(ctx, name, description, argBool(args, "is_supervisor"))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":            id,
		"name":          name,
		"workspace":     AgentsKey(c.Schema, name),
		"is_supervisor": argBool(args, "is_supervisor"),
	}, nil
}

// updateAgentTool 更新 agent（改名/改描述）。
type updateAgentTool struct{}

func (t *updateAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "update_agent",
		Category: CategoryAgentAdmin,
		Description: "更新一个 agent：可改名字、描述，「可发现未绑定的技能」开关，或「不限制工具」开关（改名会同步迁移其定义域目录）。" +
			"可以改自己（超级智能体即管理员），但**不能给自己改名**（本轮后续文件操作会失效）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "当前 agent 名（可以是自己）"},
				"new_name":      map[string]any{"type": "string", "description": "新名字（可选；不能用于改自己）"},
				"description":   map[string]any{"type": "string", "description": "新描述（可选；传空或省略都表示不改 —— 这个工具无法清空描述，清空请找管理员在管理端操作）"},
				"discovery":     map[string]any{"type": "boolean", "description": "是否允许该 agent 发现「未绑定的共享技能」（可选，不传则不改）"},
				"unrestricted":  map[string]any{"type": "boolean", "description": "是否「不限制工具」：开启后候选工具不再被 allow 过滤（可选，不传则不改）"},
				"is_supervisor": map[string]any{"type": "boolean", "description": "是否赋予超级权限（与 超级智能体同级：管理类工具 + 跨域写）。仅 supervisor 可操作；降级本租户最后一个 supervisor 会被拒绝（可选，不传则不改）"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *updateAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	// 对自己只禁止改名：改名会让本轮后续文件操作失效（技术原因，与权限无关）。
	// description / discovery / unrestricted 属于超级智能体自己的管理面，放开（D5 修订）。
	if name == c.AgentName && argString(args, "new_name") != "" {
		return nil, errors.New("不能给运行中的自己改名（本轮后续文件操作会失效）；改名请找管理员")
	}
	finalName, err := c.Agents.Update(ctx, name, argString(args, "new_name"), argString(args, "description"))
	if err != nil {
		return nil, err
	}
	// 可选的开关类参数（不传则不改）：技能可见性 discovery + 工具面 unrestricted。
	// 两者都要目标 agent 的 uuid，故按需取一次 info。
	_, dOK := args["discovery"]
	_, uOK := args["unrestricted"]
	_, sOK := args["is_supervisor"]
	if dOK || uOK || sOK {
		info, gerr := c.Agents.Get(ctx, finalName)
		if gerr != nil {
			return nil, gerr
		}
		if info == nil {
			return nil, fmt.Errorf("agent %q not found", finalName)
		}
		// 技能可见性：是否允许发现「未绑定的共享技能」
		if dOK {
			if c.Skills == nil {
				return nil, errors.New("skill manager not available")
			}
			if serr := c.Skills.SetAgentSkillDiscovery(ctx, info.ID, argBool(args, "discovery")); serr != nil {
				return nil, serr
			}
		}
		// 工具面：是否「不限制工具」（候选不再被 allow 过滤）。
		// 它是 agent 自身的属性，故走 Agents；c.Agents 已在上面判过非 nil
		if uOK {
			if uerr := c.Agents.SetUnrestricted(ctx, info.ID, argBool(args, "unrestricted")); uerr != nil {
				return nil, uerr
			}
			// 改的是自己：工具面变了，本 run 下一轮就要重算（否则开关像没生效）。
			if name == c.AgentName {
				c.MarkToolsDirty()
			}
		}
		// 超权属性：授予 / 撤销（撤销本租户最后一个 supervisor 由仓储拒绝）。
		// 撤销自己后，本 run 下一轮的管理类工具会被执行层硬闸剥掉，故同样置脏。
		if sOK {
			if serr := c.Agents.SetSupervisor(ctx, info.ID, argBool(args, "is_supervisor")); serr != nil {
				return nil, serr
			}
			if name == c.AgentName {
				c.MarkToolsDirty()
			}
		}
	}
	// id 一并返回：前端据此在智能体页定位到这只 agent（改名后光靠调用参数里的旧名找不到）。
	// 取不到不算失败 —— 前端还有 name 兜底，不能让一次成功的更新因为查 id 而报错。
	agentID := ""
	if info, gerr := c.Agents.Get(ctx, finalName); gerr == nil && info != nil {
		agentID = info.ID
	}
	return map[string]any{"id": agentID, "name": finalName, "updated": true}, nil
}

// deleteAgentTool 删除 agent。
type deleteAgentTool struct{}

func (t *deleteAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "delete_agent",
		Category:    CategoryAgentAdmin,
		Description: "删除一个 agent（同时清理其定义域目录）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "agent 名"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *deleteAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	// 不允许删除自己：正在执行的 agent 删掉自己会立刻出错
	if name == c.AgentName {
		return nil, errors.New("cannot delete the running agent itself")
	}
	if err := c.Agents.Delete(ctx, name); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "deleted": true}, nil
}

// createOwnSkillTool 创建「自己的私有」skill（写内容文件 + 登记 skills 表）。
//
// 分类是 skill（非 super）：被授权即可用 —— 每个 agent 都能建自己的私有技能。
// 「只能为自己」不靠分类，而靠 SkillsManager 构造时固化的 agentID：工具层传不进来。
type createOwnSkillTool struct{}

func (t *createOwnSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "create_own_skill",
		Category: CategorySkillOwn,
		Description: "在自己的私有空间创建一个 skill：写 SKILL.md 并登记到 skill 库。" +
			"私有 skill 只有你自己可见；要跨 agent 复用，请让超级智能体用 create_shared_skill 建共享技能再 bind_skill 绑定。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "唯一名，只含字母数字横线"},
				"skill_md":    map[string]any{"type": "string", "description": "SKILL.md 全文"},
				"description": map[string]any{"type": "string", "description": "skill 描述（可选）"},
			},
			"required": []any{"name", "skill_md"},
		},
	}
}

func (t *createOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return createSkillInSpace(ctx, c, args, SkillScopeAgent, "", "create_own_skill", "create_shared_skill", false)
}

// createSkillInSpace 在指定空间创建 skill（own / shared / agent_skill 三套工具共用实现）。
// ownerID 非空 = 目标归属 agent（*_agent_skill 系列；仅超级智能体，由工具层先解析并校验）。
// allowDeps 决定是否接受 tool_deps 参数（own 系列为 false，见 withToolDeps 的说明）。
func createSkillInSpace(ctx context.Context, c *Context, args map[string]any, space, ownerID, tool, counterpart string, allowDeps bool) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q (只允许字母数字横线)", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	if err := rejectScopeArg(args, tool, counterpart); err != nil {
		return nil, err
	}
	deps, err := skillToolDeps(args, tool, allowDeps)
	if err != nil {
		return nil, err
	}
	id, err := c.Skills.Create(ctx, SkillInput{
		Name:        name,
		Description: argString(args, "description"),
		Content:     argString(args, "skill_md"),
		Scope:       space,
		OwnerID:     ownerID,
		ToolDeps:    deps,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "name": name, "scope": space}, nil
}

// createSharedSkillTool 创建「租户级共享」skill（仅超级智能体：分类 skill-admin，三层收口）。
//
// 建后默认只授权创建者自己（企业级默认拒绝），要给别人用再 bind_skill —— 误建也不会扩散。
// 支持 tool_deps 声明工具依赖（仅超级智能体可写，见 withToolDeps）。
type createSharedSkillTool struct{}

func (t *createSharedSkillTool) Meta() ToolMeta {
	return withToolDeps(ToolMeta{
		Name:     "create_shared_skill",
		Category: CategorySkillAdmin,
		Description: "创建一个租户级共享 skill（仅超级智能体）：写 SKILL.md 并登记到 skill 库。" +
			"其他 agent 需经 bind_skill 绑定才可见；建后默认只授权你自己，不会自动扩散。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "唯一名，只含字母数字横线"},
				"skill_md":    map[string]any{"type": "string", "description": "SKILL.md 全文"},
				"description": map[string]any{"type": "string", "description": "skill 描述（可选）"},
			},
			"required": []any{"name", "skill_md"},
		},
	})
}

func (t *createSharedSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return createSkillInSpace(ctx, c, args, SkillScopeTenant, "", "create_shared_skill", "create_own_skill", true)
}

// skillUpdateMeta 构造 update_own_skill / update_shared_skill 的工具定义：
// 两者参数完全一致（定位空间由工具决定，模型传不进来），只有名字 / 分类 / 描述 / id 提示不同。
func skillUpdateMeta(name, category, description, idHint string) ToolMeta {
	return ToolMeta{
		Name:        name,
		Category:    category,
		Description: description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "当前 skill 名（定位用）"},
				"skill_id":    map[string]any{"type": "string", "description": idHint},
				"new_name":    map[string]any{"type": "string", "description": "改名后的新名字（可选）"},
				"skill_md":    map[string]any{"type": "string", "description": "新的 SKILL.md 全文（可选，不传则保留原内容）"},
				"description": map[string]any{"type": "string", "description": "新的描述（可选）"},
			},
			"required": []any{"name"},
		},
	}
}

// updateSkillInSpace 更新指定空间内的 skill（own / shared / agent_skill 三套工具共用实现）。
// ownerID 非空 = 目标归属 agent（*_agent_skill 系列；仅超级智能体）。
// allowDeps 决定是否接受 tool_deps 参数（own 系列为 false，见 withToolDeps 的说明）。
func updateSkillInSpace(ctx context.Context, c *Context, args map[string]any, space, ownerID, tool string, allowDeps bool) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	deps, err := skillToolDeps(args, tool, allowDeps)
	if err != nil {
		return nil, err
	}
	// 可见性开关：只有租户级空间接受（工具的空间定向即口径，模型传不进来）
	restricted, err := skillRestricted(args, tool, space == SkillScopeTenant)
	if err != nil {
		return nil, err
	}
	ref := skillRefIn(args, space, ownerID)
	newName := argString(args, "new_name")
	if err := c.Skills.Update(ctx, SkillInput{
		ID:          ref.ID,
		OwnerID:     ownerID,
		Name:        name,
		NewName:     newName,
		Description: argString(args, "description"),
		Content:     argString(args, "skill_md"),
		Scope:       space,
		ToolDeps:    deps,
		Restricted:  restricted,
	}); err != nil {
		return nil, err
	}
	if newName == "" {
		newName = name
	}
	return map[string]any{"name": newName, "updated": true}, nil
}

// updateOwnSkillTool 更新「自己的私有」skill（分类 skill-own：被授权即可用，空间由本工具固定）。
type updateOwnSkillTool struct{}

func (t *updateOwnSkillTool) Meta() ToolMeta {
	return skillUpdateMeta("update_own_skill", CategorySkillOwn,
		"更新你自己的私有 skill：可改内容、描述或名字（改名会同步迁移其内容文件）。只能操作自己的私有 skill。",
		"skill 的 id（可选；按 id 定位时仍须属于你自己的私有空间）")
}

func (t *updateOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return updateSkillInSpace(ctx, c, args, SkillScopeAgent, "", "update_own_skill", false)
}

// updateSharedSkillTool 更新「租户级共享」skill（分类 skill-admin：仅超级智能体）。
type updateSharedSkillTool struct{}

func (t *updateSharedSkillTool) Meta() ToolMeta {
	return withRestricted(withToolDeps(skillUpdateMeta("update_shared_skill", CategorySkillAdmin,
		"更新一个租户级共享 skill（仅超级智能体）：可改内容、描述、名字、工具依赖或可见性开关（改名会同步迁移其内容文件）。注意它可能正被多个 agent 绑定使用。",
		"skill 的 id（可选；按 id 定位时仍须是租户级共享 skill）")))
}

func (t *updateSharedSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return updateSkillInSpace(ctx, c, args, SkillScopeTenant, "", "update_shared_skill", true)
}

// ===== 工具依赖的增量维护（grant_skill_tool / revoke_skill_tool）=====
//
// 与 update_*_skill 的 tool_deps 参数分工：那个是**全量替换**（不传=不改、[]=清空），
// 适合批量声明；这两个是**增量**，不必先读现状，也不会因为漏传而静默删掉其它依赖。
//
// 命名刻意与 grant_agent_tool / revoke_agent_tool 对齐：`<动词>_<被授对象>_tool`，
// 一眼能看出「同样是给工具，对象是 agent 还是 skill」。代价是 grant 容易被读成授权，
// 所以描述第一句就否认：skill 侧只「声明需要」，不产生授权。

func skillDepMeta(name, description string) ToolMeta {
	return ToolMeta{
		Name:     name,
		Category: CategorySkillAdmin, // 仅超级智能体：依赖是 allow 授权的生效触发器
		Description: description +
			"**这不是授权**：只有当宿主 agent 已被授予该工具的 allow 授权、并且用 use_skill 激活了这个技能时才会注入。" +
			"第一个参数是要声明的 skill（name 或 skill_id），不是 agent —— 给 agent 授权请用 grant_agent_tool。" +
			"返回值带变更后的完整 tool_deps，不必再查一遍。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "skill 名（按当前可见范围定位；跨归属的私有技能改用 skill_id）"},
				"skill_id": map[string]any{"type": "string", "description": "skill 的 id（可选；与 name 二选一，非空时优先）" + skillIDDefaultHint},
				"ref":      map[string]any{"type": "string", "description": "工具引用：builtin:<工具名> / builtin:<类别> / mcp:<连接>[:<工具>] / rpc:<连接>[:<工具>] / local:rpc[:<端点>]；不接受 group 工具组"},
			},
			"required": []any{"ref"},
		},
	}
}

// skillDepByArgs 解析 name / skill_id，并做「至少给一个合法定位」的前置校验。
func skillDepByArgs(args map[string]any) (SkillRef, error) {
	ref := skillRefFrom(args, "")
	if ref.ID == "" && !ValidName(ref.Name) {
		return ref, errors.New("需要 name（合法 skill 名）或 skill_id 来定位 skill")
	}
	return ref, nil
}

type grantSkillToolTool struct{}

func (t *grantSkillToolTool) Meta() ToolMeta {
	return skillDepMeta("grant_skill_tool",
		"给某个 skill **追加**一条工具依赖（写入 skill_tool_grants；幂等，已存在则 added=false）。")
}

func (t *grantSkillToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	ref, err := skillDepByArgs(args)
	if err != nil {
		return nil, err
	}
	return c.Skills.AddToolDep(ctx, ref, argString(args, "ref"))
}

type revokeSkillToolTool struct{}

func (t *revokeSkillToolTool) Meta() ToolMeta {
	return skillDepMeta("revoke_skill_tool",
		"从某个 skill **移除**一条工具依赖（幂等；只删这一个，不清空其它依赖）。")
}

func (t *revokeSkillToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	ref, err := skillDepByArgs(args)
	if err != nil {
		return nil, err
	}
	return c.Skills.RemoveToolDep(ctx, ref, argString(args, "ref"))
}

// skillDeleteMeta 构造 delete_own_skill / delete_shared_skill 的工具定义（参数一致）。
func skillDeleteMeta(name, category, description, idHint string) ToolMeta {
	return ToolMeta{
		Name:        name,
		Category:    category,
		Description: description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "skill 名，例如 english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": idHint + skillIDDefaultHint},
			},
			"required": []any{"name"},
		},
	}
}

// deleteSkillInSpace 删除指定空间内的 skill（own / shared / agent_skill 三套工具共用实现）。
// ownerID 非空 = 目标归属 agent（*_agent_skill 系列；仅超级智能体）。
func deleteSkillInSpace(ctx context.Context, c *Context, args map[string]any, space, ownerID string) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	ref := skillRefIn(args, space, ownerID)
	ref.Name = name
	if err := c.Skills.Delete(ctx, ref); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "deleted": true}, nil
}

// deleteOwnSkillTool 删除「自己的私有」skill。
type deleteOwnSkillTool struct{}

func (t *deleteOwnSkillTool) Meta() ToolMeta {
	return skillDeleteMeta("delete_own_skill", CategorySkillOwn,
		"删除你自己的私有 skill（同时删除其内容文件）。只能操作自己的私有 skill。",
		"skill 的 id（可选；按 id 定位时仍须属于你自己的私有空间）")
}

func (t *deleteOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteSkillInSpace(ctx, c, args, SkillScopeAgent, "")
}

// deleteSharedSkillTool 删除「租户级共享」skill（仅超级智能体）。
type deleteSharedSkillTool struct{}

func (t *deleteSharedSkillTool) Meta() ToolMeta {
	return skillDeleteMeta("delete_shared_skill", CategorySkillAdmin,
		"删除一个租户级共享 skill（仅超级智能体，同时删除其内容文件）。注意它可能正被多个 agent 绑定使用。",
		"skill 的 id（可选；按 id 定位时仍须是租户级共享 skill）")
}

func (t *deleteSharedSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteSkillInSpace(ctx, c, args, SkillScopeTenant, "")
}

// skillAgentMeta 给 skill 工具的参数表补上「目标归属 agent」（*_agent_skill 系列专用，必填）。
func skillAgentMeta(m ToolMeta) ToolMeta {
	props, ok := m.Parameters["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		m.Parameters["properties"] = props
	}
	props["agent"] = map[string]any{"type": "string", "description": "目标 agent 名（该技能归属它，之后只有它自己可见）"}
	if req, ok := m.Parameters["required"].([]any); ok {
		m.Parameters["required"] = append([]any{"agent"}, req...)
	}
	return m
}

// 关于 tool_deps：本组（以及 *_shared_skill）支持声明技能的工具依赖，但**只有超级智能体能写**
// （见 withToolDeps / own 系列刻意不给）。依赖本身不产生授权 —— 它只是把目标 agent
// 已有的 allow 授权「点亮」，所以工具描述里必须写清这一点，免得被理解成"声明了就有"。

// createAgentSkillTool 为「指定 agent」创建它的**私有** skill（仅超级智能体，分类 skill-admin）。
//
// 补的是超级智能体对「别人定义域」的能力缺口：它能整篇重写别人的 AGENT.md，却建不了别人的私有技能。
// 与 create_own_skill（自己的）/ create_shared_skill（租户级）并列，构成第三套空间，
// 「工具名即口径」：归属由 agent 参数定向，非超级智能体传 agent 会被三层收口拦掉。
//
// 技能是**单文件知识文档**：正文全部进 skill_md，不提供附属文件能力（与 create_own_skill 同形）。
type createAgentSkillTool struct{}

func (t *createAgentSkillTool) Meta() ToolMeta {
	return skillAgentMeta(withToolDeps(ToolMeta{
		Name:     "create_agent_skill",
		Category: CategorySkillAdmin,
		Description: "为**指定 agent** 创建它的私有 skill（仅超级智能体）：写 SKILL.md 并登记到 skill 库。" +
			"该技能归属目标 agent，只有它自己可见；正文全部放在 skill_md。" +
			"要给多个 agent 共用，请改用 create_shared_skill + bind_skill。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "唯一名，只含字母数字横线"},
				"skill_md":    map[string]any{"type": "string", "description": "SKILL.md 全文（技能的全部内容）"},
				"description": map[string]any{"type": "string", "description": "skill 描述（可选）"},
			},
			"required": []any{"name", "skill_md"},
		},
	}))
}

func (t *createAgentSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	_, ownerID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	return createSkillInSpace(ctx, c, args, SkillScopeAgent, ownerID, "create_agent_skill", "create_own_skill / create_shared_skill", true)
}

// updateAgentSkillTool 更新「指定 agent 的私有 skill」（仅超级智能体）。
type updateAgentSkillTool struct{}

func (t *updateAgentSkillTool) Meta() ToolMeta {
	return skillAgentMeta(withToolDeps(skillUpdateMeta("update_agent_skill", CategorySkillAdmin,
		"更新**指定 agent 的私有 skill**（仅超级智能体）：可改内容、描述、名字或工具依赖（改名会同步迁移其内容文件）。",
		"skill 的 id（可选；按 id 定位时仍须属于该 agent 的私有空间）")))
}

func (t *updateAgentSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	_, ownerID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	return updateSkillInSpace(ctx, c, args, SkillScopeAgent, ownerID, "update_agent_skill", true)
}

// deleteAgentSkillTool 删除「指定 agent 的私有 skill」（仅超级智能体）。
type deleteAgentSkillTool struct{}

func (t *deleteAgentSkillTool) Meta() ToolMeta {
	return skillAgentMeta(skillDeleteMeta("delete_agent_skill", CategorySkillAdmin,
		"删除**指定 agent 的私有 skill**（仅超级智能体，同时删除其内容文件）。",
		"skill 的 id（可选；按 id 定位时仍须属于该 agent 的私有空间）"))
}

func (t *deleteAgentSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	_, ownerID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	return deleteSkillInSpace(ctx, c, args, SkillScopeAgent, ownerID)
}

// listAgentsTool 列出所有 agent 实例。
type listAgentsTool struct{}

func (t *listAgentsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_agents",
		Category:    CategoryAgent,
		Description: "列出所有已注册的 agent 实例（name/description/status）。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listAgentsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	list, err := c.Agents.List(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agents": list}, nil
}

// maxListSkills 单次 list_skills 返回的条数上限（技能过多时防止把上下文顶爆）。
const maxListSkills = 100

// listSkillsTool 列出当前 agent 可见的 skill。
// 可见范围取决于该 agent 是否开启「可发现未绑定的技能」（agents.skill_discovery）：
// 关闭时只有私有 + 已绑定的技能，开启时额外包含「全员可见」的共享技能。
type listSkillsTool struct{}

func (t *listSkillsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "list_skills",
		Category:    CategorySkill,
		Description: "列出当前 agent 可见的 skill（本 agent 私有 + 已绑定到它的技能；开启发现能力时还包含全员可见的共享技能）。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *listSkillsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	list, err := c.Skills.List(ctx)
	if err != nil {
		return nil, err
	}
	total := len(list)
	if total > maxListSkills {
		return map[string]any{
			"skills":    list[:maxListSkills],
			"total":     total,
			"truncated": true,
			"note": fmt.Sprintf("共 %d 个可见技能，这里只列出前 %d 个；如需用到的技能未列出，"+
				"可让管理员把它绑定给本 agent（或精简可见范围）", total, maxListSkills),
		}, nil
	}
	return map[string]any{"skills": list, "total": total}, nil
}

// readAgentTool 读取某个 agent 的 AGENT.md。
type readAgentTool struct{}

func (t *readAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "read_agent",
		Category: CategoryAgent,
		Description: "读取某个 agent 的 AGENT.md 内容，并返回它当前绑定的技能名（skills）；" +
			"超级智能体调用时还会带上该 agent 的工具授权现状（tool_grants：required 恒生效 / allow 为 pending 等待 use_skill 激活 / deny 为显式拒绝）、「不限制工具」开关（unrestricted）、" +
			"「可发现未绑定的技能」开关（discovery），" +
			"以及调用授权现状（can_call = 它能调用谁，called_by = 谁能调用它）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "agent 名"},
			},
			"required": []any{"name"},
		},
	}
}

func (t *readAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q", name)
	}
	data, err := c.Storage.ReadFile(ctx, path.Join(AgentsKey(c.Schema, name), agentEntryFile))
	if err != nil {
		return nil, fmt.Errorf("read agent %q: %w", name, err)
	}
	out := map[string]any{"name": name, "content": string(data)}
	// 顺带给出只读现状（技能绑定 / 工具授权）：便于改动之前先看清
	if c.Agents != nil {
		if info, gerr := c.Agents.Get(ctx, name); gerr == nil && info != nil {
			if c.Skills != nil {
				if names, serr := c.Skills.ListAgentSkills(ctx, info.ID); serr == nil {
					out["skills"] = names
				}
				// 私有技能名只在调用方是 supervisor 时给：别人的私有技能，非 supervisor 连名字都不该看到
				if c.IsSupervisor {
					if names, perr := c.Skills.ListAgentPrivateSkills(ctx, info.ID); perr == nil {
						out["private_skills"] = names
					}
				}
			}
			// 两个 agent 级开关与超权属性属实例自身配置：仅 supervisor 视角给（值直接取实例字段）
			if c.IsSupervisor {
				out["discovery"] = info.SkillDiscovery
				out["unrestricted"] = info.ToolsUnrestricted
				out["is_supervisor"] = info.IsSupervisor
			}
			// 工具授权只在 supervisor 视角给：它是「谁能用哪些工具」，非 supervisor 无权窥探他人配置
			if c.ToolGrants != nil && c.IsSupervisor {
				if views, verr := c.ToolGrants.ListAgentGrants(ctx, name, info.ID); verr == nil {
					out["tool_grants"] = views
				}
			}
			// 调用授权同理（谁能调谁属管理配置）：can_call = 出边，called_by = 入边
			if c.Relations != nil && c.IsSupervisor {
				if canCall, calledBy, rerr := c.Relations.ListRelations(ctx, info.ID); rerr == nil {
					out["can_call"] = canCall
					out["called_by"] = calledBy
				}
			}
		}
	}
	return out, nil
}

// skillDetailByArgs 按 name / skill_id 取 skill 详情（read_skill 与 use_skill 共用）。
func skillDetailByArgs(ctx context.Context, c *Context, args map[string]any) (*SkillDetail, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	if c.Skills == nil {
		return nil, errors.New("skill manager not available")
	}
	detail, err := c.Skills.Get(ctx, skillRefFrom(args, ""))
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	return detail, nil
}

// readSkillTool 读取某个 skill 的 SKILL.md —— **纯查看**，不激活它的工具依赖。
// 想按技能做事请用 use_skill（返回同样的正文，并登记「本轮已激活」）。
type readSkillTool struct{}

func (t *readSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "read_skill",
		Category: CategorySkill,
		Description: "查看某个 skill 的 SKILL.md 内容，**不会**激活它声明的工具（只想知道它讲什么、或查资料时用这个）。" +
			"返回值里的 tool_deps 是它声明的工具依赖（原样 ref，空数组 = 没声明）：只展示，不激活 —— 想用这些工具请改用 use_skill。" +
			"要按它做事，请改用 use_skill —— 后者会返回正文并让它的工具依赖从下一轮起可用。" +
			"按当前可见范围定位；超级智能体改别人的技能请用 *_agent_skill 系列，或直接用 read_file / edit_file 改文件。" +
			"租户级共享 skill 还会返回可见性现状：restricted（true = 仅白名单可见）与 bound_agents（白名单，仅超级智能体可见）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "skill 名，例如 english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": "skill 的 id（可选；仅超级智能体可按 id 跨归属定向读取）" + skillIDDefaultHint},
			},
			"required": []any{"name"},
		},
	}
}

func (t *readSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return skillDetailByArgs(ctx, c, args)
}

// useSkillTool 使用某个 skill：返回正文，并把「本轮已激活」登记下来，
// 它声明的工具依赖从下一次 LLM 调用起进入工具集（run 结束随 Context 消失）。
//
// 与 read_skill 分家的理由：读文档 ≠ 要它的工具。查看动作不该把 exec / browser /
// skill-admin 这类大 schema 摆进上下文（占用预算、撞 50 上限、扩大误选面）。
type useSkillTool struct{}

func (t *useSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "use_skill",
		Category: CategorySkill,
		Description: "按某个 skill 做事时调用：返回 SKILL.md 正文与它声明的工具依赖（tool_deps），并**激活**这些依赖（从下一次调用起可用，本 run 结束失效）。" +
			"激活后实际多出哪些工具仍取决于你被授权的范围（allow），tool_deps 只是它「需要什么」。" +
			"只查看内容、不打算按其操作，请用 read_skill（不会激活任何工具）。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "skill 名，例如 english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": "skill 的 id（可选；仅超级智能体可按 id 跨归属定向读取）" + skillIDDefaultHint},
			},
			"required": []any{"name"},
		},
	}
}

func (t *useSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	detail, err := skillDetailByArgs(ctx, c, args)
	if err != nil {
		return nil, err
	}
	// 激活 = 本轮注入：该技能声明的工具依赖从下一次 LLM 调用起生效
	// （见 Context.MarkSkillLoaded 与 agentImpl.refreshToolDefs）。
	// 依赖清单连同快照一起登记：正式运行下它就是当前版本的声明，工具解析不再回头查实时表
	// —— 否则「草稿里加一条依赖」会在线上被点亮，冻结只冻了展示那一半。
	// 不跨 run 保留：历史只回填文本，下一轮 run 重新开始时工具集回到 required。
	c.MarkSkillLoaded(detail.ID, detail.Name, detail.ToolDeps)
	return detail, nil
}

// callAgentTool 同步调用另一个 agent，并返回其最终结果。
type callAgentTool struct{}

func (t *callAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "call_agent",
		Category:    CategoryAgent,
		Description: "调用另一个 agent 执行任务，同步等待并返回其最终结果。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_name": map[string]any{"type": "string", "description": "目标 agent 名"},
				"arguments":  map[string]any{"type": "object", "description": "传给目标 agent 的参数（可选）"},
			},
			"required": []any{"agent_name"},
		},
	}
}

func (t *callAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "agent_name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q (只允许字母数字横线)", name)
	}
	// 不允许调用自己：直接自递归没有意义
	if name == c.AgentName {
		return nil, errors.New("cannot call the running agent itself")
	}
	if c.Caller == nil {
		return nil, errors.New("agent caller not available")
	}
	subArgs, _ := args["arguments"].(map[string]any)
	out, err := c.Caller.Call(ctx, CallRequest{
		CallerID:   c.AgentID,
		CallerName: c.AgentName,
		CalleeName: name,
		Args:       subArgs,
		Depth:      c.CallDepth,
		Chain:      c.CallChain,
		UserID:     c.UserID,
		SessionID:  c.SessionID,
		// 子调用模式：由 ChildMode 统一算（自身草稿 && 跟随草稿开关）。
		// 开关关掉时子 agent 读已发布版本，没有版本会明确报错，不静默回退草稿。
		DefinitionMode: c.ChildMode(),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "result": out}, nil
}

// resolveTargetAgent 指定归属类元工具共用的目标 agent 校验：名合法、存在、且处于正常态。
// 返回 (agent 名, agent uuid)。
//
// 判据与文件闸门（gate.go 的 checkWrite / checkWriteOwn）和 skillManager.checkOwnerAgent
// 同源（models.IsModifiable）：四处必须一起判，否则会出现「元工具能改、文件工具报错」的分裂。
func resolveTargetAgent(ctx context.Context, c *Context, args map[string]any) (string, string, error) {
	name := argString(args, "agent")
	if !ValidName(name) {
		return "", "", fmt.Errorf("invalid agent name %q", name)
	}
	if c.Agents == nil {
		return "", "", errors.New("agent manager not available")
	}
	if c.Skills == nil {
		return "", "", errors.New("skill manager not available")
	}
	info, err := c.Agents.Get(ctx, name)
	if err != nil {
		return "", "", err
	}
	if info == nil {
		return "", "", fmt.Errorf("agent %q not found", name)
	}
	if !models.IsModifiable(info.Status) {
		return "", "", fmt.Errorf("agent %q is deprecated; switch it back to normal first", name)
	}
	return name, info.ID, nil
}

// bindSkillTool 给某 agent 追加绑定一个技能（幂等）。仅超级智能体。
type bindSkillTool struct{}

func (t *bindSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "bind_skill",
		Category: CategoryAgentAdmin,
		Description: "给某个 agent 追加绑定一个技能（幂等，已绑定则无变化）：绑定后该 skill 才会写进它的提示词。" +
			"只接受租户级 skill 名。仅超级智能体可用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "目标 agent 名"},
				"skill": map[string]any{"type": "string", "description": "要绑定的租户级 skill 名"},
			},
			"required": []any{"agent", "skill"},
		},
	}
}

func (t *bindSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	skill := argString(args, "skill")
	if err := c.Skills.BindAgentSkill(ctx, agentID, skill); err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "skill": skill, "bound": true}, nil
}

// unbindSkillTool 移除某 agent 的一个技能绑定（幂等）。仅超级智能体。
type unbindSkillTool struct{}

func (t *unbindSkillTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "unbind_skill",
		Category:    CategoryAgentAdmin,
		Description: "移除某个 agent 的一个技能绑定（幂等，未绑定则无变化）。只接受租户级 skill 名。仅超级智能体可用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "目标 agent 名"},
				"skill": map[string]any{"type": "string", "description": "要解绑的租户级 skill 名"},
			},
			"required": []any{"agent", "skill"},
		},
	}
}

func (t *unbindSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	skill := argString(args, "skill")
	if err := c.Skills.UnbindAgentSkill(ctx, agentID, skill); err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "skill": skill, "unbound": true}, nil
}

// searchToolsTool 检索**可授权的**工具目录（仅超级智能体）。
//
// 为什么需要它：授权是对字符串的操作（`mcp:fs:read_file`），而模型手里没有「本租户
// 有哪些连接、连接里有哪些工具」这份事实 —— 不给查询入口，它只能靠用户告知连接名，
// 或者把 ref 写错再看报错。后者是试错，不是发现。
//
// 输出刻意是**授权 ref**（mcp:fs:read_file / builtin:file）而不是模型侧工具名
// （mcp__fs__read_file）：这个工具的下一步动作是 grant_agent_tool，粘的就是 ref。
// 也不返回入参 schema —— 那是「调用」才需要的，占上下文，授完权自然能看到。
type searchToolsTool struct{}

func (t *searchToolsTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "search_tools",
		Category: CategoryToolGrant,
		Description: "检索**可授权的**工具目录（仅超级智能体）：内置工具 / 连接（MCP、RPC）/ 插件 / 自定义工具 / 本地端点 / 工具组。" +
			"返回的 ref 可直接粘进 grant_agent_tool（如 builtin:file 整组、mcp:fs 整条连接、mcp:fs:read_file 单个工具）。" +
			"query 省略时只列组概览（组 ref + 成员数 + 前几个成员 ref + 是否可用），不展开到单个工具；" +
			"给了 query 才展开匹配的工具（关键字匹配连接名 / 工具名 / 描述 / ref 片段，如 fs、read_file、mcp）。" +
			"传 agent 会按该 agent 的身份过滤（「仅超级智能体可用」的工具不会出现在非超级智能体的候选里），" +
			"并回填它已经授过的 ref（granted）—— 先查再授，不要凭记忆写 ref。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "关键字：连接名 / 工具名 / 描述 / ref 片段（如 fs、read_file、mcp、builtin:file）。省略时只列组概览",
				},
				"agent": map[string]any{
					"type":        "string",
					"description": "可选。目标 agent 名：按它的身份过滤目录，并回填它当前的授权状态。省略表示按调用者自己的身份",
				},
			},
		},
	}
}

func (t *searchToolsTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	if c.ToolCatalog == nil {
		return nil, errors.New("tool catalog not available")
	}
	// 默认按调用者自己的身份与授权状态
	name, id := c.AgentName, c.AgentID
	if want := argString(args, "agent"); want != "" && want != c.AgentName {
		if c.Agents == nil {
			return nil, errors.New("agent manager not available")
		}
		info, err := c.Agents.Get(ctx, want)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, fmt.Errorf("agent %q not found", want)
		}
		name, id = info.Name, info.ID
	}
	// 只读检索：不校验目标是否处于正常态（废弃的 agent 也能看它有什么）
	return c.ToolCatalog.Search(ctx, argString(args, "query"), name, id)
}

// grantAgentToolTool 给某个 agent 赋予一条工具授权（仅超级智能体）。
//
// kind 省略时由系统判定 —— 模型看不到各 skill 声明的 tool_deps（存在 skill_tool_grants 表里，
// 不在 SKILL.md 正文中），凭猜写 allow 的后果是「授权了却永不生效」。
// 显式指定 required / allow / deny 时按各自语义校验，见 service/agent/toolgrants.go。
type grantAgentToolTool struct{}

func (t *grantAgentToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "grant_agent_tool",
		Category: CategoryToolGrant,
		Description: "给**指定 agent** 赋予一条工具授权（仅超级智能体，目标须处于编辑态）。" +
			"kind 可选：required（恒注入常驻，不随技能进出）/ allow（按需：必须已有技能声明该依赖，" +
			"use_skill 激活后才注入）/ deny（显式拒绝：即使技能声明也不注入，会覆盖该 ref 原有的行）。" +
			"省略 kind 则由系统判定：有技能声明 → allow，否则 required。" +
			"ref 形如 builtin:exec / builtin:file（类别组）、builtin:read_file（单工具）、mcp:<连接>:<工具>、group:<组名>。" +
			"每个 ref 只保留一行，改 kind 即覆盖旧行（返回值会说明覆盖了什么）。" +
			"「仅超级智能体可用」的工具不能授给非超级智能体（会被明确拒绝）。" +
			"可以给自己加（超级智能体即管理员）：本 run 的下一轮工具集就会重算，不必等下一次会话。" +
			"废弃的 agent 一律拒写，你自己也不例外。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "目标 agent 名（可以是自己）"},
				"ref":   map[string]any{"type": "string", "description": "工具引用：builtin:<工具名>、builtin:<类别>、mcp:<连接>[:<工具>]、rpc:<连接>[:<工具>]、local:rpc[:<端点>]、group:<组名>"},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []any{"required", "allow", "deny"},
					"description": "可选。required=恒注入常驻；allow=按需（必须有技能声明该依赖）；deny=显式拒绝并覆盖旧行。省略则由系统判定",
				},
			},
			"required": []any{"agent", "ref"},
		},
	}
}

func (t *grantAgentToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	// D5（修订）：超级智能体可以给自己加 —— 它即管理员；边界由 status 承担（废弃一律拒，
	// 而改 status 只有管理端能做，故不会出现自我解锁）。
	if c.ToolGrants == nil {
		return nil, errors.New("tool grant manager not available")
	}
	ref := argString(args, "ref")
	kind, reason, err := c.ToolGrants.GrantAgentTool(ctx, name, agentID, ref, argString(args, "kind"))
	if err != nil {
		return nil, err
	}
	// 改的是自己：本 run 的工具集要重算，否则刚授的权自己看不见（会被误判为失败而重试）。
	if name == c.AgentName {
		c.MarkToolsDirty()
	}
	return map[string]any{
		"agent": name, "ref": ref, "grant_kind": kind, "reason": reason, "granted": true,
	}, nil
}

// revokeAgentToolTool 移除某个 agent 的一条工具授权（仅超级智能体）。
type revokeAgentToolTool struct{}

func (t *revokeAgentToolTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "revoke_agent_tool",
		Category: CategoryToolGrant,
		Description: "移除**指定 agent** 的一条工具授权（仅超级智能体，目标须处于编辑态；幂等）。" +
			"按 ref 精确移除，与它当初以 required / allow / deny 写入无关（返回值会带被删那一行的 kind，便于审计）。" +
			"可以收回自己的（超级智能体即管理员）：本 run 的下一轮工具集随之重算。" +
			"建议先看 read_agent 返回的 tool_grants 再收回，避免误删仍在用的授权。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "目标 agent 名"},
				"ref":   map[string]any{"type": "string", "description": "要移除的工具引用（与授权时一致）"},
			},
			"required": []any{"agent", "ref"},
		},
	}
}

func (t *revokeAgentToolTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	if c.ToolGrants == nil {
		return nil, errors.New("tool grant manager not available")
	}
	ref := argString(args, "ref")
	removed, kind, err := c.ToolGrants.RevokeAgentTool(ctx, agentID, ref)
	if err != nil {
		return nil, err
	}
	// 收回自己的授权：同样要重算，否则工具会「看不见消失」，直到下次会话。
	if name == c.AgentName {
		c.MarkToolsDirty()
	}
	return map[string]any{
		"agent": name, "ref": ref, "removed": removed, "grant_kind": kind, "revoked": true,
	}, nil
}

// resolveCallTarget 定位「被调用方」agent（名合法 + 存在）；selfName 非空时顺带拒绝指向自己。
// 写时守卫（非超级智能体 / 非废弃）留在调用方 —— 收回授权不该被这些条件挡住（可能正需要清理陈数据）。
func resolveCallTarget(ctx context.Context, c *Context, target, selfName string) (*AgentInfo, error) {
	if !ValidName(target) {
		return nil, fmt.Errorf("invalid agent name %q", target)
	}
	if selfName != "" && target == selfName {
		return nil, fmt.Errorf("agent %q cannot call itself", target)
	}
	if c.Agents == nil {
		return nil, errors.New("agent manager not available")
	}
	info, err := c.Agents.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("agent %q not found", target)
	}
	return info, nil
}

// allowAgentCallTool 让某个 agent 获得调用另一个 agent 的授权（仅超级智能体）。
//
// 调用授权就是 agent_relations 里的一行 —— caller.go 的校验对所有 agent 一视同仁
// （无行即拒绝，超级智能体也没有豁免），所以「让超级智能体能调用 poet」必须真的写这一行。
type allowAgentCallTool struct{}

func (t *allowAgentCallTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "allow_agent_call",
		Category: CategoryToolGrant,
		Description: "让**指定 agent** 获得调用另一个 agent 的授权（仅超级智能体；幂等）。" +
			"这只写入一条调用关系（agent_relations），真正调用时（call_agent）才会被校验。" +
			"被调用方可以是任意未废弃的 agent（含超级智能体 —— 它被调起时仍是完整的管理面），但不能是自己。" +
			"目标若尚未发布正式版本，返回里会带 warning：授权照样写入，但正式入口（MCP / RPC / 端点 / flow）调用它会失败。" +
			"改动前可用 read_agent 看目标的 can_call / called_by 现状。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":  map[string]any{"type": "string", "description": "调用方 agent 名（须处于编辑态）"},
				"target": map[string]any{"type": "string", "description": "被调用方 agent 名（含超级智能体；不能是自己）"},
			},
			"required": []any{"agent", "target"},
		},
	}
}

func (t *allowAgentCallTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	info, err := resolveCallTarget(ctx, c, argString(args, "target"), name)
	if err != nil {
		return nil, err
	}
	// 写时守卫：废弃的 agent 根本调不通，不写进库，免得留下永远用不上的死授权。
	// supervisor 不再排除 —— 它可被调用，被调起时按 resolveDefinitionMode 恒读草稿。
	if !models.IsRunnable(info.Status) {
		return nil, fmt.Errorf("agent %q is deprecated; it cannot be called", info.Name)
	}
	if c.Relations == nil {
		return nil, errors.New("agent relations manager not available")
	}
	if err := c.Relations.AllowCall(ctx, agentID, info.ID); err != nil {
		return nil, err
	}
	out := map[string]any{"agent": name, "target": info.Name, "allowed": true}
	// 授权与版本是两件事：这条边只代表「允许调用」，正式链还得被调方有 is_current 版本。
	// 目标从未发布时，父 agent 走正式入口（MCP / RPC / 端点 / flow）调用会命 ErrNoReleasedVersion，
	// 只有编辑页调试能调起来 —— 先提示，不拦（常见路径是「先建 agent + 授权，再发布」）。
	if !agentHasReleasedVersion(c.Schema, info.Name) {
		out["unreleased"] = true
		out["warning"] = fmt.Sprintf("agent %q 还没有发布正式版本：授权已写入，但正式入口（MCP / RPC / 端点 / flow）调用它会报「尚未发布正式版本」，只有编辑页调试能调起来", info.Name)
	}
	return out, nil
}

// agentHasReleasedVersion 目标是否已有正式版本（is_current）。
// 只用于「提示」：查询不可用或报错时按「有」处理 —— 探测失败不该把一次成功的授权标成可疑。
func agentHasReleasedVersion(schema, agentName string) bool {
	if db.DB == nil {
		return true
	}
	_, _, err := versioning.AgentCurrentVersion(schema, agentName)
	return !errors.Is(err, versions.ErrNoReleasedVersion)
}

// revokeAgentCallTool 收回某个 agent 调用另一个 agent 的授权（仅超级智能体）。
type revokeAgentCallTool struct{}

func (t *revokeAgentCallTool) Meta() ToolMeta {
	return ToolMeta{
		Name:     "revoke_agent_call",
		Category: CategoryToolGrant,
		Description: "收回**指定 agent** 调用另一个 agent 的授权（仅超级智能体；幂等，返回删除条数）。" +
			"建议先看 read_agent 的 can_call 再收回，避免误删仍在用的调用链路。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":  map[string]any{"type": "string", "description": "调用方 agent 名"},
				"target": map[string]any{"type": "string", "description": "被调用方 agent 名"},
			},
			"required": []any{"agent", "target"},
		},
	}
}

func (t *revokeAgentCallTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name, agentID, err := resolveTargetAgent(ctx, c, args)
	if err != nil {
		return nil, err
	}
	// 收回时不套写时守卫：陈数据（比如曾被误授的 main）必须还能清理
	info, err := resolveCallTarget(ctx, c, argString(args, "target"), "")
	if err != nil {
		return nil, err
	}
	if c.Relations == nil {
		return nil, errors.New("agent relations manager not available")
	}
	removed, err := c.Relations.RevokeCall(ctx, agentID, info.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": name, "target": info.Name, "removed": removed, "revoked": true}, nil
}

func init() {
	Register(&createAgentTool{})
	Register(&updateAgentTool{})
	Register(&deleteAgentTool{})
	Register(&createOwnSkillTool{})
	Register(&updateOwnSkillTool{})
	Register(&deleteOwnSkillTool{})
	Register(&createSharedSkillTool{})
	Register(&updateSharedSkillTool{})
	Register(&deleteSharedSkillTool{})
	Register(&createAgentSkillTool{})
	Register(&updateAgentSkillTool{})
	Register(&deleteAgentSkillTool{})
	Register(&bindSkillTool{})
	Register(&unbindSkillTool{})
	Register(&grantSkillToolTool{})
	Register(&revokeSkillToolTool{})
	Register(&searchToolsTool{})
	Register(&grantAgentToolTool{})
	Register(&revokeAgentToolTool{})
	Register(&allowAgentCallTool{})
	Register(&revokeAgentCallTool{})
	Register(&listAgentsTool{})
	Register(&listSkillsTool{})
	Register(&readAgentTool{})
	Register(&readSkillTool{})
	Register(&useSkillTool{})
	Register(&callAgentTool{})
	// 自定义工具的元工具（分类 custom-tool，天然只给超级智能体）。
	Register(&createCustomToolTool{})
	Register(&updateCustomToolTool{})
	Register(&deleteCustomToolTool{})
	Register(&listCustomToolsTool{})
}
