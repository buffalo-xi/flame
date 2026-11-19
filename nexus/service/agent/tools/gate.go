package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
)

// 根寻址闸门："以 / 开头的路径 = 从租户根算"（路径语法见 paths.SafePathEx）。
//
// 语法对所有 agent 开放，**权限按身份分两档**（见 resolveRead / resolveWrite 的分流）：
//
//	超级智能体 → 全租户（checkWrite；读不受限，仅写被闸）
//	非超级智能体 → **读**：自己的定义目录 agents/{self}/**（含 AGENT.md）；
//	          **写**：只有自己的私有 skill 目录 agents/{self}/skills/{skill}/**，
//	          且要过两道状态闸（checkWriteOwn）。别人的定义与其它实体一律不可达。
//
// 读写不对称是有意的：读自己的定义是「自省」（自述、诊断、对比本轮与上一轮的行为差异），
// 不改任何东西；写自己的 AGENT.md 属于身份变更，必须由人或 supervisor 做出 —— 否则
// 「这份草稿是谁产生的」无法回答，版本模型里「禁止自发布」的前提就没了。
//
// 为什么给非超级智能体开这一格：私有 skill 是**单文件知识文档**的旧口径下，"改自己的技能"
// 只能改 SKILL.md 正文（update_own_skill）；而超级智能体与管理端早就能管理整个技能目录
// （附属文件 / 子目录）。开这一格是为了把三者口径对齐 —— 编辑态下的 agent 能以同一套
// 文件工具管理自己技能的附属资源。
//
// 判定单位是**实体目录**（= 一行 DB 记录）：
//
//	agents/{agent}                  → agent 实体
//	agents/{agent}/skills/{skill}   → skill 实体（归属 {agent}）
//	skills/{skill}                  → 租户级 skill 实体
//	users/{uid}                     → 用户域（本人或超级智能体）
//
// 规则（对两档都成立）：
//   - 实体目录**内部**的任意文件与子目录（含 AGENT.md / SKILL.md 正文，以及任何
//     附属资源）→ 文件工具可写，过该实体的状态闸（models.IsModifiable）；写 agent
//     私有 skill 时还要过**归属 agent** 的状态闸（非编辑态 → 整片冻结，超级智能体亦然）；
//   - 实体**根目录本身**的写/删 → 拒绝，交给元工具（create_* / delete_*）；
//   - 写一个**不存在的实体** → 拒绝，提示用 create_*（防止长出不登记的目录）。
//
// 本文件不含规则：状态判据唯一真源是 shared/models 的纯谓词；这里只做
// 「路径 → 实体」识别、「取实例」与调谓词。与业务层（manager.go / skills.go）
// 的闸门不叠加 —— 经业务方法的写在那里判，直落 storage 的跨域写才在这里判。

// pathTarget 一条租户根相对路径解析出的实体。
type pathTarget struct {
	kind   string // "user" | "agent" | "skill"
	owner  string // skill 的归属 agent 名（租户级为 ""）；agent 时为 agent 名
	name   string // 实体名：agent 名 / skill 名 / uid
	isRoot bool   // 路径恰为实体根目录本身（而非其内部文件）
}

// resolveTarget 纯字符串解析（不碰 IO，便于单测）。
func resolveTarget(tenantRel string) (pathTarget, error) {
	seg := strings.Split(tenantRel, "/")
	if len(seg) < 2 || seg[0] == "" || seg[1] == "" {
		return pathTarget{}, fmt.Errorf("path not allowed (missing target): %s", tenantRel)
	}
	switch seg[0] {
	case paths.AgentsSubdir:
		agent := seg[1]
		if len(seg) >= 3 && seg[2] == paths.AgentSkillsSubdir {
			if len(seg) < 4 || seg[3] == "" {
				return pathTarget{}, fmt.Errorf("path not allowed (missing skill name): %s", tenantRel)
			}
			return pathTarget{kind: "skill", owner: agent, name: seg[3], isRoot: len(seg) == 4}, nil
		}
		return pathTarget{kind: "agent", owner: agent, name: agent, isRoot: len(seg) == 2}, nil
	case paths.TenantSkillsSubdir:
		return pathTarget{kind: "skill", owner: "", name: seg[1], isRoot: len(seg) == 2}, nil
	case paths.ToolsSubdir:
		// 租户级自定义工具：tools/{name}/**。它是「定义域」内容（代码在盘上），
		// 实体 = {schema}.custom_tools 的一行，与 skills/{name} 同级。
		return pathTarget{kind: "custom_tool", name: seg[1], isRoot: len(seg) == 2}, nil
	case paths.UsersSubdir:
		return pathTarget{kind: "user", name: seg[1], isRoot: len(seg) == 2}, nil
	default:
		return pathTarget{}, fmt.Errorf("path not allowed: %s", tenantRel)
	}
}

