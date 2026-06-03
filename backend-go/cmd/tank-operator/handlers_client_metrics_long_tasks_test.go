package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

func TestHandleLongTaskMetricsRecordsPrometheus(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/long-tasks", strings.NewReader(`{
		"events": [{
			"durationMs": 180,
			"startMs": 600,
			"sessionMode": "claude_gui",
			"sinceTankEventMs": 90,
			"sinceSessionSwitchMs": 1200,
			"sinceScrollMs": 800,
			"attribution": "self",
			"pagePath": "/sessions/188"
		}]
	}`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleLongTaskMetrics(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("metrics status = %d body=%s, want 202", res.Code, res.Body.String())
	}
	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_client_long_task_reports_total{result="ok"}`,
		// Correlation should be event_burst because sinceTankEventMs=90
		// is the closest in-window signal (session_switch=1200 is still
		// inside the 1500ms window but later than the tank-event).
		`tank_client_long_task_total{attribution="self",correlation="event_burst",session_mode="claude_gui"} 1`,
		`tank_client_long_task_duration_seconds_bucket{correlation="event_burst",session_mode="claude_gui",le="0.25"}`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s\n%s", want, metrics)
		}
	}
}

func TestHandleLongTaskMetricsBucketsCorrelationToIdleOutsideWindow(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/long-tasks", strings.NewReader(`{
		"events": [{
			"durationMs": 120,
			"sessionMode": "codex_gui",
			"sinceTankEventMs": 5000,
			"sinceSessionSwitchMs": 9000,
			"attribution": "self"
		}]
	}`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleLongTaskMetrics(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("metrics status = %d body=%s, want 202", res.Code, res.Body.String())
	}
	metrics := scrapePrometheus(t)
	if !strings.Contains(metrics, `tank_client_long_task_total{attribution="self",correlation="idle",session_mode="codex_gui"}`) {
		t.Fatalf("expected correlation=idle when all signals are outside the 1.5s window; got:\n%s", metrics)
	}
}

func TestHandleLongTaskMetricsRejectsTooManyEvents(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	var body bytes.Buffer
	body.WriteString(`{"events":[`)
	for i := 0; i < longTaskMetricsMaxEvents+1; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"durationMs":100,"sessionMode":"claude_gui"}`)
	}
	body.WriteString(`]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/long-tasks", &body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleLongTaskMetrics(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("metrics status = %d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestHandleLongTaskMetricsRejectsNegativeAndNaN(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	cases := []string{
		`{"events":[{"durationMs":-1,"sessionMode":"claude_gui"}]}`,
		`{"events":[{"sessionMode":"claude_gui"}]}`,
	}
	for _, payload := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/long-tasks", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		res := httptest.NewRecorder()
		app.handleLongTaskMetrics(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("payload %q got status %d, want 400", payload, res.Code)
		}
	}
}

func TestLongTaskMetricsAttributionLabelsAreBounded(t *testing.T) {
	durationMs := 200.0
	sinceTankEventMs := 50.0
	event := longTaskMetricEvent{
		DurationMs:       &durationMs,
		SessionMode:      "mode-for-user-123",
		Attribution:      "user-controlled-attribution-value",
		SinceTankEventMs: &sinceTankEventMs,
	}
	recordLongTaskClientEvent(event)

	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_client_long_task_total{attribution="other",correlation="event_burst",session_mode="unknown"}`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s\n%s", want, metrics)
		}
	}
	if strings.Contains(metrics, "user-controlled-attribution-value") ||
		strings.Contains(metrics, "mode-for-user-123") {
		t.Fatalf("scrape leaked unbounded labels:\n%s", metrics)
	}
}

func TestHandleLongTaskMetricsRejectsServiceCallers(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/long-tasks", strings.NewReader(`{"events":[]}`))
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-77", adminEmail))
	res := httptest.NewRecorder()

	app.handleLongTaskMetrics(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("service caller got status %d, want 403", res.Code)
	}
}
