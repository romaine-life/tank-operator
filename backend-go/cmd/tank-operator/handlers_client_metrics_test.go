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

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
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

func TestHandleChatScrollMetricsLogsThinkingRowInvariantContext(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", strings.NewReader(`{
		"events": [{
			"event": "thinking-row-missing",
			"surface": "session",
			"sessionMode": "codex_gui",
			"sessionId": "251",
			"thinkingGroups": 0,
			"activityGroups": 1,
			"activeActivityGroups": 0,
			"durableActiveActivityGroups": 1,
			"turnActivityShells": 1,
			"durableActiveTurnActivityShells": 1,
			"entries": 8
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
		`"event":"thinking-row-missing"`,
		`"session_id":"251"`,
		`"thinking_groups":0`,
		`"activity_groups":1`,
		`"active_activity_groups":0`,
		`"durable_active_activity_groups":1`,
		`"turn_activity_shells":1`,
		`"durable_active_turn_activity_shells":1`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("slog output missing %s; got: %s", want, logged)
		}
	}

	metrics := scrapePrometheus(t)
	if !strings.Contains(metrics, `tank_chat_scroll_client_events_total{at_bottom="unknown",event="thinking-row-missing",has_scroll_parent="unknown",session_mode="codex_gui",surface="session"}`) {
		t.Fatalf("scrape missing thinking-row-missing event:\n%s", metrics)
	}
}

func TestHandleChatScrollMetricsLogsSelectedTurnActivityLoadingContext(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", strings.NewReader(`{
		"events": [
			{
				"event": "turn-activity-selected-loading-stranded",
				"surface": "session",
				"sessionMode": "codex_gui",
				"sessionId": "1038",
				"previousSessionId": "1031",
				"key": "turn_a80515c0-be0a-43c9-8ffa-06e5b7eb080b",
				"source": "session-switch",
				"reason": "absent",
				"status": 0,
				"entries": 0,
				"groups": 0,
				"activityEntries": 65,
				"turnActivityShells": 1,
				"durableActiveTurnActivityShells": 1
			},
			{
				"event": "turn-activity-selected-route-session-mismatch",
				"surface": "session",
				"sessionMode": "claude_gui",
				"sessionId": "1049",
				"routeSessionId": "1031",
				"selectedTurnId": "turn_28d18c7c-a36c-4ee3-9512-7492f83291a0",
				"key": "1031",
				"source": "turns-selected",
				"reason": "route-session-mismatch",
				"status": 0,
				"entries": 0,
				"groups": 0,
				"activityEntries": 0,
				"turnActivityShells": 1,
				"durableActiveTurnActivityShells": 1
			}
		]
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
		`"event":"turn-activity-selected-loading-stranded"`,
		`"event":"turn-activity-selected-route-session-mismatch"`,
		`"session_id":"1038"`,
		`"session_id":"1049"`,
		`"previous_session_id":"1031"`,
		`"route_session_id":"1031"`,
		`"selected_turn_id":"turn_28d18c7c-a36c-4ee3-9512-7492f83291a0"`,
		`"key":"turn_a80515c0-be0a-43c9-8ffa-06e5b7eb080b"`,
		`"key":"1031"`,
		`"source":"session-switch"`,
		`"source":"turns-selected"`,
		`"reason":"absent"`,
		`"reason":"route-session-mismatch"`,
		`"status":0`,
		`"entries":0`,
		`"groups":0`,
		`"activity_entries":65`,
		`"turn_activity_shells":1`,
		`"durable_active_turn_activity_shells":1`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("slog output missing %s; got: %s", want, logged)
		}
	}

	metrics := scrapePrometheus(t)
	if !strings.Contains(metrics, `tank_chat_scroll_client_events_total{at_bottom="unknown",event="turn-activity-selected-loading-stranded",has_scroll_parent="unknown",session_mode="codex_gui",surface="session"}`) {
		t.Fatalf("scrape missing turn-activity-selected-loading-stranded event:\n%s", metrics)
	}
	if !strings.Contains(metrics, `tank_chat_scroll_client_events_total{at_bottom="unknown",event="turn-activity-selected-route-session-mismatch",has_scroll_parent="unknown",session_mode="claude_gui",surface="session"}`) {
		t.Fatalf("scrape missing turn-activity-selected-route-session-mismatch event:\n%s", metrics)
	}
	if got := chatScrollEventLabel("turn-activity-selected-loading-slow"); got != "turn-activity-selected-loading-slow" {
		t.Fatalf("slow selected-loading event label = %q", got)
	}
	if got := chatScrollEventLabel("turn-activity-selected-loading-stranded"); got != "turn-activity-selected-loading-stranded" {
		t.Fatalf("stranded selected-loading event label = %q", got)
	}
	if got := chatScrollEventLabel("turn-activity-selected-route-session-mismatch"); got != "turn-activity-selected-route-session-mismatch" {
		t.Fatalf("selected route/session mismatch event label = %q", got)
	}
	if strings.Contains(metrics, `session_id="1038"`) ||
		strings.Contains(metrics, `session_id="1049"`) ||
		strings.Contains(metrics, `previous_session_id="1031"`) ||
		strings.Contains(metrics, `route_session_id="1031"`) ||
		strings.Contains(metrics, `turn_a80515c0`) ||
		strings.Contains(metrics, `turn_28d18c7c`) ||
		strings.Contains(metrics, `reason="absent"`) {
		t.Fatalf("scrape leaked selected turn loading trace context:\n%s", metrics)
	}
}