// checkWrite 根寻址的写闸（supervisor 档）：身份（仅超级智能体）+ 非实体根 + 目标存在且可改。
// 身份分流在调用点（resolveWrite）已做，这里保留一道做纵深防御。
func checkWrite(ctx context.Context, c *Context, tenantRel string) error {
	if !c.IsSupervisor {
		return errors.New("only a supervisor may access paths outside the workspace")
	}
	t, err := resolveTarget(tenantRel)
	if err != nil {
		return err
	}
	// 实体根目录本身的写/删一律拒绝，交给元工具
	if t.isRoot {
		return fmt.Errorf("refusing to write the entity directory itself (%s); use the matching meta tool", tenantRel)
	}
	switch t.kind {
	case "user":
		// 用户域：本人或超级智能体（已在下方分支之外确认过身份）
		if t.name != c.UserID {
			return fmt.Errorf("not allowed to access files of user %q", t.name)
		}
		return nil
	case "agent":
		if c.Agents == nil {
			return errors.New("agent manager not available")
		}
		info, err := c.Agents.Get(ctx, t.name)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("agent %q not found; use create_agent to register a new agent", t.name)
		}
		if !models.IsModifiable(info.Status) {
			return fmt.Errorf("agent %q is deprecated; switch it back to normal first", t.name)
		}
		return nil
	case "skill":
		if c.Skills == nil {
			return errors.New("skill manager not available")
		}
		ownerID := ""
		if t.owner != "" {
			if c.Agents == nil {
				return errors.New("agent manager not available")
			}
			ai, err := c.Agents.Get(ctx, t.owner)
			if err != nil {
				return err
			}
			if ai == nil {
				return fmt.Errorf("agent %q not found", t.owner)
			}
			// 归属 agent 能否写它的私有 skill —— 与 checkWriteOwn 第 3 条是同一条判据
			// （那里判「自己」，这里判「归属者」），超级智能体不得例外：否则同一判据在
			// 非超级智能体档是冻结、在 supervisor 档却敞开。
			if !models.IsModifiable(ai.Status) {
				return fmt.Errorf("agent %q is deprecated; its private skills are frozen", t.owner)
			}
			ownerID = ai.ID
		}
		info, err := c.Skills.FindForWrite(ctx, ownerID, t.name)
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("skill %q not found; use create_own_skill / create_shared_skill / create_agent_skill to register a new skill", t.name)
		}
		if !models.IsModifiable(info.Status) {
			return fmt.Errorf("skill %q is deprecated; switch it back to normal first", t.name)
		}
		return nil
	case "custom_tool":
		if c.CustomTools == nil {
			return errors.New("script tool source not available")
		}
		status, found, err := c.CustomTools.Status(ctx, t.name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("custom tool %q does not exist; create it with create_custom_tool (or the \"Custom tools\" page in the admin UI) instead of creating the directory directly", t.name)
		}
		if !models.IsModifiable(status) {
			return fmt.Errorf("custom tool %q is locked or deprecated; switch it back to normal first", t.name)
		}
		return nil
	}
	return fmt.Errorf("path not allowed: %s", tenantRel)
}

// checkReadOwn 根寻址的**读**闸：超级智能体不受限；非超级智能体可读自己的定义目录
// （agents/{self}/**，含 AGENT.md 与私有 skill 的附属文件），其余一律拒绝。
//
// 为什么读放得比写宽：读自己的定义是自省；写自己的 AGENT.md 是身份变更（见
// checkWriteOwn），两者不是一个量级。读不查 DB、不设状态闸 —— 废弃的实体
// 也能读（D4）；路径不存在时交给 storage 报 not found。
func checkReadOwn(c *Context, tenantRel string) error {
	if c.IsSupervisor {
		return nil
	}
	t, err := resolveTarget(tenantRel)
	if err != nil {
		return err
	}
	// 自己的定义目录：整棵可读（AGENT.md、私有 skill 正文与附属文件都在里面）
	if t.kind == "agent" && t.name == c.AgentName {
		return nil
	}
	if t.kind == "skill" && t.owner == c.AgentName {
		return nil
	}
	return fmt.Errorf("not allowed to access %q: you may only read your own definition directory (/agents/%s/**), "+
		"and only /agents/%s/skills/<skill>/... inside it is writable", tenantRel, c.AgentName, c.AgentName)
}

