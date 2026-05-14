package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"strings"
)

type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWKProvider interface {
	CurrentJWK(ctx context.Context) (JWK, error)
}

func rsaPublicJWK(kid string, pub *rsa.PublicKey) JWK {
	return JWK{
		Kty: "RSA",
		Use: "sig",
		Kid: kid,
		Alg: "RS256",
		N:   b64URL(pub.N.Bytes()),
		E:   b64URL(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func b64URL(b []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}
