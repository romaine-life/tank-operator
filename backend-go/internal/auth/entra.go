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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	entraJWKSURL  = "https://login.microsoftonline.com/common/discovery/v2.0/keys"
	jwksCacheTTL  = 10 * time.Minute
	clientTimeout = 10 * time.Second
)

var issuerPattern = regexp.MustCompile(`^https://login\.microsoftonline\.com/.+/v2\.0$`)

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

var globalJWKS = &jwksCache{
	httpClient: &http.Client{Timeout: clientTimeout},
}

func (c *jwksCache) getKey(kid string) (*rsa.PublicKey, error) {
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
	if err := c.refresh(); err != nil {
		return nil, err
	}
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown kid %q after refresh", kid)
}

func (c *jwksCache) refresh() error {
	resp, err := c.httpClient.Get(entraJWKSURL)
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

// ExchangeEntraToken validates an Entra ID token and returns (email, name, sub) or an error.
// clientID is used as the expected audience.
func ExchangeEntraToken(ctx context.Context, idToken, clientID, allowedEmails string) (email, name, sub string, err error) {
	allowed := map[string]struct{}{}
	for _, e := range strings.Split(allowedEmails, ",") {
		normalized := strings.ToLower(strings.TrimSpace(e))
		if normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(idToken, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "RS256" {
			return nil, fmt.Errorf("unexpected alg: %s", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		return globalJWKS.getKey(kid)
	}, jwt.WithLeeway(60*time.Second))
	if err != nil || !token.Valid {
		if err == nil {
			err = fmt.Errorf("invalid token")
		}
		return "", "", "", errHTTP{status: http.StatusUnauthorized, message: "invalid Entra token: " + err.Error()}
	}

	// Validate issuer
	iss, _ := claims["iss"].(string)
	if !issuerPattern.MatchString(iss) {
		return "", "", "", errHTTP{status: http.StatusUnauthorized, message: "invalid issuer: " + iss}
	}

	// Extract email
	rawEmail, _ := claims["email"].(string)
	if rawEmail == "" {
		rawEmail, _ = claims["preferred_username"].(string)
	}
	rawEmail = strings.ToLower(strings.TrimSpace(rawEmail))
	if rawEmail == "" {
		return "", "", "", errHTTP{status: http.StatusUnauthorized, message: "token missing email claim"}
	}

	if _, ok := allowed[rawEmail]; !ok {
		return "", "", "", errHTTP{status: http.StatusForbidden, message: "email not in allowlist"}
	}

	rawName, _ := claims["name"].(string)
	rawSub, _ := claims["sub"].(string)
	return rawEmail, rawName, rawSub, nil
}
