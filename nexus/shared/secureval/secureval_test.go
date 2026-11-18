package secureval

import (
	"strings"
	"testing"

	"flame/nexus/shared/config"
)

const testKey = "test-value-key-0123456789-0123456789-abcdef"

// withConfig 在本测试期内换上指定配置，结束后还原（config.Cfg 是包级全局）。
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
		t.Fatalf("加密失败: %v", err)
	}
	if !IsEncrypted(sealed) || !strings.HasPrefix(sealed, "enc:v1:") {
		t.Fatalf("密文应带 enc:v1: 前缀，实际 %q", sealed)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("密文里不该出现明文")
	}

	back, err := Decrypt(sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if back != plain {
		t.Fatalf("还原不一致: 期望 %q，实际 %q", plain, back)
	}
}

// 同一明文两次加密应得到不同密文（nonce 随机），否则能从库里看出两行值相同。
func TestEncryptIsRandomized(t *testing.T) {
	withConfig(t, testKey, "")

	a, _ := Encrypt("same-value")
	b, _ := Encrypt("same-value")
	if a == b {
		t.Fatal("同一明文的两次加密结果相同，nonce 没有随机化")
	}
	for _, s := range []string{a, b} {
		if got, err := Decrypt(s); err != nil || got != "same-value" {
			t.Fatalf("解密 %q 失败: %v / %q", s, err, got)
		}
	}
}

// 空串不加密：很多地方用 value == "" 表示「还没设置」。
func TestEncryptEmptyStaysEmpty(t *testing.T) {
	withConfig(t, testKey, "")

	sealed, err := Encrypt("")
	if err != nil || sealed != "" {
		t.Fatalf("空串应原样返回，实际 %q err=%v", sealed, err)
	}
	back, err := Decrypt("")
	if err != nil || back != "" {
		t.Fatalf("空串解密应原样返回，实际 %q err=%v", back, err)
	}
}

// 已加密的输入不能再加密一层：否则下次解密得到的是密文。
func TestEncryptSkipsAlreadyEncrypted(t *testing.T) {
	withConfig(t, testKey, "")

	first, _ := Encrypt("v")
	second, err := Encrypt(first)
	if err != nil {
		t.Fatalf("重复加密报错: %v", err)
	}
	if second != first {
		t.Fatal("已是密文的输入被再加密了一次")
	}
}

// 加密功能上线前写入的明文没有前缀，必须原样返回而不是报错。
func TestDecryptPlaintextPassthrough(t *testing.T) {
	withConfig(t, testKey, "")

	back, err := Decrypt("legacy-plain-secret")
	if err != nil {
		t.Fatalf("明文应原样返回，实际报错: %v", err)
	}
	if back != "legacy-plain-secret" {
		t.Fatalf("明文被改动: %q", back)
	}
}

// 换密钥后必须硬失败：静默返回空串会让「没配密钥也能跑」变成线上现实。
func TestDecryptWithRotatedKeyFails(t *testing.T) {
	withConfig(t, testKey, "")
	sealed, _ := Encrypt("top-secret")

	withConfig(t, "another-value-key-0123456789-0123456789-xyz", "")
	if _, err := Decrypt(sealed); err == nil {
		t.Fatal("换了密钥仍能解密成功，完整性校验失效")
	}
}

// 密文被改写应解密失败，而不是返回垃圾值。
func TestDecryptTamperedFails(t *testing.T) {
	withConfig(t, testKey, "")
	sealed, _ := Encrypt("top-secret")

	tampered := sealed[:len(sealed)-2] + "ZZ"
	if _, err := Decrypt(tampered); err == nil {
		t.Fatal("被改写的密文仍解密成功")
	}
}

// 没配 security.value_key 时回退到 jwt.secret_key：存量部署不需要改配置。
func TestKeyFallsBackToJWTSecret(t *testing.T) {
	withConfig(t, "", "jwt-secret-key-0123456789-0123456789")

	sealed, err := Encrypt("v")
	if err != nil {
		t.Fatalf("回退派生失败: %v", err)
	}
	if back, err := Decrypt(sealed); err != nil || back != "v" {
		t.Fatalf("回退派生解密失败: %v / %q", err, back)
	}
}

// 配了 value_key 就以它为准：换 jwt.secret_key 不该让已加密的值解不开。
// 这正是把两把密钥拆开的理由（轮换登录密钥不该牵连存量 secret）。
func TestValueKeyTakesPrecedence(t *testing.T) {
	withConfig(t, testKey, "jwt-secret-key-0123456789-0123456789")
	sealed, err := Encrypt("v")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	withConfig(t, testKey, "rotated-jwt-key-0123456789-0123456789")
	if back, err := Decrypt(sealed); err != nil || back != "v" {
		t.Fatalf("换 jwt.secret_key 后解密失败（说明 value_key 没生效）: %v / %q", err, back)
	}
}

// 配置未加载时应报错，而不是 panic。
func TestNoConfigFails(t *testing.T) {
	prev := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = prev })

	if _, err := Encrypt("v"); err == nil {
		t.Fatal("配置为 nil 时加密应报错")
	}
}
