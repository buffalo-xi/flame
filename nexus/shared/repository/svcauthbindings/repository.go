package svcauthbindings

import "flame/nexus/shared/models"

// BindingIdentity 服务凭证命中的身份。
// 绑定只提供服务调用所需的身份（审计用），不限制角色，因此这里只带出账号状态。
type BindingIdentity struct {
	UserID string
	Status string // users.status（用户行缺失时为空串）

	// 该凭证的端点访问范围（对应 svc_auth_bindings.restrict_targets）。
	// 新建绑定默认 true（安全默认），此时只允许 TargetAllow 里的目标，空集合表示一个都不允许；
	// false = 不限制，需显式关闭（存量绑定在迁移时保持 false）。
	// 元素形如 "agent:<uuid>" / "flow:<uuid>"。
	RestrictTargets bool
	TargetAllow     []string

	// ExcludeSupervisor 仅在 RestrictTargets=false 时生效：放行全部端点，但排除超级智能体。
	// RestrictTargets=true 时忽略（白名单已经精确表达）。
	ExcludeSupervisor bool
}

// Repository 服务端认证身份绑定存储接口（MCP/RPC 共用）。
type Repository interface {
	// ResolveBindingIdentity 按 (authType, issuer, subject) 解析绑定身份；
	// 仅命中 is_active = true 的绑定，未命中返回 (nil, nil)。
	ResolveBindingIdentity(schema, authType, issuer, subject string) (*BindingIdentity, error)
	// List 列出绑定（authType 非空时按类型过滤），created_at 倒序。
	List(schema, authType string) ([]models.SvcAuthBinding, error)
	// GetByID 按 id 取绑定；不存在返回 (nil, nil)。
	GetByID(schema, id string) (*models.SvcAuthBinding, error)
	// Create 新建绑定（id 为空时自动生成）。
	Create(schema string, b *models.SvcAuthBinding) error
	// Update 更新绑定（external_subject / user_id / is_active）。
	Update(schema string, b *models.SvcAuthBinding) error
	// Delete 删除绑定。
	Delete(schema, id string) error
	// GetTargets 读取某绑定的端点访问范围（restrict 开关 + 排除超级智能体 + 白名单；
	// 不存在返回 sql.ErrNoRows）。
	GetTargets(schema, bindingID string) (restrict, excludeSupervisor bool, targets []BindingTarget, err error)
	// SetTargets 全量替换某绑定的端点访问范围（restrict=true 且列表为空 = 一个都不允许）。
	// restrict=true 时 excludeSupervisor 会被强制写成 false —— 两档互斥，白名单已经精确表达。
	SetTargets(schema, bindingID string, restrict, excludeSupervisor bool, targets []BindingTarget) error
	// ListTargetCandidates 列出可授权的候选目标：有对外开放端点的 agent / flow。
	ListTargetCandidates(schema string) ([]BindingTarget, error)
}
