// Package secureval handles the "encryption at rest of sensitive config values".
//
// It has exactly one job: turn a string into ciphertext that can be stored, and restore it verbatim. What
// should be encrypted is the caller's decision (currently the value of kind=secret rows in
// {schema}.global_vars); this package knows nothing about any table schema.
//
// Ciphertext format: enc:v1:<base64url(nonce||ciphertext||tag)>
//   - the prefix is what lets existing plaintext keep working: anything without a prefix is returned as is.
//     That way going live needs no one-off data migration, and a forgotten tenant cannot fail to read.
//   - AES-256-GCM: integrity is built in, so a rewritten ciphertext fails to decrypt instead of returning
//     garbage.
//   - v1 is the format version: a future algorithm change (KMS, say) can coexist and be routed by prefix.
package secureval

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/config"
)

// The ciphertext markers. Changing one is equivalent to making every already-encrypted value "have no prefix"
// and therefore be read as plaintext, so they are only ever added: a new algorithm gets a new prefix.
//
// The two prefixes are the two modes, differing only in where the nonce comes from:
//   - plainPrefix    random nonce, so encrypting the same plaintext twice gives different results. The default.
//   - deterministic  the nonce is derived from the key and the plaintext, so the same plaintext always yields
//     the same ciphertext.
//
// Why the deterministic mode exists: flows.env_vars is one jsonb block, and "did the version change" is decided
// in SQL by comparing the flows row with the flow_versions row using IS DISTINCT FROM. With a random nonce, a
// user who only edits the description and saves again produces a re-encrypted env_vars → a flow with unchanged
// variables is judged to have "unreleased changes". Determinism guarantees "plaintext unchanged → ciphertext
// unchanged".
// The cost is that equality becomes visible (two flows holding the same key show the same ciphertext). That is
// acceptable: whoever can read the database can already see these rows, and it does not yield the key itself.
const (
	plainPrefix = "enc:v1:"
	detPrefix   = "enc:d1:"
)

// keyInfo is derivation context that separates different uses of one root key: if something else ever derives
// a key from jwt.secret_key, it will not end up with the same key.
const keyInfo = "flame/nexus/secureval/v1"

// IsEncrypted reports whether a stored value is already ciphertext (either mode).
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, plainPrefix) || strings.HasPrefix(stored, detPrefix)
}

// Encrypt encrypts a value.
//
// An empty string is not encrypted (returned as is): there is nothing to protect, and many call sites use
// `value == ""` to mean "not set yet", which producing a ciphertext would break.
// An input that is already ciphertext is returned as is, so it is not encrypted twice (which would make the
// next decryption yield ciphertext).
func Encrypt(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}

	aead, err := newAEAD()
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate the nonce: %w", err)
	}

	// Seal appends the ciphertext after the nonce, so one string carries everything decryption needs and no
	// separate column is required.
	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return plainPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// EncryptDeterministic encrypts deterministically: the same plaintext and key always yield the same ciphertext.
//
// Used only where "a change of ciphertext is itself meaningful" (see the detPrefix comment; today that is the
// version comparison of flows.env_vars). Everywhere else use Encrypt — a random nonce does not reveal that two
// rows hold the same value.
//
// The nonce is derived as HMAC(key, "nonce|" + plaintext): different plaintexts give different nonces
// (collision probability negligible), and identical plaintext gives the identical nonce with identical
// plaintext — which is no nonce reuse in the GCM sense.
func EncryptDeterministic(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}

	key, err := deriveKey()
	if err != nil {
		return "", err
	}
	aead, err := aeadFor(key)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("nonce|"))
	mac.Write([]byte(plain))
	copy(nonce, mac.Sum(nil)) // Sum is 32 bytes, NonceSize is 12

	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return detPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts a value.
//
// An input without a prefix is returned as is: it is plaintext written before encryption shipped, or a field
// that never needed it. Do **not** treat that as an error — otherwise every historical secret fails to read the
// moment this goes live.
func Decrypt(stored string) (string, error) {
	// The two modes differ only in where the nonce comes from; the layout is identical (nonce || ct || tag), so
	// there is one decryption path: strip the marker and go.
	var body string
	switch {
	case strings.HasPrefix(stored, plainPrefix):
		body = strings.TrimPrefix(stored, plainPrefix)
	case strings.HasPrefix(stored, detPrefix):
		body = strings.TrimPrefix(stored, detPrefix)
	default:
		return stored, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("failed to base64-decode the ciphertext: %w", err)
	}

	aead, err := newAEAD()
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("the ciphertext is too short (the nonce is missing)")
	}

	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		// Only two things cause this: the key changed, or the ciphertext was modified.
		// No degrading to the empty string — a silently empty key is far harder to trace than an outright
		// failure.
		return "", errors.New("failed to decrypt: the key differs from the one used to write it, or the ciphertext is corrupted")
	}
	return string(plain), nil
}

// newAEAD derives a key from the current config each time. There is deliberately no in-process cache: the
// derivation is one HMAC-SHA256 (microseconds), while a cache would make "the config changed but the process
// still holds the old key" much harder to explain.
func newAEAD() (cipher.AEAD, error) {
	key, err := deriveKey()
	if err != nil {
		return nil, err
	}
	return aeadFor(key)
}

func aeadFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to initialise AES: %w", err)
	}
	return cipher.NewGCM(block)
}

// deriveKey derives a 32-byte key from the config.
//
// security.value_key wins (an independent key, so rotating jwt.secret_key does not invalidate already encrypted
// data); when it is not configured it falls back to jwt.secret_key, which is a random value validated at
// startup and therefore works out of the box — the price being that changing jwt.secret_key makes existing
// encrypted values unreadable (the config comments say so).
func deriveKey() ([]byte, error) {
	if config.Cfg == nil {
		return nil, errors.New("the config is not loaded, so the encryption key cannot be derived")
	}

	source := strings.TrimSpace(config.Cfg.Security.ValueKey)
	if source == "" {
		source = strings.TrimSpace(config.Cfg.JWT.SecretKey)
		if source == "" {
			return nil, errors.New("no encryption key available: configure security.value_key (or jwt.secret_key)")
		}
	}

	mac := hmac.New(sha256.New, []byte(source))
	mac.Write([]byte(keyInfo))
	return mac.Sum(nil), nil
}
