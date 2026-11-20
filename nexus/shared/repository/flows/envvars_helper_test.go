package repository

import (
	"encoding/json"
	"strings"
	"testing"

	"flame/nexus/shared/config"
	"flame/tools/xsql"
)

const testValueKey = "test-value-key-0123456789-0123456789-abcdef"

func withValueKey(t *testing.T) {
	t.Helper()
	prev := config.Cfg
	config.Cfg = &config.Config{Security: config.SecurityConfig{ValueKey: testValueKey}}
	t.Cleanup(func() { config.Cfg = prev })
}

func envVarsJSONB() xsql.JSONB {
	return xsql.JSONB{Data: json.RawMessage(`{"API_TOKEN":{"type":"secret","value":"sk-1","description":"令牌"},"HOST":{"type":"string","value":"a.b"}}`)}
}

// Secrets are encrypted before storage and restored after reading — and the argument is not modified.
func TestSealOpenEnvVarsJSONB(t *testing.T) {
	withValueKey(t)

	in := envVarsJSONB()
	sealed, err := sealEnvVars(in)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if strings.Contains(string(jsonbBytes(sealed)), "sk-1") {
		t.Fatal("the stored value still contains plaintext")
	}
	if strings.Contains(string(jsonbBytes(in)), "enc:") {
		t.Fatal("sealEnvVars must not modify its argument (the response and the execution context need plaintext)")
	}

	opened, err := openEnvVars(sealed)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(jsonbBytes(opened), &got); err != nil {
		t.Fatalf("the decrypted result is not valid JSON: %v", err)
	}
	if got["API_TOKEN"]["value"] != "sk-1" || got["API_TOKEN"]["description"] != "令牌" {
		t.Fatalf("round trip mismatch: %v", got["API_TOKEN"])
	}
	if got["HOST"]["value"] != "a.b" {
		t.Fatalf("a non-secret value was modified: %v", got["HOST"])
	}
}

// NULL / empty JSONB passes straight through: the env_vars returned by ListFlows is NULL.
func TestSealOpenEnvVarsNil(t *testing.T) {
	withValueKey(t)

	var nilJSONB xsql.JSONB
	if out, err := sealEnvVars(nilJSONB); err != nil || out.Data != nil {
		t.Fatalf("nil should be returned as is: %+v err=%v", out, err)
	}
	if out, err := openEnvVars(nilJSONB); err != nil || out.Data != nil {
		t.Fatalf("nil should be returned as is: %+v err=%v", out, err)
	}
}
