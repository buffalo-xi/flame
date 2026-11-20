package globalvars

import (
	"strings"
	"testing"

	"flame/nexus/shared/config"
	"flame/nexus/shared/secureval"
)

const testKey = "test-value-key-0123456789-0123456789-abcdef"

func withConfig(t *testing.T) {
	t.Helper()
	prev := config.Cfg
	config.Cfg = &config.Config{Security: config.SecurityConfig{ValueKey: testKey}}
	t.Cleanup(func() { config.Cfg = prev })
}

// A secret is stored as ciphertext and read back as plaintext — without modifying the argument.
func TestSealOpenSecret(t *testing.T) {
	withConfig(t)

	v := &GlobalVar{Name: "API_TOKEN", Kind: KindSecret, Value: "sk-live-123"}
	sealed, err := sealValue(v)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if !secureval.IsEncrypted(sealed) || strings.Contains(sealed, "sk-live-123") {
		t.Fatalf("a secret should be encrypted before storage, got %q", sealed)
	}
	if v.Value != "sk-live-123" {
		t.Fatal("sealValue must not modify its argument (the handler still needs it for the response)")
	}

	row := &GlobalVar{Name: v.Name, Kind: KindSecret, Value: sealed}
	if err := openValue(row); err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if row.Value != "sk-live-123" {
		t.Fatalf("round trip mismatch: %q", row.Value)
	}
}

// Everything that is not a secret is stored as is: plaintext must not be encrypted along with it (otherwise the value in
// the database could not be queried directly).
func TestSealSkipsNonSecret(t *testing.T) {
	withConfig(t)

	for _, kind := range []string{KindText, KindNumber, KindBool} {
		v := &GlobalVar{Name: "X", Kind: kind, Value: "123"}
		sealed, err := sealValue(v)
		if err != nil {
			t.Fatalf("encrypting %s errored: %v", kind, err)
		}
		if sealed != "123" {
			t.Fatalf("%s should not be encrypted, got %q", kind, sealed)
		}
	}
}

// Legacy plaintext (written before encryption existed) must still read, and must not error just because it has no prefix.
func TestOpenLegacyPlaintext(t *testing.T) {
	withConfig(t)

	row := &GlobalVar{Name: "OLD", Kind: KindSecret, Value: "plain-in-db"}
	if err := openValue(row); err != nil {
		t.Fatalf("legacy plaintext should be readable: %v", err)
	}
	if row.Value != "plain-in-db" {
		t.Fatalf("legacy plaintext was modified: %q", row.Value)
	}
}

// An empty value is neither encrypted nor an error: it means "not set yet".
func TestSealOpenEmpty(t *testing.T) {
	withConfig(t)

	v := &GlobalVar{Name: "E", Kind: KindSecret, Value: ""}
	sealed, err := sealValue(v)
	if err != nil || sealed != "" {
		t.Fatalf("an empty value should be returned as is: %q err=%v", sealed, err)
	}
	row := &GlobalVar{Name: "E", Kind: KindSecret, Value: ""}
	if err := openValue(row); err != nil || row.Value != "" {
		t.Fatalf("decrypting an empty value should return it as is: %q err=%v", row.Value, err)
	}
}
