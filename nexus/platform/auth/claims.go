// Package auth holds platform-side identity: the role vocabulary, token issuing/parsing and the two middlewares.
//
// It is fully independent of the tenant-side shared/auth, and the claim shapes are mutually exclusive by nature:
//
//	tenant token: {id, name, schema}                    - no scope
//	platform token: {sub, name, scope:"platform", role} - no schema
//
// So a platform token sent to /admin/* is rejected by the tenant-side AuthMiddleware (parseClaims requires schema), and a
// tenant token sent to /platform/* is rejected by this package's PlatformAuthMiddleware (which requires scope).
package auth

import (
	"errors"

	"flame/nexus/shared/config"
	"flame/tools/xjwt"
)

const (
	// ScopePlatform is the scope value of a platform token. Tenant tokens do not carry the field.
	ScopePlatform = "platform"

	// RoleAdmin: administrator; manages all tenants and platform users.
	RoleAdmin = "admin"
	// RoleUser: user; can only see and manage the tenants under their own name.
	RoleUser = "user"
)

// PlatformAuthInfo is platform-side authentication info (parsed from the token).
type PlatformAuthInfo struct {
	ID   string
	Name string
	Role string // admin | user
}

// IsAdmin reports whether this is a platform administrator.
func (p *PlatformAuthInfo) IsAdmin() bool { return p != nil && p.Role == RoleAdmin }

// jwtConfig shares the tenant side's JWT parameters (same process, same key, same expiry).
func jwtConfig() xjwt.JWTConfig { return xjwt.JWTConfig(config.Cfg.JWT) }

// GenerateToken issues a platform token.
//
// It deliberately carries no schema: see the package comment - that is what keeps a platform token out of tenant endpoints.
// name is for display and audit only; the frontend does not use it for authorisation (that looks at role alone).
func GenerateToken(id, name, role string) (string, error) {
	return xjwt.GenerateTokenWithMapClaims(map[string]any{
		"sub":   id,
		"name":  name,
		"scope": ScopePlatform,
		"role":  role,
	}, jwtConfig())
}

// ParseToken validates and parses a platform token; any non-compliant item returns an error (fail-closed).
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
