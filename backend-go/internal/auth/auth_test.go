package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifierAcceptsBearerToken(t *testing.T) {
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey, "USER@example.com")
	token := signedTestToken(t, jwtKey, "user@example.com", nil)
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
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey, "user@example.com")
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: signedTestToken(t, jwtKey, "user@example.com", nil)})

	if _, err := verifier.CurrentUser(request); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
}

func TestVerifierRejectsMissingAuthentication(t *testing.T) {
	verifier := NewVerifier(newTestJWT(t), "user@example.com")
	_, err := verifier.CurrentUser(httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if err == nil || ErrorStatus(err) != http.StatusUnauthorized || !strings.Contains(err.Error(), "missing authentication") {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierRejectsDisallowedEmail(t *testing.T) {
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey, "allowed@example.com")
	_, err := verifier.Decode(signedTestToken(t, jwtKey, "other@example.com", nil))
	if err == nil || ErrorStatus(err) != http.StatusForbidden {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierRejectsHS256Tokens(t *testing.T) {
	verifier := NewVerifier(newTestJWT(t), "user@example.com")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "sub-1",
		"email": "user@example.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	hs256, err := tok.SignedString([]byte("hs256-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Decode(hs256); err == nil || ErrorStatus(err) != http.StatusUnauthorized {
		t.Fatalf("HS256 token accepted; want 401. err = %v", err)
	}
}

func TestMinterIssuesVerifiableSession(t *testing.T) {
	jwtKey := newTestJWT(t)
	minter := NewMinter(jwtKey, jwtKey, "user@example.com")
	tok, err := minter.MintSession("sub-1", "user@example.com", "User")
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(jwtKey, "user@example.com")
	got, err := verifier.Decode(tok)
	if err != nil {
		t.Fatalf("minted token did not verify: %v", err)
	}
	if got.Email != "user@example.com" || got.Sub != "sub-1" {
		t.Fatalf("user = %#v", got)
	}
}

func TestInstallStateRoundtrips(t *testing.T) {
	jwtKey := newTestJWT(t)
	minter := NewMinter(jwtKey, jwtKey, "user@example.com")
	tok, err := minter.MintInstallState("user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	email, err := minter.VerifyInstallState(tok)
	if err != nil {
		t.Fatal(err)
	}
	if email != "user@example.com" {
		t.Fatalf("email = %q, want %q", email, "user@example.com")
	}
}

func TestInstallStateRejectsSessionTokenWithDifferentAudience(t *testing.T) {
	jwtKey := newTestJWT(t)
	minter := NewMinter(jwtKey, jwtKey, "user@example.com")
	sessionTok, err := minter.MintSession("sub-1", "user@example.com", "User")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := minter.VerifyInstallState(sessionTok); err == nil {
		t.Fatal("session token accepted as install-state token; audience check broken")
	}
}

func TestGravatarURLMatchesPython(t *testing.T) {
	got := GravatarURL("  USER@Example.COM  ", 128)
	want := "https://www.gravatar.com/avatar/b58996c504c5638798eb6b511e6f49af?s=128&d=mp"
	if got != want {
		t.Fatalf("GravatarURL = %q, want %q", got, want)
	}
}

func newTestJWT(t *testing.T) *InMemoryJWT {
	t.Helper()
	j, err := NewInMemoryJWT("test-kid")
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func signedTestToken(t *testing.T, jwtKey *InMemoryJWT, email string, extra jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":   "sub-1",
		"email": email,
		"name":  "User",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	tok, err := jwtKey.MintJWT(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
