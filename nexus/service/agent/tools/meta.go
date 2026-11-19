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
const skillIDDefaultHint = "; omit it by default — locate the skill by name; fill it only when you already have the exact id from list_skills"

// rejectScopeArg 拒绝旧接口遗留的 scope 参数。
//
// 历史形态是「单个 create_skill + scope 参数」；拆分后工具名即口径，模型若按旧习惯传
// scope 必须显式纠正 —— 静默忽略会让它"以为建了共享、其实建了私有"。
// counterpart 为对偶工具名，用于给出可执行的纠正指引。
func rejectScopeArg(args map[string]any, tool, counterpart string) error {
	if _, ok := args["scope"]; !ok {
		return nil
	}
	return fmt.Errorf("%s does not take a scope argument (the tool name carries the scope); use %s to create a skill in the other space", tool, counterpart)
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
		return nil, fmt.Errorf("%s does not take a tool_deps argument: skill tool deps belong to the grant surface, so only the supervisor can declare them via *_agent_skill / *_shared_skill", tool)
	}
	refs, ok := argStringSlice(args, "tool_deps")
	if !ok {
		return nil, fmt.Errorf("%s: tool_deps must be an array of strings (refs look like builtin:exec)", tool)
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
		"description": "Tool refs this skill declares it needs (e.g. builtin:exec, builtin:file); replaced wholesale — pass [] to clear, omit to leave unchanged. Only takes effect when the host agent already has an allow grant for those tools",
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
		return nil, fmt.Errorf("%s does not take a restricted argument: visibility only applies to tenant-level shared skills, use update_shared_skill instead", tool)
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%s: restricted must be a boolean", tool)
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
		"description": "Visibility switch (omit to leave unchanged): true = only agents on the whitelist can see it (default), false = visible to everyone. " +
			"Whitelist members are added and removed with bind_skill / unbind_skill — this parameter only flips the switch",
	}
	return m
}

// createAgentTool 创建新 agent（写 AGENT.md + 注册实例）。
type createAgentTool struct{}

func (t *createAgentTool) Meta() ToolMeta {
	return ToolMeta{
		Name:        "create_agent",
		Category:    CategoryAgentAdmin,
		Description: "Create a new agent instance: write AGENT.md and register it in the system.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only — it is also the definition-domain directory name"},
				"description": map[string]any{"type": "string", "description": "Agent description"},
				"agent_md":    map[string]any{"type": "string", "description": "Full text of AGENT.md"},
				"is_supervisor": map[string]any{"type": "boolean", "description": "Whether to create it directly as a supervisor (optional, default false). " +
					"When true the starter kit is materialized as well: the core tool group (including builtin:agent-admin), unrestricted tools, discovery of unbound skills, " +
					"and the agent-authoring / skill-authoring / custom-tool-authoring / flow-authoring skills bound"},
			},
			"required": []any{"name", "agent_md"},
		},
	}
}

func (t *createAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q (letters, digits and hyphens only)", name)
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
		Description: "Update an agent: rename it, change its description, the \"discover unbound skills\" switch or the \"unrestricted tools\" switch (a rename migrates its definition-domain directory too). " +
			"You may update yourself (the supervisor is the admin), but **you cannot rename yourself** (the file operations later in this run would break).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "Current agent name (may be yourself)"},
				"new_name":      map[string]any{"type": "string", "description": "New name (optional; cannot be used on yourself)"},
				"description":   map[string]any{"type": "string", "description": "New description (optional; empty or omitted both mean unchanged — this tool cannot clear a description, ask an admin to do it in the admin UI)"},
				"discovery":     map[string]any{"type": "boolean", "description": "Whether this agent may discover unbound shared skills (optional; omit to leave unchanged)"},
				"unrestricted":  map[string]any{"type": "boolean", "description": "Whether tools are unrestricted: when on, candidate tools are no longer filtered by allow (optional; omit to leave unchanged)"},
				"is_supervisor": map[string]any{"type": "boolean", "description": "Whether to grant supervisor power (same level as the supervisor: admin tools + cross-domain writes). Only a supervisor may do this; demoting the tenant's last supervisor is refused (optional; omit to leave unchanged)"},
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
		return nil, errors.New("cannot rename the running agent itself (later file operations in this round would break); ask an administrator to rename it")
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
		Description: "Delete an agent (its definition-domain directory is cleaned up too).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Agent name"},
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
		Description: "Create a skill in your own private space: write SKILL.md and register it in the skill library. " +
			"A private skill is visible to you only; to reuse it across agents, have the supervisor create a shared skill with create_shared_skill and bind it with bind_skill.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only"},
				"skill_md":    map[string]any{"type": "string", "description": "Full text of SKILL.md"},
				"description": map[string]any{"type": "string", "description": "Skill description (optional)"},
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
		return nil, fmt.Errorf("invalid skill name %q (letters, digits and hyphens only)", name)
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
		Description: "Create a tenant-level shared skill (supervisor only): write SKILL.md and register it in the skill library. " +
			"Other agents only see it after bind_skill binds it; by default only you are granted, so it never spreads on its own.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only"},
				"skill_md":    map[string]any{"type": "string", "description": "Full text of SKILL.md"},
				"description": map[string]any{"type": "string", "description": "Skill description (optional)"},
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
				"name":        map[string]any{"type": "string", "description": "Current skill name (used to locate it)"},
				"skill_id":    map[string]any{"type": "string", "description": idHint},
				"new_name":    map[string]any{"type": "string", "description": "New name after renaming (optional)"},
				"skill_md":    map[string]any{"type": "string", "description": "New full text of SKILL.md (optional; omit to keep the current content)"},
				"description": map[string]any{"type": "string", "description": "New description (optional)"},
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
		"Update your own private skill: change its content, description or name (a rename migrates its content file). Only your own private skills can be updated.",
		"Skill id (optional; when locating by id it must still be inside your own private space)")
}

