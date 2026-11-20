// Package account holds the account primitives: password hashing / verification plus the account status vocabulary.
//
// A neutral package shared by both sides:
//   - tenant users: <schema>.users (shared/auth re-exports it through an alias, see auth/password.go)
//   - platform users: platform.users (nexus_platform references it directly)
//
// Why it was extracted from shared/auth: that package also holds the tenant login handler (login.go, which depends on gin),
// while the platform side only needs "hash / verify / the status vocabulary" and should not drag tenant login logic into
// its dependency graph for that.
//
// Password storage policy (identical to the original implementation, semantics unchanged):
//   - every new write (create user / reset password / set a password while changing the role) uses bcrypt:
//     it carries its own salt and algorithm/cost marker (of the form `$2a$10$...`), and the comparison is constant time.
//   - legacy MD5 (32 lowercase hex characters, unsalted) stays readable: once the verification passes the caller upgrades it
//     to bcrypt transparently (see the needRehash return value of VerifyPassword).
//   - any other format is refused outright, never guessed at.
//
// Note: bcrypt only uses the first 72 bytes of the plaintext, so an over-long password is refused outright, avoiding the
// ambiguity of "identical in the first 72 bytes means it passes".
package account

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MaxPasswordBytes is the plaintext limit bcrypt can handle (bytes)
	MaxPasswordBytes = 72
	// MinPasswordLength is the minimum password length (kept in step with the frontend check)
	MinPasswordLength = 8
)

// Account status vocabulary: shared by tenant users (<schema>.users.status) and platform users
// (platform.users.status).
// Meaning: whether the account is usable — both login and API calls look at it.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusLocked   = "locked"
)

// ErrPasswordTooLong means the password exceeds the bcrypt limit.
var ErrPasswordTooLong = errors.New("password too long (at most 72 bytes)")

// HashPassword produces a password hash (bcrypt).
func HashPassword(plain string) (string, error) {
	if len(plain) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword checks a plaintext password against a stored value.
// needRehash = true means "the verification passed but the stored value is still in the old (MD5) format", and the caller
// should re-hash it and write it back.
func VerifyPassword(stored, plain string) (ok bool, needRehash bool) {
	if stored == "" {
		return false, false
	}

	// bcrypt: $2a$ / $2b$ / $2y$
	if strings.HasPrefix(stored, "$2") {
		if len(plain) > MaxPasswordBytes {
			return false, false
		}
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil, false
	}

	// Old format: 32 hex characters = md5(plaintext)
	if len(stored) == 32 && isHex(stored) {
		sum := md5.Sum([]byte(plain))
		want := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(stored))) == 1 {
			return true, true
		}
		return false, false
	}

	// Any other format: refused
	return false, false
}

// isHex reports whether the string consists only of hexadecimal characters.
func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
