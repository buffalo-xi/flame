package secureval

import (
	"encoding/json"
	"testing"
)

// 确定性加密：同一明文恒得同一密文（版本比对依赖这一点）。
func TestEncryptDeterministicIsStable(t *testing.T) {
	withConfig(t, testKey, "")

	a, err := EncryptDeterministic("sk-live-1")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	b, _ := EncryptDeterministic("sk-live-1")
	if a != b {
		t.Fatal("同一明文两次确定性加密结果不同")
	}
	if got, err := Decrypt(a); err != nil || got != "sk-live-1" {
		t.Fatalf("解密失败: %v / %q", err, got)
	}

	c, _ := EncryptDeterministic("sk-live-2")
	if a == c {
		t.Fatal("不同明文的确定性密文相同")
	}
}

// 两种模式的密文都要能解：同一个库里可能同时存在（先随机后确定性）。
func TestDecryptHandlesBothModes(t *testing.T) {
	withConfig(t, testKey, "")

	random, _ := Encrypt("v1")
	det, _ := EncryptDeterministic("v2")
	for _, tc := range []struct{ sealed, want string }{{random, "v1"}, {det, "v2"}} {
		if got, err := Decrypt(tc.sealed); err != nil || got != tc.want {
			t.Fatalf("解密 %q 失败: %v / %q", tc.sealed, err, got)
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

// 只有 type=secret 的 value 被加密，其余字段与其它条目原样不动。
func TestSealEnvVars(t *testing.T) {
	withConfig(t, testKey, "")

	sealed, err := SealEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if string(sealed) == string(envVarsFixture()) {
		t.Fatal("没有任何字段被加密")
	}

	got := decodeEnvVars(t, sealed)
	if v, _ := field(got, "API_TOKEN", "value").(string); !IsEncrypted(v) {
		t.Fatalf("secret 未加密: %q", v)
	}
	if v, _ := field(got, "API_TOKEN", "description").(string); v != "对外接口令牌" {
		t.Fatalf("description 被改动: %q", v)
	}
	if v, _ := field(got, "DB_HOST", "value").(string); v != "db.internal" {
		t.Fatalf("非 secret 被加密: %q", v)
	}
	// type=global 的 value 是变量名，不是凭据 —— 加密它就把引用打断了
	if v, _ := field(got, "TENANT_KEY", "value").(string); v != "CRM_TOKEN" {
		t.Fatalf("global 引用被改动: %q", v)
	}
	if got["LEGACY_FLAT"] != "plain-value" {
		t.Fatalf("扁平旧写法应原样保留，实际 %v", got["LEGACY_FLAT"])
	}
}

// decodeEnvVars 解析成 {名字: 任意}：表里既有结构化项也有扁平项。
func decodeEnvVars(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("不是合法 JSON 对象: %v", err)
	}
	return out
}

// field 取结构化项的某个字段（不是结构化项就返回 nil）。
func field(top map[string]any, name, key string) any {
	item, ok := top[name].(map[string]any)
	if !ok {
		return nil
	}
	return item[key]
}

// 加密后能原样解回来（含 description 等旁路字段）。
func TestSealOpenEnvVarsRoundTrip(t *testing.T) {
	withConfig(t, testKey, "")

	sealed, err := SealEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	opened, err := OpenEnvVars(sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}

	want := decodeEnvVars(t, envVarsFixture())
	got := decodeEnvVars(t, opened)
	if v, _ := field(got, "API_TOKEN", "value").(string); v != "sk-live-abc" {
		t.Fatalf("还原不一致: %v", v)
	}
	if field(got, "API_TOKEN", "description") != field(want, "API_TOKEN", "description") {
		t.Fatal("description 在往返中丢失")
	}
}

// 重复加加密（比如对已加密的行再保存一次）不能叠加成两层密文。
func TestSealEnvVarsIsIdempotent(t *testing.T) {
	withConfig(t, testKey, "")

	once, err := SealEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("第一次加密失败: %v", err)
	}
	twice, err := SealEnvVars(once)
	if err != nil {
		t.Fatalf("第二次加密失败: %v", err)
	}
	if string(once) != string(twice) {
		t.Fatal("已加密的 env_vars 被再加密了一次")
	}
}

// 存量明文（加密上线前写入）必须能直接读：不能因为没有前缀就报错或改动。
func TestOpenEnvVarsLeavesPlaintext(t *testing.T) {
	withConfig(t, testKey, "")

	out, err := OpenEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("存量明文不该报错: %v", err)
	}
	if string(out) != string(envVarsFixture()) {
		t.Fatalf("存量明文被改动: %s", out)
	}
}

// 空值 / 空 JSON：不加密也不报错（列可能是 NULL 或 '{}'）。
func TestEnvVarsEmptyInputs(t *testing.T) {
	withConfig(t, testKey, "")

	for _, in := range []string{"", "{}"} {
		out, err := SealEnvVars([]byte(in))
		if err != nil {
			t.Fatalf("输入 %q 报错: %v", in, err)
		}
		if string(out) != in {
			t.Fatalf("输入 %q 被改动: %q", in, out)
		}
	}
}