func (t *updateOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return updateSkillInSpace(ctx, c, args, SkillScopeAgent, "", "update_own_skill", false)
}

// updateSharedSkillTool 更新「租户级共享」skill（分类 skill-admin：仅超级智能体）。
type updateSharedSkillTool struct{}

func (t *updateSharedSkillTool) Meta() ToolMeta {
	return withRestricted(withToolDeps(skillUpdateMeta("update_shared_skill", CategorySkillAdmin,
		"Update a tenant-level shared skill (supervisor only): change its content, description, name, tool dependencies or visibility switch (a rename migrates its content file). Note it may currently be bound by several agents.",
		"Skill id (optional; when locating by id it must still be a tenant-level shared skill)")))
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
			"**This is not a grant**: the dependency is only injected when the host agent already has an allow grant for that tool and has activated this skill with use_skill. " +
			"The first argument is the skill to declare it on (name or skill_id), not an agent — to grant an agent, use grant_agent_tool. " +
			"The return value carries the complete tool_deps after the change, so there is no need to look it up again.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name (located within the currently visible scope; for a private skill owned by another agent use skill_id)"},
				"skill_id": map[string]any{"type": "string", "description": "Skill id (optional; alternative to name, takes precedence when non-empty)" + skillIDDefaultHint},
				"ref":      map[string]any{"type": "string", "description": "Tool reference: builtin:<tool> / builtin:<category> / mcp:<connection>[:<tool>] / rpc:<connection>[:<tool>] / local:rpc[:<endpoint>]; group refs are not accepted"},
			},
			"required": []any{"ref"},
		},
	}
}

// skillDepByArgs 解析 name / skill_id，并做「至少给一个合法定位」的前置校验。
func skillDepByArgs(args map[string]any) (SkillRef, error) {
	ref := skillRefFrom(args, "")
	if ref.ID == "" && !ValidName(ref.Name) {
		return ref, errors.New("a name (a valid skill name) or a skill_id is required to locate the skill")
	}
	return ref, nil
}

type grantSkillToolTool struct{}

func (t *grantSkillToolTool) Meta() ToolMeta {
	return skillDepMeta("grant_skill_tool",
		"**Append** one tool dependency to a skill (written to skill_tool_grants; idempotent, added=false when it already exists).")
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
		"**Remove** one tool dependency from a skill (idempotent; removes only this one and leaves the other dependencies alone).")
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
				"name":     map[string]any{"type": "string", "description": "Skill name, e.g. english-joke-writing"},
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
		"Delete your own private skill (its content file is deleted too). Only your own private skills can be deleted.",
		"Skill id (optional; when locating by id it must still be inside your own private space)")
}

func (t *deleteOwnSkillTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	return deleteSkillInSpace(ctx, c, args, SkillScopeAgent, "")
}

// deleteSharedSkillTool 删除「租户级共享」skill（仅超级智能体）。
type deleteSharedSkillTool struct{}

