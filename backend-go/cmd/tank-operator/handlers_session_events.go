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

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionstream"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

const (
	sessionEventStreamPageLimit = 100
	sessionEventStreamHeartbeat = 15 * time.Second
)

// handleListSessionEvents reads canonical SDK events from the session_events
// Postgres table for the SPA's durable history path. The anchor query param
// selects the shape of the read:
//
//   - anchor=newest                — last N events (tail).
//   - anchor=oldest                — first N events (head of ledger). Powers
//     the SPA's "jump to start" affordance —
//     Discord/Slack-style symmetric pair with
//     anchor=newest.
//   - anchor=<order_key>           — page centered on that order_key.
//   - before_order_key=<order_key> — strictly older than the cursor (DESC).
//   - after_order_key=<order_key>  — strictly newer than the cursor (ASC).
//     Used for "catch up forward" inside a
//     bounded forward-paginate from the SPA.
//   - none of the above            — same as anchor=newest.
//
// num_before / num_after govern the symmetric anchor reads; limit governs
// before/after cursor reads and the tail. Unknown cursors are explicit 409
// resync errors per docs/product-inspirations.md.
func (s *appServer) handleListSessionEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	body, status, err := s.sessionTimelineBody(r.Context(), r, user, sessionID)
	if err != nil {
		if status >= 500 {
			recordSessionEventTimelineFailure()
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) sessionTimelineBody(ctx context.Context, r *http.Request, user auth.User, sessionID string) (map[string]any, int, error) {
	if _, status, err := s.authorizeSessionRead(ctx, user, sessionID); err != nil {
		return nil, status, err
	}

	eventStore := s.sessionEvents
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}
	readState, err := s.getSessionReadState(r, user.OwnerEmail(), sessionID)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	intent := sessionEventReadIntentFromRequest(r)
	recordSessionEventTimelineRequest(intent.metricLabel)
	if status, err := resolveSessionEventTimelineAnchor(ctx, eventStore, sessionID, &intent); err != nil {
		return nil, status, err
	}

	// Cursor-existence validation: only meaningful for caller-supplied
	// order_keys (after/before/anchor=<key>). Tail and timeline_id anchors
	// have no caller-supplied cursor to validate.
	if intent.validateCursor != "" {
		if ok, err := eventStore.HasOrderKey(ctx, sessionID, intent.validateCursor); err != nil {
			return nil, http.StatusInternalServerError, err
		} else if !ok {
			return nil, http.StatusConflict, fmt.Errorf("event cursor not found; reload timeline")
		}
	}

	page, err := s.runSessionEventRead(ctx, eventStore, sessionID, intent)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if page.Events == nil {
		page.Events = []map[string]any{}
	}
	body := map[string]any{
		"session_id":      sessionID,
		"events":          page.Events,
		"next_order_key":  page.NextOrderKey,
		"prev_order_key":  page.PrevOrderKey,
		"has_more":        page.HasMore,
		"found_oldest":    page.FoundOldest,
		"found_newest":    page.FoundNewest,
		"anchor":          intent.responseAnchor,
		"cursor_semantic": "order_key",
		"read_state":      sessionReadStateBody(readState),
	}
	if intent.timelineID != "" {
		body["target_timeline_id"] = intent.timelineID
		body["target_order_key"] = intent.anchorOrderKey
	}
	return body, http.StatusOK, nil
}

func (s *appServer) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	s.handleListSessionEvents(w, r)
}

