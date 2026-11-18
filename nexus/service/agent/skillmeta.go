package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	agents "flame/nexus/shared/repository/agents"
	"flame/nexus/shared/versioning"
	"flame/tools/xsql"
)

// ===== 正式运行的元数据（P1.5） =====
//
// 只冻正文不冻元数据，就会出现「改一句描述、不用发布」也改变模型看到的清单口径；
// 只冻展示不冻生效，则是「草稿里给技能加一条依赖，线上照样被点亮」。
//
// 这一份读取器是唯一实现：skillManager（常驻清单 / read_skill / use_skill）与
// toolGrantManager（grant_agent_tool 的自动 kind、read_agent 的 via_skill 提示）
// 都从它取。两边同源才不会出现「管理面说 use_skill 之后会亮、运行时永远不亮」。
// 草稿模式什么都不做 —— 编辑页要看到正在改的那份。

// skillMetaReader 读「当前版本」的技能元数据快照（私有技能按归属 agent 缓存一份）。
type skillMetaReader struct {
	schema    string
	agentID   string // 当前 agent uuid（判「是不是自己的私有技能」，省一次反查）
	agentName string // 当前 agent 名
	agents    agents.Repository
	mode      tools.DefinitionMode // current = 正式运行；零值 = 草稿

	// mu / private 正式运行时缓存「按归属 agent 的私有技能元数据快照」。
	// 一次 run 内不会有人发布新版本，所以缓存一次就够；nil 表示该归属者没有可用快照。
	mu      sync.Mutex
	private map[string]map[string]versioning.SkillMeta
}

// newSkillMetaReader 创建读取器（schema / 身份 / 模式在构造时固化）。
func newSkillMetaReader(schema, agentID, agentName string, repo agents.Repository, mode tools.DefinitionMode) *skillMetaReader {
	return &skillMetaReader{
		schema:    schema,
		agentID:   agentID,
		agentName: agentName,
		agents:    repo,
		mode:      mode,
	}
}

// versionMeta 取某技能在当前版本里的元数据快照（ownerName 为归属 agent 名，空 = 租户级）。
// 草稿模式、或该技能没有当前版本时返回 false —— 调用方保持库里的值。
func (r *skillMetaReader) versionMeta(s *models.Skill, ownerName string) (versioning.SkillMeta, bool) {
	if r == nil || !r.mode.IsCurrent() {
		return versioning.SkillMeta{}, false
	}
	if ownerName == "" {
		got, err := versioning.TenantSkillMeta(r.schema, []string{s.ID})
		if err != nil {
			return versioning.SkillMeta{}, false
		}
		meta, ok := got[s.ID]
		return meta, ok
	}
	return r.privateMetaFor(ownerName, s.Name)
}

// ownerMeta 按技能行自身的归属解析 ownerName 后再取快照。
// 跨归属反查失败（归属 agent 已不存在等）视为「没有版本信息」，由调用方回落。
func (r *skillMetaReader) ownerMeta(s *models.Skill) (versioning.SkillMeta, bool) {
	// 先判模式再反查归属：草稿模式下连一次库都不该查（编辑页高频路径）。
	if r == nil || !r.mode.IsCurrent() {
		return versioning.SkillMeta{}, false
	}
	ownerName, err := ownerAgentName(r.schema, r.agentID, r.agentName, string(s.AgentID), r.agents)
	if err != nil {
		return versioning.SkillMeta{}, false
	}
	return r.versionMeta(s, ownerName)
}

// depsOf 取某技能在当前版本里声明的工具依赖。
//
//	ok=false —— 没有版本信息（草稿模式 / 该技能还没发布）→ 调用方回落实时表；
//	ok=true  —— 权威结论：即便列表为空，也说明这一版确实没声明依赖，不得再查库。
func (r *skillMetaReader) depsOf(s models.Skill) ([]string, bool) {
	if r == nil || !r.mode.IsCurrent() {
		return nil, false
	}
	meta, ok := r.ownerMeta(&s)
	if !ok {
		return nil, false
	}
	return meta.ToolRefs(), true
}