// checkWriteOwn 根寻址的**写**闸（非超级智能体档）：只放行自己的私有 skill 目录内部。
//
// 五道判据，缺一不可：
//  1. 形态与归属：必须是 agents/{self}/skills/{skill}/<非空>；其余（别人的 agent /
//     别人的 skill / 租户级 skills/ / users/）一律拒绝；
//  2. 实体根目录本身不能直接写（同 D6：实体的建 / 改 / 删走元工具）；
//  3. **自己是正常态** —— 废弃(3) 的 agent 不可运行、也不可改；
//  4. skill 已登记且属于自己（FindForWrite 按「自身 agentID + 名」查）；
//  5. skill 自身是正常态。
func checkWriteOwn(ctx context.Context, c *Context, tenantRel string) error {
	t, err := resolveTarget(tenantRel)
	if err != nil {
		return err
	}
	// 1) 形态 + 归属
	if t.kind != "skill" || t.owner != c.AgentName {
		return fmt.Errorf("not allowed to write %q: you may only change your own private skill directory (/agents/%s/skills/<skill>/...)",
			tenantRel, c.AgentName)
	}
	// 2) 实体根目录本身交给元工具
	if t.isRoot {
		return fmt.Errorf("refusing to write the skill directory itself (%s); use create_own_skill / update_own_skill / delete_own_skill", tenantRel)
	}
	// 3) 自己必须是正常态
	if c.Agents == nil {
		return errors.New("agent manager not available")
	}
	self, err := c.Agents.Get(ctx, c.AgentName)
	if err != nil {
		return err
	}
	if self == nil {
		return fmt.Errorf("agent %q not found", c.AgentName)
	}
	if !models.IsModifiable(self.Status) {
		return fmt.Errorf("agent %q is deprecated; its private skills are frozen", c.AgentName)
	}
	// 4) skill 必须是自己登记的私有 skill。AgentID 缺失时 FindForWrite 会按「租户级」
	//    查（FindByName 的 agentID=="" 即租户级），把共享 skill 误当私有 —— 先拦掉。
	if c.AgentID == "" {
		return errors.New("no agent context for private skill")
	}
	if c.Skills == nil {
		return errors.New("skill manager not available")
	}
	info, err := c.Skills.FindForWrite(ctx, c.AgentID, t.name)
	if err != nil {
		return err
	}
	if info == nil {
		return fmt.Errorf("skill %q not found; use create_own_skill to register a new private skill", t.name)
	}
	// 5) skill 自身可改
	if !models.IsModifiable(info.Status) {
		return fmt.Errorf("skill %q is locked or deprecated; switch it back to normal first", t.name)
	}
	return nil
}

// entityLikePath 识别「会话工作区里仿造实体目录」的路径形状 —— 即模仿租户根下的
// agents/{name}/... 或 skills/{name}/...（skills 同时覆盖私有与租户级两种写法）。
//
// 为什么要单独拦：这类路径没有 "/" 前缀，SafePathEx 会把它当成工作区相对路径，
// 对非超级智能体而言既不登记 DB 也不生效，只会在工作区留下假实体目录，并让模型误判
// 「已创建」—— 是幻觉式成功的直接来源。（带 "/" 前缀的根寻址走 checkWrite /
// checkWriteOwn，不受此限。）
//
// 返回给模型的正确指引；ok=false 表示不像实体，按普通工作区路径放行。
// selfName 用来区分「自己的定义域（有正解：改用根寻址）」与「别人的定义域」。
// 对别人的实体**不能**说「改用元工具」：create_agent / create_shared_skill 是 super
// （仅超级智能体），非超级智能体恒不可达（toolgrant.Catalog 不产出 + registry.go 执行层硬闸）。
func entityLikePath(rel, selfName string) (hint string, ok bool) {
	seg := strings.Split(rel, "/")
	if len(seg) < 2 || seg[1] == "" {
		return "", false
	}
	switch seg[0] {
	case paths.AgentsSubdir:
		if seg[1] == selfName {
			return fmt.Sprintf("this is how you address your own definition domain, but the leading \"/\" is missing, so it will not take effect; "+
				"to edit your own skills use /agents/%s/skills/<skill-name>/... (AGENT.md can only be changed through meta tools)", selfName), true
		}
		return "agent definitions cannot be created with file tools (and you do not have that permission anyway); to create or change another agent, ask an admin or the supervisor.", true
	case paths.TenantSkillsSubdir:
		return fmt.Sprintf("tenant-level shared skills cannot be changed here; for your own private skill use /agents/%s/skills/<skill-name>/...", selfName), true
	}
	return "", false
}

