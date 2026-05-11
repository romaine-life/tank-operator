package auth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const CookieName = "auth_token"

// keyResolveTimeout caps how long a verify call can spend fetching a missing
// public key from KV. Verify is on the request path; a stalled KV call must
// not block forever.
const keyResolveTimeout = 5 * time.Second

type User struct {
	Sub   string
	Email string
	Name  string
}

type Verifier struct {
	resolver      KeyResolver
	allowedEmails map[string]struct{}
}

func NewVerifier(resolver KeyResolver, allowedEmails string) *Verifier {
	allowed := map[string]struct{}{}
	for _, email := range strings.Split(allowedEmails, ",") {
		normalized := strings.ToLower(strings.TrimSpace(email))
		if normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return &Verifier{resolver: resolver, allowedEmails: allowed}
}

func (v *Verifier) CurrentUser(r *http.Request) (User, error) {
	token, err := tokenFromRequest(r)
	if err != nil {
		return User{}, err
	}
	return v.Decode(token)
}

func (v *Verifier) Decode(tokenString string) (User, error) {
	if v.resolver == nil {
		return User{}, errHTTP{status: http.StatusInternalServerError, message: "JWT key resolver not configured"}
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token missing kid")
		}
		ctx, cancel := context.WithTimeout(context.Background(), keyResolveTimeout)
		defer cancel()
		return v.resolver.PublicKey(ctx, kid)
	})
	if err != nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid token")
		}
		return User{}, errHTTP{status: http.StatusUnauthorized, message: "invalid session token: " + err.Error()}
	}

	email := strings.ToLower(stringClaim(claims, "email"))
	if email == "" {
		return User{}, errHTTP{status: http.StatusUnauthorized, message: "invalid session token: missing email"}
	}
	if _, ok := v.allowedEmails[email]; !ok {
		return User{}, errHTTP{status: http.StatusForbidden, message: "email no longer allowed"}
	}
	return User{
		Sub:   stringClaim(claims, "sub"),
		Email: email,
		Name:  stringClaim(claims, "name"),
	}, nil
}

func tokenFromRequest(r *http.Request) (string, error) {
	if authorization := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return strings.TrimSpace(authorization[7:]), nil
	}
	if cookie, err := r.Cookie(CookieName); err == nil && cookie.Value != "" {
		return cookie.Value, nil
	}
	return "", errHTTP{status: http.StatusUnauthorized, message: "missing authentication"}
}

func stringClaim(claims jwt.MapClaims, name string) string {
	value, _ := claims[name].(string)
	return value
}

func GravatarURL(email string, size int) string {
	if size <= 0 {
		size = 64
	}
	normalized := strings.ToLower(strings.TrimSpace(email))
	sum := md5.Sum([]byte(normalized))
	return fmt.Sprintf("https://www.gravatar.com/avatar/%s?s=%d&d=mp", hex.EncodeToString(sum[:]), size)
}

type errHTTP struct {
	status  int
	message string
}

func (e errHTTP) Error() string {
	return e.message
}

func ErrorStatus(err error) int {
	var httpErr errHTTP
	if errors.As(err, &httpErr) {
		return httpErr.status
	}
	return http.StatusInternalServerError
}
