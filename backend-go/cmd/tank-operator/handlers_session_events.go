package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

const (
	sessionEventStreamPageLimit = 100
	sessionEventStreamHeartbeat = 15 * time.Second
)

// handleListSessionEvents reads canonical SDK events from the session-events
// Cosmos container for the SPA's durable history path. The only resume cursor
// is order_key; unknown cursors are explicit resync errors instead of silent
// replay from the beginning.
func (s *appServer) handleListSessionEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	cursor := sessionEventCursorFromRequest(r)
	if ok, err := s.sessionEventCursorExists(r.Context(), sessionID, cursor); err != nil {
		recordSessionEventTimelineFailure()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if !ok {
		recordSessionEventTimelineFailure()
		writeError(w, http.StatusConflict, "event cursor not found; reload timeline")
		return
	}

	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	eventStore := s.sessionEvents
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}
	page, err := eventStore.ListBySession(r.Context(), sessionID, cursor, limit)
	if err != nil {
		recordSessionEventTimelineFailure()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	readState, err := s.getSessionReadState(r, user.Email, sessionID)
	if err != nil {
		recordSessionEventTimelineFailure()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if page.Events == nil {
		page.Events = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":      sessionID,
		"events":          page.Events,
		"next_order_key":  page.NextOrderKey,
		"has_more":        page.HasMore,
		"cursor_semantic": "order_key",
		"read_state":      sessionReadStateBody(readState),
	})
}

func (s *appServer) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	s.handleListSessionEvents(w, r)
}

// handleSessionEventStream streams the durable transcript ledger over SSE.
// Each browser event id is the Tank order_key, so native EventSource reconnects
// resume from Last-Event-ID without relying on runner-local websocket state.
func (s *appServer) handleSessionEventStream(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}
	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	cursor := sessionEventCursorFromRequest(r)
	if ok, err := s.sessionEventCursorExists(r.Context(), sessionID, cursor); err != nil {
		recordSessionEventStreamError()
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{
			"reason": "cursor_check_failed",
			"detail": err.Error(),
		})
		flusher.Flush()
		return
	} else if !ok {
		sessionEventStreamResyncTotal.Add(1)
		slog.Warn("session event stream resync required",
			"session_id", sessionID,
			"email", user.Email,
			"last_order_key", cursor.AfterOrderKey,
		)
		writeSSEJSONEvent(w, "resync_required", "", map[string]any{
			"reason":         "cursor_not_found",
			"last_order_key": cursor.AfterOrderKey,
		})
		flusher.Flush()
		return
	}

	writeSSEJSONEvent(w, "ready", "", map[string]any{
		"session_id":     sessionID,
		"last_order_key": cursor.AfterOrderKey,
	})
	flusher.Flush()
	sessionEventStreamOpenTotal.Add(1)
	if cursor.AfterOrderKey != "" {
		sessionEventStreamReconnectTotal.Add(1)
	}

	notify := make(<-chan struct{})
	unsubscribe := func() {}
	if s.sessionBus != nil {
		var err error
		notify, unsubscribe, err = s.sessionBus.SubscribeWakes(r.Context(), sessionID)
		if err != nil {
			sessionEventWakeSubscribeFailures.Add(1)
			recordSessionEventStreamError()
			writeSSEJSONEvent(w, "stream-error", "", map[string]any{
				"reason": "event_wake_subscribe_failed",
				"detail": err.Error(),
			})
			flusher.Flush()
			return
		}
		defer unsubscribe()
	} else {
		recordSessionEventStreamError()
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{
			"reason": "session_bus_unavailable",
		})
		flusher.Flush()
		return
	}
	slog.Info("session event stream open",
		"session_id", sessionID,
		"email", user.Email,
		"last_order_key", cursor.AfterOrderKey,
		"resumed", cursor.AfterOrderKey != "",
	)
	defer slog.Info("session event stream close",
		"session_id", sessionID,
		"email", user.Email,
	)

	heartbeat := time.NewTicker(sessionEventStreamHeartbeat)
	defer heartbeat.Stop()

	for {
		hasMore, count, err := s.writeSessionEventStreamPage(r.Context(), w, sessionID, &cursor)
		if err != nil {
			recordSessionEventStreamError()
			slog.Warn("session event stream page failed",
				"session_id", sessionID,
				"email", user.Email,
				"last_order_key", cursor.AfterOrderKey,
				"error", err,
			)
			writeSSEJSONEvent(w, "stream-error", "", map[string]any{
				"reason": "event_page_failed",
				"detail": err.Error(),
			})
			flusher.Flush()
			return
		}
		flusher.Flush()
		if count > 0 {
			slog.Debug("session event stream emitted events",
				"session_id", sessionID,
				"count", count,
				"last_order_key", cursor.AfterOrderKey,
				"has_more", hasMore,
			)
		}
		if hasMore {
			continue
		}

		select {
		case <-r.Context().Done():
			return
		case <-notify:
		case <-heartbeat.C:
			sessionEventStreamHeartbeatTotal.Add(1)
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func (s *appServer) writeSessionEventStreamPage(ctx context.Context, w http.ResponseWriter, sessionID string, cursor *store.SessionEventCursor) (bool, int, error) {
	eventStore := s.sessionEvents
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}
	page, err := eventStore.ListBySession(ctx, sessionID, *cursor, sessionEventStreamPageLimit)
	if err != nil {
		return false, 0, err
	}
	count := 0
	for _, event := range page.Events {
		orderKey, _ := event["order_key"].(string)
		if orderKey == "" {
			continue
		}
		recordSessionEventLag(event)
		writeSSEJSONEvent(w, "tank-event", orderKey, event)
		cursor.AfterOrderKey = orderKey
		count++
		sessionEventStreamEmittedTotal.Add(1)
	}
	if page.NextOrderKey != "" {
		cursor.AfterOrderKey = page.NextOrderKey
	}
	return page.HasMore, count, nil
}

func (s *appServer) sessionEventCursorExists(ctx context.Context, sessionID string, cursor store.SessionEventCursor) (bool, error) {
	if strings.TrimSpace(cursor.AfterOrderKey) == "" {
		return true, nil
	}
	eventStore := s.sessionEvents
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}
	return eventStore.HasOrderKey(ctx, sessionID, cursor.AfterOrderKey)
}

func sessionEventCursorFromRequest(r *http.Request) store.SessionEventCursor {
	if lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID")); lastEventID != "" {
		return store.SessionEventCursor{AfterOrderKey: lastEventID}
	}
	if afterOrderKey := strings.TrimSpace(r.URL.Query().Get("after_order_key")); afterOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: afterOrderKey}
	}
	if lastOrderKey := strings.TrimSpace(r.URL.Query().Get("last_order_key")); lastOrderKey != "" {
		return store.SessionEventCursor{AfterOrderKey: lastOrderKey}
	}
	return store.SessionEventCursor{}
}

func writeSSEJSONEvent(w http.ResponseWriter, eventName, id string, value any) {
	if id = sanitizeSSEField(id); id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	if eventName = sanitizeSSEField(eventName); eventName != "" {
		fmt.Fprintf(w, "event: %s\n", eventName)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(`{"detail":"json marshal failed"}`)
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
}

func sanitizeSSEField(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "")
}
