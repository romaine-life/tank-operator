package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

func TestSessionEventStreamMetricsAcceptsValidBatch(t *testing.T) {
	app := adminTestServer(t)
	body := bytes.NewBufferString(`{"events":[
			{"event":"opened","sessionMode":"claude_gui"},
			{"event":"transcript_rows_received","eventType":"transcript_rows","sessionMode":"claude_gui"},
			{"event":"stream_silent_while_running","sessionMode":"claude_gui","idleSeconds":42.5,"whileRunning":true},
			{"event":"terminal_matched_by_turn_id","eventType":"turn.completed","sessionMode":"hermes_gui"},
			{"event":"queued_followup_blocked_after_terminal","sessionMode":"hermes_gui"},
			{"event":"stale_running_blocked_submit","sessionMode":"codex_gui"},
			{"event":"closed_unmount","sessionMode":"claude_gui"}
		]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-events-stream", body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.handleSessionEventStreamMetrics(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestSessionEventStreamMetricsRejectsServicePrincipal(t *testing.T) {
	app := adminTestServer(t)
	body := bytes.NewBufferString(`{"events":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-events-stream", body)
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "svc@example.com", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.handleSessionEventStreamMetrics(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for service principal", resp.Code)
	}
}

func TestSessionEventStreamMetricsRejectsInvalidJSON(t *testing.T) {
	app := adminTestServer(t)
	body := strings.NewReader(`{"events":[{`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-events-stream", body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.handleSessionEventStreamMetrics(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.Code)
	}
}

func TestSessionEventStreamMetricsRejectsNaNIdleSeconds(t *testing.T) {
	app := adminTestServer(t)
	body := bytes.NewBufferString(`{"events":[{"event":"stream_silent_while_running","sessionMode":"claude_gui","idleSeconds":-1}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-events-stream", body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	app.handleSessionEventStreamMetrics(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("negative idleSeconds should be rejected; got %d", resp.Code)
	}
}

func TestSessionEventStreamClientEventLabelClamp(t *testing.T) {
	if got := sessionEventStreamClientEventLabel("opened"); got != "opened" {
		t.Fatalf("opened label = %q", got)
	}
	if got := sessionEventStreamClientEventLabel("terminal_local_run_mismatch"); got != "terminal_local_run_mismatch" {
		t.Fatalf("terminal mismatch label = %q", got)
	}
	if got := sessionEventStreamClientEventLabel("stale_running_blocked_submit"); got != "stale_running_blocked_submit" {
		t.Fatalf("stale submit label = %q", got)
	}
	if got := sessionEventStreamClientEventLabel("malicious-event-name"); got != "other" {
		t.Fatalf("unknown event should clamp to other, got %q", got)
	}
	if got := sessionEventStreamClientResultLabel("malicious"); got != "other" {
		t.Fatalf("unknown result should clamp to other, got %q", got)
	}
	if got := sessionEventTypeLabel("not-a-real-type"); got != "other" {
		t.Fatalf("unknown event_type should clamp to other, got %q", got)
	}
	if got := sessionEventTypeLabel("user_message.created"); got != "user_message.created" {
		t.Fatalf("known event_type should pass through, got %q", got)
	}
}