// parseRootAddr 只做路径规范化与「是否根寻址」的识别，**不含任何闸门**。
// 刻意把解析与判权拆开：写侧若复用读侧，会先撞上读闸、报出读口径的错误文案
// （读写白名单虽同形，闸门与措辞不同，见 checkReadOwn / checkWriteOwn）。
func parseRootAddr(c *Context, p string) (sp, rel string, fromRoot bool, err error) {
	rel, fromRoot, err = SafePathEx(p, true)
	if err != nil {
		return "", "", false, err
	}
	if !fromRoot {
		return c.StoragePath(rel), rel, false, nil
	}
	return c.TenantPath(rel), rel, true, nil
}

// isVersionDomain 路径是否落在版本域（租户根下的 versions/）。
//
// 版本域是「线上正在跑的那份定义」的历史快照：可能含已回滚掉的口令、策略与废弃技能
// 正文，模型读到就是历史泄露；写进去更等于无声改线上。所以它对**所有身份**关闭
// （包括超级智能体）。
func isVersionDomain(tenantRel string) bool {
	seg := strings.Split(tenantRel, "/")
	return len(seg) > 0 && seg[0] == paths.VersionsSubdir
}

// resolveRead 把工具入参路径解析为存储键（读类）。
// 根寻址按身份分流：超级智能体不受限（D4：读不受限）；非超级智能体可读自己的定义目录
// （agents/{self}/**，含 AGENT.md 与私有 skill）。
//
// 例外：版本域对谁都不可达。它不在 checkReadOwn 的 switch 里 —— 超级智能体的读压根
// 不过那一层（checkReadOwn 第一句就 return nil），所以这条必须挡在身份分流之前。
func resolveRead(c *Context, p string) (sp, rel string, fromRoot bool, err error) {
	sp, rel, fromRoot, err = parseRootAddr(c, p)
	if err != nil || !fromRoot {
		return sp, rel, fromRoot, err
	}
	if isVersionDomain(rel) {
		return "", "", false, fmt.Errorf("version directories are not exposed to file tools: %s"+
			" (historical versions are only for rollback and audit; read the body through read_skill / the entity's file API)", rel)
	}
	if err := checkReadOwn(c, rel); err != nil {
		return "", "", false, err
	}
	return sp, rel, true, nil
}

// resolveWrite 同 resolveRead，但写类工具额外过闸：
//  1. 根寻址（"/" 前缀）→ 超级智能体走 checkWrite（全租户 + 实体状态闸）；
//     非超级智能体走 checkWriteOwn（仅自己的私有 skill 目录 + 自身与 skill 双重状态闸）；
//  2. 工作区内仿造实体目录 → entityLikePath（仅非超级智能体）。严格度 B：只在目标**不存在**
//     （即「正在新建假实体」）时拒绝；已存在的按普通工作区文件放行，
//     既覆盖全部幻觉场景，又不误伤用户上传的同名目录。
func resolveWrite(ctx context.Context, c *Context, p string) (sp, rel string, fromRoot bool, err error) {
	sp, rel, fromRoot, err = parseRootAddr(c, p)
	if err != nil {
		return "", "", false, err
	}
	if fromRoot {
		if c.IsSupervisor {
			if err := checkWrite(ctx, c, rel); err != nil {
				return "", "", false, err
			}
		} else if err := checkWriteOwn(ctx, c, rel); err != nil {
			return "", "", false, err
		}
		return sp, rel, true, nil
	}
	if c.Storage != nil && !c.IsSupervisor {
		if hint, ok := entityLikePath(rel, c.AgentName); ok {
			exists, e := c.Storage.Exists(ctx, sp)
			if e != nil {
				return "", "", false, e
			}
			if !exists {
				return "", "", false, fmt.Errorf("path %q resolves inside your session workspace, so it will not take effect: %s", rel, hint)
			}
		}
	}
	return sp, rel, false, nil
}
