package xjwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig holds the JWT settings.
type JWTConfig struct {
	SecretKey   string
	TokenExpiry int
	Issuer      string
}

// GenerateTokenWithMapClaims builds and signs a JWT.
func GenerateTokenWithMapClaims(info map[string]any, config JWTConfig) (string, error) {
	// No fallback to a fixed default key: a missing key must fail loudly, otherwise anyone could forge tokens.
	if config.SecretKey == "" {
		return "", errors.New("jwt secret key is empty")
	}

	// Build map claims.
	claims := jwt.MapClaims{
		"exp": time.Now().Add(time.Duration(config.TokenExpiry) * time.Minute).Unix(),
		"iat": time.Now().Unix(),
		"iss": config.Issuer,
	}

	// Merge caller-supplied claims.
	for key, value := range info {
		claims[key] = value
	}

	// Create the token.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Sign the token.
	tokenString, err := token.SignedString([]byte(config.SecretKey))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

// ValidateTokenWithMapClaims verifies a JWT.
func ValidateTokenWithMapClaims(tokenString string, config JWTConfig) (jwt.MapClaims, error) {
	// Same here: no fallback — an empty key fails closed.
	if config.SecretKey == "" {
		return nil, errors.New("jwt secret key is empty")
	}

	// Parse with an explicit algorithm list, which rules out algorithm-confusion attacks.
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
