package auth

// BootstrapAdminName 平台**受保护管理员**的登录名：由环境初始数据预置的那个 active 管理员。
//
// **固定为 plat，不可配置** —— 与租户侧的 shared/tenants.PrimaryUserName 对称：两者都是
// "系统预置的那个账号"，因此管理台都禁止修改（资料 / 角色 / 状态）与删除
// （守卫在 handler/users.go 的 isProtectedPlatformUser；删除接口本就不存在）。
//
// 放 auth 包的原因：它属于平台身份词表（与角色词表同处），handler 层直接引用，
// 且不该反向依赖 handler。
const BootstrapAdminName = "plat"
