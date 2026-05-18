package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
)

// chainedKeyResolver tries each underlying resolver in order, returning
// the first one that successfully resolves the kid. Built for the
// auth.romaine.life cutover: tank-operator now accepts JWTs minted by
// the platform IdP (auth.romaine.life — JWKS resolver) AND legacy
// JWTs it minted itself (KV resolver). The two key namespaces are
// disjoint in production — auth.romaine.life signs with the auth-jwt-
// signing key in KV, tank-operator's own exchange signs with tank-
// operator-jwt-signing — so the chain produces a deterministic answer
// based on which key signed the token.
//
// The chain order is: try the JWKS (auth.romaine.life) resolver first,
// fall back to the KV resolver. Rationale: the JWKS path is becoming
// the primary path (admin bot tokens, future service-role JWTs, the
// /api/auth/exchange Stage C retirement) — short-circuiting on JWKS
// success keeps the common case fast. KV lookups happen in-process
// after the first fetch but still cost a "kid not in cache" path.
type chainedKeyResolver struct {
	resolvers []KeyResolver
}

// NewChainedKeyResolver returns a resolver that tries each provided
// resolver in order. Last-error semantics: if every resolver fails,
// the returned error wraps the last failure.
func NewChainedKeyResolver(resolvers ...KeyResolver) KeyResolver {
	return &chainedKeyResolver{resolvers: resolvers}
}

func (c *chainedKeyResolver) PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if len(c.resolvers) == 0 {
		return nil, errors.New("no key resolvers configured")
	}
	var lastErr error
	for _, r := range c.resolvers {
		key, err := r.PublicKey(ctx, kid)
		if err == nil {
			return key, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no resolver could verify kid %q: %w", kid, lastErr)
}

// romaineLifeKeyResolver adapts the existing jwks_remote.go cache into
// the KeyResolver interface so it composes cleanly with the KV-backed
// resolver via NewChainedKeyResolver. The cache itself is package-
// global and shared with ExchangeRomaineLifeToken — both paths see
// the same key rotation.
type romaineLifeKeyResolver struct{}

// NewRomaineLifeKeyResolver returns a KeyResolver that fetches public
// keys from auth.romaine.life's JWKS endpoint (Better Auth's
// /api/auth/jwks). Caches keys for 10 minutes via the package-global
// romaineLifeJWKS cache (see jwks_remote.go).
func NewRomaineLifeKeyResolver() KeyResolver {
	return romaineLifeKeyResolver{}
}

func (romaineLifeKeyResolver) PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	return romaineLifeJWKS.getKey(ctx, authRomaineLifeJWKSURL, kid)
}
