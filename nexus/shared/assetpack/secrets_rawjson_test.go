package assetpack

import (
	"encoding/json"
	"strings"
	"testing"
)

// 生产形态：payload 里的 jsonb 列是数据库给的原始字节（json.RawMessage），
// 不是 map[string]any —— 上架与导出用的就是这个形状（见 admin/share/planWorkflow）。
// 脱敏必须对这种输入同样生效，否则「包里只有引用名」只是注释里的说法。
func TestStripSecretsDecodesRawJSONB(t *testing.T) {
	payload := map[string]any{
		"name": "demo",
		"env_vars": json.RawMessage(`{
			"API_TOKEN": {"type": "secret", "value": "sk-live-leak", "description": "令牌"},
			"REGION":    {"type": "string", "value": "cn"},
			"TENANT":    {"type": "global", "value": "CRM_TOKEN"}
		}`),
		"diagram": json.RawMessage(`{"nodes":[{"config":{"api_key":"sk-node-leak","host":"a.b"}}]}`),
	}

	out, warnings := StripSecrets(KindWorkflow, payload)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	body := string(raw)

	if strings.Contains(body, "sk-live-leak") {
		t.Fatalf("env_vars 的 secret 明文进了导出内容: %s", body)
	}
	if strings.Contains(body, "sk-node-leak") {
		t.Fatalf("diagram 里的凭据明文进了导出内容: %s", body)
	}
	if len(warnings) == 0 {
		t.Fatal("清空了值却没有 warning：审核页会以为什么都没发生")
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	envVars, _ := got["env_vars"].(map[string]any)
	if token, _ := envVars["API_TOKEN"].(map[string]any); token != nil {
		if v, _ := token["value"].(string); v != "" {
			t.Fatalf("secret 的 value 没被清空: %q", v)
		}
		if d, _ := token["description"].(string); d != "令牌" {
			t.Fatalf("description 应保留，实际 %q", d)
		}
	} else {
		t.Fatalf("env_vars 结构丢了: %v", envVars)
	}
	// global 的 value 是变量名，清掉就断了引用
	if ref, _ := envVars["TENANT"].(map[string]any); ref != nil {
		if v, _ := ref["value"].(string); v != "CRM_TOKEN" {
			t.Fatalf("全局引用被清掉: %q", v)
		}
	}
}

// 同一份 payload 的全局变量依赖也要能从原始字节里扫出来（否则导入时不会提示缺变量）。
// 生产形态是 jsonb 原始字节：不归一化就会静默扫出空清单。
func TestCollectGlobalVarRefsDecodesRawJSONB(t *testing.T) {
	payload := map[string]any{
		"diagram": json.RawMessage(
			`{"nodes":[{"config":{"inputs":{"base":{"type":"string","source_type":"global","source_key":"API_BASE"}}}}]}`),
	}

	refs := CollectGlobalVarRefs(nil, nil, "", payload)
	if len(refs) != 1 || refs[0] != "API_BASE" {
		t.Fatalf("refs = %v, want [API_BASE]", refs)
	}
}
