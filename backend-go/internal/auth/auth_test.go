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
	verifier := NewVerifier(jwtKey)
	token := signedTestToken(t, jwtKey, "user@example.com", "user", nil)
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)

	user, err := verifier.CurrentUser(request)
	if err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
	if user.Email != "user@example.com" || user.Sub != "sub-1" || user.Name != "User" || user.Role != "user" {
		t.Fatalf("user = %#v", user)
	}
}

func TestVerifierAcceptsCookie(t *testing.T) {
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: CookieName, Value: signedTestToken(t, jwtKey, "user@example.com", "user", nil)})

	if _, err := verifier.CurrentUser(request); err != nil {
		t.Fatalf("CurrentUser returned error: %v", err)
	}
}

func TestVerifierRejectsMissingAuthentication(t *testing.T) {
	verifier := NewVerifier(newTestJWT(t))
	_, err := verifier.CurrentUser(httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if err == nil || ErrorStatus(err) != http.StatusUnauthorized || !strings.Contains(err.Error(), "missing authentication") {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierRejectsPendingRole(t *testing.T) {
	// auth.romaine.life mints role=pending by default for fresh Microsoft
	// sign-ins. Tank-operator must refuse those tokens — the upstream
	// admin promotion via /admin is the gate.
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	_, err := verifier.Decode(signedTestToken(t, jwtKey, "user@example.com", "pending", nil))
	if err == nil || ErrorStatus(err) != http.StatusForbidden {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierRejectsMissingRole(t *testing.T) {
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	_, err := verifier.Decode(signedTestToken(t, jwtKey, "user@example.com", "", nil))
	if err == nil || ErrorStatus(err) != http.StatusForbidden {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierAcceptsAdminRole(t *testing.T) {
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	user, err := verifier.Decode(signedTestToken(t, jwtKey, "user@example.com", "admin", nil))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if user.Role != "admin" {
		t.Fatalf("role = %q, want admin", user.Role)
	}
}

func TestVerifierAcceptsServiceRoleWithActorEmail(t *testing.T) {
	// Service principals carry the human owner's email as `actor_email`.
	// See nelsong6/tank-operator#486.
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	user, err := verifier.Decode(signedTestToken(t, jwtKey,
		"pod-session-abc@service.tank.romaine.life", RoleService,
		jwt.MapClaims{"actor_email": "Owner@Example.com"},
	))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !user.IsService() {
		t.Fatalf("IsService() = false, want true")
	}
	if user.IsHuman() {
		t.Fatalf("IsHuman() = true on service role; should be false")
	}
	if user.ActorEmail != "owner@example.com" {
		t.Fatalf("ActorEmail = %q, want lowercased owner@example.com", user.ActorEmail)
	}
	if user.Email != "pod-session-abc@service.tank.romaine.life" {
		t.Fatalf("Email (principal) = %q, want synthetic", user.Email)
	}
}

func TestVerifierRejectsServiceRoleWithoutActorEmail(t *testing.T) {
	// Service-role token missing actor_email is unscoped — refuse 401.
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	_, err := verifier.Decode(signedTestToken(t, jwtKey,
		"pod-session-abc@service.tank.romaine.life", RoleService, nil,
	))
	if err == nil || ErrorStatus(err) != http.StatusUnauthorized {
		t.Fatalf("err = %v, status = %d; want 401", err, ErrorStatus(err))
	}
}

func TestUser_IsHuman_IsService_ConvenienceHelpers(t *testing.T) {
	cases := []struct {
		role          string
		wantIsHuman   bool
		wantIsService bool
	}{
		{RoleAdmin, true, false},
		{RoleUser, true, false},
		{RoleService, false, true},
		{"", false, false},
		{"pending", false, false},
	}
	for _, tc := range cases {
		u := User{Role: tc.role}
		if got := u.IsHuman(); got != tc.wantIsHuman {
			t.Errorf("role=%q IsHuman() = %v, want %v", tc.role, got, tc.wantIsHuman)
		}
		if got := u.IsService(); got != tc.wantIsService {
			t.Errorf("role=%q IsService() = %v, want %v", tc.role, got, tc.wantIsService)
		}
	}
}

func TestVerifierRejectsHS256Tokens(t *testing.T) {
	verifier := NewVerifier(newTestJWT(t))
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   "sub-1",
		"email": "user@example.com",
		"role":  "user",
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
	minter := NewMinter(jwtKey, jwtKey)
	tok, err := minter.MintSession("sub-1", "user@example.com", "User", "user")
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(jwtKey)
	got, err := verifier.Decode(tok)
	if err != nil {
		t.Fatalf("minted token did not verify: %v", err)
	}
	if got.Email != "user@example.com" || got.Sub != "sub-1" || got.Role != "user" {
		t.Fatalf("user = %#v", got)
	}
}

func TestMinterIssuesGitHubMCPAttestation(t *testing.T) {
	jwtKey := newTestJWT(t)
	minter := NewMinter(jwtKey, jwtKey)
	installationID := int64(42)
	tok, expiresAt, err := minter.MintGitHubMCPAttestation(GitHubMCPAttestationSubject{
		Email:          "USER@example.com",
		InstallationID: &installationID,
		IsHost:         false,
		IsSuperAdmin:   true,
		SessionScope:   "slot-a",
		SessionID:      "12",
		PodName:        "session-12",
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(expiresAt) <= 0 || time.Until(expiresAt) > GitHubMCPAttestationTTL {
		t.Fatalf("expiresAt = %s", expiresAt)
	}

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, func(token *jwt.Token) (any, error) {
		return jwtKey.PublicKey(context.Background(), "test-kid")
	}, jwt.WithAudience(GitHubMCPAttestationAudience), jwt.WithIssuer("tank-operator"))
	if err != nil || !parsed.Valid {
		t.Fatalf("attestation did not verify: token=%v err=%v", parsed, err)
	}
	if got, want := claims["owner_email"], "user@example.com"; got != want {
		t.Fatalf("owner_email = %v, want %q", got, want)
	}
	if got, want := claims["github_installation_id"], float64(42); got != want {
		t.Fatalf("github_installation_id = %v, want %v", got, want)
	}
	if got, want := claims["session_scope"], "slot-a"; got != want {
		t.Fatalf("session_scope = %v, want %q", got, want)
	}
}

func TestMinterPublishesJWKS(t *testing.T) {
	jwtKey := newTestJWT(t)
	minter := NewMinter(jwtKey, jwtKey)
	jwks, err := minter.PublicJWKS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 1 {
		t.Fatalf("jwks key count = %d, want 1", len(jwks.Keys))
	}
	key := jwks.Keys[0]
	if key.Kid != "test-kid" || key.Kty != "RSA" || key.Alg != "RS256" || key.N == "" || key.E == "" {
		t.Fatalf("unexpected jwk = %#v", key)
	}
}

func TestInstallStateRoundtrips(t *testing.T) {
	jwtKey := newTestJWT(t)
	minter := NewMinter(jwtKey, jwtKey)
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
	minter := NewMinter(jwtKey, jwtKey)
	sessionTok, err := minter.MintSession("sub-1", "user@example.com", "User", "user")
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

func signedTestToken(t *testing.T, jwtKey *InMemoryJWT, email, role string, extra jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":   "sub-1",
		"email": email,
		"name":  "User",
		"role":  role,
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
