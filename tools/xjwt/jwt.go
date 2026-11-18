package xjwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig JWT配置结构
type JWTConfig struct {
	SecretKey   string
	TokenExpiry int
	Issuer      string
}

// GenerateTokenWithMapClaims 生成JWT token
func GenerateTokenWithMapClaims(info map[string]any, config JWTConfig) (string, error) {
	// 不再兜底为固定默认密钥：密钥缺失时必须显式报错，否则等于任何人可伪造 token。
	if config.SecretKey == "" {
		return "", errors.New("jwt secret key is empty")
	}

	// 创建map形式的claims
	claims := jwt.MapClaims{
		"exp": time.Now().Add(time.Duration(config.TokenExpiry) * time.Minute).Unix(),
		"iat": time.Now().Unix(),
		"iss": config.Issuer,
	}

	// 合并自定义claims
	for key, value := range info {
		claims[key] = value
	}

	// 创建token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// 签名token
	tokenString, err := token.SignedString([]byte(config.SecretKey))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

// ValidateTokenWithMapClaims 验证JWT token
func ValidateTokenWithMapClaims(tokenString string, config JWTConfig) (jwt.MapClaims, error) {
	// 校验侧同样不兜底：密钥为空直接失败（fail-closed）
	if config.SecretKey == "" {
		return nil, errors.New("jwt secret key is empty")
	}

	// 解析token：显式限定签名算法，避免算法混淆类问题
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.SecretKey), nil
	}, jwt.WithValidMethods([]string{"HS256"}))

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}
