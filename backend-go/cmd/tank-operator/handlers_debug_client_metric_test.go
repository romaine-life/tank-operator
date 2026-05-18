package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

// TestClientMetricAcceptsAllowlistedName confirms the beacon endpoint
// translates the SPA's allowlisted metric name into a 204 and (by
// proxy) a counter increment. The exact counter assertion lives in
// the package-level promauto registration; the handler-side test is
// the wire-contract gate.
func TestClientMetricAcceptsAllowlistedName(t *testing.T) {
	srv := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/debug/client-metric",
		strings.NewReader(`{"name":"session_list.placeholder_synthesized"}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "u@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	srv.handleClientMetric(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", resp.Code, resp.Body.String())
	}
}

// TestClientMetricRejectsUnknownName is the cardinality gate. The
// endpoint must never accept an arbitrary metric name — that would let
// the SPA register unbounded Prometheus counters. New SPA-side
// counters require an explicit server-side allowlist entry in
// handlers_debug_client_metric.go.
func TestClientMetricRejectsUnknownName(t *testing.T) {
	srv := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/debug/client-metric",
		strings.NewReader(`{"name":"arbitrary_unregistered_name"}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "u@example.com"))
	resp := httptest.NewRecorder()

	srv.handleClientMetric(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown metric names must be rejected to keep cardinality bounded)", resp.Code)
	}
}

// TestClientMetricRequiresAuth confirms the beacon is not anonymous.
// An unauthenticated push would let any visitor inflate the counter
// and mask the real regression signal.
func TestClientMetricRequiresAuth(t *testing.T) {
	srv := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/debug/client-metric",
		strings.NewReader(`{"name":"session_list.placeholder_synthesized"}`))
	resp := httptest.NewRecorder()

	srv.handleClientMetric(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (anonymous push must be rejected)", resp.Code)
	}
}
