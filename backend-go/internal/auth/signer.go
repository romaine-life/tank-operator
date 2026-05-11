package auth

import (
	"context"
	"crypto/rsa"

	"github.com/golang-jwt/jwt/v5"
)

// Signer mints a signed RS256 JWT from the given claims. Implementations stamp
// `kid` in the header so verifiers know which public key to validate against
// — production signs in Key Vault (private key never leaves the vault), tests
// use an in-process ephemeral RSA key.
type Signer interface {
	MintJWT(ctx context.Context, claims jwt.MapClaims) (string, error)
}

// KeyResolver returns the RSA public key matching a JWT's kid header. KV's
// versioned-key model maps cleanly onto JWT key rotation: each key version
// produces a stable kid, the resolver caches the public bytes per kid so
// hot-path verifies stay in-process.
type KeyResolver interface {
	PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error)
}
