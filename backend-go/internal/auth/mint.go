package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	SessionTTL       = 7 * 24 * time.Hour
	installStateTTL  = 10 * time.Minute
	installAudience  = "tank-operator/github-install"
	mintTimeout      = 10 * time.Second
)

// Minter signs session and install-state JWTs via a remote Signer (Key Vault
// in prod, in-memory in tests). Holds the verifier's KeyResolver too so it
// can validate the install-state tokens it issued.
type Minter struct {
	signer   Signer
	resolver KeyResolver
}

func NewMinter(signer Signer, resolver KeyResolver) *Minter {
	return &Minter{signer: signer, resolver: resolver}
}

// MintSession stamps a tank-operator session JWT. Role rides along so every
// protected endpoint can verify against this service's signing key alone —
// no round-trip to auth.romaine.life on the read path. Exchange is what
// pulls the role from the upstream JWT once; from then on the local key is
// authoritative for the session window.
func (m *Minter) MintSession(sub, email, name, role string) (string, error) {
	if m.signer == nil {
		return "", fmt.Errorf("JWT signer not configured")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":   sub,
		"email": strings.ToLower(strings.TrimSpace(email)),
		"name":  name,
		"role":  role,
		"iat":   now.Unix(),
		"exp":   now.Add(SessionTTL).Unix(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), mintTimeout)
	defer cancel()
	return m.signer.MintJWT(ctx, claims)
}

func (m *Minter) PublicJWKS(ctx context.Context) (JWKS, error) {
	provider, ok := m.signer.(JWKProvider)
	if !ok {
		return JWKS{}, fmt.Errorf("JWT signer does not expose a public JWK")
	}
	jwk, err := provider.CurrentJWK(ctx)
	if err != nil {
		return JWKS{}, err
	}
	return JWKS{Keys: []JWK{jwk}}, nil
}

func (m *Minter) MintInstallState(email string) (string, error) {
	if m.signer == nil {
		return "", fmt.Errorf("JWT signer not configured")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"email": strings.ToLower(strings.TrimSpace(email)),
		"aud":   installAudience,
		"iat":   now.Unix(),
		"exp":   now.Add(installStateTTL).Unix(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), mintTimeout)
	defer cancel()
	return m.signer.MintJWT(ctx, claims)
}

func (m *Minter) VerifyInstallState(state string) (string, error) {
	if m.resolver == nil {
		return "", errHTTP{status: http.StatusInternalServerError, message: "JWT key resolver not configured"}
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(state, claims,
		func(t *jwt.Token) (any, error) {
			if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
				return nil, fmt.Errorf("unexpected alg: %s", t.Method.Alg())
			}
			kid, _ := t.Header["kid"].(string)
			if kid == "" {
				return nil, errors.New("token missing kid")
			}
			ctx, cancel := context.WithTimeout(context.Background(), keyResolveTimeout)
			defer cancel()
			return m.resolver.PublicKey(ctx, kid)
		},
		jwt.WithAudience(installAudience),
	)
	if err != nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid token")
		}
		return "", errHTTP{status: http.StatusBadRequest, message: "invalid install state: " + err.Error()}
	}
	email, _ := claims["email"].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", errHTTP{status: http.StatusBadRequest, message: "install state missing email"}
	}
	return email, nil
}

// TokenForWebSocket extracts the auth token from WebSocket upgrade headers,
// including cookie, Authorization header, or query param.
func TokenForWebSocket(r *http.Request) (string, error) {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:]), nil
	}
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value, nil
	}
	if q := r.URL.Query().Get("access_token"); q != "" {
		return q, nil
	}
	if q := r.URL.Query().Get("auth_token"); q != "" {
		return q, nil
	}
	return "", errHTTP{status: http.StatusUnauthorized, message: "missing authentication"}
}

// CurrentUserFromWebSocket validates the token from a WebSocket upgrade request.
func (v *Verifier) CurrentUserFromWebSocket(r *http.Request) (User, error) {
	tok, err := TokenForWebSocket(r)
	if err != nil {
		return User{}, err
	}
	return v.Decode(tok)
}

// RandomHex returns n random hex bytes (2*n characters).
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
