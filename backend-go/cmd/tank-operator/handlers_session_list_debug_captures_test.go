package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

func TestSessionListDebugCaptureRejectsServicePrincipal(t *testing.T) {
	app := adminTestServer(t)
	body := strings.NewReader(validSessionListDebugCaptureBody())
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-list-debug-capture", body)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "svc@example.com", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleSessionListDebugCapture(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for service principal; body = %s", resp.Code, resp.Body.String())
	}
}

func TestSessionListDebugCaptureRejectsInvalidJSON(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-list-debug-capture", strings.NewReader(`{"reason":`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleSessionListDebugCapture(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
}

func TestSessionListDebugCaptureRejectsMissingRequiredFields(t *testing.T) {
	app := adminTestServer(t)
	body := bytes.NewBufferString(`{"snapshot":{"version":1},"detail":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-list-debug-capture", body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleSessionListDebugCapture(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "reason and session_id") {
		t.Fatalf("error body should explain missing fields; got %s", resp.Body.String())
	}
}

func TestSessionListDebugCaptureRejectsNonObjectSnapshot(t *testing.T) {
	app := adminTestServer(t)
	body := bytes.NewBufferString(`{"reason":"manual-capture","session_id":"223","snapshot":[],"detail":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-list-debug-capture", body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleSessionListDebugCapture(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
}

func TestSessionListDebugCaptureRequiresPostgresStore(t *testing.T) {
	app := adminTestServer(t)
	body := strings.NewReader(validSessionListDebugCaptureBody())
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-list-debug-capture", body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleSessionListDebugCapture(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when pgPool is unset; body = %s", resp.Code, resp.Body.String())
	}
}

func TestDebugSessionListCapturesAdminGateAndStoreRequirement(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"

	t.Run("non-admin role 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-list-captures", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
		resp := httptest.NewRecorder()

		app.handleDebugSessionListCaptures(resp, req)

		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("admin without postgres gets 503", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-list-captures", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		resp := httptest.NewRecorder()

		app.handleDebugSessionListCaptures(resp, req)

		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("invalid limit rejected before store access", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-list-captures?limit=0", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		resp := httptest.NewRecorder()

		app.handleDebugSessionListCaptures(resp, req)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
		}
	})
}

func TestSessionListDebugCaptureLabelClamp(t *testing.T) {
	if got := sessionListDebugCaptureReasonLabel("manual-capture"); got != "manual-capture" {
		t.Fatalf("known reason label = %q", got)
	}
	if got := sessionListDebugCaptureReasonLabel("raw-user-supplied-value"); got != "other" {
		t.Fatalf("unknown reason should clamp to other, got %q", got)
	}
	if got := sessionListDebugCaptureResultLabel("store_error"); got != "store_error" {
		t.Fatalf("known result label = %q", got)
	}
	if got := sessionListDebugCaptureResultLabel("anything"); got != "other" {
		t.Fatalf("unknown result should clamp to other, got %q", got)
	}
	if got := sessionListDebugCaptureReadResultLabel("forbidden"); got != "forbidden" {
		t.Fatalf("known read result label = %q", got)
	}
	if got := sessionListDebugCaptureReadResultLabel("anything"); got != "other" {
		t.Fatalf("unknown read result should clamp to other, got %q", got)
	}
}

func validSessionListDebugCaptureBody() string {
	return `{
			"reason":"manual-capture",
			"session_id":"223",
			"source":"SessionListDebugPage",
			"active_id":"223",
			"client_seq":12,
			"snapshot":{"version":1,"events":[]},
			"detail":{"phase":"capture-now"}
		}`
}
