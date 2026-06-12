package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// auth.romaine.life is the single upstream identity provider. Its Better
// Auth JWT plugin publishes RS256 keys at /api/auth/jwks; the issuer claim
// is the service's baseURL.
const (
	authRomaineLifeJWKSURL = "https://auth.romaine.life/api/auth/jwks"
	authRomaineLifeIssuer  = "https://auth.romaine.life"
	jwksCacheTTL           = 10 * time.Minute
	jwksHTTPTimeout        = 10 * time.Second
)

type jwksKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksResponse struct {
	Keys []jwksKey `json:"keys"`
}

type jwksCache struct {
	mu         sync.RWMutex
	keys       map[string]*rsa.PublicKey
	fetchedAt  time.Time
	httpClient *http.Client
	kidMissRefreshAt time.Time
}

var romaineLifeJWKS = &jwksCache{
	httpClient: &http.Client{Timeout: jwksHTTPTimeout},
}

// jwksKidMissRefreshInterval rate-limits refresh-on-unknown-kid. A kid
// miss inside the cache TTL is either a freshly rotated signing key (the
// break-glass `az keyvault key rotate auth-jwt-signing` path and normal
// rotation both mint tokens the stale cache rejects — previously a
// hard auth outage for up to the full 10-minute TTL, issue #1079) or
// attacker-supplied garbage. One refresh per interval picks rotations up
// in seconds while keeping forged-kid spam from becoming an upstream
// fetch amplifier.
const jwksKidMissRefreshInterval = 30 * time.Second

func (c *jwksCache) getKey(ctx context.Context, url, kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	if time.Since(c.fetchedAt) < jwksCacheTTL {
		if key, ok := c.keys[kid]; ok {
			c.mu.RUnlock()
			return key, nil
		}
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < jwksCacheTTL {
		return key, nil
	}
	// Refresh when the TTL lapsed OR the kid is unknown (rate-limited):
	// a rotated key must authenticate within seconds, not after the TTL.
	if time.Since(c.fetchedAt) >= jwksCacheTTL || time.Since(c.kidMissRefreshAt) >= jwksKidMissRefreshInterval {
		c.kidMissRefreshAt = time.Now()
		if err := c.refresh(ctx, url); err != nil {
			return nil, err
		}
	}
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown kid %q after refresh", kid)
}

func (c *jwksCache) refresh(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("JWKS request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("JWKS fetch: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("JWKS read: %w", err)
	}
	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("JWKS parse: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	c.keys = keys
	c.fetchedAt = time.Now()
	return nil
}

func rsaPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	decode := func(s string) ([]byte, error) {
		s = strings.ReplaceAll(s, "-", "+")
		s = strings.ReplaceAll(s, "_", "/")
		switch len(s) % 4 {
		case 2:
			s += "=="
		case 3:
			s += "="
		}
		return base64.StdEncoding.DecodeString(s)
	}
	nb, err := decode(nB64)
	if err != nil {
		return nil, err
	}
	eb, err := decode(eB64)
	if err != nil {
		return nil, err
	}
	eVal := 0
	for _, b := range eb {
		eVal = eVal<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: eVal}, nil
}
