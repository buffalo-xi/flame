package repository

import (
	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
)

// ListAgentsRequest 智能体列表请求参数
type ListAgentsRequest struct {
	Page      int      `json:"page" form:"page"`
	Limit     int      `json:"limit" form:"limit"`
	Name      string   `json:"name" form:"name"`
	SortBy    []string `json:"sort_by" form:"sort_by"`
	SortOrder []string `json:"sort_order" form:"sort_order"`
}

// Repository 智能体存储接口
type Repository interface {
	// ListAgents 获取所有智能体
	ListAgents(authInfo *auth.AuthInfo, req *ListAgentsRequest) ([]models.Agent, int, error)

	// GetAgent 根据ID获取智能体
	GetAgent(authInfo *auth.AuthInfo, id string) (*models.Agent, error)
	// FindByName 按名字取智能体（同租户内唯一）；不存在返回 (nil, nil)。
	// 与 ExistsName 的区别：覆盖式导入（资产包/市场安装）要拿既有行的 id 与身份开关，
	// 只判存在性拿不到那些东西。
	FindByName(authInfo *auth.AuthInfo, name string) (*models.Agent, error)
	// ExistsName 判断同租户下是否已存在同名 agent（excludeID 非空时忽略自身，用于改名重名校验）
	ExistsName(authInfo *auth.AuthInfo, name string, excludeID string) (bool, error)
	// CreateAgent 创建智能体。creatorAgentID 非空时，在同一事务内把新 agent
	// 授权给创建者（agent_relations），保证"谁建的谁能调"。
	CreateAgent(authInfo *auth.AuthInfo, agent *models.Agent, creatorAgentID string) (string, error)
	// UpdateAgent 更新智能体
	UpdateAgent(authInfo *auth.AuthInfo, id string, agent *models.Agent) error
	// DeleteAgent 删除智能体；同一事务内清理其授权关联与私有 skill 记录
	// （skills.agent_id 没有外键，不能指望 DB 级联）。
	DeleteAgent(authInfo *auth.AuthInfo, id string) error
	// UpdateStatus 仅更新 status（1=active 启用 | 2=disabled 停用）
	UpdateStatus(authInfo *auth.AuthInfo, id string, status int) error
	// GetUnrestricted 读取 agent 的「不限制工具」开关（agents.tools_unrestricted；
	// agent 不存在返回 false, nil）。
	GetUnrestricted(authInfo *auth.AuthInfo, agentID string) (bool, error)
	// SetUnrestricted 设置 agent 的「不限制工具」开关（单列 UPDATE，不触碰其它字段）。
	SetUnrestricted(authInfo *auth.AuthInfo, agentID string, unrestricted bool) error
	// SetSupervisor 设置 agent 的超级权限属性（单列 UPDATE）。
	// 降级时会拒绝对「最后一个 supervisor」操作（否则租户永久失去管理面）。
	SetSupervisor(authInfo *auth.AuthInfo, agentID string, isSupervisor bool) error
}
