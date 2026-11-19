package assetpack

import (
	"strings"
	"testing"
)

// 全局变量引用不是凭据：它是「名字」，真正的值在目标租户自己的
// {schema}.global_vars 里。清洗时把它清掉，装上后就是一个坏的空配置 ——
// 静默坏比泄漏更难查，所以这条必须锁住。
func TestStripSecretsKeepsGlobalVarRefs(t *testing.T) {
	payload := map[string]any{
		"env_vars": map[string]any{
			// 引用全局变量：value 是变量名，不该被清
			"API_TOKEN": map[string]any{
				"type":        "global",
				"value":       "CRM_API_TOKEN",
				"description": "CRM 接口密钥",
			},
			// 内联密钥：仍然要清（防回归）
			"LEGACY_TOKEN": map[string]any{
				"type":  "secret",
				"value": "sk-real-secret",
			},
			// 普通文本：不动
			"REGION": map[string]any{
				"type":  "string",
				"value": "cn-north",
			},
		},
		"diagram": map[string]any{
			"nodes": []any{
				map[string]any{
					"id": "http1",
					"config": map[string]any{
						// 整体是引用 → 保留
						"api_key": "{{.global_vars.CRM_TOKEN}}",
						// 混合 → 保留但记 warning
						"authorization": "Bearer {{.global_vars.CRM_TOKEN}}",
						// 内联凭据 → 清空
						"webhook_token": "wh-real-secret",
					},
				},
			},
		},
	}

	out, warnings := StripSecrets(KindWorkflow, payload)

	envVars := out["env_vars"].(map[string]any)
	ref := envVars["API_TOKEN"].(map[string]any)
	if ref["value"] != "CRM_API_TOKEN" {
		t.Fatalf("全局变量引用被清空了：%v", ref["value"])
	}
	if ref["type"] != "global" {
		t.Fatalf("type 被改动：%v", ref["type"])
	}

	legacy := envVars["LEGACY_TOKEN"].(map[string]any)
	if legacy["value"] != "" {
		t.Fatalf("内联密钥应被清空，实际 %v", legacy["value"])
	}

	region := envVars["REGION"].(map[string]any)
	if region["value"] != "cn-north" {
		t.Fatalf("普通文本被误清：%v", region["value"])
	}

	cfg := out["diagram"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["config"].(map[string]any)
	if cfg["api_key"] != "{{.global_vars.CRM_TOKEN}}" {
		t.Fatalf("api_key 的全局引用被清空：%v", cfg["api_key"])
	}
	if cfg["authorization"] != "Bearer {{.global_vars.CRM_TOKEN}}" {
		t.Fatalf("混合引用被清空：%v", cfg["authorization"])
	}
	if cfg["webhook_token"] != "" {
		t.Fatalf("内联 webhook_token 应被清空，实际 %v", cfg["webhook_token"])
	}

	// 混合场景要留一条 warning 提示人工确认，不能静默保留
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "authorization") && strings.Contains(w, "global variable") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("混合引用缺少 warning，实际 warnings=%v", warnings)
	}
}

// 传入 payload 不能被改动：调用方还会拿原值做别的判断。
func TestStripSecretsDoesNotMutateInput(t *testing.T) {
	payload := map[string]any{
		"diagram": map[string]any{
			"api_key": "{{.global_vars.A}}",
			"token":   "sk-real",
		},
	}
	_, _ = StripSecrets(KindWorkflow, payload)

	diagram := payload["diagram"].(map[string]any)
	if diagram["token"] != "sk-real" {
		t.Fatalf("入参被改动：%v", diagram["token"])
	}
	if diagram["api_key"] != "{{.global_vars.A}}" {
		t.Fatalf("入参被改动：%v", diagram["api_key"])
	}
}

// 全局变量改成节点输入的来源后，凭据字段里写的是 {{ .input.token }}：
// 值来自运行期的输入来源、不在包里，清洗时同样必须保留 —— 否则装上去就是一个空配置。
func TestStripSecretsKeepsInputRefs(t *testing.T) {
	payload := map[string]any{
		"diagram": map[string]any{
			"nodes": []any{
				map[string]any{
					"id": "http1",
					"config": map[string]any{
						"inputs": map[string]any{
							"token": map[string]any{"type": "string", "source_type": "global", "source_key": "CRM_TOKEN"},
						},
						// 整体是输入引用 → 保留
						"api_key": "{{ .input.token }}",
						// 混合 → 保留但记 warning
						"authorization": "Bearer {{ .input.token }}",
						// 内联凭据 → 照旧清空
						"webhook_token": "wh-real-secret",
					},
				},
			},
		},
	}

	out, warnings := StripSecrets(KindWorkflow, payload)
	cfg := out["diagram"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["config"].(map[string]any)
	if cfg["api_key"] != "{{ .input.token }}" {
		t.Fatalf("输入引用被清空：%v", cfg["api_key"])
	}
	if cfg["authorization"] != "Bearer {{ .input.token }}" {
		t.Fatalf("混合引用被清空：%v", cfg["authorization"])
	}
	if cfg["webhook_token"] != "" {
		t.Fatalf("内联凭据应被清空，实际 %v", cfg["webhook_token"])
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "authorization") && strings.Contains(w, "reference") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("混合引用缺少 warning，实际 warnings=%v", warnings)
	}
}
