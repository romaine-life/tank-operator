package auth

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

const CookieName = "auth_token"

type User struct {
	Sub   string
	Email string
	Name  string
}

type Verifier struct {
	secret        []byte
	allowedEmails map[string]struct{}
}

func NewVerifier(secret, allowedEmails string) *Verifier {
	allowed := map[string]struct{}{}
	for _, email := range strings.Split(allowedEmails, ",") {
		normalized := strings.ToLower(strings.TrimSpace(email))
		if normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return &Verifier{secret: []byte(secret), allowedEmails: allowed}
}

func (v *Verifier) CurrentUser(r *http.Request) (User, error) {
	token, err := tokenFromRequest(r)
	if err != nil {
		return User{}, err
	}
	return v.Decode(token)
}

func (v *Verifier) Decode(tokenString string) (User, error) {
	if len(v.secret) == 0 {
		return User{}, errHTTP{status: http.StatusInternalServerError, message: "JWT_SECRET not configured"}
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		}
		return v.secret, nil
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
