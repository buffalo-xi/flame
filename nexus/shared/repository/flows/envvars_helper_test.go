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

// 落库前加密 secret，读出后还原 —— 且不改入参。
func TestSealOpenEnvVarsJSONB(t *testing.T) {
	withValueKey(t)

	in := envVarsJSONB()
	sealed, err := sealEnvVars(in)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if strings.Contains(string(jsonbBytes(sealed)), "sk-1") {
		t.Fatal("落库值里还有明文")
	}
	if strings.Contains(string(jsonbBytes(in)), "enc:") {
		t.Fatal("sealEnvVars 不该改动入参（出参 / 执行上下文要的是明文）")
	}

	opened, err := openEnvVars(sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(jsonbBytes(opened), &got); err != nil {
		t.Fatalf("解密结果不是合法 JSON: %v", err)
	}
	if got["API_TOKEN"]["value"] != "sk-1" || got["API_TOKEN"]["description"] != "令牌" {
		t.Fatalf("还原不一致: %v", got["API_TOKEN"])
	}
	if got["HOST"]["value"] != "a.b" {
		t.Fatalf("非 secret 被改动: %v", got["HOST"])
	}
}

// NULL / 空 JSONB 原样透传：ListFlows 查出来的 env_vars 就是 NULL。
func TestSealOpenEnvVarsNil(t *testing.T) {
	withValueKey(t)

	var nilJSONB xsql.JSONB
	if out, err := sealEnvVars(nilJSONB); err != nil || out.Data != nil {
		t.Fatalf("nil 应原样返回: %+v err=%v", out, err)
	}
	if out, err := openEnvVars(nilJSONB); err != nil || out.Data != nil {
		t.Fatalf("nil 应原样返回: %+v err=%v", out, err)
	}
}