// privateMetaFor 取某归属 agent 当前版本里某私有技能的元数据（按归属者缓存）。
func (r *skillMetaReader) privateMetaFor(ownerName, skillName string) (versioning.SkillMeta, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.private == nil {
		r.private = make(map[string]map[string]versioning.SkillMeta, 2)
	}
	snapshot, loaded := r.private[ownerName]
	if !loaded {
		// 失败（含未发布）记为 nil：本 run 内不反复查库，也不退回草稿
		snapshot, _ = versioning.AgentPrivateSkillMeta(r.schema, ownerName)
		r.private[ownerName] = snapshot
	}
	meta, ok := snapshot[skillName]
	return meta, ok
}

// runtimeMeta 批量把列表里的技能元数据换成当前版本（system prompt 常驻清单用）。
// 草稿模式原样返回 —— 编辑页与调试器要看到正在改的那份。
//
// 正式运行会**剔除当前版本里没有的技能**：清单必须与 /skill 挂载、read_skill / use_skill
// 同源。发布后新加的私有技能、从未发布过版本的租户级技能，正文与脚本都不在版本目录里，
// 留在提示词里只会诱导一次必然失败的读取。
func (r *skillMetaReader) runtimeMeta(list []models.Skill) []models.Skill {
	if r == nil || !r.mode.IsCurrent() || len(list) == 0 {
		return list
	}

	// 租户级：一次批量查当前版本；私有：按归属 agent 取快照（自身命中缓存）
	ids := make([]string, 0, len(list))
	for i := range list {
		if string(list[i].AgentID) == "" {
			ids = append(ids, list[i].ID)
		}
	}
	tenant := map[string]versioning.SkillMeta{}
	if len(ids) > 0 {
		if got, err := versioning.TenantSkillMeta(r.schema, ids); err == nil {
			tenant = got
		}
	}

	out := make([]models.Skill, 0, len(list))
	for i := range list {
		s := list[i]
		if string(s.AgentID) == "" {
			meta, ok := tenant[s.ID]
			if !ok {
				continue // 这一版里没有它 → 不进清单
			}
			applySkillMeta(&s, meta)
			out = append(out, s)
			continue
		}
		ownerName, err := ownerAgentName(r.schema, r.agentID, r.agentName, string(s.AgentID), r.agents)
		if err != nil {
			continue // 归属都解析不出：正式运行不认
		}
		meta, ok := r.privateMetaFor(ownerName, s.Name)
		if !ok {
			continue
		}
		applySkillMeta(&s, meta)
		out = append(out, s)
	}
	return out
}

// ownerAgentName 反查归属 agent 名：空 → 租户级（返回 ""）；自身 → 直接用当前名；其它 → 查库。
func ownerAgentName(schema, selfAgentID, selfAgentName, ownerAgentID string, repo agents.Repository) (string, error) {
	if ownerAgentID == "" {
		return "", nil
	}
	if ownerAgentID == selfAgentID {
		return selfAgentName, nil
	}
	inst, err := repo.GetAgent(&auth.AuthInfo{Schema: schema}, ownerAgentID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("skill owner agent %s not found", ownerAgentID)
		}
		return "", err
	}
	return inst.Name, nil
}

// applySkillMeta 把版本里的元数据盖到实体行上（只盖定义字段，不碰 id / status / 时间戳）。
func applySkillMeta(s *models.Skill, meta versioning.SkillMeta) {
	s.Description = xsql.String(meta.Description)
	if len(meta.Metadata) > 0 {
		s.Metadata = xsql.JSONB{Data: json.RawMessage(meta.Metadata)}
	}
	s.UserInvocable = xsql.Bool(meta.UserInvocable)
	s.CommandDispatch = xsql.String(meta.CommandDispatch)
	s.CommandTool = xsql.String(meta.CommandTool)
	s.Restricted = xsql.Bool(meta.Restricted)
}
