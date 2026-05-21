package auth

import (
	"context"
	"crypto/rsa"
)

// KeyResolver returns the RSA public key matching a JWT's kid header.
type KeyResolver interface {
	PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error)
}
