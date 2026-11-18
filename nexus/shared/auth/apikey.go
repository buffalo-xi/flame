package auth

import (
	"crypto/rand"
	"encoding/base64"
)

// GenerateAPIKey 生成符合大模型调用习惯的 API Key：sk- 前缀 + 32 字节高熵随机数。
//
// info 会拼在 "sk-" 之后，用于肉眼区分用途（如租户 schema、服务名）；
// 不需要区分时传空串。
func GenerateAPIKey(info string) (string, error) {
	// 32 字节（256 位）随机数，URL 安全的 Base64 编码（与 OpenAI 格式一致）
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return "sk-" + info + base64.URLEncoding.EncodeToString(randomBytes), nil
}
