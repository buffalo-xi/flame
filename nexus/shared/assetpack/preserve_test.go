package assetpack

import "testing"

// 覆盖安装时保留目标凭据：只认「包内是空串 + 键名像凭据」的组合。
func TestPreserveSecretKeys(t *testing.T) {
	incoming := map[string]any{
		"api_key":     "",      // 上架时被清空 → 应该拿回目标的值
		"model":       "gpt-4", // 包内给了值 → 按包走
		"description": "",      // 键名不像凭据 → 不动（可能业务上就该为空）
		"new_token":   "",      // 目标没有 → 保持空
		"provider": map[string]any{
			"token": "",          // 嵌套层级同样要保留
			"url":   "https://a", // 包内值优先
		},
	}
	target := map[string]any{
		"api_key":     "sk-live-1",
		"model":       "old-model",
		"description": "旧说明",
		"provider": map[string]any{
			"token": "tgt-token",
			"url":   "https://old",
		},
	}

	kept := PreserveSecretKeys(incoming, target)
	if kept != 2 {
		t.Fatalf("保留数量 = %d，期望 2（api_key 与 provider.token）", kept)
	}

	if got := incoming["api_key"].(string); got != "sk-live-1" {
		t.Fatalf("api_key 没拿回目标值: %q", got)
	}
	if got := incoming["model"].(string); got != "gpt-4" {
		t.Fatalf("包内非空的字段被目标覆盖了: %q", got)
	}
	if got := incoming["description"].(string); got != "" {
		t.Fatalf("键名不像凭据的空值不该被填: %q", got)
	}
	if got := incoming["new_token"].(string); got != "" {
		t.Fatalf("目标没有的键不该凭空填值: %q", got)
	}
	provider := incoming["provider"].(map[string]any)
	if got := provider["token"].(string); got != "tgt-token" {
		t.Fatalf("嵌套凭据没拿回目标值: %q", got)
	}
	if got := provider["url"].(string); got != "https://a" {
		t.Fatalf("嵌套非凭据字段被改动: %q", got)
	}
}

// 包内给了非空凭据：那是用户明确换了值，不能被目标旧值盖掉。
func TestPreserveSecretKeysPrefersIncomingValue(t *testing.T) {
	incoming := map[string]any{"token": "new"}
	target := map[string]any{"token": "old"}
	if kept := PreserveSecretKeys(incoming, target); kept != 0 {
		t.Fatalf("不应保留，实际 kept=%d", kept)
	}
	if got := incoming["token"].(string); got != "new" {
		t.Fatalf("包内新值被覆盖: %q", got)
	}
}

// 目标那边是空 / 类型不符：不填，保持包内的空值。
func TestPreserveSecretKeysNoTargetValue(t *testing.T) {
	incoming := map[string]any{"token": "", "password": ""}
	target := map[string]any{"token": "", "password": 12345}
	if kept := PreserveSecretKeys(incoming, target); kept != 0 {
		t.Fatalf("不应保留，实际 kept=%d", kept)
	}
}

// 空 map 不需要遍历（config 常见为空）。
func TestPreserveSecretKeysEmpty(t *testing.T) {
	if kept := PreserveSecretKeys(map[string]any{}, map[string]any{"token": "x"}); kept != 0 {
		t.Fatalf("空 incoming 应返回 0，实际 %d", kept)
	}
	if kept := PreserveSecretKeys(map[string]any{"token": ""}, nil); kept != 0 {
		t.Fatalf("空 target 应返回 0，实际 %d", kept)
	}
}
