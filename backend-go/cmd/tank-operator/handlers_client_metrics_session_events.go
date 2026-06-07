// Client-side telemetry ingestion for the per-session SSE event
// stream consumer. Pairs with frontend/src/sessionEventStreamTelemetry.ts.
// The browser emits semantic events (opened, transcript_rows_received,
// transcript_rows_applied,
// stream_silent_while_running, resync_required, stream_error,
// closed, plus terminal/local-run correlation regressions); the
// orchestrator buckets them into bounded Prometheus
// labels — the SPA never sets labels directly so a misbehaving
// client can't blow the active-series budget.
//
// This is the candidate-B / candidate-C stethoscope on the browser
// side. Combined with the server-side counters in observability.go
// it tells whether the wake fabric, the SSE socket, or the SPA
// reducer is the layer where events are getting dropped. Per
// memory/feedback_no_devtools_build_surfaces_instead.md — the
// browser cannot reach for devtools so this surface is the only
// way the client gets to participate in diagnosis.
package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
)

const (
	sessionEventStreamMetricsMaxBodyBytes = 64 * 1024
	sessionEventStreamMetricsMaxEvents    = 100
)

type sessionEventStreamMetricsRequest struct {
	Events []sessionEventStreamMetricEvent `json:"events"`
}

type sessionEventStreamMetricEvent struct {
	Event        string   `json:"event"`
	EventType    string   `json:"eventType,omitempty"`
	SessionMode  string   `json:"sessionMode"`
	IdleSeconds  *float64 `json:"idleSeconds,omitempty"`
	WhileRunning *bool    `json:"whileRunning,omitempty"`
}

func (s *appServer) handleSessionEventStreamMetrics(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !user.IsHuman() {
		recordSessionEventStreamClientReport("denied_role")
		writeError(w, http.StatusForbidden, "human user required")
		return
	}

	var body sessionEventStreamMetricsRequest
	limited := http.MaxBytesReader(w, r.Body, sessionEventStreamMetricsMaxBodyBytes)
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&body); err != nil {
		recordSessionEventStreamClientReport("invalid_json")
		writeError(w, http.StatusBadRequest, "invalid session-event stream metrics payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		recordSessionEventStreamClientReport("invalid_json")
		writeError(w, http.StatusBadRequest, "invalid session-event stream metrics payload")
		return
	}
	if len(body.Events) > sessionEventStreamMetricsMaxEvents {
		recordSessionEventStreamClientReport("too_many_events")
		writeError(w, http.StatusBadRequest, "too many session-event stream metric events")
		return
	}
	for _, event := range body.Events {
		if !validSessionEventStreamMetricNumbers(event) {
			recordSessionEventStreamClientReport("invalid_value")
			writeError(w, http.StatusBadRequest, "invalid session-event stream metric value")
			return
		}
	}
	for _, event := range body.Events {
		recordSessionEventStreamClientEvent(event)
	}
	recordSessionEventStreamClientReport("ok")
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": len(body.Events)})
}

func validSessionEventStreamMetricNumbers(event sessionEventStreamMetricEvent) bool {
	if event.IdleSeconds != nil {
		if math.IsNaN(*event.IdleSeconds) || math.IsInf(*event.IdleSeconds, 0) {
			return false
		}
		if *event.IdleSeconds < 0 {
			return false
		}
	}
	return true
}

var sessionEventStreamClientEventLabels = map[string]struct{}{
	"opened":                                 {},
	"transcript_rows_received":               {},
	"transcript_rows_applied":                {},
	"ready":                                  {},
	"stream_silent_while_running":            {},
	"terminal_matched_by_turn_id":            {},
	"terminal_local_run_mismatch":            {},
	"queued_followup_blocked_after_terminal": {},
	"stale_running_blocked_submit":           {},
	"turn_activity_load_started":             {},
	"turn_activity_load_succeeded":           {},
	"turn_activity_load_failed":              {},
	"turn_activity_load_timed_out":           {},
	"turn_activity_load_stale":               {},
	"turn_activity_refresh_failed":           {},
	"turn_activity_refresh_gave_up":          {},
	"turn_activity_refresh_recovered":        {},
	"turn_number_unavailable_target":         {},
	"resync_required":                        {},
	"stream_error":                           {},
	"closed_unmount":                         {},
	"closed_error":                           {},
	"reconnect_scheduled":                    {},
}

func sessionEventStreamClientEventLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if _, ok := sessionEventStreamClientEventLabels[raw]; ok {
		return raw
	}
	return "other"
}