func TestHandleChatScrollMetricsLogsTurnDirectoryLoopContext(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", strings.NewReader(`{
		"events": [
			{
				"event": "turn-directory-new-turn-loop",
				"surface": "session",
				"sessionMode": "claude_gui",
				"sessionId": "1049",
				"pagePath": "/sessions/1027/turns/29/pages/1",
				"source": "new-turn",
				"reason": "stable-missing-turn-activity-shell",
				"key": "turn_answer-7fca9ecff10e7ee52e443acb",
				"eventCount": 1,
				"canonicalEventCount": 3,
				"entries": 9,
				"turnActivityShells": 4
			},
			{
				"event": "turn-directory-route-session-mismatch",
				"surface": "session",
				"sessionMode": "claude_gui",
				"sessionId": "1049",
				"pagePath": "/sessions/1027/turns/29/pages/1",
				"source": "new-turn",
				"reason": "route-session-mismatch",
				"key": "1027",
				"eventCount": 3
			}
		]
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
		`"event":"turn-directory-new-turn-loop"`,
		`"event":"turn-directory-route-session-mismatch"`,
		`"session_id":"1049"`,
		`"page_path":"/sessions/1027/turns/29/pages/1"`,
		`"source":"new-turn"`,
		`"reason":"stable-missing-turn-activity-shell"`,
		`"reason":"route-session-mismatch"`,
		`"key":"turn_answer-7fca9ecff10e7ee52e443acb"`,
		`"key":"1027"`,
		`"event_count":1`,
		`"canonical_event_count":3`,
		`"entries":9`,
		`"turn_activity_shells":4`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("slog output missing %s; got: %s", want, logged)
		}
	}

	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_chat_scroll_client_events_total{at_bottom="unknown",event="turn-directory-new-turn-loop",has_scroll_parent="unknown",session_mode="claude_gui",surface="session"}`,
		`tank_chat_scroll_client_events_total{at_bottom="unknown",event="turn-directory-route-session-mismatch",has_scroll_parent="unknown",session_mode="claude_gui",surface="session"}`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s:\n%s", want, metrics)
		}
	}
	if strings.Contains(metrics, `turn_answer-7fca9`) ||
		strings.Contains(metrics, `session_id="1049"`) ||
		strings.Contains(metrics, `page_path=`) ||
		strings.Contains(metrics, `reason="route-session-mismatch"`) {
		t.Fatalf("scrape leaked turn-directory trace context:\n%s", metrics)
	}
}

func TestHandleChatScrollMetricsRecordsNavigationModeTransitions(t *testing.T) {
	// The two navigation-mode event names are the durable
	// observability surface for the user-trust failure that motivated
	// the navigation-mode refactor (session 269, 2026-05-27). The
	// allowlist must accept both, the structured slog must carry the
	// bounded reason, and the Prometheus event counter must increment
	// without leaking the reason into a high-cardinality label.
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/chat-scroll", strings.NewReader(`{
		"events": [
			{
				"event": "navigation-mode-entered-historical-anchor",
				"surface": "session",
				"sessionMode": "claude_gui",
				"sessionId": "269",
				"reason": "user-scroll-up",
				"hasScrollParent": true,
				"bottomDistance": 12
			},
			{
				"event": "navigation-mode-entered-live-tail",
				"surface": "session",
				"sessionMode": "claude_gui",
				"sessionId": "269",
				"reason": "down-button",
				"hasScrollParent": true,
				"bottomDistance": 0
			}
		]
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
		`"event":"navigation-mode-entered-historical-anchor"`,
		`"event":"navigation-mode-entered-live-tail"`,
		`"reason":"user-scroll-up"`,
		`"reason":"down-button"`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("slog output missing %s; got: %s", want, logged)
		}
	}

	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_chat_scroll_client_events_total{at_bottom="unknown",event="navigation-mode-entered-historical-anchor",has_scroll_parent="true",session_mode="claude_gui",surface="session"}`,
		`tank_chat_scroll_client_events_total{at_bottom="unknown",event="navigation-mode-entered-live-tail",has_scroll_parent="true",session_mode="claude_gui",surface="session"}`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s\n%s", want, metrics)
		}
	}
	if strings.Contains(metrics, `reason="user-scroll-up"`) ||
		strings.Contains(metrics, `reason="down-button"`) {
		t.Fatalf("scrape leaked reason as a high-cardinality label:\n%s", metrics)
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
