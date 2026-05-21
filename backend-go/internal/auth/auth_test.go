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

func TestVerifierRejectsMissingAuthentication(t *testing.T) {
	verifier := NewVerifier(newTestJWT(t))
	_, err := verifier.CurrentUser(httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if err == nil || ErrorStatus(err) != http.StatusUnauthorized || !strings.Contains(err.Error(), "missing authentication") {
		t.Fatalf("err = %v, status = %d", err, ErrorStatus(err))
	}
}

func TestVerifierRejectsPendingRole(t *testing.T) {
	// auth.romaine.life mints role=pending by default for fresh Microsoft
	// sign-ins. Tank-operator must refuse those tokens â€” the upstream
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

func TestVerifierRejectsUnexpectedIssuer(t *testing.T) {
	jwtKey := newTestJWT(t)
	verifier := NewVerifier(jwtKey)
	_, err := verifier.Decode(signedTestToken(t, jwtKey, "user@example.com", "user", jwt.MapClaims{
		"iss": "https://not-auth.romaine.life",
	}))
	if err == nil || ErrorStatus(err) != http.StatusUnauthorized {
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
	// Service-role token missing actor_email is unscoped â€” refuse 401.
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

func TestUser_OwnerEmail(t *testing.T) {
	cases := []struct {
		name string
		user User
		want string
	}{
		{
			name: "human",
			user: User{Email: "user@example.com", Role: RoleUser},
			want: "user@example.com",
		},
		{
			name: "admin",
			user: User{Email: "admin@example.com", Role: RoleAdmin},
			want: "admin@example.com",
		},
		{
			name: "service uses actor",
			user: User{
				Email:      "pod-94@service.tank.romaine.life",
				Role:       RoleService,
				ActorEmail: "owner@example.com",
			},
			want: "owner@example.com",
		},
		{
			name: "service missing actor falls back to principal",
			user: User{Email: "pod-94@service.tank.romaine.life", Role: RoleService},
			want: "pod-94@service.tank.romaine.life",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.OwnerEmail(); got != tc.want {
				t.Fatalf("OwnerEmail() = %q, want %q", got, tc.want)
			}
		})
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
		"iss":   authRomaineLifeIssuer,
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
