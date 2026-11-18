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

// secret 落库是密文，读出来是明文 —— 且不改入参。
func TestSealOpenSecret(t *testing.T) {
	withConfig(t)

	v := &GlobalVar{Name: "API_TOKEN", Kind: KindSecret, Value: "sk-live-123"}
	sealed, err := sealValue(v)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if !secureval.IsEncrypted(sealed) || strings.Contains(sealed, "sk-live-123") {
		t.Fatalf("secret 应加密落库，实际 %q", sealed)
	}
	if v.Value != "sk-live-123" {
		t.Fatal("sealValue 不该改动入参（handler 出参还要用它）")
	}

	row := &GlobalVar{Name: v.Name, Kind: KindSecret, Value: sealed}
	if err := openValue(row); err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if row.Value != "sk-live-123" {
		t.Fatalf("还原不一致: %q", row.Value)
	}
}

// 非 secret 一律原样落库：明文不该被一起加密（否则库里的值没法直接查）。
func TestSealSkipsNonSecret(t *testing.T) {
	withConfig(t)

	for _, kind := range []string{KindText, KindNumber, KindBool} {
		v := &GlobalVar{Name: "X", Kind: kind, Value: "123"}
		sealed, err := sealValue(v)
		if err != nil {
			t.Fatalf("%s 加密报错: %v", kind, err)
		}
		if sealed != "123" {
			t.Fatalf("%s 不该加密，实际 %q", kind, sealed)
		}
	}
}

// 存量明文（加密上线前写入）必须能读出来，不能因为没前缀就报错。
func TestOpenLegacyPlaintext(t *testing.T) {
	withConfig(t)

	row := &GlobalVar{Name: "OLD", Kind: KindSecret, Value: "plain-in-db"}
	if err := openValue(row); err != nil {
		t.Fatalf("存量明文应可读: %v", err)
	}
	if row.Value != "plain-in-db" {
		t.Fatalf("存量明文被改动: %q", row.Value)
	}
}

// 空值不加密、不当错误：它表示「还没设置」。
func TestSealOpenEmpty(t *testing.T) {
	withConfig(t)

	v := &GlobalVar{Name: "E", Kind: KindSecret, Value: ""}
	sealed, err := sealValue(v)
	if err != nil || sealed != "" {
		t.Fatalf("空值应原样返回: %q err=%v", sealed, err)
	}
	row := &GlobalVar{Name: "E", Kind: KindSecret, Value: ""}
	if err := openValue(row); err != nil || row.Value != "" {
		t.Fatalf("空值解密应原样返回: %q err=%v", row.Value, err)
	}
}
