// Per-owner durable session-list event surface. Mirrors the shape of
// handlers_session_events.go for the chat ledger: REST timeline replay +
// SSE cursor-resume, both reading from the same session_lifecycle_events
// store the producers (sessions.Manager, sessioncontroller k8s-watch + chat-activity via
// lifecycle_emitter) write to. Lives in its own file so the chat-side
// handlers and this stay independently grep-able.
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

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
)

const (
	// listEventStreamPageLimit caps the number of rows the SSE catch-up
	// loop and the /timeline REST page hand back in a single round. Same
	// bound the chat-side ledger uses; lifecycle traffic is sparser by
	// orders of magnitude so this is roomy.
	listEventStreamPageLimit  = 100
	sessionListStreamHeartbeat = 15 * time.Second
)

// handleSessionListTimeline returns a paginated, cursor-resumable slice
// of the per-owner session_lifecycle_events ledger. The SPA uses this
// for the initial-state bootstrap (replacing the deleted activity
// polling endpoint that folded the chat-event ledger on every call) and
// for post-resync recovery when the SSE handler sends resync_required.
func (s *appServer) handleSessionListTimeline(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	owner := listSessionsOwner(user, r)
	if s.lifecycleEvents == nil {
		// Stub path (no Postgres): empty page is the correct contract.
		writeJSON(w, http.StatusOK, map[string]any{
			"events":         []any{},
			"next_order_key": "",
			"has_more":       false,
			"cursor_semantic": "order_key",
		})
		return
	}
	cursor := sessionListCursorFromRequest(r)
	if cursor.AfterOrderKey != "" {
		if ok, err := s.lifecycleEvents.HasOrderKey(r.Context(), owner, s.sessionScope, cursor.AfterOrderKey); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if !ok {
			writeError(w, http.StatusConflict, "lifecycle cursor not found; reload session list")
			return
		}
	}
	limit := listEventStreamPageLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	page, err := s.lifecycleEvents.ListByOwner(r.Context(), owner, s.sessionScope, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows := make([]map[string]any, 0, len(page.Events))
	for _, evt := range page.Events {
		rows = append(rows, eventToJSONMap(evt))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":          rows,
		"next_order_key":  page.NextOrderKey,
		"has_more":        page.HasMore,
		"cursor_semantic": "order_key",
	})
}

// writeSessionListStreamPage emits at most listEventStreamPageLimit rows
// from the durable ledger past the current cursor, advancing the cursor
// as it goes. Returns hasMore so the SSE handler can loop until the
// catch-up is fully drained before subscribing to live NATS payloads.
func (s *appServer) writeSessionListStreamPage(ctx context.Context, w http.ResponseWriter, owner string, cursor *lifecycleevents.Cursor) (bool, int, error) {
	if s.lifecycleEvents == nil {
		return false, 0, nil
	}
	page, err := s.lifecycleEvents.ListByOwner(ctx, owner, s.sessionScope, *cursor, listEventStreamPageLimit)
	if err != nil {
		return false, 0, err
	}
	count := 0
	for _, evt := range page.Events {
		if evt.OrderKey == "" {
			continue
		}
		writeSSEJSONEvent(w, "session-event", evt.OrderKey, eventToJSONMap(evt))
		cursor.AfterOrderKey = evt.OrderKey
		count++
		sessionListStreamEmittedTotal.Inc()
	}
	if page.NextOrderKey != "" {
		cursor.AfterOrderKey = page.NextOrderKey
	}
	return page.HasMore, count, nil
}

