package main

import (
	"bytes"
	"io"
	"log/slog"
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

func TestHandleChatScrollMetricsLogsBoundedTraceContext(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", strings.NewReader(`{
		"events": [{
			"event": "keyboard-edge-navigation",
			"surface": "session",
			"sessionMode": "codex_gui",
			"sessionId": "188",
			"pagePath": "/",
			"pageSearch": "?session=188",
			"key": "Home",
			"targetEdge": "oldest",
			"navInFlight": "oldest",
			"signal": 2,
			"foundOldest": false,
			"foundNewest": true,
			"hasScrollParent": true,
			"scrollTop": 640,
			"bottomDistance": 320
		}]
	}`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	var logs bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prevLogger)

	app.handleChatScrollMetrics(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("metrics status = %d body=%s, want 202", res.Code, res.Body.String())
	}
	logged := logs.String()
	for _, want := range []string{
		`"msg":"browser chat scroll event"`,
		`"event":"keyboard-edge-navigation"`,
		`"session_id":"188"`,
		`"page_search":"?session=188"`,
		`"key":"Home"`,
		`"target_edge":"oldest"`,
		`"nav_in_flight":"oldest"`,
		`"found_oldest":false`,
		`"found_newest":true`,
		`"bottom_distance":320`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("slog output missing %s; got: %s", want, logged)
		}
	}

	metrics := scrapePrometheus(t)
	if strings.Contains(metrics, "?session=188") ||
		strings.Contains(metrics, `session_id="188"`) ||
		strings.Contains(metrics, `sessionId`) {
		t.Fatalf("scrape leaked high-cardinality trace context:\n%s", metrics)
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
