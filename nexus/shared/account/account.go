// Package account 账号原语：密码哈希/校验 + 账号状态词表。
//
// 中立包，两侧共用：
//   - 租户用户：<schema>.users（shared/auth 通过别名转发复用本包，见 auth/password.go）
//   - 平台用户：platform.users（nexus_platform 直接引用）
//
// 之所以从 shared/auth 抽出来：auth 包里还有租户登录 handler（login.go，依赖 gin），
// 而平台侧只需要"哈希 / 校验 / 状态词表"，不该为此把租户登录逻辑拖进依赖图。
//
// 密码存储策略（与原实现完全一致，未改语义）：
//   - 新写入（新建用户 / 重置密码 / 改角色设密码）一律 bcrypt：
//     自带 salt 与算法/成本标识（形如 `$2a$10$...`），比较是恒定时间的。
//   - 兼容历史 MD5（32 位小写 hex，无盐）：登录校验通过后由调用方透明升级为 bcrypt
//     （见 VerifyPassword 的 needRehash 返回值）。
//   - 其它格式一律拒绝，不做猜测。
//
// 注意：bcrypt 只使用明文的前 72 字节，因此超长密码直接拒绝，
// 避免"前 72 字节相同即通过"的歧义。
package account

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MaxPasswordBytes bcrypt 可处理的明文上限（字节）
	MaxPasswordBytes = 72
	// MinPasswordLength 密码最小长度（与前端校验保持一致）
	MinPasswordLength = 8
)

// 账号状态词表：租户用户（<schema>.users.status）与平台用户（platform.users.status）共用。
// 语义：账号是否可用 —— 登录与 API 调用两侧都看它。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusLocked   = "locked"
)

// ErrPasswordTooLong 密码超过 bcrypt 上限
var ErrPasswordTooLong = errors.New("密码过长（最多 72 字节）")

// HashPassword 生成密码哈希（bcrypt）。
func HashPassword(plain string) (string, error) {
	if len(plain) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 校验明文密码是否与存储值匹配。
// needRehash = true 表示"校验通过，但存储值仍是旧格式（MD5）"，调用方应重新哈希后落库。
func VerifyPassword(stored, plain string) (ok bool, needRehash bool) {
	if stored == "" {
		return false, false
	}

	// bcrypt：$2a$ / $2b$ / $2y$
	if strings.HasPrefix(stored, "$2") {
		if len(plain) > MaxPasswordBytes {
			return false, false
		}
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil, false
	}

	// 旧格式：32 位 hex = md5(明文)
	if len(stored) == 32 && isHex(stored) {
		sum := md5.Sum([]byte(plain))
		want := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(stored))) == 1 {
			return true, true
		}
		return false, false
	}

	// 其它格式：拒绝
	return false, false
}

// isHex 判断字符串是否全为十六进制字符。
func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
