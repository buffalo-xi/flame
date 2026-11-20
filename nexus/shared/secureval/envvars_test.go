package secureval

import (
	"encoding/json"
	"testing"
)

// Deterministic encryption: the same plaintext always yields the same ciphertext (version comparison relies on it).
func TestEncryptDeterministicIsStable(t *testing.T) {
	withConfig(t, testKey, "")

	a, err := EncryptDeterministic("sk-live-1")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	b, _ := EncryptDeterministic("sk-live-1")
	if a != b {
		t.Fatal("deterministic encryption of the same plaintext returned two different results")
	}
	if got, err := Decrypt(a); err != nil || got != "sk-live-1" {
		t.Fatalf("decrypt failed: %v / %q", err, got)
	}

	c, _ := EncryptDeterministic("sk-live-2")
	if a == c {
		t.Fatal("different plaintexts produced the same deterministic ciphertext")
	}
}

// Both ciphertext modes must decrypt: one database may hold both (random first, deterministic later).
func TestDecryptHandlesBothModes(t *testing.T) {
	withConfig(t, testKey, "")

	random, _ := Encrypt("v1")
	det, _ := EncryptDeterministic("v2")
	for _, tc := range []struct{ sealed, want string }{{random, "v1"}, {det, "v2"}} {
		if got, err := Decrypt(tc.sealed); err != nil || got != tc.want {
			t.Fatalf("decrypt %q failed: %v / %q", tc.sealed, err, got)
		}
	}
}

func envVarsFixture() []byte {
	return []byte(`{
		"API_TOKEN":   {"type": "secret", "value": "sk-live-abc", "description": "对外接口令牌"},
		"DB_HOST":     {"type": "string", "value": "db.internal", "description": "数据库地址"},
		"TENANT_KEY":  {"type": "global", "value": "CRM_TOKEN", "description": "引用全局变量"},
		"LEGACY_FLAT": "plain-value"
	}`)
}

// Only the value of type=secret is encrypted; the other fields and the other entries stay untouched.
func TestSealEnvVars(t *testing.T) {
	withConfig(t, testKey, "")

	sealed, err := SealEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if string(sealed) == string(envVarsFixture()) {
		t.Fatal("no field at all was encrypted")
	}

	got := decodeEnvVars(t, sealed)
	if v, _ := field(got, "API_TOKEN", "value").(string); !IsEncrypted(v) {
		t.Fatalf("secret was not encrypted: %q", v)
	}
	if v, _ := field(got, "API_TOKEN", "description").(string); v != "对外接口令牌" {
		t.Fatalf("description was modified: %q", v)
	}
	if v, _ := field(got, "DB_HOST", "value").(string); v != "db.internal" {
		t.Fatalf("a non-secret value was encrypted: %q", v)
	}
	// A type=global value is a variable name, not a credential — encrypting it breaks the reference
	if v, _ := field(got, "TENANT_KEY", "value").(string); v != "CRM_TOKEN" {
		t.Fatalf("the global reference was modified: %q", v)
	}
	if got["LEGACY_FLAT"] != "plain-value" {
		t.Fatalf("the legacy flat form should be preserved verbatim, got %v", got["LEGACY_FLAT"])
	}
}

// decodeEnvVars decodes into {name: anything}: the table holds both structured and flat entries.
func decodeEnvVars(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not a valid JSON object: %v", err)
	}
	return out
}

// field reads one field of a structured entry (returns nil when it is not structured).
func field(top map[string]any, name, key string) any {
	item, ok := top[name].(map[string]any)
	if !ok {
		return nil
	}
	return item[key]
}

// What was sealed must open back exactly (side fields such as description included).
func TestSealOpenEnvVarsRoundTrip(t *testing.T) {
	withConfig(t, testKey, "")

	sealed, err := SealEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	opened, err := OpenEnvVars(sealed)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	want := decodeEnvVars(t, envVarsFixture())
	got := decodeEnvVars(t, opened)
	if v, _ := field(got, "API_TOKEN", "value").(string); v != "sk-live-abc" {
		t.Fatalf("round trip mismatch: %v", v)
	}
	if field(got, "API_TOKEN", "description") != field(want, "API_TOKEN", "description") {
		t.Fatal("description was lost in the round trip")
	}
}

// Sealing twice (say, saving an already sealed row again) must not stack a second layer of ciphertext.
func TestSealEnvVarsIsIdempotent(t *testing.T) {
	withConfig(t, testKey, "")

	once, err := SealEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("first seal failed: %v", err)
	}
	twice, err := SealEnvVars(once)
	if err != nil {
		t.Fatalf("second seal failed: %v", err)
	}
	if string(once) != string(twice) {
		t.Fatal("already sealed env_vars was sealed a second time")
	}
}

// Legacy plaintext (written before encryption existed) must read as is: a missing prefix may neither error nor modify it.
func TestOpenEnvVarsLeavesPlaintext(t *testing.T) {
	withConfig(t, testKey, "")

	out, err := OpenEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("legacy plaintext should not error: %v", err)
	}
	if string(out) != string(envVarsFixture()) {
		t.Fatalf("legacy plaintext was modified: %s", out)
	}
}