// handleSessionEventStream streams the durable transcript ledger over SSE.
// Each browser event id is the Tank order_key, so native EventSource reconnects
// resume from Last-Event-ID without relying on runner-local websocket state.
func (s *appServer) handleSessionEventStream(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	user, ok := s.requireBrowserStreamAuth(w, r, streamKindSessionEvents, sessionID)
	if !ok {
		return
	}
	if _, status, err := s.authorizeSessionRead(r.Context(), user, sessionID); err != nil {
		writeError(w, status, err.Error())
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

	// Per-stream diagnostic state. The registry is the source of
	// truth for the /api/debug/session-event-streams admin endpoint;
	// it has to be registered before SubscribeWakesWithRecorder fires
	// any callbacks so the first wake (which can race the subscribe
	// in low-latency clusters) lands on a registered state object.
	streamID := auth.RandomHex(16)
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	state := sessionstream.NewStreamState(
		streamID,
		sessionID,
		storageKey,
		user.Email,
		time.Now(),
		cursor.AfterOrderKey,
	)
	s.streamRegistry.Register(state)
	defer s.streamRegistry.Deregister(streamID)

	notify := make(<-chan struct{})
	unsubscribe := func() {}
	if s.sessionBus != nil {
		var err error
		notify, unsubscribe, err = s.sessionBus.SubscribeWakesWithRecorder(r.Context(), sessionID, state)
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
		"stream_id", streamID,
		"storage_key", storageKey,
		"last_order_key", cursor.AfterOrderKey,
		"resumed", cursor.AfterOrderKey != "",
	)
	defer slog.Info("session event stream close",
		"session_id", sessionID,
		"email", user.Email,
		"stream_id", streamID,
	)

	heartbeat := time.NewTicker(sessionEventStreamHeartbeat)
	defer heartbeat.Stop()

	for {
		cursorBefore := cursor.AfterOrderKey
		hasMore, count, err := s.writeSessionEventStreamPage(r.Context(), w, sessionID, &cursor, state)
		if err != nil {
			recordSessionEventStreamError()
			slog.Warn("session event stream page failed",
				"session_id", sessionID,
				"email", user.Email,
				"stream_id", streamID,
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
		state.RecordPageRead(time.Now(), count)
		if count > 0 {
			slog.Info("session event stream emitted events",
				"session_id", sessionID,
				"stream_id", streamID,
				"count", count,
				"cursor_before", cursorBefore,
				"cursor_after", cursor.AfterOrderKey,
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
			state.RecordHeartbeat(time.Now())
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func (s *appServer) writeSessionEventStreamPage(ctx context.Context, w http.ResponseWriter, sessionID string, cursor *store.SessionEventCursor, state *sessionstream.StreamState) (bool, int, error) {
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
		eventType, _ := event["type"].(string)
		recordSessionEventStreamEmittedByType(eventType)
		state.RecordEmit(time.Now(), orderKey, eventType, orderKey)
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

// sessionEventReadKind enumerates the shapes the /timeline read can take.
// Centralized so the metric label and the dispatcher stay in sync.
type sessionEventReadKind int

const (
	sessionEventReadTail sessionEventReadKind = iota
	// sessionEventReadHead is the symmetric counterpart of sessionEventReadTail:
	// the FIRST N events of the ledger in ASC order. Indexed seek (same plan
	// as an empty-cursor ascending scan, but exposed as a named anchor with
	// its own metric label) — dispatched by anchor=oldest from the SPA's
	// "jump to start" button.
	sessionEventReadHead
	sessionEventReadAround
	sessionEventReadAfter
	sessionEventReadBefore
)

// sessionEventReadIntent is the parsed shape of one /timeline request.
// It carries the dispatch decision plus the cursor (if any) that needs the
// 409-resync existence check, plus the metric label and the anchor string
// echoed back in the response so the SPA can confirm what it got.
type sessionEventReadIntent struct {
	kind           sessionEventReadKind
	limit          int
	numBefore      int
	numAfter       int
	anchorOrderKey string
	afterOrderKey  string
	beforeOrderKey string
	validateCursor string
	timelineID     string
	metricLabel    string
	responseAnchor string
}

func sessionEventReadIntentFromRequest(r *http.Request) sessionEventReadIntent {
	q := r.URL.Query()
	anchor := strings.TrimSpace(q.Get("anchor"))
	timelineID := strings.TrimSpace(q.Get("timeline_id"))
	if timelineID == "" {
		timelineID = strings.TrimSpace(q.Get("message_id"))
	}
	if timelineID == "" {
		timelineID = strings.TrimSpace(q.Get("message"))
	}
	beforeOrderKey := strings.TrimSpace(q.Get("before_order_key"))
	afterOrderKey := strings.TrimSpace(q.Get("after_order_key"))
	if afterOrderKey == "" {
		afterOrderKey = strings.TrimSpace(q.Get("last_order_key"))
	}
	if afterOrderKey == "" {
		afterOrderKey = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}

	limit := parseSessionEventIntParam(q.Get("limit"), 200, 1, 1000)
	numBefore := parseSessionEventIntParam(q.Get("num_before"), 100, 0, 250)
	numAfter := parseSessionEventIntParam(q.Get("num_after"), 100, 0, 250)

	// Resolution precedence: explicit before_order_key wins (back-paginate),
	// then explicit transcript timeline_id/message anchors, then named
	// newest/oldest anchors or explicit order_key anchors, then
	// after_order_key (forward catch-up), then tail fallback.
	if beforeOrderKey != "" {
		return sessionEventReadIntent{
			kind:           sessionEventReadBefore,
			limit:          limit,
			beforeOrderKey: beforeOrderKey,
			validateCursor: beforeOrderKey,
			metricLabel:    "before",
			responseAnchor: "before:" + beforeOrderKey,
		}
	}
	if timelineID != "" {
		return sessionEventReadIntent{
			kind:           sessionEventReadAround,
			numBefore:      numBefore,
			numAfter:       numAfter,
			timelineID:     timelineID,
			metricLabel:    "timeline_id",
			responseAnchor: "timeline_id:" + timelineID,
		}
	}

	switch anchor {
	case "newest":
		return sessionEventReadIntent{
			kind:           sessionEventReadTail,
			limit:          limit,
			metricLabel:    "newest",
			responseAnchor: "newest",
		}
	case "oldest":
		// Head of ledger, ASC. Uses an empty cursor ascending scan, exposed
		// as a named, intentional
		// anchor so the metric label and the SPA contract reflect the
		// "jump to start" semantics. Sets FoundOldest=true; FoundNewest
		// when the ledger has <=limit events.
		return sessionEventReadIntent{
			kind:           sessionEventReadHead,
			limit:          limit,
			metricLabel:    "oldest",
			responseAnchor: "oldest",
		}
	case "":
		// no-op; falls through
	default:
		// Treat any non-keyword anchor as an order_key.
		return sessionEventReadIntent{
			kind:           sessionEventReadAround,
			numBefore:      numBefore,
			numAfter:       numAfter,
			anchorOrderKey: anchor,
			validateCursor: anchor,
			metricLabel:    "around",
			responseAnchor: "around:" + anchor,
		}
	}

	if afterOrderKey != "" {
		return sessionEventReadIntent{
			kind:           sessionEventReadAfter,
			limit:          limit,
			afterOrderKey:  afterOrderKey,
			validateCursor: afterOrderKey,
			metricLabel:    "after",
			responseAnchor: "after:" + afterOrderKey,
		}
	}

	return sessionEventReadIntent{
		kind:           sessionEventReadTail,
		limit:          limit,
		metricLabel:    "newest",
		responseAnchor: "newest",
	}
}

func resolveSessionEventTimelineAnchor(
	ctx context.Context,
	eventStore store.SessionEventStore,
	sessionID string,
	intent *sessionEventReadIntent,
) (int, error) {
	if intent == nil || strings.TrimSpace(intent.timelineID) == "" {
		return http.StatusOK, nil
	}
	orderKey, err := eventStore.OrderKeyForTimelineID(ctx, sessionID, intent.timelineID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if orderKey == "" {
		return http.StatusNotFound, fmt.Errorf("timeline target not found")
	}
	intent.anchorOrderKey = orderKey
	intent.validateCursor = ""
	intent.responseAnchor = "timeline_id:" + intent.timelineID
	return http.StatusOK, nil
}

func (s *appServer) runSessionEventRead(ctx context.Context, eventStore store.SessionEventStore, sessionID string, intent sessionEventReadIntent) (store.SessionEventPage, error) {
	switch intent.kind {
	case sessionEventReadTail:
		return eventStore.LatestEvents(ctx, sessionID, intent.limit)
	case sessionEventReadHead:
		// ASC from the head of the ledger. The empty cursor lands on the
		// `default` branch of ListBySession's switch, which is
		// `ORDER BY order_key ASC LIMIT $1` — the indexed forward scan.
		// sessionEventPageFromAscendingScan then stamps
		// FoundOldest=true (no AfterOrderKey/BeforeOrderKey was supplied)
		// and FoundNewest=!hasMore (no row beyond limit fetched).
		return eventStore.ListBySession(ctx, sessionID, store.SessionEventCursor{}, intent.limit)
	case sessionEventReadAround:
		return eventStore.EventsAround(ctx, sessionID, intent.anchorOrderKey, intent.numBefore, intent.numAfter)
	case sessionEventReadBefore:
		return eventStore.ListBySession(ctx, sessionID, store.SessionEventCursor{
			BeforeOrderKey: intent.beforeOrderKey,
			Direction:      "desc",
		}, intent.limit)
	case sessionEventReadAfter:
		return eventStore.ListBySession(ctx, sessionID, store.SessionEventCursor{
			AfterOrderKey: intent.afterOrderKey,
		}, intent.limit)
	default:
		return eventStore.LatestEvents(ctx, sessionID, intent.limit)
	}
}

func parseSessionEventIntParam(raw string, fallback, min, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
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
