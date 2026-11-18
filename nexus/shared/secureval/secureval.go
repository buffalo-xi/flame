// Package secureval 负责「敏感配置值的静态加密」。
//
// 只有一个职责：把一个字符串变成可落库的密文，再原样还原回来。
// 谁该加密由调用方决定（目前是 {schema}.global_vars 里 kind=secret 的 value），
// 本包不认识任何表结构。
//
// 密文格式：enc:v1:<base64url(nonce||ciphertext||tag)>
//   - 带前缀是为了让「已有明文」能继续读：没有前缀一律当明文原样返回。
//     这样上线不需要一次性迁移存量数据，也不会因为漏迁移某个租户而读失败。
//   - AES-256-GCM：自带完整性校验，密文被改写会解密失败而不是返回垃圾值。
//   - v1 是格式版本位：将来换算法（比如上 KMS）时可以并存，按前缀分流。
package secureval

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/config"
)

// 密文标记。改了它等于让所有已加密数据变成「无前缀」→ 被当明文读，
// 所以它只增不改：换算法时用新的前缀。
//
// 两个前缀对应两种模式，差别只有 nonce 从哪来：
//   - plainPrefix    随机 nonce，同一明文的两次加密结果不同。默认用它。
//   - deterministic  nonce 由密钥与明文推导，同一明文恒得同一密文。
//
// 为什么要确定性模式：flows.env_vars 是整块 jsonb，版本「有没有改动」是拿
// flows 行和 flow_versions 行在 SQL 里 IS DISTINCT FROM 比的。用随机 nonce，
// 用户只改描述再保存一次，env_vars 因为重新加密而变了 → 明明没改变量却被判成
// 「有未发布改动」。确定性保证了「明文没变 → 密文没变」。
// 代价是相等性可见（两个流程填同一个密钥，库里是同一串密文）。这个代价可以接受：
// 能读库的人本来就看得到这些行，而它换不来密钥本身。
const (
	plainPrefix = "enc:v1:"
	detPrefix   = "enc:d1:"
)

// keyInfo 派生信息，用来把「同一个根密钥的不同用途」隔开：
// 将来若别的地方也用 jwt.secret_key 派生密钥，不会得出同一把 key。
const keyInfo = "flame/nexus/secureval/v1"

// IsEncrypted 判断一个落库值是否已经是密文（两种模式都算）。
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, plainPrefix) || strings.HasPrefix(stored, detPrefix)
}

// Encrypt 加密。
//
// 空串不加密（原样返回空）：空值没有需要保护的内容，而且很多地方用
// `value == ""` 表达「还没设置」，给它编一段密文会把这个语义搞坏。
// 已经是密文的输入原样返回，避免重复加密（重复加密会让下次解密得到密文）。
func Encrypt(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}

	aead, err := newAEAD()
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}

	// Seal 把密文追加在 nonce 之后：一个字符串同时带齐解密所需的全部信息，
	// 不需要额外的列存 nonce。
	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return plainPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// EncryptDeterministic 确定性加密：同一明文 + 同一密钥恒得同一密文。
//
// 只在「密文变化本身有意义」的地方用（见 detPrefix 的注释，目前是 flows.env_vars
// 的版本比对）。其它场景用 Encrypt —— 随机 nonce 不泄漏「两行值相同」。
//
// nonce 由 HMAC(key, "nonce|" + 明文) 推导：不同明文得到不同 nonce（碰撞概率可忽略），
// 同一明文得到同一 nonce 且明文相同 —— 不构成 GCM 意义上的 nonce 复用。
func EncryptDeterministic(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}

	key, err := deriveKey()
	if err != nil {
		return "", err
	}
	aead, err := aeadFor(key)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("nonce|"))
	mac.Write([]byte(plain))
	copy(nonce, mac.Sum(nil)) // Sum 是 32 字节，NonceSize 是 12

	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return detPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密。
//
// 没有前缀的输入原样返回：那是加密功能上线前写入的明文，或者本来就不需要
// 加密的字段。**不要**把它当错误 —— 否则上线瞬间所有历史 secret 全部读失败。
func Decrypt(stored string) (string, error) {
	// 两种模式只在「nonce 从哪来」上不同，密文布局一致（nonce || ct || tag），
	// 所以解密只有一处：按前缀剥掉标记即可。
	var body string
	switch {
	case strings.HasPrefix(stored, plainPrefix):
		body = strings.TrimPrefix(stored, plainPrefix)
	case strings.HasPrefix(stored, detPrefix):
		body = strings.TrimPrefix(stored, detPrefix)
	default:
		return stored, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}

	aead, err := newAEAD()
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("密文长度不足（nonce 缺失）")
	}

	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		// 这里只会是两种情况：密钥换了，或者密文被改过。
		// 不降级成空串 —— 静默返回空密钥比直接失败难查得多。
		return "", errors.New("密文解密失败：密钥与写入时不一致，或密文已损坏")
	}
	return string(plain), nil
}

// newAEAD 每次按当前配置派生密钥。不做进程内缓存：派生本身是一次 HMAC-SHA256
// （微秒级），而缓存会让「配置改了但进程内还是老 key」这种问题变得难解释。
func newAEAD() (cipher.AEAD, error) {
	key, err := deriveKey()
	if err != nil {
		return nil, err
	}
	return aeadFor(key)
}

func aeadFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	return cipher.NewGCM(block)
}

// deriveKey 从配置派生 32 字节密钥。
//
// 优先用 security.value_key（独立密钥，轮换 jwt.secret_key 不会连带废掉已加密数据）；
// 没配就退回 jwt.secret_key —— 它是启动期强制校验的随机值，能保证「开箱可用」，
// 代价是换 jwt.secret_key 会导致已加密值解不开（配置注释里写明了这一点）。
func deriveKey() ([]byte, error) {
	if config.Cfg == nil {
		return nil, errors.New("配置未加载，无法派生加密密钥")
	}

	source := strings.TrimSpace(config.Cfg.Security.ValueKey)
	if source == "" {
		source = strings.TrimSpace(config.Cfg.JWT.SecretKey)
		if source == "" {
			return nil, errors.New("没有可用的加密密钥：请配置 security.value_key（或 jwt.secret_key）")
		}
	}

	mac := hmac.New(sha256.New, []byte(source))
	mac.Write([]byte(keyInfo))
	return mac.Sum(nil), nil
}