// Empty input / empty JSON: neither encrypt nor error (the column may be NULL or an empty object).
func TestEnvVarsEmptyInputs(t *testing.T) {
	withConfig(t, testKey, "")

	for _, in := range []string{"", "{}"} {
		out, err := SealEnvVars([]byte(in))
		if err != nil {
			t.Fatalf("input %q errored: %v", in, err)
		}
		if string(out) != in {
			t.Fatalf("input %q was modified: %q", in, out)
		}
	}
}

// Output masking: a secret's value is emptied and has_value is set; every other field stays as is.
func TestMaskEnvVars(t *testing.T) {
	withConfig(t, testKey, "")

	masked, err := MaskEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("masking failed: %v", err)
	}
	got := decodeEnvVars(t, masked)

	if v, _ := field(got, "API_TOKEN", "value").(string); v != "" {
		t.Fatalf("a secret's output value must be empty (otherwise the frontend sends it back as a new value), got %q", v)
	}
	if v, _ := field(got, "API_TOKEN", "has_value").(bool); !v {
		t.Fatal("a secret with a value should carry has_value=true")
	}
	if v, _ := field(got, "API_TOKEN", "description").(string); v != "对外接口令牌" {
		t.Fatalf("description was modified: %q", v)
	}
	if v, _ := field(got, "DB_HOST", "value").(string); v != "db.internal" {
		t.Fatalf("a non-secret value was masked: %q", v)
	}
	if v, _ := field(got, "TENANT_KEY", "value").(string); v != "CRM_TOKEN" {
		t.Fatalf("the global reference was masked: %q", v)
	}
	if got["LEGACY_FLAT"] != "plain-value" {
		t.Fatalf("the legacy flat form should be preserved verbatim, got %v", got["LEGACY_FLAT"])
	}
}

// Sending a masked entry back (with an empty value) must not wipe the secret: this is the core regression for "change the
// description and save once".
func TestMergeEnvVarSecretsKeepsOldValue(t *testing.T) {
	withConfig(t, testKey, "")

	old := []byte(`{"API_TOKEN":{"type":"secret","value":"sk-live-abc","description":"旧说明"}}`)

	// The user changed the description while the secret input is still empty (the value is the masked empty string, filled
	// back in by name on the server)
	edited := []byte(`{"API_TOKEN":{"type":"secret","value":"","description":"新说明","has_value":true}}`)
	merged, err := MergeEnvVarSecrets(old, edited)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	got := decodeEnvVars(t, merged)
	if v, _ := field(got, "API_TOKEN", "value").(string); v != "sk-live-abc" {
		t.Fatalf("the secret was wiped: %q", v)
	}
	if v, _ := field(got, "API_TOKEN", "description").(string); v != "新说明" {
		t.Fatalf("the submitted description did not take effect: %q", v)
	}
}

// The user really typed a new value: it must not be overwritten by the old one.
func TestMergeEnvVarSecretsPrefersNewValue(t *testing.T) {
	withConfig(t, testKey, "")

	old := []byte(`{"API_TOKEN":{"type":"secret","value":"old"}}`)
	edited := []byte(`{"API_TOKEN":{"type":"secret","value":"new"}}`)
	merged, err := MergeEnvVarSecrets(old, edited)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if v, _ := field(decodeEnvVars(t, merged), "API_TOKEN", "value").(string); v != "new" {
		t.Fatalf("the new value was overwritten by the old one: %q", v)
	}
}

// The old row does not have this variable (a new one): leave it empty rather than conjuring a value.
func TestMergeEnvVarSecretsNoOld(t *testing.T) {
	withConfig(t, testKey, "")

	old := []byte(`{"OTHER":{"type":"secret","value":"x"}}`)
	edited := []byte(`{"API_TOKEN":{"type":"secret","value":""}}`)
	merged, err := MergeEnvVarSecrets(old, edited)
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if v, _ := field(decodeEnvVars(t, merged), "API_TOKEN", "value").(string); v != "" {
		t.Fatalf("a new variable should not be given a value: %q", v)
	}
}

// has_value is an output-only field and must not reach the database (otherwise the row holds a second, drifting truth).
func TestSealEnvVarsDropsHasValue(t *testing.T) {
	withConfig(t, testKey, "")

	in := []byte(`{"API_TOKEN":{"type":"secret","value":"sk-live-abc","has_value":true}}`)
	sealed, err := SealEnvVars(in)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if v := field(decodeEnvVars(t, sealed), "API_TOKEN", "has_value"); v != nil {
		t.Fatalf("has_value was written to the database: %v", v)
	}

	// An entry without a value must have it dropped too (otherwise "empty value + has_value" stays in the row forever)
	empty := []byte(`{"API_TOKEN":{"type":"secret","value":"","has_value":true}}`)
	sealed, err = SealEnvVars(empty)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	item := decodeEnvVars(t, sealed)["API_TOKEN"].(map[string]any)
	if _, ok := item["has_value"]; ok {
		t.Fatalf("has_value in an empty-value entry was not removed: %v", item)
	}
}

// Something that is not a JSON object (an array, broken data) must error rather than be treated as an empty table and
// wiped.
func TestSealEnvVarsRejectsInvalid(t *testing.T) {
	withConfig(t, testKey, "")

	for _, in := range []string{"[1,2]", `{"A": {"type": "secret", "value": `} {
		if _, err := SealEnvVars([]byte(in)); err == nil {
			t.Fatalf("input %q should error", in)
		}
	}
}
