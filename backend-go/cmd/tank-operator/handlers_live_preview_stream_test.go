package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// TestHandleInternalLivePreviewStream_AuthGate pins the auth gate on the
// in-pod live-preview daemon's control channel. The stream must reject anything
// but a session pod streaming its OWN session (requireServicePrincipal +
// internalCallerMatchesSession, the #1207 invariant). These paths reject before
// touching the manager or session bus, so a minimal appServer suffices.
func TestHandleInternalLivePreviewStream_AuthGate(t *testing.T) {
	jwtKey, err := auth.NewInMemoryJWT("svc-test-kid")
	if err != nil {
		t.Fatal(err)
	}
	server := &appServer{verifier: auth.NewVerifier(jwtKey)}

	// mint builds a JWT with the given role and subject. A nil sub keeps the
	// default per-session subject.
	mint := func(t *testing.T, role, sub string, extra jwt.MapClaims) string {
		t.Helper()
		claims := jwt.MapClaims{
			"sub":   sub,
			"email": "pod@service.tank.romaine.life",
			"iss":   "https://auth.romaine.life",
			"name":  "Service: tank pod",
			"role":  role,
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

	const sessionID = "session-x"
	newReq := func(auth string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/"+sessionID+"/live-preview/stream", nil)
		req.SetPathValue("session_id", sessionID)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		return req
	}

	t.Run("missing bearer → 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.handleInternalLivePreviewStream(rec, newReq(""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("role=user → 403", func(t *testing.T) {
		rec := httptest.NewRecorder()
		server.handleInternalLivePreviewStream(rec, newReq(mint(t, "user", "svc:tank:"+sessionID, nil)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("service principal for a DIFFERENT session → 403", func(t *testing.T) {
		// Valid role=service + actor_email, but the verified per-session subject
		// encodes another session, so internalCallerMatchesSession must reject
		// (a pod may stream only its own session).
		rec := httptest.NewRecorder()
		tok := mint(t, "service", "svc:tank:some-other-session", jwt.MapClaims{"actor_email": "owner@example.com"})
		server.handleInternalLivePreviewStream(rec, newReq(tok))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}
