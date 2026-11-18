// Package toolgrants 提供工具授权体系的数据访问：
//   - 工具组（tool_groups / tool_group_items）
//   - agent 工具授权（agent_tool_grants）
//   - skill 工具依赖（skill_tool_grants）
//
// 语义约定：
//   - grant_kind = required（必须导入，恒注入）/ allow（可使用）/ deny（显式拒绝，优先于 allow）
//   - 同一 ref 的 allow 与 deny 互斥，写入时由上层保证（设置 allow 即删除同 ref 的 deny，反之亦然）
//   - 工具组成员只能是天然组或单个工具，不能引用其它工具组（DB 层 CHECK 兜底）
package toolgrants

import "flame/nexus/shared/models"

// Repository 工具授权仓储接口（按租户 schema 操作）。
type Repository interface {
	// ===== 自定义工具组 =====
	// ListGroups 列出全部自定义组（含成员）。
	ListGroups(schema string) ([]models.ToolGroup, error)
	// GetGroup 按名字取自定义组（含成员）；不存在返回 (nil, nil)。
	GetGroup(schema, name string) (*models.ToolGroup, error)
	// UpsertGroup 按名字创建/更新自定义组并全量替换成员（事务），返回组 id。
	UpsertGroup(schema, name, description, userID string, items []models.ToolGroupItem) (string, error)
	// DeleteGroup 按名字删除自定义组（成员级联删除）。
	DeleteGroup(schema, name string) error

	// ===== agent 工具授权 =====
	// ListAgentGrants 列出某 agent 的全部授权项。
	ListAgentGrants(schema, agentID string) ([]models.AgentToolGrant, error)
	// ReplaceAgentGrants 全量替换某 agent 的授权项（事务）。
	ReplaceAgentGrants(schema, agentID, userID string, grants []models.AgentToolGrant) error
	// UpsertAgentGrant 加/改某 agent 的一条授权（事务：先按 ref 删旧行，再插入）。
	// 同 ref 换 kind 即覆盖 —— 天然保证一个 ref 只有一行（allow 与 required 不共存）。
	UpsertAgentGrant(schema, agentID, userID string, g models.AgentToolGrant) error
	// DeleteAgentGrant 精确移除某 agent 的某条授权，返回删除行数（幂等报告用）。
	DeleteAgentGrant(schema, agentID, ref string) (int64, error)

	// ===== skill 工具依赖 =====
	// ListSkillGrants 列出某 skill 声明的工具依赖。
	ListSkillGrants(schema, skillID string) ([]models.SkillToolGrant, error)
	// ReplaceSkillGrants 全量替换某 skill 的工具依赖（事务）。
	ReplaceSkillGrants(schema, skillID string, grants []models.SkillToolGrant) error
	// AddSkillGrant 追加一条依赖（ON CONFLICT DO NOTHING）。返回是否真的插入（幂等）。
	AddSkillGrant(schema, skillID string, g models.SkillToolGrant) (bool, error)
	// RemoveSkillGrant 按 ref 移除一条依赖（跨 item_type），返回删除行数。
	// 刻意不校验 ref 是否存在 —— 工具/连接被删后留下的悬空依赖必须还能清掉。
	RemoveSkillGrant(schema, skillID, ref string) (int64, error)

	// ===== 反查（删除连接 / 端点 / 自定义组前的引用检查）=====
	// CountRefUsages 统计 ref 在 agent 授权、skill 依赖、自定义组成员中的引用次数。
	CountRefUsages(schema, ref string) (int, error)
	// CountRefPrefixUsages 统计 ref 及其子项（ref:xxx）的引用次数，
	// 例如删除连接 fs 时统计 "mcp:fs" 与 "mcp:fs:*" 两类引用。
	CountRefPrefixUsages(schema, ref string) (int, error)
	// ListRefUsages 返回 ref 的引用明细（管理端「被引用 N 处」用）。
	// prefix=true 时同时匹配子项（如 ref=mcp:fs 也统计 mcp:fs:read_file）。
	ListRefUsages(schema, ref string, prefix bool) (*RefUsages, error)
}

// RefUsageAgent 一条 agent 授权引用。
type RefUsageAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GrantKind string `json:"grant_kind"` // required | allow | deny
	Ref       string `json:"ref"`        // 实际命中的 ref（前缀查询时可能是子项）
}

// RefUsageSkill 一条 skill 依赖引用。
type RefUsageSkill struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// RefUsageGroup 一条自定义组成员引用。
type RefUsageGroup struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// RefUsages ref 的引用明细：谁在用（agent 授权 / skill 依赖 / 自定义组成员）。
type RefUsages struct {
	Ref    string          `json:"ref"`
	Prefix bool            `json:"prefix"`
	Total  int             `json:"total"`
	Agents []RefUsageAgent `json:"agents"`
	Skills []RefUsageSkill `json:"skills"`
	Groups []RefUsageGroup `json:"groups"`
}
