package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

func TestDefaultAuthExchangeURLMatchesPlatformExchange(t *testing.T) {
	if got, want := defaultAuthExchangeURL, "https://auth.romaine.life/api/auth/exchange/k8s"; got != want {
		t.Fatalf("defaultAuthExchangeURL = %q, want %q", got, want)
	}
}

func TestAuthExchangeSessionResolverProductionAuthority(t *testing.T) {
	signer := testAuthSigner(t)
	authToken := mintServiceToken(t, signer, "svc:tank:908", "pod-908@service.tank.romaine.life")
	resolver, seenToken := testAuthExchangeResolver(t, signer, authToken, http.StatusOK)

	key, err := resolver.SessionStorageKeyFromToken(context.Background(), "projected-sa-token")
	if err != nil {
		t.Fatalf("SessionStorageKeyFromToken: %v", err)
	}
	if key != "908" {
		t.Fatalf("storage key = %q, want 908", key)
	}
	if *seenToken != "projected-sa-token" {
		t.Fatalf("exchange bearer = %q, want projected-sa-token", *seenToken)
	}
}

func TestAuthExchangeSessionResolverGlimmungSlotAuthority(t *testing.T) {
	signer := testAuthSigner(t)
	authToken := mintServiceToken(t, signer, "svc:tank:slot-5-session-175", "pod-slot-5-session-175@service.tank.romaine.life")
	resolver, _ := testAuthExchangeResolver(t, signer, authToken, http.StatusOK)

	key, err := resolver.SessionStorageKeyFromToken(context.Background(), "slot-projected-token")
	if err != nil {
		t.Fatalf("SessionStorageKeyFromToken: %v", err)
	}
	if key != "tank-operator-slot-5:175" {
		t.Fatalf("storage key = %q, want tank-operator-slot-5:175", key)
	}
}

func TestStorageKeyFromAuthUserRejectsNonTankServicePrincipal(t *testing.T) {
	_, err := storageKeyFromAuthUser(auth.User{
		Sub:        "svc:tank-operator:orchestrator",
		Email:      "pod-orchestrator@service.tank-operator.romaine.life",
		Role:       auth.RoleService,
		ActorEmail: "user@example.com",
	})
	assertDenyResult(t, err, "denied_subject_untrusted")
}

func TestStorageKeyFromAuthUserRejectsEmailSubjectMismatch(t *testing.T) {
	_, err := storageKeyFromAuthUser(auth.User{
		Sub:        "svc:tank:908",
		Email:      "pod-909@service.tank.romaine.life",
		Role:       auth.RoleService,
		ActorEmail: "user@example.com",
	})
	assertDenyResult(t, err, "denied_subject_untrusted")
}

func TestAuthExchangeSessionResolverRejectsHumanJWT(t *testing.T) {
	signer := testAuthSigner(t)
	authToken := mintToken(t, signer, map[string]any{
		"sub":   "human-1",
		"email": "user@example.com",
		"name":  "User",
		"role":  auth.RoleUser,
	})
	resolver, _ := testAuthExchangeResolver(t, signer, authToken, http.StatusOK)

	_, err := resolver.SessionStorageKeyFromToken(context.Background(), "projected-sa-token")
	assertDenyResult(t, err, "denied_subject_untrusted")
}

func TestAuthExchangeSessionResolverRejectsExchangeFailure(t *testing.T) {
	signer := testAuthSigner(t)
	resolver, _ := testAuthExchangeResolver(t, signer, "", http.StatusForbidden)

	_, err := resolver.SessionStorageKeyFromToken(context.Background(), "projected-sa-token")
	assertDenyResult(t, err, "denied_auth_exchange")
}

func testAuthSigner(t *testing.T) *auth.InMemoryJWT {
	t.Helper()
	signer, err := auth.NewInMemoryJWT("test-kid")
	if err != nil {
		t.Fatalf("NewInMemoryJWT: %v", err)
	}
	return signer
}

func testAuthExchangeResolver(t *testing.T, signer *auth.InMemoryJWT, token string, status int) (*authExchangeSessionResolver, *string) {
	t.Helper()
	seenToken := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("exchange method = %s, want POST", r.Method)
		}
		seenToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"detail":"denied"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
	}))
	t.Cleanup(srv.Close)
	return &authExchangeSessionResolver{
		http:        srv.Client(),
		exchangeURL: srv.URL,
		verifier:    auth.NewVerifier(signer),
	}, &seenToken
}

func mintServiceToken(t *testing.T, signer *auth.InMemoryJWT, sub, email string) string {
	t.Helper()
	return mintToken(t, signer, map[string]any{
		"sub":         sub,
		"email":       email,
		"name":        "Service",
		"role":        auth.RoleService,
		"actor_email": "user@example.com",
	})
}

func mintToken(t *testing.T, signer *auth.InMemoryJWT, claims map[string]any) string {
	t.Helper()
	now := time.Now()
	payload := jwtlib.MapClaims{
		"iss": "https://auth.romaine.life",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range claims {
		payload[k] = v
	}
	token, err := signer.MintJWT(context.Background(), payload)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}
	return token
}

func assertDenyResult(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", want)
	}
	var denied calloutDenyError
	if !errors.As(err, &denied) {
		t.Fatalf("error %v is not calloutDenyError", err)
	}
	if denied.result != want {
		t.Fatalf("deny result = %q, want %q (err=%v)", denied.result, want, err)
	}
}
