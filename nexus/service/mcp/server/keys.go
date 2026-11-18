package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
)

// 解析 JWK 的 base64url 字段
func b64u(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// buildRSAPublicKey 由 n/e 构造 RSA 公钥
func buildRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := b64u(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := b64u(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if e.BitLen() > 32 {
		return nil, fmt.Errorf("exponent too large")
	}
	pub := &rsa.PublicKey{
		N: n,
		E: int(e.Uint64()),
	}
	return pub, nil
}

// buildECPublicKey 由 crv/x/y 构造 EC 公钥
func buildECPublicKey(crv, xStr, yStr string) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported ec curve %q", crv)
	}
	xBytes, err := b64u(xStr)
	if err != nil {
		return nil, fmt.Errorf("decode x: %w", err)
	}
	yBytes, err := b64u(yStr)
	if err != nil {
		return nil, fmt.Errorf("decode y: %w", err)
	}
	pub := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}
	return pub, nil
}