// emitSessionListPayload forwards a NATS payload to the connected client.
// Payloads are already JSON-marshaled lifecycleevents.Event documents;
// we re-decode just enough to extract order_key + session_scope for the
// SSE `id:` line, the cursor advance, and the defensive scope guard.
//
// A payload whose order_key is ≤ the cursor was either already streamed
// during catch-up or is a duplicate of a row we just emitted — skip it
// rather than re-rendering the same transition on the client.
//
// A payload whose session_scope does not match this orchestrator's
// configured scope is dropped with a counter increment. The (email,
// scope) NATS subject already makes this physically unreachable in
// steady state; the check exists so a producer-side regression (wrong
// scope passed to PublishSessionListEvent) cannot silently mutate
// sidebar state on connected clients, and shows up as a non-zero rate
// on tank_session_list_cross_scope_events_dropped_total instead.
func (s *appServer) emitSessionListPayload(w http.ResponseWriter, cursor *lifecycleevents.Cursor, payload []byte) {
	var probe struct {
		OrderKey     string `json:"order_key"`
		SessionScope string `json:"session_scope"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return
	}
	if scope := strings.TrimSpace(probe.SessionScope); scope != "" && scope != s.sessionScope {
		sessionListCrossScopeEventsDroppedTotal.Inc()
		slog.Warn("session list payload dropped: scope mismatch",
			"expected_scope", s.sessionScope,
			"payload_scope", scope,
			"order_key", probe.OrderKey,
		)
		return
	}
	orderKey := strings.TrimSpace(probe.OrderKey)
	if orderKey == "" {
		return
	}
	if cursor.AfterOrderKey != "" && !orderKeyGreater(orderKey, cursor.AfterOrderKey) {
		return
	}
	// Emit the payload verbatim; the producer already shaped it as the
	// lifecycleevents.Event wire document.
	writeRawSSEEvent(w, "session-event", orderKey, payload)
	cursor.AfterOrderKey = orderKey
	sessionListStreamEmittedTotal.Inc()
}

// writeRawSSEEvent writes an SSE frame with a pre-marshaled JSON `data`
// payload. The chat-side writeSSEJSONEvent always marshals from a Go
// value; this variant lets the session-list handler forward NATS bytes
// without paying for a roundtrip re-encode.
func writeRawSSEEvent(w http.ResponseWriter, eventName, id string, data []byte) {
	if id = sanitizeSSEField(id); id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	if eventName = sanitizeSSEField(eventName); eventName != "" {
		fmt.Fprintf(w, "event: %s\n", eventName)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// sessionListCursorFromRequest extracts the SSE cursor from either the
// standard EventSource `Last-Event-ID` header (set by the browser on
// auto-reconnect) or the explicit `last_order_key` / `after_order_key`
// query string the SPA passes on first connect after a resync.
func sessionListCursorFromRequest(r *http.Request) lifecycleevents.Cursor {
	if lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID")); lastEventID != "" {
		return lifecycleevents.Cursor{AfterOrderKey: lastEventID}
	}
	if afterOrderKey := strings.TrimSpace(r.URL.Query().Get("after_order_key")); afterOrderKey != "" {
		return lifecycleevents.Cursor{AfterOrderKey: afterOrderKey}
	}
	if lastOrderKey := strings.TrimSpace(r.URL.Query().Get("last_order_key")); lastOrderKey != "" {
		return lifecycleevents.Cursor{AfterOrderKey: lastOrderKey}
	}
	return lifecycleevents.Cursor{}
}

// eventToJSONMap renders an Event as the SSE/REST wire shape. Keeps
// json.Marshal off the hot path and lets us add fields without changing
// the wire schema (the frontend reducer ignores unknown fields).
func eventToJSONMap(evt lifecycleevents.Event) map[string]any {
	out := map[string]any{
		"order_key":     evt.OrderKey,
		"email":         evt.Email,
		"session_scope": evt.SessionScope,
		"session_id":    evt.SessionID,
		"type":          evt.Type,
		"event_id":      evt.EventID,
		"payload":       evt.Payload,
		"occurred_at":   evt.OccurredAt,
	}
	return out
}

// orderKeyGreater compares two BIGSERIAL order_keys as integers. Falls
// back to string-compare if either side is not a valid integer (defensive
// — production producers always set integer order_keys).
func orderKeyGreater(a, b string) bool {
	ai, aErr := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	bi, bErr := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
	if aErr == nil && bErr == nil {
		return ai > bi
	}
	return a > b
}
