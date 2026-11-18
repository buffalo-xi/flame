// Package auth 平台侧身份：角色词表、token 签发/解析、两把中间件。
//
// 与租户侧 shared/auth 完全独立，靠 claims 形态天然互斥：
//
//	租户 token：{id, name, schema}                     —— 无 scope
//	平台 token：{sub, name, scope:"platform", role}    —— 无 schema
//
// 因此平台 token 打 /admin/* 会被租户侧 AuthMiddleware（parseClaims 强制要 schema）拒；
// 租户 token 打 /platform/* 会被本包 PlatformAuthMiddleware（强制要 scope）拒。
package auth

import (
	"errors"

	"flame/nexus/shared/config"
	"flame/tools/xjwt"
)

const (
	// ScopePlatform 平台 token 的 scope 值。租户 token 不含该字段。
	ScopePlatform = "platform"

	// RoleAdmin 管理员：管理所有租户与平台用户。
	RoleAdmin = "admin"
	// RoleUser 用户：只能看/管自己名下的租户。
	RoleUser = "user"
)

// PlatformAuthInfo 平台侧认证信息（由 token 解析而来）。
type PlatformAuthInfo struct {
	ID   string
	Name string
	Role string // admin | user
}

// IsAdmin 是否平台管理员。
func (p *PlatformAuthInfo) IsAdmin() bool { return p != nil && p.Role == RoleAdmin }

// jwtConfig 与租户侧共用同一套 JWT 参数（同进程、同密钥、同有效期）。
func jwtConfig() xjwt.JWTConfig { return xjwt.JWTConfig(config.Cfg.JWT) }

// GenerateToken 签发平台 token。
//
// 刻意不带 schema：见包注释，这让平台 token 天然进不了租户接口。
// name 仅供展示与审计，前端不依赖它判定权限（权限只看 role）。
func GenerateToken(id, name, role string) (string, error) {
	return xjwt.GenerateTokenWithMapClaims(map[string]any{
		"sub":   id,
		"name":  name,
		"scope": ScopePlatform,
		"role":  role,
	}, jwtConfig())
}

// ParseToken 校验并解析平台 token；任何一项不合规都返回错误（fail-closed）。
func ParseToken(tokenString string) (*PlatformAuthInfo, error) {
	claims, err := xjwt.ValidateTokenWithMapClaims(tokenString, jwtConfig())
	if err != nil {
		return nil, err
	}

	scope, _ := claims["scope"].(string)
	if scope != ScopePlatform {
		return nil, errors.New("not a platform token")
	}
	id, _ := claims["sub"].(string)
	if id == "" {
		return nil, errors.New("platform token missing sub")
	}
	name, _ := claims["name"].(string)
	role, _ := claims["role"].(string)
	if role != RoleAdmin && role != RoleUser {
		return nil, errors.New("platform token has invalid role")
	}

	return &PlatformAuthInfo{ID: id, Name: name, Role: role}, nil
}
