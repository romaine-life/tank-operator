package main

import (
	"encoding/json"
	"io"
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
	}
	recordChatScrollClientReport("ok")
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": len(body.Events)})
}

func validChatScrollMetricNumbers(event chatScrollMetricEvent) bool {
	for _, value := range []*float64{
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

var chatScrollMetricEventLabels = map[string]struct{}{
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
