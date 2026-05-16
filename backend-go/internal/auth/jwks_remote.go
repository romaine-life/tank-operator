package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
}

var romaineLifeJWKS = &jwksCache{
	httpClient: &http.Client{Timeout: jwksHTTPTimeout},
}

func (c *jwksCache) getKey(ctx context.Context, url, kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	if time.Since(c.fetchedAt) < jwksCacheTTL {
		if key, ok := c.keys[kid]; ok {
			c.mu.RUnlock()
			return key, nil
		}
		c.mu.RUnlock()
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < jwksCacheTTL {
		if key, ok := c.keys[kid]; ok {
			return key, nil
		}
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	if err := c.refresh(ctx, url); err != nil {
		return nil, err
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

// ExchangeRomaineLifeToken verifies a JWT issued by auth.romaine.life and
// returns the user identity. The allowedEmails check is the tank-operator-
// specific access gate: auth.romaine.life mints tokens for any user it has
// onboarded; this service decides which of those users may use it.
func ExchangeRomaineLifeToken(ctx context.Context, tokenString, allowedEmails string) (email, name, sub string, err error) {
	allowed := map[string]struct{}{}
	for _, e := range strings.Split(allowedEmails, ",") {
		normalized := strings.ToLower(strings.TrimSpace(e))
		if normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "RS256" {
			return nil, fmt.Errorf("unexpected alg: %s", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token missing kid")
		}
		return romaineLifeJWKS.getKey(ctx, authRomaineLifeJWKSURL, kid)
	}, jwt.WithLeeway(60*time.Second))
	if err != nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid token")
		}
		return "", "", "", errHTTP{status: http.StatusUnauthorized, message: "invalid auth.romaine.life token: " + err.Error()}
	}

	iss, _ := claims["iss"].(string)
	if iss != authRomaineLifeIssuer {
		return "", "", "", errHTTP{status: http.StatusUnauthorized, message: "unexpected issuer: " + iss}
	}

	rawEmail, _ := claims["email"].(string)
	rawEmail = strings.ToLower(strings.TrimSpace(rawEmail))
	if rawEmail == "" {
		return "", "", "", errHTTP{status: http.StatusUnauthorized, message: "token missing email claim"}
	}
	if _, ok := allowed[rawEmail]; !ok {
		return "", "", "", errHTTP{status: http.StatusForbidden, message: "email not in tank-operator allowlist"}
	}

	rawName, _ := claims["name"].(string)
	rawSub, _ := claims["sub"].(string)
	return rawEmail, rawName, rawSub, nil
}
