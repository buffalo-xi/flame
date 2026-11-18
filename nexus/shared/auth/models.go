package auth

import "flame/nexus/shared/account"

// 用户角色（对应 <schema>.users.role）：
// 只决定「能不能登录控制台 / 访问后台接口」，与服务凭证无关（凭证不限制角色）。
const (
	RoleManage  = "manage"  // 管理用户：可登录
	RoleService = "service" // 服务用户：不可登录（仅作为 API 调用身份）
)

// 用户状态（对应 <schema>.users.status）：
// 账号是否可用 —— 登录与 API 调用两侧都看它。
// 词表真源在中立包 shared/account（平台用户 platform.users.status 同用一套），此处仅转发。
const (
	UserStatusActive   = account.StatusActive
	UserStatusDisabled = account.StatusDisabled
	UserStatusLocked   = account.StatusLocked
)

// AuthInfo 认证信息结构体
//
// 注意：这里刻意不带 role —— 角色只在「登录」这一关判定（login.go），
// 且当前 token 的唯一签发点就是 /login，因此后台接口无需再做角色判定。
// 服务侧（MCP/RPC）的 AuthInfo 由绑定解析而来，ID 即绑定用户，同样不需要角色。
type AuthInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Schema string `json:"schema"`

	// RestrictTargets / TargetAllow 只对服务侧（MCP / RPC）的绑定身份有意义：
	// 限制这条凭证能调用哪些**端点目标**（agent / flow）。
	//
	//   RestrictTargets=false          → 不限制（需显式关闭；存量绑定在迁移时保持 false）
	//   RestrictTargets=true, 空集合   → 一个都不允许（新建绑定的初始态）
	//   RestrictTargets=true, 有集合   → 只允许列出的目标
	//
	// 授权按目标而不是端点：同一个 agent 通常有 rpc / mcp 两条端点，
	// 按端点授权会出现「只勾了 rpc，MCP 连接就全空」以及「端点重发后授权失效」。
	//
	// json:"-"：这两个字段是运行期策略，不该出现在接口响应或被序列化进 token。
	RestrictTargets bool                `json:"-"`
	TargetAllow     map[string]struct{} `json:"-"`

	// ExcludeSupervisor 是「不限制」的中间档：RestrictTargets=false 时，放行全部端点但排除
	// 超级智能体的端点（supervisor 的端点等于对外开放租户管理能力）。
	// RestrictTargets=true 时本字段被忽略 —— 白名单已经精确表达范围。
	ExcludeSupervisor bool `json:"-"`
}

// TargetAllowed 判断某个端点目标是否在允许范围内。
//
// nil 接收者 / 未启用限制时恒为 true —— 调用方（rpc / mcp 的装配层）不需要自己判空。
func (a *AuthInfo) TargetAllowed(targetType, targetID string) bool {
	if a == nil || !a.RestrictTargets {
		return true
	}
	_, ok := a.TargetAllow[targetType+":"+targetID]
	return ok
}

// TargetAllowedWith 在 TargetAllowed 之上再叠一层「排除超级智能体」。
//
// supervisor 由调用方从端点行带出（models.SvcEndpoint.TargetSupervisor），这里不查库：
// rpc 的 findEndpoint 在每次工具调用上都要判一次，回查 agents 会把热路径翻倍。
func (a *AuthInfo) TargetAllowedWith(targetType, targetID string, supervisor bool) bool {
	if a == nil {
		return true
	}
	if !a.TargetAllowed(targetType, targetID) {
		return false
	}
	// 白名单档已经精确表达范围，排除档不参与判定（即使库里被人手工写成了两列都为 true）
	if a.RestrictTargets {
		return true
	}
	return !(a.ExcludeSupervisor && targetType == "agent" && supervisor)
}
