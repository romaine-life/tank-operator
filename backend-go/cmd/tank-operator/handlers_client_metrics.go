package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
)

const (
	chatScrollMetricsMaxBodyBytes = 64 * 1024
	chatScrollMetricsMaxEvents    = 50
)

type chatScrollMetricsRequest struct {
	Events []chatScrollMetricEvent `json:"events"`
}

type chatScrollMetricEvent struct {
	Event           string   `json:"event"`
	Surface         string   `json:"surface"`
	SessionMode     string   `json:"sessionMode"`
	SessionID       string   `json:"sessionId,omitempty"`
	PagePath        string   `json:"pagePath,omitempty"`
	PageSearch      string   `json:"pageSearch,omitempty"`
	Source          string   `json:"source,omitempty"`
	Anchor          string   `json:"anchor,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	Key             string   `json:"key,omitempty"`
	TargetEdge      string   `json:"targetEdge,omitempty"`
	NavInFlight     string   `json:"navInFlight,omitempty"`
	Signal          *float64 `json:"signal,omitempty"`
	Status          *float64 `json:"status,omitempty"`
	DurationMs      *float64 `json:"durationMs,omitempty"`
	EventCount      *float64 `json:"eventCount,omitempty"`
	CanonicalEvents *float64 `json:"canonicalEventCount,omitempty"`
	FoundOldest     *bool    `json:"foundOldest,omitempty"`
	FoundNewest     *bool    `json:"foundNewest,omitempty"`
	HasPrevCursor   *bool    `json:"hasPrevCursor,omitempty"`
	HasNextCursor   *bool    `json:"hasNextCursor,omitempty"`
	ClearRealtime   *bool    `json:"clearRealtime,omitempty"`
	AtBottom        *bool    `json:"atBottom,omitempty"`
	HasScrollParent *bool    `json:"hasScrollParent,omitempty"`
	ScrollTop       *float64 `json:"scrollTop,omitempty"`
	ScrollHeight    *float64 `json:"scrollHeight,omitempty"`
	ClientHeight    *float64 `json:"clientHeight,omitempty"`
	BottomDistance  *float64 `json:"bottomDistance,omitempty"`
	Entries         *float64 `json:"entries,omitempty"`
	Groups          *float64 `json:"groups,omitempty"`
	Messages        *float64 `json:"messages,omitempty"`
	ToolGroups      *float64 `json:"toolGroups,omitempty"`
}

func (s *appServer) handleChatScrollMetrics(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !user.IsHuman() {
		recordChatScrollClientReport("denied_role")
		writeError(w, http.StatusForbidden, "human user required")
		return
	}

	var body chatScrollMetricsRequest
	limited := http.MaxBytesReader(w, r.Body, chatScrollMetricsMaxBodyBytes)
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&body); err != nil {
		recordChatScrollClientReport("invalid_json")
		writeError(w, http.StatusBadRequest, "invalid chat scroll metrics payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		recordChatScrollClientReport("invalid_json")
		writeError(w, http.StatusBadRequest, "invalid chat scroll metrics payload")
		return
	}
	if len(body.Events) > chatScrollMetricsMaxEvents {
		recordChatScrollClientReport("too_many_events")
		writeError(w, http.StatusBadRequest, "too many chat scroll metric events")
		return
	}
	for _, event := range body.Events {
		if !validChatScrollMetricNumbers(event) {
			recordChatScrollClientReport("invalid_value")
			writeError(w, http.StatusBadRequest, "invalid chat scroll metric value")
			return
		}
	}
	for _, event := range body.Events {
		recordChatScrollClientEvent(event)
		logChatScrollClientEvent(user.Email, event)
	}
	recordChatScrollClientReport("ok")
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": len(body.Events)})
}

func validChatScrollMetricNumbers(event chatScrollMetricEvent) bool {
	for _, value := range []*float64{
		event.Signal,
		event.Status,
		event.DurationMs,
		event.EventCount,
		event.CanonicalEvents,
		event.ScrollTop,
		event.ScrollHeight,
		event.ClientHeight,
		event.BottomDistance,
		event.Entries,
		event.Groups,
		event.Messages,
		event.ToolGroups,
	} {
		if value == nil {
			continue
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			return false
		}
	}
	return true
}

var chatScrollStructuredLogEvents = map[string]struct{}{
	"keyboard-edge-navigation": {},
	"timeline-request":         {},
	"timeline-error":           {},
	"timeline-stale":           {},
	"timeline-loaded":          {},
	"older-missing-cursor":     {},
	"older-request":            {},
	"older-loaded":             {},
	"prepend-preserve-scroll":  {},
	"scroll-to-latest":         {},
	"scroll-to-oldest":         {},
	"start-reached":            {},
	"at-bottom-change":         {},
}

func logChatScrollClientEvent(email string, event chatScrollMetricEvent) {
	eventLabel := chatScrollEventLabel(event.Event)
	if _, ok := chatScrollStructuredLogEvents[eventLabel]; !ok {
		return
	}
	slog.Info("browser chat scroll event",
		"email", boundedChatScrollLogString(email, 160),
		"event", eventLabel,
		"raw_event", boundedChatScrollLogString(event.Event, 80),
		"surface", chatScrollSurfaceLabel(event.Surface),
		"session_mode", chatScrollSessionModeLabel(event.SessionMode),
		"session_id", boundedChatScrollLogString(event.SessionID, 80),
		"page_path", boundedChatScrollLogString(event.PagePath, 160),
		"page_search", boundedChatScrollLogString(event.PageSearch, 240),
		"source", boundedChatScrollLogString(event.Source, 80),
		"anchor", boundedChatScrollLogString(event.Anchor, 80),
		"reason", boundedChatScrollLogString(event.Reason, 80),
		"key", boundedChatScrollLogString(event.Key, 40),
		"target_edge", boundedChatScrollLogString(event.TargetEdge, 40),
		"nav_in_flight", boundedChatScrollLogString(event.NavInFlight, 40),
		"signal", metricLogFloat(event.Signal),
		"status", metricLogFloat(event.Status),
		"duration_ms", metricLogFloat(event.DurationMs),
		"event_count", metricLogFloat(event.EventCount),
		"canonical_event_count", metricLogFloat(event.CanonicalEvents),
		"found_oldest", metricLogBool(event.FoundOldest),
		"found_newest", metricLogBool(event.FoundNewest),
		"has_prev_cursor", metricLogBool(event.HasPrevCursor),
		"has_next_cursor", metricLogBool(event.HasNextCursor),
		"clear_realtime", metricLogBool(event.ClearRealtime),
		"at_bottom", metricLogBool(event.AtBottom),
		"has_scroll_parent", metricLogBool(event.HasScrollParent),
		"scroll_top", metricLogFloat(event.ScrollTop),
		"scroll_height", metricLogFloat(event.ScrollHeight),
		"client_height", metricLogFloat(event.ClientHeight),
		"bottom_distance", metricLogFloat(event.BottomDistance),
		"entries", metricLogFloat(event.Entries),
		"groups", metricLogFloat(event.Groups),
		"messages", metricLogFloat(event.Messages),
		"tool_groups", metricLogFloat(event.ToolGroups),
	)
}

func boundedChatScrollLogString(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func metricLogBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func metricLogFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

var chatScrollMetricEventLabels = map[string]struct{}{
	"keyboard-edge-navigation":      {},
	"tail-bootstrap-reset":          {},
	"timeline-request":              {},
	"timeline-error":                {},
	"timeline-stale":                {},
	"timeline-loaded":               {},
	"older-missing-cursor":          {},
	"older-request":                 {},
	"older-loaded":                  {},
	"prepend-preserve-scroll":       {},
	"virtuoso-window":               {},
	"scroll-to-latest":              {},
	"scroll-to-oldest":              {},
	"start-reached":                 {},
	"at-bottom-change":              {},
	"scroll-parent-mounted":         {},
	"scroll-parent-unmounted":       {},
	"debug-scroll-parent-mounted":   {},
	"debug-scroll-parent-unmounted": {},
	"debug-reset-transcript":        {},
	"debug-prepend-older":           {},
	"debug-append-burst":            {},
	"debug-mock-reply-stopped":      {},
	"debug-submit-message":          {},
	"debug-mock-reply-complete":     {},
}

func chatScrollEventLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if _, ok := chatScrollMetricEventLabels[raw]; ok {
		return raw
	}
	return "other"
}

func chatScrollSurfaceLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "session", "debug_lab":
		return raw
	default:
		return "unknown"
	}
}

var chatScrollSessionModeLabels = map[string]struct{}{
	"api_key":          {},
	"claude_cli":       {},
	"claude_gui":       {},
	"config":           {},
	"codex_cli":        {},
	"codex_gui":        {},
	"codex_exec_gui":   {},
	"codex_app_server": {},
	"codex_config":     {},
	"debug":            {},
	"hermes_gui":       {},
	"pi_cli":           {},
	"pi_config":        {},
}

func chatScrollSessionModeLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if _, ok := chatScrollSessionModeLabels[raw]; ok {
		return raw
	}
	return "unknown"
}