func (t *deleteSharedSkillTool) Meta() ToolMeta {
	return skillDeleteMeta("delete_shared_skill", CategorySkillAdmin,
		"Delete a tenant-level shared skill (supervisor only; its content file is deleted too). Note it may currently be bound by several agents.",
		"Skill id (optional; when locating by id it must still be a tenant-level shared skill)")
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
	props["agent"] = map[string]any{"type": "string", "description": "Target agent name (the skill belongs to it; afterwards only that agent can see it)"}
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
		Description: "Create a **specified agent**'s private skill (supervisor only): write SKILL.md and register it in the skill library. " +
			"The skill belongs to the target agent and only that agent can see it; all content goes into skill_md. " +
			"To share it between several agents, use create_shared_skill + bind_skill instead.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique name, letters/digits/hyphens only"},
				"skill_md":    map[string]any{"type": "string", "description": "Full text of SKILL.md (the entire content of the skill)"},
				"description": map[string]any{"type": "string", "description": "Skill description (optional)"},
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
		"Update a **specified agent**'s private skill (supervisor only): change its content, description, name or tool dependencies (a rename migrates its content file).",
		"Skill id (optional; when locating by id it must still be inside that agent's private space)")))
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
		"Delete a **specified agent**'s private skill (supervisor only; its content file is deleted too).",
		"Skill id (optional; when locating by id it must still be inside that agent's private space)"))
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
		Description: "List all registered agent instances (name/description/status).",
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
		Description: "List the skills visible to the current agent (its own private skills + skills bound to it; shared skills visible to everyone are included when skill discovery is on).",
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
			"note": fmt.Sprintf("%d skills are visible and only the first %d are listed here; if the skill you need is missing, "+
				"ask an admin to bind it to this agent (or narrow the visible scope)", total, maxListSkills),
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
		Description: "Read an agent's AGENT.md and return the names of the skills currently bound to it (skills). " +
			"When called by a supervisor it also returns that agent's tool grants (tool_grants: required always applies / allow is pending until use_skill activates it / deny is an explicit refusal), the \"unrestricted tools\" switch (unrestricted), " +
			"the \"discover unbound skills\" switch (discovery), " +
			"and its call grants (can_call = whom it may call, called_by = who may call it).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Agent name"},
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
		Description: "View a skill's SKILL.md — **viewing only**, the tools it declares are NOT activated (use this when you just want to know what it says or look something up). " +
			"tool_deps in the result lists the tool dependencies it declares (raw refs; an empty array means none): shown, not activated — to use those tools call use_skill instead. " +
			"To work according to the skill, call use_skill — that returns the body and makes its tool dependencies available from the next round on. " +
			"Locate it within the currently visible scope; a supervisor editing someone else's skill should use the *_agent_skill tools, or edit the file directly with read_file / edit_file. " +
			"For a tenant-level shared skill the result also carries visibility state: restricted (true = whitelist only) and bound_agents (the whitelist, supervisor only).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name, e.g. english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": "Skill id (optional; only a supervisor can target across ownership by id)" + skillIDDefaultHint},
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
		Description: "Call this when working according to a skill: returns the SKILL.md body and the tool dependencies it declares (tool_deps), and **activates** them (available from the next call on; expires when this run ends). " +
			"Which tools actually show up still depends on your granted scope (allow) — tool_deps only says what the skill *needs*. " +
			"To merely view the content without acting on it, use read_skill (it activates nothing).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Skill name, e.g. english-joke-writing"},
				"skill_id": map[string]any{"type": "string", "description": "Skill id (optional; only a supervisor can target across ownership by id)" + skillIDDefaultHint},
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
		Description: "Call another agent to run a task; waits synchronously and returns its final result.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_name": map[string]any{"type": "string", "description": "Target agent name"},
				"arguments":  map[string]any{"type": "object", "description": "Arguments passed to the target agent (optional)"},
			},
			"required": []any{"agent_name"},
		},
	}
}

