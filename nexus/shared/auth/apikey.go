package auth

import (
	"crypto/rand"
	"encoding/base64"
)

// GenerateAPIKey produces an API key shaped the way LLM clients expect: an sk- prefix followed by 32 bytes of
// high-entropy randomness.
//
// info is appended after "sk-" so the purpose is visible to the eye (a tenant schema, a service name); pass an empty
// string when no such distinction is needed.
func GenerateAPIKey(info string) (string, error) {
	// 32 bytes (256 bits) of randomness, URL-safe Base64 encoded (the same shape as OpenAI's keys)
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return "sk-" + info + base64.URLEncoding.EncodeToString(randomBytes), nil
}
