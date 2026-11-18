// Package skills 提供 skills 表的数据访问。
// skill 的元数据与归属（agent_id）存库，内容存文件（路径由 skill name 与归属推导）。
package skills

import "flame/nexus/shared/models"

// Repository skills 数据访问接口。
type Repository interface {
	// Create 新建 skill（ID 为空时自动生成），返回 id。
	Create(schema string, s *models.Skill) (string, error)
	// Update 按 id 更新 name/description/metadata/status/updated_by，
	// 以及 user_invocable/command_dispatch/command_tool。
	Update(schema string, s *models.Skill) error
	// Delete 按 id 删除。
	Delete(schema, id string) error
	// Get 按 id 获取；不存在返回 (nil, nil)。
	Get(schema, id string) (*models.Skill, error)
	// FindVisibleByName 在可见范围内按 name 查：优先私有（agent_id=agentID），
	// 其次租户级（agent_id IS NULL）。不存在返回 (nil, nil)。
	FindVisibleByName(schema, name, agentID string) (*models.Skill, error)
	// ListVisible 列出可见 skill（租户级 + 私有，status<>3 即不含废弃），
	// 按 name 排序。租户级 skill 受 agent_skill_bindings 收窄（见 visibleCond）。
	ListVisible(schema, agentID string) ([]models.Skill, error)
	// ListInjected 列出「注入给该 agent」的 skill：其私有 skill + 租户级中被它绑定的 skill。
	// 与 ListVisible 的区别：全员可见（restricted=false）的租户级 skill 不自动注入，
	// 只有显式绑定（agent_skill_bindings）才算「这个 agent 用这个 skill」。
	ListInjected(schema, agentID string) ([]models.Skill, error)
	// FindInjectedByName 在「注入范围」内按 name 查（口径同 ListInjected）。
	FindInjectedByName(schema, name, agentID string) (*models.Skill, error)
	// ListForSupervisor 列出「超级智能体全可见」的 skill：其私有 skill ∪ **全部租户级** skill（status<>3），
	// 忽略 restricted / 白名单。超级智能体是 skill 的管理者，需能枚举全量（否则能改却看不见）。
	ListForSupervisor(schema, agentID string) ([]models.Skill, error)
	// FindForSupervisorByName 超级智能体按 name 查：优先其私有，其次任意租户级（忽略白名单）；
	// 不存在返回 (nil, nil)。
	FindForSupervisorByName(schema, name, agentID string) (*models.Skill, error)
	// ListBindingNamesBySkills 批量返回若干 skill 的白名单 agent 名（按 skill_id 归组；未出现的为空）。
	ListBindingNamesBySkills(schema string, skillIDs []string) (map[string][]string, error)
	// GetAgentSkillDiscovery 读取「可发现未绑定的技能」开关（agent 不存在返回 false, nil）。
	GetAgentSkillDiscovery(schema, agentID string) (bool, error)
	// SetAgentSkillDiscovery 设置「可发现未绑定的技能」开关。
	SetAgentSkillDiscovery(schema, agentID string, discovery bool) error
	// List 列出 skill（管理端：分页 + 过滤），返回列表与总数。
	List(schema string, req *ListRequest) ([]models.Skill, int, error)
	// FindByName 在指定归属范围内按 name 查（全状态，用于重名检查）。
	// agentID 为空表示租户级（agent_id IS NULL）。不存在返回 (nil, nil)。
	FindByName(schema, name, agentID string) (*models.Skill, error)
	// UpdateStatus 仅更新 status（启用/停用）。
	UpdateStatus(schema, id string, status int) error
	// DeleteByAgentID 删除某个 agent 的全部私有 skill 记录（agent 删除时级联清理）。
	DeleteByAgentID(schema, agentID string) error

	// ListBindings 列出某 skill 的授权 agent（含 agent_name），按 agent 名排序。
	ListBindings(schema, skillID string) ([]models.AgentSkillBinding, error)
	// SetVisibility 设置可见性：restricted 开关 + 授权 agent 白名单（事务内全量替换）。
	// restricted=false 表示全员可见；true 表示仅白名单内 agent 可见。
	SetVisibility(schema, skillID string, restricted bool, agentIDs []string, operatorID string) error
	// SetRestricted 只改 skills.restricted 一列（不碰白名单 —— 白名单由 bind / unbind 维护）。
	//
	// 注意**不要**用 SetVisibility 代劳：它会在事务里 DELETE 整张 agent_skill_bindings 再重建，
	// 把 bind_skill 建的白名单全清掉。
	SetRestricted(schema, skillID string, restricted bool, operatorID string) error
	// ListBindingCounts 返回指定 skill 集合的授权 agent 数量（管理端列表展示用；未出现的即为 0）。
	ListBindingCounts(schema string, skillIDs []string) (map[string]int, error)

	// ListSkillIDsByAgent 列出某 agent 绑定的 skill id（agent 侧「可使用的技能」配置用），按 skill 名排序。
	ListSkillIDsByAgent(schema, agentID string) ([]string, error)
	// ReplaceByAgent 全量替换某 agent 的 skill 绑定（同一张 agent_skill_bindings 的 agent 侧视角）。
	// 只接受「已存在的租户级 skill」（私有 skill 天然只对归属 agent 可见，绑定无意义）；
	// 只动该 agent 的行，不影响同一 skill 对其他 agent 的授权。
	ReplaceByAgent(schema, agentID string, skillIDs []string, operatorID string) error
	// AddBinding 幂等追加一条 skill 绑定；只接受「已存在的租户级 skill」。
	// 返回是否真的新增（false = skill 不存在 / 非租户级 / 已绑定）。
	AddBinding(schema, agentID, skillID, operatorID string) (bool, error)
	// RemoveBinding 幂等移除一条 skill 绑定；返回是否真的移除。
	RemoveBinding(schema, agentID, skillID string) (bool, error)
	// ListBoundSkillNames 列出某 agent 绑定的 skill 名（按名排序，只含仍存在的 skill）。
	ListBoundSkillNames(schema, agentID string) ([]string, error)
	// ListPrivateNames 列出某 agent **私有** skill 的名字（按名排序，不含废弃）。
	ListPrivateNames(schema, agentID string) ([]string, error)
}

// ListRequest skill 列表查询条件（管理端）。
type ListRequest struct {
	Page    int
	Limit   int
	AgentID string // 非空：只看该 agent 的私有 skill；为空：全部
	Status  *int   // nil：不限状态
	Keyword string // name / description 模糊匹配
}

// NewRepository 创建 Repository（PostgreSQL 实现）。
func NewRepository() Repository {
	return &PostgresRepository{}
}
