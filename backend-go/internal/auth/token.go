package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// TokenForWebSocket extracts an auth.romaine.life JWT from WebSocket upgrade
// headers. Browsers cannot set Authorization on native WebSocket upgrades, so
// access_token is accepted as the explicit query-string carrier.
func TokenForWebSocket(r *http.Request) (string, error) {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:]), nil
	}
	if q := r.URL.Query().Get("access_token"); q != "" {
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

// RandomHex returns n random bytes encoded as 2*n lowercase hex characters.
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
