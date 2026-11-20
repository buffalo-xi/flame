package auth

import "flame/nexus/shared/account"

// Compatibility layer: password hashing / verification and the length constraints moved to the neutral package
// shared/account (the platform side needs them for platform.users too, but should not depend on auth — that package also
// holds the tenant login handler). The same names are re-exported here, so existing call sites (login.go / admin/users /
// ...) need no change.
const (
	// MaxPasswordBytes is the plaintext limit bcrypt can handle (bytes).
	MaxPasswordBytes = account.MaxPasswordBytes
	// MinPasswordLength is the minimum password length (kept in step with the frontend check).
	MinPasswordLength = account.MinPasswordLength
)

var (
	// ErrPasswordTooLong means the password exceeds the bcrypt limit.
	ErrPasswordTooLong = account.ErrPasswordTooLong
	// HashPassword produces a password hash (bcrypt).
	HashPassword = account.HashPassword
	// VerifyPassword checks a plaintext password; needRehash=true means the old MD5 matched and the caller should upgrade
	// it to bcrypt.
	VerifyPassword = account.VerifyPassword
)
