package secureval

import (
	"strings"
	"testing"

	"flame/nexus/shared/config"
)

const testKey = "test-value-key-0123456789-0123456789-abcdef"

// withConfig swaps in the given configuration for the duration of one test and restores it afterwards (config.Cfg is a
// package-level global).
func withConfig(t *testing.T, valueKey, jwtKey string) {
	t.Helper()
	prev := config.Cfg
	config.Cfg = &config.Config{
		JWT:      config.JWTConfig{SecretKey: jwtKey},
		Security: config.SecurityConfig{ValueKey: valueKey},
	}
	t.Cleanup(func() { config.Cfg = prev })
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	withConfig(t, testKey, "")

	plain := "sk-live-abcdefghijklmnop"
	sealed, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if !IsEncrypted(sealed) || !strings.HasPrefix(sealed, "enc:v1:") {
		t.Fatalf("the ciphertext should carry the enc:v1: prefix, got %q", sealed)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("the plaintext must not appear inside the ciphertext")
	}

	back, err := Decrypt(sealed)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if back != plain {
		t.Fatalf("round trip mismatch: want %q, got %q", plain, back)
	}
}

// Encrypting the same plaintext twice must give different ciphertexts (a random nonce), or the database would reveal that
// two rows hold the same value.
func TestEncryptIsRandomized(t *testing.T) {
	withConfig(t, testKey, "")

	a, _ := Encrypt("same-value")
	b, _ := Encrypt("same-value")
	if a == b {
		t.Fatal("two encryptions of the same plaintext matched: the nonce is not randomised")
	}
	for _, s := range []string{a, b} {
		if got, err := Decrypt(s); err != nil || got != "same-value" {
			t.Fatalf("decrypt %q failed: %v / %q", s, err, got)
		}
	}
}

// An empty string is not encrypted: many places use an empty value to mean "not set yet".
func TestEncryptEmptyStaysEmpty(t *testing.T) {
	withConfig(t, testKey, "")

	sealed, err := Encrypt("")
	if err != nil || sealed != "" {
		t.Fatalf("an empty string should come back unchanged, got %q err=%v", sealed, err)
	}
	back, err := Decrypt("")
	if err != nil || back != "" {
		t.Fatalf("decrypting an empty string should return it unchanged, got %q err=%v", back, err)
	}
}

// An already encrypted input must not be encrypted again: otherwise the next decryption yields ciphertext.
func TestEncryptSkipsAlreadyEncrypted(t *testing.T) {
	withConfig(t, testKey, "")

	first, _ := Encrypt("v")
	second, err := Encrypt(first)
	if err != nil {
		t.Fatalf("re-encrypting errored: %v", err)
	}
	if second != first {
		t.Fatal("an already encrypted input was encrypted a second time")
	}
}

// Plaintext written before encryption existed has no prefix and must be returned as is rather than erroring.
func TestDecryptPlaintextPassthrough(t *testing.T) {
	withConfig(t, testKey, "")

	back, err := Decrypt("legacy-plain-secret")
	if err != nil {
		t.Fatalf("plaintext should be returned as is, but errored: %v", err)
	}
	if back != "legacy-plain-secret" {
		t.Fatalf("the plaintext was modified: %q", back)
	}
}

// After a key change decryption must fail hard: silently returning an empty string would make "it runs without a
// configured key" a production reality.
func TestDecryptWithRotatedKeyFails(t *testing.T) {
	withConfig(t, testKey, "")
	sealed, _ := Encrypt("top-secret")

	withConfig(t, "another-value-key-0123456789-0123456789-xyz", "")
	if _, err := Decrypt(sealed); err == nil {
		t.Fatal("decryption still succeeded after the key changed: the integrity check is not working")
	}
}

// A tampered ciphertext must fail to decrypt rather than return garbage.
func TestDecryptTamperedFails(t *testing.T) {
	withConfig(t, testKey, "")
	sealed, _ := Encrypt("top-secret")

	tampered := sealed[:len(sealed)-2] + "ZZ"
	if _, err := Decrypt(tampered); err == nil {
		t.Fatal("a tampered ciphertext still decrypted successfully")
	}
}

// Without security.value_key the key is derived from jwt.secret_key: existing deployments need no configuration change.
func TestKeyFallsBackToJWTSecret(t *testing.T) {
	withConfig(t, "", "jwt-secret-key-0123456789-0123456789")

	sealed, err := Encrypt("v")
	if err != nil {
		t.Fatalf("fallback derivation failed: %v", err)
	}
	if back, err := Decrypt(sealed); err != nil || back != "v" {
		t.Fatalf("fallback-derived decryption failed: %v / %q", err, back)
	}
}

// When value_key is configured it wins: changing jwt.secret_key must not make already encrypted values undecryptable.
// This is exactly why the two keys were split (rotating the login key must not drag existing secrets along).
func TestValueKeyTakesPrecedence(t *testing.T) {
	withConfig(t, testKey, "jwt-secret-key-0123456789-0123456789")
	sealed, err := Encrypt("v")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	withConfig(t, testKey, "rotated-jwt-key-0123456789-0123456789")
	if back, err := Decrypt(sealed); err != nil || back != "v" {
		t.Fatalf("decryption failed after rotating jwt.secret_key (value_key did not take effect): %v / %q", err, back)
	}
}

// An unloaded configuration must error rather than panic.
func TestNoConfigFails(t *testing.T) {
	prev := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = prev })

	if _, err := Encrypt("v"); err == nil {
		t.Fatal("encrypting with a nil configuration should error")
	}
}