// 出参掩码：secret 的值清空、给 has_value，其余字段原样。
func TestMaskEnvVars(t *testing.T) {
	withConfig(t, testKey, "")

	masked, err := MaskEnvVars(envVarsFixture())
	if err != nil {
		t.Fatalf("掩码失败: %v", err)
	}
	got := decodeEnvVars(t, masked)

	if v, _ := field(got, "API_TOKEN", "value").(string); v != "" {
		t.Fatalf("secret 出参必须是空串（否则会被前端回传成新值），实际 %q", v)
	}
	if v, _ := field(got, "API_TOKEN", "has_value").(bool); !v {
		t.Fatal("有值的 secret 应带 has_value=true")
	}
	if v, _ := field(got, "API_TOKEN", "description").(string); v != "对外接口令牌" {
		t.Fatalf("description 被改动: %q", v)
	}
	if v, _ := field(got, "DB_HOST", "value").(string); v != "db.internal" {
		t.Fatalf("非 secret 被掩码: %q", v)
	}
	if v, _ := field(got, "TENANT_KEY", "value").(string); v != "CRM_TOKEN" {
		t.Fatalf("global 引用被掩码: %q", v)
	}
	if got["LEGACY_FLAT"] != "plain-value" {
		t.Fatalf("扁平旧写法应原样保留，实际 %v", got["LEGACY_FLAT"])
	}
}

// 掩码后回传（value 为空）不能把密钥清掉：这是「只改描述保存一次」的核心回归。
func TestMergeEnvVarSecretsKeepsOldValue(t *testing.T) {
	withConfig(t, testKey, "")

	old := []byte(`{"API_TOKEN":{"type":"secret","value":"sk-live-abc","description":"旧说明"}}`)

	// 用户改了描述，secret 输入框仍为空（值是掩码后的空串，由后端按名字补回）
	edited := []byte(`{"API_TOKEN":{"type":"secret","value":"","description":"新说明","has_value":true}}`)
	merged, err := MergeEnvVarSecrets(old, edited)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	got := decodeEnvVars(t, merged)
	if v, _ := field(got, "API_TOKEN", "value").(string); v != "sk-live-abc" {
		t.Fatalf("密钥被清空了: %q", v)
	}
	if v, _ := field(got, "API_TOKEN", "description").(string); v != "新说明" {
		t.Fatalf("提交的新描述没生效: %q", v)
	}
}

// 用户真的填了新值：不能被旧值覆盖。
func TestMergeEnvVarSecretsPrefersNewValue(t *testing.T) {
	withConfig(t, testKey, "")

	old := []byte(`{"API_TOKEN":{"type":"secret","value":"old"}}`)
	edited := []byte(`{"API_TOKEN":{"type":"secret","value":"new"}}`)
	merged, err := MergeEnvVarSecrets(old, edited)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if v, _ := field(decodeEnvVars(t, merged), "API_TOKEN", "value").(string); v != "new" {
		t.Fatalf("新值被旧值覆盖: %q", v)
	}
}

// 旧行里没有这个变量（新建）：保持空，不能凭空造一个值。
func TestMergeEnvVarSecretsNoOld(t *testing.T) {
	withConfig(t, testKey, "")

	old := []byte(`{"OTHER":{"type":"secret","value":"x"}}`)
	edited := []byte(`{"API_TOKEN":{"type":"secret","value":""}}`)
	merged, err := MergeEnvVarSecrets(old, edited)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if v, _ := field(decodeEnvVars(t, merged), "API_TOKEN", "value").(string); v != "" {
		t.Fatalf("新变量不该被填上值: %q", v)
	}
}

// has_value 是出参字段，不能被落库（否则库里会多一份会漂移的真相）。
func TestSealEnvVarsDropsHasValue(t *testing.T) {
	withConfig(t, testKey, "")

	in := []byte(`{"API_TOKEN":{"type":"secret","value":"sk-live-abc","has_value":true}}`)
	sealed, err := SealEnvVars(in)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if v := field(decodeEnvVars(t, sealed), "API_TOKEN", "has_value"); v != nil {
		t.Fatalf("has_value 被写进了库: %v", v)
	}

	// 没有 value 的条目也要把它删掉（否则「空值 + has_value」会一直留在行里）
	empty := []byte(`{"API_TOKEN":{"type":"secret","value":"","has_value":true}}`)
	sealed, err = SealEnvVars(empty)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	item := decodeEnvVars(t, sealed)["API_TOKEN"].(map[string]any)
	if _, ok := item["has_value"]; ok {
		t.Fatalf("空值条目里的 has_value 没被删: %v", item)
	}
}

// 不是 JSON 对象（比如数组 / 坏数据）应报错，而不是当成空表清掉。
func TestSealEnvVarsRejectsInvalid(t *testing.T) {
	withConfig(t, testKey, "")

	for _, in := range []string{"[1,2]", `{"A": {"type": "secret", "value": `} {
		if _, err := SealEnvVars([]byte(in)); err == nil {
			t.Fatalf("输入 %q 应报错", in)
		}
	}
}
