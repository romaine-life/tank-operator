package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifierAcceptsBearerToken(t *testing.T) {
	verifier := NewVerifier("secret", "USER@example.com")
	token := signedToken(t, "secret", "user@example.com")
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)

	user, err := verifier.CurrentUser(request)
	if err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
	if user.Email != "user@example.com" || user.Sub != "sub-1" || user.Name != "User" {
		t.Fatalf("user = %#v", user)
	}
}

func TestVerifierAcceptsCookie(t *testing.T) {
	verifier := NewVerifier("secret", "user@example.com")
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: signedToken(t, "secret", "user@example.com")})

	if _, err := verifier.CurrentUser(request); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
}

func TestVerifierRejectsMissingAuthentication(t *testing.T) {
	verifier := NewVerifier("secret", "user@example.com")
	_, err := verifier.CurrentUser(httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if err == nil || ErrorStatus(err) != http.StatusUnauthorized || !strings.Contains(err.Error(), "missing authentication") {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierRejectsDisallowedEmail(t *testing.T) {
	verifier := NewVerifier("secret", "allowed@example.com")
	_, err := verifier.Decode(signedToken(t, "secret", "other@example.com"))
	if err == nil || ErrorStatus(err) != http.StatusForbidden {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestGravatarURLMatchesPython(t *testing.T) {
	got := GravatarURL("  USER@Example.COM  ", 128)
	want := "https://www.gravatar.com/avatar/b58996c504c5638798eb6b511e6f49af?s=128&d=mp"
	if got != want {
		t.Fatalf("GravatarURL = %q, want %q", got, want)
	}
}

func signedToken(t *testing.T, secret, email string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "sub-1",
		"email": email,
		"name":  "User",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
