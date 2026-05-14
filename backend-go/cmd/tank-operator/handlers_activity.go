package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

const (
	sessionActivityPageLimit = 1000
	sessionActivityMaxPages  = 50
)

type sessionActivityResponse struct {
	Sessions []sessionActivitySummary `json:"sessions"`
}

type sessionActivitySummary struct {
	SessionID    string  `json:"session_id"`
	Status       string  `json:"status"`
	LastOrderKey *string `json:"last_order_key"`
	UnreadCount  int     `json:"unread_count"`
	NeedsInput   bool    `json:"needs_input"`
	Failed       bool    `json:"failed"`
	ActiveTurnID *string `json:"active_turn_id"`
	UpdatedAt    *string `json:"updated_at"`
}

type unreadOutputMarker struct {
	orderKey string
	cursor   string
	id       string
}

func (s *appServer) handleSessionActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}

	infos, err := s.mgr.ListSessions(r.Context(), user.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	eventStore := s.sessionEvents
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}

	out := make([]sessionActivitySummary, 0, len(infos))
	for _, info := range infos {
		summary := defaultSessionActivity(info)
		events, err := loadSessionActivityEvents(r.Context(), eventStore, info.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		readState, err := s.getSessionReadState(r, user.Email, info.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		readOrderKey := ""
		if readState != nil {
			readOrderKey = readState.LastReadOrderKey
		}
		out = append(out, summarizeSessionActivity(summary, events, readOrderKey))
	}

	writeJSON(w, http.StatusOK, sessionActivityResponse{Sessions: out})
}

func loadSessionActivityEvents(ctx context.Context, eventStore store.SessionEventStore, sessionID string) ([]map[string]any, error) {
	cursor := store.SessionEventCursor{}
	out := make([]map[string]any, 0)
	for pageNo := 0; pageNo < sessionActivityMaxPages; pageNo++ {
		page, err := eventStore.ListBySession(ctx, sessionID, cursor, sessionActivityPageLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Events...)
		if !page.HasMore || page.NextOrderKey == "" || page.NextOrderKey == cursor.AfterOrderKey {
			break
		}
		cursor = store.SessionEventCursor{AfterOrderKey: page.NextOrderKey}
	}
	return out, nil
}

func defaultSessionActivity(info sessions.Info) sessionActivitySummary {
	summary := sessionActivitySummary{
		SessionID:   info.ID,
		Status:      "ready",
		UpdatedAt:   firstActivityTime(info.ReadyAt, info.CreatedAt, info.RequestedAt),
		UnreadCount: 0,
	}
	switch info.Status {
	case "Failed":
		summary.Status = "error"
		summary.Failed = true
	case "Pending":
		summary.Status = "submitted"
	}
	return summary
}

func summarizeSessionActivity(summary sessionActivitySummary, events []map[string]any, readOrderKey string) sessionActivitySummary {
	outputMarkers := make([]unreadOutputMarker, 0)

	for _, event := range events {
		if !isDurableTankActivityEvent(event) {
			continue
		}

		if orderKey := activityEventOrderKey(event); orderKey != "" {
			summary.LastOrderKey = stringPtr(orderKey)
		}
		if updatedAt := activityEventTime(event); updatedAt != "" {
			summary.UpdatedAt = stringPtr(updatedAt)
		}
		if isUnreadOutputEvent(event) {
			outputMarkers = append(outputMarkers, unreadOutputMarker{
				orderKey: activityEventOrderKey(event),
				cursor:   activityEventCursor(event),
				id:       unreadOutputID(event),
			})
		}

		switch activityEventType(event) {
		case "turn.submitted":
			summary.Status = "submitted"
			if activeTurnID := activityOptionalString(event, "turn_id"); activeTurnID != nil {
				summary.ActiveTurnID = activeTurnID
			}
			summary.NeedsInput = false
			summary.Failed = false
		case "turn.started":
			summary.Status = "streaming"
			if activeTurnID := activityOptionalString(event, "turn_id"); activeTurnID != nil {
				summary.ActiveTurnID = activeTurnID
			}
			summary.NeedsInput = false
			summary.Failed = false
		case "turn.completed":
			summary.Status = "ready"
			summary.ActiveTurnID = nil
			summary.NeedsInput = false
			summary.Failed = false
		case "turn.failed":
			summary.Status = "error"
			summary.ActiveTurnID = nil
			summary.NeedsInput = false
			summary.Failed = true
		case "turn.interrupted":
			summary.Status = "stopped"
			summary.ActiveTurnID = nil
			summary.NeedsInput = false
			summary.Failed = false
		case "item.failed":
			summary.Status = "error"
			summary.Failed = true
		case "tool.approval_requested":
			summary.Status = "needs_input"
			if activeTurnID := activityOptionalString(event, "turn_id"); activeTurnID != nil {
				summary.ActiveTurnID = activeTurnID
			}
			summary.NeedsInput = true
		case "tool.approval_resolved":
			summary.NeedsInput = false
			if summary.ActiveTurnID != nil {
				summary.Status = "streaming"
			} else {
				summary.Status = "ready"
			}
		case "session.activity_updated":
			applyActivityUpdatedEvent(&summary, event)
		case "read_state.updated":
			eventReadOrderKey := activityPayloadString(event, "last_read_order_key")
			if eventReadOrderKey == "" {
				eventReadOrderKey = activityEventOrderKey(event)
			}
			if eventReadOrderKey > readOrderKey {
				readOrderKey = eventReadOrderKey
			}
		}
	}

	summary.UnreadCount = countUnreadOutputs(outputMarkers, readOrderKey)
	return summary
}

func applyActivityUpdatedEvent(summary *sessionActivitySummary, event map[string]any) {
	if status := activityPayloadString(event, "status"); isActivityStatus(status) {
		summary.Status = status
	}
	if needsInput, ok := activityPayloadBool(event, "needs_input"); ok {
		summary.NeedsInput = needsInput
		if needsInput {
			summary.Status = "needs_input"
		}
	}
	if failed, ok := activityPayloadBool(event, "failed"); ok {
		summary.Failed = failed
		if failed {
			summary.Status = "error"
		}
	}
	if activeTurnID := activityPayloadString(event, "active_turn_id"); activeTurnID != "" {
		summary.ActiveTurnID = stringPtr(activeTurnID)
	}
}

func isActivityStatus(status string) bool {
	switch status {
	case "ready", "submitted", "streaming", "needs_input", "stopped", "error":
		return true
	default:
		return false
	}
}

func isDurableTankActivityEvent(event map[string]any) bool {
	if err := conversation.ValidateEventMap(event); err != nil {
		return false
	}
	if visibility, _ := event["visibility"].(string); visibility == "live-only" {
		return false
	}
	switch activityEventType(event) {
	case "conversation.started",
		"conversation.archived",
		"user_message.created",
		"turn.submitted",
		"turn.started",
		"turn.completed",
		"turn.failed",
		"turn.interrupted",
		"item.started",
		"item.delta",
		"item.completed",
		"item.failed",
		"tool.approval_requested",
		"tool.approval_resolved",
		"session.activity_updated",
		"read_state.updated":
		return true
	default:
		return false
	}
}

func isUnreadOutputEvent(event map[string]any) bool {
	if actor, _ := event["actor"].(string); actor == "user" {
		return false
	}
	switch activityEventType(event) {
	case "item.started",
		"item.delta",
		"item.completed",
		"item.failed",
		"tool.approval_requested",
		"tool.approval_resolved",
		"turn.failed",
		"turn.interrupted":
		return true
	default:
		return false
	}
}

func countUnreadOutputs(markers []unreadOutputMarker, readOrderKey string) int {
	seen := make(map[string]struct{}, len(markers))
	for _, marker := range markers {
		if readOrderKey != "" && !unreadMarkerAfterRead(marker, readOrderKey) {
			continue
		}
		id := marker.id
		if id == "" {
			id = marker.orderKey
		}
		if id == "" {
			continue
		}
		seen[id] = struct{}{}
	}
	return len(seen)
}

func unreadMarkerAfterRead(marker unreadOutputMarker, readOrderKey string) bool {
	if readOrderKey == "" {
		return true
	}
	if strings.Contains(readOrderKey, "\x1f") {
		return marker.cursor != "" && marker.cursor > readOrderKey
	}
	if marker.orderKey != "" {
		return marker.orderKey > readOrderKey
	}
	return marker.cursor > readOrderKey
}

func unreadOutputID(event map[string]any) string {
	for _, field := range []string{"timeline_id", "turn_id", "event_id", "id", "uuid"} {
		if value, _ := event[field].(string); value != "" {
			return value
		}
	}
	return ""
}

func activityEventType(event map[string]any) string {
	value, _ := event["type"].(string)
	return value
}

func activityEventOrderKey(event map[string]any) string {
	if value, _ := event["order_key"].(string); value != "" {
		return value
	}
	return ""
}

func activityEventCursor(event map[string]any) string {
	return activityEventOrderKey(event)
}

func activityEventTime(event map[string]any) string {
	for _, field := range []string{"created_at", "written_at", "timestamp", "time"} {
		if value, _ := event[field].(string); value != "" {
			return value
		}
	}
	return ""
}

func activityPayload(event map[string]any) map[string]any {
	payload, _ := event["payload"].(map[string]any)
	return payload
}

func activityPayloadString(event map[string]any, key string) string {
	value, _ := activityPayload(event)[key].(string)
	return value
}

func activityPayloadBool(event map[string]any, key string) (bool, bool) {
	value, ok := activityPayload(event)[key].(bool)
	return value, ok
}

func activityOptionalString(event map[string]any, key string) *string {
	if value, _ := event[key].(string); value != "" {
		return stringPtr(value)
	}
	return nil
}

func firstActivityTime(values ...*string) *string {
	for _, value := range values {
		if value != nil && *value != "" {
			return value
		}
	}
	return nil
}

func stringPtr(value string) *string {
	return &value
}
