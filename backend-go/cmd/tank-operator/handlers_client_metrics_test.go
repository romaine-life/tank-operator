package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

func TestHandleChatScrollMetricsRecordsPrometheus(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", strings.NewReader(`{
		"events": [{
			"event": "at-bottom-change",
			"surface": "session",
			"sessionMode": "codex_gui",
			"atBottom": false,
			"hasScrollParent": true,
			"bottomDistance": 240,
			"entries": 180
		}]
	}`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleChatScrollMetrics(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("metrics status = %d body=%s, want 202", res.Code, res.Body.String())
	}
	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_chat_scroll_client_reports_total{result="ok"}`,
		`tank_chat_scroll_client_events_total{at_bottom="false",event="at-bottom-change",has_scroll_parent="true",session_mode="codex_gui",surface="session"}`,
		`tank_chat_scroll_client_bottom_distance_pixels_bucket{event="at-bottom-change",session_mode="codex_gui",surface="session",le="500"}`,
		`tank_chat_scroll_client_entries_bucket{event="at-bottom-change",session_mode="codex_gui",surface="session",le="200"}`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s\n%s", want, metrics)
		}
	}
}

func TestHandleChatScrollMetricsRejectsUnboundedBatch(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	var body bytes.Buffer
	body.WriteString(`{"events":[`)
	for i := 0; i < chatScrollMetricsMaxEvents+1; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"event":"at-bottom-change","surface":"session","sessionMode":"codex_gui"}`)
	}
	body.WriteString(`]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", &body)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleChatScrollMetrics(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("metrics status = %d body=%s, want 400", res.Code, res.Body.String())
	}
}

func TestChatScrollMetricLabelsAreBounded(t *testing.T) {
	trueValue := true
	event := chatScrollMetricEvent{
		Event:           "random-user-controlled-value",
		Surface:         "/raw/path/123",
		SessionMode:     "mode-for-user-123",
		AtBottom:        nil,
		HasScrollParent: &trueValue,
	}
	recordChatScrollClientEvent(event)

	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_chat_scroll_client_events_total{at_bottom="unknown",event="other",has_scroll_parent="true",session_mode="unknown",surface="unknown"}`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s\n%s", want, metrics)
		}
	}
	if strings.Contains(metrics, "random-user-controlled-value") || strings.Contains(metrics, "/raw/path/123") {
		t.Fatalf("scrape leaked unbounded labels:\n%s", metrics)
	}
}

func scrapePrometheus(t *testing.T) string {
	t.Helper()
	res := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(res.Body)
	return string(body)
}
