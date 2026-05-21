package auth

import (
	"context"
	"crypto/rsa"
)

type romaineLifeKeyResolver struct{}

// NewRomaineLifeKeyResolver returns a KeyResolver backed by auth.romaine.life's
// JWKS endpoint.
func NewRomaineLifeKeyResolver() KeyResolver {
	return romaineLifeKeyResolver{}
}

func (romaineLifeKeyResolver) PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	return romaineLifeJWKS.getKey(ctx, authRomaineLifeJWKSURL, kid)
}
