package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// InMemoryJWT is a test-only Signer + KeyResolver. Generates an ephemeral
// RSA key, signs in-process, resolves its own kid. Lets unit tests exercise
// the Verifier/Minter without standing up Key Vault.
type InMemoryJWT struct {
	priv *rsa.PrivateKey
	pub  *rsa.PublicKey
	kid  string
}

func NewInMemoryJWT(kid string) (*InMemoryJWT, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if kid == "" {
		kid = "test"
	}
	return &InMemoryJWT{priv: priv, pub: &priv.PublicKey, kid: kid}, nil
}

func (m *InMemoryJWT) MintJWT(_ context.Context, claims jwt.MapClaims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = m.kid
	return token.SignedString(m.priv)
}

func (m *InMemoryJWT) PublicKey(_ context.Context, kid string) (*rsa.PublicKey, error) {
	if kid != m.kid {
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	return m.pub, nil
}
