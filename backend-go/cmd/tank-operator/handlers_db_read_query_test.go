package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// dbReadQueryServer builds an appServer that accepts a service-principal token
// and serves a session pod (restricted or not). pgPool is intentionally nil so
// the gating paths (auth + restricted check) are unit-testable without a DB.
func dbReadQueryServer(t *testing.T, sessionID string, restricted bool) (*appServer, string) {
	t.Helper()
	jwtKey, err := auth.NewInMemoryJWT("svc-db-kid")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwtKey.MintJWT(context.Background(), jwt.MapClaims{
		"sub":         "svc:tank:db",
		"email":       "pod-db@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"role":        "service",
		"actor_email": "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var env []corev1.EnvVar
	if restricted {
		env = []corev1.EnvVar{{Name: "TANK_RESTRICTED_GIT", Value: "true"}}
	}
	k8s := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-" + sessionID,
			Namespace: "tank-operator-sessions",
			UID:       types.UID("pod-" + sessionID),
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "tank-operator", "tank-operator/session-id": sessionID},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "claude-session",
			Containers:         []corev1.Container{{Name: "sandbox", Env: env}},
		},
	})
	return &appServer{
		k8s:                   k8s,
		namespace:             "tank-operator-sessions",
		sessionScope:          "default",
		sessionServiceAccount: "claude-session",
		verifier:              auth.NewVerifier(jwtKey),
	}, tok
}

func postDBReadQuery(t *testing.T, s *appServer, tok, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/"+sessionID+"/db-read-query", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.SetPathValue("session_id", sessionID)
	rec := httptest.NewRecorder()
	s.handleInternalSessionDBReadQuery(rec, req)
	return rec
}

func TestDBReadQuery_RestrictedRefused(t *testing.T) {
	s, tok := dbReadQueryServer(t, "55", true)
	rec := postDBReadQuery(t, s, tok, "55", `{"sql":"SELECT 1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "restricted") {
		t.Fatalf("body should explain the restricted refusal: %s", rec.Body.String())
	}
}

func TestDBReadQuery_NonRestrictedRequiresPool(t *testing.T) {
	s, tok := dbReadQueryServer(t, "56", false) // pgPool nil → 503 after the non-restricted check passes
	rec := postDBReadQuery(t, s, tok, "56", `{"sql":"SELECT 1"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rec.Code, rec.Body.String())
	}
}
