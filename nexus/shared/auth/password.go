package auth

import "flame/nexus/shared/account"

// 兼容层：密码哈希/校验与长度约束已挪到中立包 shared/account
// （平台侧 platform.users 也要用，但不该依赖 auth —— 那里面还有租户登录 handler）。
// 这里保留同名导出，现有调用点（login.go / admin/users / …）无需改动。
const (
	// MaxPasswordBytes bcrypt 可处理的明文上限（字节）
	MaxPasswordBytes = account.MaxPasswordBytes
	// MinPasswordLength 密码最小长度（与前端校验保持一致）
	MinPasswordLength = account.MinPasswordLength
)

var (
	// ErrPasswordTooLong 密码超过 bcrypt 上限
	ErrPasswordTooLong = account.ErrPasswordTooLong
	// HashPassword 生成密码哈希（bcrypt）
	HashPassword = account.HashPassword
	// VerifyPassword 校验明文密码；needRehash=true 表示命中旧 MD5，调用方应升级为 bcrypt
	VerifyPassword = account.VerifyPassword
)
