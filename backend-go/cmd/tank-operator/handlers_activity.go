package main

import (
	"net/http"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// activityLifecycleLimit caps the LatestLifecycleEvents read per
// session. 50 covers a few turns' worth of lifecycle markers — more
// than enough to determine current run-status / active-turn-id /
// needs-input. The previous implementation folded up to 50,000 events
// per session per /activity call; this is the bound replacement.
const activityLifecycleLimit = 50

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
		readState, err := s.getSessionReadState(r, user.Email, info.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		readOrderKey := ""
		if readState != nil {
			readOrderKey = readState.LastReadOrderKey
		}
		// Bounded read: just the latest lifecycle markers, not the
		// full event ledger. The fold below is O(activityLifecycleLimit)
		// regardless of session age.
		events, err := eventStore.LatestLifecycleEvents(r.Context(), info.ID, activityLifecycleLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Unread count is a separate Cosmos COUNT query — O(1) RU
		// regardless of how much output the session has produced.
		unread, err := eventStore.UnreadOutputCount(r.Context(), info.ID, readOrderKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, summarizeSessionActivity(summary, events, unread))
	}

	writeJSON(w, http.StatusOK, sessionActivityResponse{Sessions: out})
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

// summarizeSessionActivity applies the lifecycle fold over an ASC-ordered
// slice of recent lifecycle events. The caller bounds the slice to the
// most recent N events via SessionEventStore.LatestLifecycleEvents, so
// this is O(N) over a small constant — not over the session's full
// history.
func summarizeSessionActivity(summary sessionActivitySummary, events []map[string]any, unreadCount int) sessionActivitySummary {
	for _, event := range events {
		if !isDurableLifecycleEvent(event) {
			continue
		}
		if orderKey := activityEventOrderKey(event); orderKey != "" {
			summary.LastOrderKey = stringPtr(orderKey)
		}
		if updatedAt := activityEventTime(event); updatedAt != "" {
			summary.UpdatedAt = stringPtr(updatedAt)
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
		case "turn.failed", "turn.command_failed":
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
		}
	}
	summary.UnreadCount = unreadCount
	return summary
}

func isDurableLifecycleEvent(event map[string]any) bool {
	if err := conversation.ValidateEventMap(event); err != nil {
		return false
	}
	if visibility, _ := event["visibility"].(string); visibility == "live-only" {
		return false
	}
	t := activityEventType(event)
	for _, allowed := range store.LifecycleEventTypes {
		if t == allowed {
			return true
		}
	}
	return false
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

func activityEventTime(event map[string]any) string {
	for _, field := range []string{"created_at", "written_at", "timestamp", "time"} {
		if value, _ := event[field].(string); value != "" {
			return value
		}
	}
	return ""
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