func (t *callAgentTool) Execute(ctx context.Context, c *Context, args map[string]any) (any, error) {
	name := argString(args, "agent_name")
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid agent name %q (letters, digits and hyphens only)", name)
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
		Description: "Bind one more skill to an agent (idempotent; no change when already bound): only bound skills are written into its prompt. " +
			"Only tenant-level skill names are accepted. Supervisor only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name"},
				"skill": map[string]any{"type": "string", "description": "Tenant-level skill name to bind"},
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
		Description: "Remove one skill binding from an agent (idempotent; no change when not bound). Only tenant-level skill names are accepted. Supervisor only.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name"},
				"skill": map[string]any{"type": "string", "description": "Tenant-level skill name to unbind"},
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
		Description: "Search the **grantable** tool catalog (supervisor only): builtin tools / connections (MCP, RPC) / plugins / custom tools / local endpoints / tool groups. " +
			"The returned refs can be pasted straight into grant_agent_tool (e.g. builtin:file for a whole group, mcp:fs for a whole connection, mcp:fs:read_file for a single tool). " +
			"With query omitted it only lists the group overview (group ref + member count + first member refs + availability) and does not expand to individual tools; " +
			"with a query it expands the matching tools (the keyword matches connection name / tool name / description / ref fragment, e.g. fs, read_file, mcp). " +
			"Passing agent filters the catalog by that agent's identity (tools marked \"supervisor only\" never appear for a non-supervisor) " +
			"and fills in the refs it has already been granted (granted) — look it up before granting, do not write refs from memory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Keyword: connection name / tool name / description / ref fragment (e.g. fs, read_file, mcp, builtin:file). Omit to list only the group overview",
				},
				"agent": map[string]any{
					"type":        "string",
					"description": "Optional. Target agent name: filter the catalog by its identity and fill in its current grant state. Omit to use the caller's own identity",
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
		Description: "Grant one tool authorization to a **specified agent** (supervisor only; the target must be editable). " +
			"kind: required (always injected, independent of skills) / allow (on demand: some skill must already declare that dependency, " +
			"and it is injected only after use_skill activates it) / deny (explicit refusal: not injected even if a skill declares it, and it overrides the existing row for that ref). " +
			"If kind is omitted the system decides: allow when some skill declares it, otherwise required. " +
			"ref looks like builtin:exec / builtin:file (category group), builtin:read_file (single tool), mcp:<connection>:<tool>, group:<group name>. " +
			"Only one row is kept per ref — changing kind overwrites the old row (the result says what was overwritten). " +
			"Tools marked \"supervisor only\" cannot be granted to a non-supervisor (the call is explicitly refused). " +
			"You may grant yourself (the supervisor is the admin): the tool set is recomputed on the next round of this run, no new session needed. " +
			"Deprecated agents always refuse writes, yourself included.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name (may be yourself)"},
				"ref":   map[string]any{"type": "string", "description": "Tool reference: builtin:<tool>, builtin:<category>, mcp:<connection>[:<tool>], rpc:<connection>[:<tool>], local:rpc[:<endpoint>], group:<group name>"},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []any{"required", "allow", "deny"},
					"description": "Optional. required = always injected; allow = on demand (some skill must declare the dependency); deny = explicit refusal that overrides the old row. Omit to let the system decide",
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
		Description: "Remove one tool authorization from a **specified agent** (supervisor only; the target must be editable; idempotent). " +
			"Removal is by exact ref and does not care whether the row was written as required / allow / deny (the result returns the kind of the removed row for auditing). " +
			"You may revoke your own (the supervisor is the admin): the tool set is recomputed on the next round of this run. " +
			"Check the tool_grants returned by read_agent first, so you don't delete a grant that is still in use.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string", "description": "Target agent name"},
				"ref":   map[string]any{"type": "string", "description": "Tool reference to remove (the same one used when granting)"},
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
		Description: "Let a **specified agent** call another agent (supervisor only; idempotent). " +
			"This only writes one call relation (agent_relations); it is enforced when the call actually happens (call_agent). " +
			"The callee may be any non-deprecated agent (including the supervisor — when invoked it still has the full admin surface), but not yourself. " +
			"If the target has no released version yet the result carries a warning: the grant is still written, but calling it through a formal entry point (MCP / RPC / endpoint / flow) will fail. " +
			"Before changing anything, read_agent shows the target's current can_call / called_by.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":  map[string]any{"type": "string", "description": "Caller agent name (must be editable)"},
				"target": map[string]any{"type": "string", "description": "Callee agent name (the supervisor included; cannot be yourself)"},
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
		out["warning"] = fmt.Sprintf("agent %q has no released version yet: the grant is written, but calling it through the live entry points (MCP / RPC / endpoint / flow) reports that no released version exists yet; only the editor debugger can run it", info.Name)
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
		Description: "Revoke a **specified agent**'s permission to call another agent (supervisor only; idempotent, returns the number of rows deleted). " +
			"Check can_call from read_agent first, so you don't break a call chain that is still in use.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent":  map[string]any{"type": "string", "description": "Caller agent name"},
				"target": map[string]any{"type": "string", "description": "Callee agent name"},
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
