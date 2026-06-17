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
	Event                       string   `json:"event"`
	Surface                     string   `json:"surface"`
	SessionMode                 string   `json:"sessionMode"`
	SessionID                   string   `json:"sessionId,omitempty"`
	PagePath                    string   `json:"pagePath,omitempty"`
	PageSearch                  string   `json:"pageSearch,omitempty"`
	Source                      string   `json:"source,omitempty"`
	Anchor                      string   `json:"anchor,omitempty"`
	RequestID                   string   `json:"requestId,omitempty"`
	BeforeCursor                string   `json:"beforeCursor,omitempty"`
	AfterCursor                 string   `json:"afterCursor,omitempty"`
	Reason                      string   `json:"reason,omitempty"`
	Key                         string   `json:"key,omitempty"`
	FirstGroupKey               string   `json:"firstGroupKey,omitempty"`
	LastGroupKey                string   `json:"lastGroupKey,omitempty"`
	PreviousFirstGroupKey       string   `json:"previousFirstGroupKey,omitempty"`
	PreviousLastGroupKey        string   `json:"previousLastGroupKey,omitempty"`
	TargetEdge                  string   `json:"targetEdge,omitempty"`
	NavInFlight                 string   `json:"navInFlight,omitempty"`
	Signal                      *float64 `json:"signal,omitempty"`
	Status                      *float64 `json:"status,omitempty"`
	DurationMs                  *float64 `json:"durationMs,omitempty"`
	EventCount                  *float64 `json:"eventCount,omitempty"`
	CanonicalEvents             *float64 `json:"canonicalEventCount,omitempty"`
	FoundOldest                 *bool    `json:"foundOldest,omitempty"`
	FoundNewest                 *bool    `json:"foundNewest,omitempty"`
	HasPrevCursor               *bool    `json:"hasPrevCursor,omitempty"`
	HasNextCursor               *bool    `json:"hasNextCursor,omitempty"`
	ClearRealtime               *bool    `json:"clearRealtime,omitempty"`
	AtBottom                    *bool    `json:"atBottom,omitempty"`
	HasScrollParent             *bool    `json:"hasScrollParent,omitempty"`
	ScrollTop                   *float64 `json:"scrollTop,omitempty"`
	ScrollHeight                *float64 `json:"scrollHeight,omitempty"`
	ClientHeight                *float64 `json:"clientHeight,omitempty"`
	BottomDistance              *float64 `json:"bottomDistance,omitempty"`
	Entries                     *float64 `json:"entries,omitempty"`
	PreviousEntries             *float64 `json:"previousEntries,omitempty"`
	EntriesDelta                *float64 `json:"entriesDelta,omitempty"`
	Groups                      *float64 `json:"groups,omitempty"`
	PreviousGroups              *float64 `json:"previousGroups,omitempty"`
	GroupsDelta                 *float64 `json:"groupsDelta,omitempty"`
	VisibleRowsAdded            *float64 `json:"visibleRowsAdded,omitempty"`
	VisibleRowsRemoved          *float64 `json:"visibleRowsRemoved,omitempty"`
	Messages                    *float64 `json:"messages,omitempty"`
	PreviousMessages            *float64 `json:"previousMessages,omitempty"`
	MessagesDelta               *float64 `json:"messagesDelta,omitempty"`
	Reasoning                   *float64 `json:"reasoning,omitempty"`
	Meta                        *float64 `json:"meta,omitempty"`
	BackgroundTasks             *float64 `json:"backgroundTasks,omitempty"`
	ThinkingGroups              *float64 `json:"thinkingGroups,omitempty"`
	ToolGroups                  *float64 `json:"toolGroups,omitempty"`
	ToolEntries                 *float64 `json:"toolEntries,omitempty"`
	ActivityGroups              *float64 `json:"activityGroups,omitempty"`
	ActiveActivityGroups        *float64 `json:"activeActivityGroups,omitempty"`
	DurableActiveActivityGroups *float64 `json:"durableActiveActivityGroups,omitempty"`
	ActivityEntries             *float64 `json:"activityEntries,omitempty"`
	TurnActivityShells          *float64 `json:"turnActivityShells,omitempty"`
	DurableActiveTurnShells     *float64 `json:"durableActiveTurnActivityShells,omitempty"`
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
		event.PreviousEntries,
		event.EntriesDelta,
		event.Groups,
		event.PreviousGroups,
		event.GroupsDelta,
		event.VisibleRowsAdded,
		event.VisibleRowsRemoved,
		event.Messages,
		event.PreviousMessages,
		event.MessagesDelta,
		event.Reasoning,
		event.Meta,
		event.BackgroundTasks,
		event.ThinkingGroups,
		event.ToolGroups,
		event.ToolEntries,
		event.ActivityGroups,
		event.ActiveActivityGroups,
		event.DurableActiveActivityGroups,
		event.ActivityEntries,
		event.TurnActivityShells,
		event.DurableActiveTurnShells,
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
	"keyboard-edge-navigation":                  {},
	"timeline-request":                          {},
	"timeline-error":                            {},
	"timeline-stale":                            {},
	"timeline-loaded":                           {},
	"turn-directory-request":                    {},
	"turn-directory-loaded":                     {},
	"turn-directory-error":                      {},
	"turn-directory-timeout":                    {},
	"turn-directory-reconcile":                  {},
	"turn-directory-stuck":                      {},
	"older-missing-cursor":                      {},
	"older-request":                             {},
	"older-error":                               {},
	"older-loaded":                              {},
	"older-no-visible-change":                   {},
	"prepend-preserve-scroll":                   {},
	"scroll-to-latest":                          {},
	"scroll-to-oldest":                          {},
	"start-reached":                             {},
	"at-bottom-change":                          {},
	"thinking-row-missing":                      {},
	"navigation-mode-entered-live-tail":         {},
	"navigation-mode-entered-historical-anchor": {},
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
		"request_id", boundedChatScrollLogString(event.RequestID, 80),
		"before_cursor", boundedChatScrollLogString(event.BeforeCursor, 120),
		"after_cursor", boundedChatScrollLogString(event.AfterCursor, 120),
		"reason", boundedChatScrollLogString(event.Reason, 80),
		"key", boundedChatScrollLogString(event.Key, 40),
		"first_group_key", boundedChatScrollLogString(event.FirstGroupKey, 120),
		"last_group_key", boundedChatScrollLogString(event.LastGroupKey, 120),
		"previous_first_group_key", boundedChatScrollLogString(event.PreviousFirstGroupKey, 120),
		"previous_last_group_key", boundedChatScrollLogString(event.PreviousLastGroupKey, 120),
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
		"previous_entries", metricLogFloat(event.PreviousEntries),
		"entries_delta", metricLogFloat(event.EntriesDelta),
		"groups", metricLogFloat(event.Groups),
		"previous_groups", metricLogFloat(event.PreviousGroups),
		"groups_delta", metricLogFloat(event.GroupsDelta),
		"visible_rows_added", metricLogFloat(event.VisibleRowsAdded),
		"visible_rows_removed", metricLogFloat(event.VisibleRowsRemoved),
		"messages", metricLogFloat(event.Messages),
		"previous_messages", metricLogFloat(event.PreviousMessages),
		"messages_delta", metricLogFloat(event.MessagesDelta),
		"reasoning", metricLogFloat(event.Reasoning),
		"meta", metricLogFloat(event.Meta),
		"background_tasks", metricLogFloat(event.BackgroundTasks),
		"thinking_groups", metricLogFloat(event.ThinkingGroups),
		"tool_groups", metricLogFloat(event.ToolGroups),
		"tool_entries", metricLogFloat(event.ToolEntries),
		"activity_groups", metricLogFloat(event.ActivityGroups),
		"active_activity_groups", metricLogFloat(event.ActiveActivityGroups),
		"durable_active_activity_groups", metricLogFloat(event.DurableActiveActivityGroups),
		"activity_entries", metricLogFloat(event.ActivityEntries),
		"turn_activity_shells", metricLogFloat(event.TurnActivityShells),
		"durable_active_turn_activity_shells", metricLogFloat(event.DurableActiveTurnShells),
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
	"turn-directory-request":        {},
	"turn-directory-loaded":         {},
	"turn-directory-error":          {},
	"turn-directory-timeout":        {},
	"turn-directory-reconcile":      {},
	"turn-directory-stuck":          {},
	"older-missing-cursor":          {},
	"older-request":                 {},
	"older-error":                   {},
	"older-loaded":                  {},
	"older-no-visible-change":       {},
	"prepend-preserve-scroll":       {},
	"virtuoso-window":               {},
	"scroll-to-latest":              {},
	"scroll-to-oldest":              {},
	"start-reached":                 {},
	"at-bottom-change":              {},
	"thinking-row-missing":          {},
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
	// Navigation-mode transitions emitted from
	// frontend/src/App.tsx → dispatchNavigationMode. Pairs with the
	// TankChatScrollUserAtBottomLatched alert in
	// k8s/templates/observability.yaml: a rising rate of
	// navigation-mode-entered-historical-anchor during a session
	// where the user is not gesturing is the durable signature of
	// the retired DOM-distance latch bug class (session 269 case,
	// 2026-05-27). See frontend/src/navigationMode.ts for the
	// reason set; the bounded reason name rides in the structured
	// log payload, not in metric labels.
	"navigation-mode-entered-live-tail":         {},
	"navigation-mode-entered-historical-anchor": {},
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
	"api_key":                 {},
	"claude_cli":              {},
	"claude_gui":              {},
	"config":                  {},
	"claude_secondary_cli":    {},
	"claude_secondary_gui":    {},
	"claude_secondary_config": {},
	"codex_cli":               {},
	"codex_gui":               {},
	"codex_exec_gui":          {},
	"codex_app_server":        {},
	"codex_config":            {},
	"debug":                   {},
}

func chatScrollSessionModeLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if _, ok := chatScrollSessionModeLabels[raw]; ok {
		return raw
	}
	return "unknown"
}
