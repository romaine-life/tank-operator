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

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

const (
	sessionEventStreamPageLimit              = 100
	sessionEventStreamHeartbeat              = 15 * time.Second
	transcriptMaterializationOnDemandTimeout = 60 * time.Second
)

type sessionEventStreamWakeReason string

const (
	sessionEventStreamWakeInitial   sessionEventStreamWakeReason = "initial"
	sessionEventStreamWakeDrain     sessionEventStreamWakeReason = "drain"
	sessionEventStreamWakeNotify    sessionEventStreamWakeReason = "notify"
	sessionEventStreamWakeHeartbeat sessionEventStreamWakeReason = "heartbeat"
)

// handleListSessionEvents returns the projected transcript-row read model.
// The durable session_events ledger remains the write source for projection
// and Turn activity detail, but main transcript navigation pages
// session_transcript_rows so the browser never asks for raw event windows.
func (s *appServer) handleListSessionEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	body, status, err := s.sessionTimelineBody(r.Context(), r, user, sessionID, sessionScope)
	if err != nil {
		if status >= 500 {
			recordSessionEventTimelineFailure()
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *appServer) sessionTimelineBody(ctx context.Context, r *http.Request, user auth.User, sessionID, sessionScope string) (map[string]any, int, error) {
	info, status, err := s.authorizeSessionTranscriptReadInScope(ctx, user, sessionID, sessionScope)
	if err != nil {
		return nil, status, err
	}

	eventStore := s.sessionEventStoreForScope(sessionScope)
	rowStore := s.sessionTranscriptRowStoreForScope(sessionScope)
	readState, err := s.getSessionReadState(r, user.OwnerEmail(), sessionID, sessionScope)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	intent, status, err := sessionTranscriptReadIntentFromRequest(r)
	if err != nil {
		return nil, status, err
	}
	recordSessionEventTimelineRequest(intent.metricLabel)
	if err := s.ensureSessionTranscriptRows(ctx, sessionID, sessionScope); err != nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("transcript materialization failed: %w", err)
	}
	page, targetCursor, status, err := runSessionTranscriptRowRead(ctx, rowStore, sessionID, intent)
	if err != nil {
		return nil, status, err
	}
	liveOrderKey := ""
	if live, err := eventStore.LatestEvents(ctx, sessionID, 1); err == nil && len(live.Events) > 0 {
		liveOrderKey = transcriptString(live.Events[len(live.Events)-1], "order_key")
	} else if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	body := map[string]any{
		"session_id":      sessionID,
		"rows":            page.Rows,
		"projection":      "server_transcript_rows_v1",
		"next_cursor":     page.NextCursor,
		"prev_cursor":     page.PrevCursor,
		"found_oldest":    page.FoundOldest,
		"found_newest":    page.FoundNewest,
		"anchor":          intent.responseAnchor,
		"cursor_semantic": "transcript_row",
		"live_order_key":  liveOrderKey,
		"activity":        info.Activity,
		"read_state":      sessionReadStateBody(readState),
	}
	if intent.timelineID != "" {
		body["target_timeline_id"] = intent.timelineID
		body["target_cursor"] = targetCursor
	}
	return body, http.StatusOK, nil
}

func (s *appServer) handleSessionTurnActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	turnID := strings.TrimSpace(r.PathValue("turn_id"))
	if turnID == "" {
		writeError(w, http.StatusBadRequest, "turn_id is required")
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if _, status, err := s.authorizeSessionTranscriptReadInScope(r.Context(), user, sessionID, sessionScope); err != nil {
		writeError(w, status, err.Error())
		return
	}
	eventStore := s.sessionEventStoreForScope(sessionScope)
	events, err := readAllTurnEvents(r.Context(), eventStore, sessionID, turnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pages := projectTurnPages(turnID, events)
	recordTurnActivityPages(len(pages.Pages), pages.TotalEventCount)

	// Default to the last page: for an active or just-completed long turn the
	// user wants the latest activity and the terminal, not the oldest prefix.
	selected := len(pages.Pages)
	if requested := strings.TrimSpace(r.URL.Query().Get("page")); requested != "" {
		if n, convErr := strconv.Atoi(requested); convErr == nil {
			selected = n
		}
	}
	if selected < 1 {
		selected = 1
	}
	if selected > len(pages.Pages) {
		selected = len(pages.Pages)
	}

	directory := pages.Shell["pages"]
	if directory == nil {
		directory = []map[string]any{}
	}
	body := map[string]any{
		"session_id":          sessionID,
		"turn_id":             turnID,
		"entries":             []map[string]any{},
		"compacted_entry_ids": []string{},
		"summary":             pages.Shell,
		"page":                selected,
		"page_count":          len(pages.Pages),
		"pages":               directory,
		"total_event_count":   pages.TotalEventCount,
		"has_more":            false,
		"cursor_semantic":     "order_key",
		"projection":          "server_turn_activity_v2",
	}
	if selected >= 1 && selected <= len(pages.Pages) {
		current := pages.Pages[selected-1]
		entries := current.Entries
		if entries == nil {
			entries = []map[string]any{}
		}
		body["entries"] = entries
		body["sealed"] = current.Sealed
		body["page_start_order_key"] = current.StartOrderKey
		body["page_end_order_key"] = current.EndOrderKey
		body["has_more"] = selected < len(pages.Pages)
	}
	writeJSON(w, http.StatusOK, body)
}

// handleResolveSessionTurnNumber maps a public per-session turn number to its
// durable turn_id and an anchor row cursor. This is the server-side resolution
// the transcript-navigation contract requires for deep links: a cold load of
// /sessions/{id}/turns/{n} resolves from session_turns regardless of what the
// browser has paged in, and an unknown number returns 404 so the SPA can show
// an explicit unavailable-target state instead of silently falling back.
func (s *appServer) handleResolveSessionTurnNumber(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	number, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("number")), 10, 64)
	if err != nil || number < 1 {
		recordTurnNumberResolve("invalid")
		writeError(w, http.StatusBadRequest, "turn number must be a positive integer")
		return
	}
	sessionScope, status, scopeErr := s.resolveSessionScopeFromRequest(user, r)
	if scopeErr != nil {
		writeError(w, status, scopeErr.Error())
		return
	}
	if _, status, err := s.authorizeSessionTranscriptReadInScope(r.Context(), user, sessionID, sessionScope); err != nil {
		writeError(w, status, err.Error())
		return
	}
	// Materialize transcript rows so a turn outside the live-tail window still
	// resolves to a usable anchor cursor. Resolution of the number itself is
	// durable (session_turns) and does not depend on materialization.
	if err := s.ensureSessionTranscriptRows(r.Context(), sessionID, sessionScope); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolution, found, err := s.sessionTurnStoreForScope(sessionScope).ResolveTurnNumber(r.Context(), sessionID, number)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		recordTurnNumberResolve("not_found")
		writeError(w, http.StatusNotFound, "turn not found")
		return
	}
	recordTurnNumberResolve("ok")
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":  sessionID,
		"turn_id":     resolution.TurnID,
		"turn_number": resolution.TurnNumber,
		"row_cursor":  resolution.RowCursor,
	})
}

func (s *appServer) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	s.handleListSessionEvents(w, r)
}

// handleSessionEventStream streams the server-owned transcript row projection
// over SSE. The durable session_events ledger remains the write source of truth
// and the resume cursor, but the browser's main transcript receives only
// top-level projected rows: messages, meta rows, background rows, and compacted
// turn_activity shells. Raw item/tool events stay behind the Turn activity
// detail endpoint and admin debug surfaces.
//
// Each browser event id is the latest Tank order_key represented by the emitted
// projected rows, so native EventSource reconnects resume without relying on
// runner-local websocket state.
func (s *appServer) handleSessionEventStream(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	user, sessionScope, ok := s.requireBrowserStreamAuth(w, r, streamKindSessionEvents, sessionID)
	if !ok {
		return
	}
	if _, status, err := s.authorizeSessionReadInScope(r.Context(), user, sessionID, sessionScope); err != nil {
		writeError(w, status, err.Error())
		return
	}
	eventStore := s.sessionEventStoreForScope(sessionScope)
	rowStore := s.sessionTranscriptRowStoreForScope(sessionScope)

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
	if ok, err := s.sessionEventCursorExists(r.Context(), eventStore, sessionID, cursor); err != nil {
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

	if err := s.ensureSessionTranscriptRows(r.Context(), sessionID, sessionScope); err != nil {
		recordSessionEventStreamError()
		slog.Warn("session event stream transcript materialization failed",
			"session_id", sessionID,
			"email", user.Email,
			"error", err,
		)
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{
			"reason": "transcript_materialization_failed",
			"detail": err.Error(),
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
	storageKey := sessionmodel.SessionStorageKey(sessionScope, sessionID)
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
		notify, unsubscribe, err = s.sessionBus.SubscribeWakesForStorageKey(r.Context(), storageKey, state)
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

	wakeReason := sessionEventStreamWakeInitial
	for {
		cursorBefore := cursor.AfterOrderKey
		hasMore, count, err := s.writeSessionEventStreamPage(r.Context(), w, rowStore, sessionID, &cursor, state)
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
			if isSessionEventStreamHeartbeatCatchup(wakeReason, count) {
				recordSessionEventStreamHeartbeatCatchup()
				slog.Warn("session event stream caught up from heartbeat",
					"session_id", sessionID,
					"email", user.Email,
					"stream_id", streamID,
					"storage_key", storageKey,
					"count", count,
					"cursor_before", cursorBefore,
					"cursor_after", cursor.AfterOrderKey,
				)
			}
			slog.Info("session event stream emitted transcript rows",
				"session_id", sessionID,
				"stream_id", streamID,
				"count", count,
				"cursor_before", cursorBefore,
				"cursor_after", cursor.AfterOrderKey,
				"has_more", hasMore,
			)
		}
		if hasMore {
			wakeReason = sessionEventStreamWakeDrain
			continue
		}

		select {
		case <-r.Context().Done():
			return
		case <-notify:
			wakeReason = sessionEventStreamWakeNotify
		case <-heartbeat.C:
			sessionEventStreamHeartbeatTotal.Add(1)
			state.RecordHeartbeat(time.Now())
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
			wakeReason = sessionEventStreamWakeHeartbeat
		}
	}
}

func isSessionEventStreamHeartbeatCatchup(wakeReason sessionEventStreamWakeReason, emitCount int) bool {
	return wakeReason == sessionEventStreamWakeHeartbeat && emitCount > 0
}

func (s *appServer) writeSessionEventStreamPage(ctx context.Context, w http.ResponseWriter, rowStore store.SessionTranscriptRowStore, sessionID string, cursor *store.SessionEventCursor, state *sessionstream.StreamState) (bool, int, error) {
	if rowStore == nil {
		rowStore = store.StubSessionTranscriptRowStore{}
	}
	page, err := rowStore.ListChangedAfterOrderKey(ctx, sessionID, cursor.AfterOrderKey, sessionEventStreamPageLimit)
	if err != nil {
		return false, 0, err
	}
	for _, delta := range page.Rows {
		recordSessionTranscriptRowLag(delta.UpdatedAt)
	}
	count := 0
	for _, group := range transcriptRowDeltaGroups(page.Rows) {
		orderKey := group.OrderKey
		if orderKey == "" {
			continue
		}
		writeSSEJSONEvent(w, "transcript-rows", orderKey, map[string]any{
			"order_key": orderKey,
			"rows":      group.Rows,
		})
		cursor.AfterOrderKey = orderKey
		count += len(group.Rows)
		sessionEventStreamEmittedTotal.Add(1)
		recordSessionEventStreamEmittedByType("transcript_rows")
		state.RecordEmit(time.Now(), orderKey, "transcript_rows", orderKey)
	}
	if page.NextOrderKey != "" {
		cursor.AfterOrderKey = page.NextOrderKey
	}
	return page.HasMore, count, nil
}

type transcriptRowDeltaGroup struct {
	OrderKey string
	Rows     []map[string]any
}

func transcriptRowDeltaGroups(deltas []store.TranscriptRowDelta) []transcriptRowDeltaGroup {
	groups := make([]transcriptRowDeltaGroup, 0, len(deltas))
	for _, delta := range deltas {
		orderKey := strings.TrimSpace(delta.OrderKey)
		if orderKey == "" || delta.Row == nil {
			continue
		}
		if len(groups) > 0 && groups[len(groups)-1].OrderKey == orderKey {
			groups[len(groups)-1].Rows = append(groups[len(groups)-1].Rows, delta.Row)
			continue
		}
		groups = append(groups, transcriptRowDeltaGroup{
			OrderKey: orderKey,
			Rows:     []map[string]any{delta.Row},
		})
	}
	return groups
}

func (s *appServer) sessionEventCursorExists(ctx context.Context, eventStore store.SessionEventStore, sessionID string, cursor store.SessionEventCursor) (bool, error) {
	if strings.TrimSpace(cursor.AfterOrderKey) == "" {
		return true, nil
	}
	if eventStore == nil {
		eventStore = store.StubSessionEventStore{}
	}
	return eventStore.HasOrderKey(ctx, sessionID, cursor.AfterOrderKey)
}

func (s *appServer) sessionEventStoreForScope(scope string) store.SessionEventStore {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() && s.sessionEvents != nil {
		return s.sessionEvents
	}
	if s.pgPool != nil {
		return store.NewPostgresSessionEventStore(s.pgPool, scope)
	}
	return store.StubSessionEventStore{}
}

func (s *appServer) sessionTranscriptRowStoreForScope(scope string) store.SessionTranscriptRowStore {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() && s.transcriptRows != nil {
		return s.transcriptRows
	}
	if s.pgPool != nil {
		return store.NewPostgresSessionTranscriptRowStore(s.pgPool, scope)
	}
	return store.StubSessionTranscriptRowStore{}
}

func (s *appServer) sessionTurnStoreForScope(scope string) store.SessionTurnStore {
	scope = normalizeSessionScope(scope)
	if scope == s.localSessionScope() && s.turns != nil {
		return s.turns
	}
	if s.pgPool != nil {
		return store.NewPostgresSessionTurnStore(s.pgPool, scope)
	}
	return store.StubSessionTurnStore{}
}

func (s *appServer) ensureSessionTranscriptRows(ctx context.Context, sessionID, scope string) error {
	materializer := transcriptRowsMaterializer{
		events: s.sessionEventStoreForScope(scope),
		rows:   s.sessionTranscriptRowStoreForScope(scope),
		turns:  s.sessionTurnStoreForScope(scope),
	}
	ctx, cancel := context.WithTimeout(ctx, transcriptMaterializationOnDemandTimeout)
	defer cancel()
	return materializer.EnsureSession(ctx, sessionID)
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

type sessionTranscriptReadKind int

const (
	sessionTranscriptReadTail sessionTranscriptReadKind = iota
	sessionTranscriptReadHead
	sessionTranscriptReadAround
	sessionTranscriptReadBefore
)

const (
	sessionTranscriptRowsDefault       = 24
	sessionTranscriptOlderRowsDefault  = 8
	sessionTranscriptRowsMax           = 80
	sessionTranscriptAroundRowsDefault = 12
	sessionTranscriptAroundRowsMax     = 40
)

type sessionTranscriptReadIntent struct {
	kind           sessionTranscriptReadKind
	rows           int
	rowsBefore     int
	rowsAfter      int
	beforeCursor   string
	timelineID     string
	metricLabel    string
	responseAnchor string
}

func sessionTranscriptReadIntentFromRequest(r *http.Request) (sessionTranscriptReadIntent, int, error) {
	q := r.URL.Query()
	for _, name := range []string{
		"limit",
		"before_order_key",
		"after_order_key",
		"last_order_key",
		"num_before",
		"num_after",
		"min_transcript_entries",
	} {
		if _, ok := q[name]; ok {
			return sessionTranscriptReadIntent{}, http.StatusBadRequest, fmt.Errorf("%s is not supported by /timeline; use transcript row cursors", name)
		}
	}
	if strings.TrimSpace(r.Header.Get("Last-Event-ID")) != "" {
		return sessionTranscriptReadIntent{}, http.StatusBadRequest, fmt.Errorf("Last-Event-ID is not supported by /timeline")
	}

	anchor := strings.TrimSpace(q.Get("anchor"))
	beforeCursor := strings.TrimSpace(q.Get("before_cursor"))
	timelineID := strings.TrimSpace(q.Get("timeline_id"))
	if timelineID == "" {
		timelineID = strings.TrimSpace(q.Get("message_id"))
	}
	if timelineID == "" {
		timelineID = strings.TrimSpace(q.Get("message"))
	}

	specifiedShapes := 0
	if beforeCursor != "" {
		specifiedShapes++
	}
	if timelineID != "" {
		specifiedShapes++
	}
	if anchor != "" {
		specifiedShapes++
	}
	if specifiedShapes > 1 {
		return sessionTranscriptReadIntent{}, http.StatusBadRequest, fmt.Errorf("specify only one timeline anchor")
	}

	if beforeCursor != "" {
		if _, err := store.DecodeTranscriptRowCursor(beforeCursor); err != nil {
			return sessionTranscriptReadIntent{}, http.StatusBadRequest, err
		}
		return sessionTranscriptReadIntent{
			kind:           sessionTranscriptReadBefore,
			rows:           parseSessionTranscriptIntParam(q.Get("rows"), sessionTranscriptOlderRowsDefault, 1, sessionTranscriptRowsMax),
			beforeCursor:   beforeCursor,
			metricLabel:    "before_cursor",
			responseAnchor: "before_cursor",
		}, http.StatusOK, nil
	}
	if timelineID != "" {
		return sessionTranscriptReadIntent{
			kind:           sessionTranscriptReadAround,
			rowsBefore:     parseSessionTranscriptIntParam(q.Get("rows_before"), sessionTranscriptAroundRowsDefault, 0, sessionTranscriptAroundRowsMax),
			rowsAfter:      parseSessionTranscriptIntParam(q.Get("rows_after"), sessionTranscriptAroundRowsDefault, 0, sessionTranscriptAroundRowsMax),
			timelineID:     timelineID,
			metricLabel:    "timeline_id",
			responseAnchor: "timeline_id:" + timelineID,
		}, http.StatusOK, nil
	}

	switch anchor {
	case "", "newest":
		return sessionTranscriptReadIntent{
			kind:           sessionTranscriptReadTail,
			rows:           parseSessionTranscriptIntParam(q.Get("rows"), sessionTranscriptRowsDefault, 1, sessionTranscriptRowsMax),
			metricLabel:    "newest",
			responseAnchor: "newest",
		}, http.StatusOK, nil
	case "oldest":
		return sessionTranscriptReadIntent{
			kind:           sessionTranscriptReadHead,
			rows:           parseSessionTranscriptIntParam(q.Get("rows"), sessionTranscriptRowsDefault, 1, sessionTranscriptRowsMax),
			metricLabel:    "oldest",
			responseAnchor: "oldest",
		}, http.StatusOK, nil
	default:
		return sessionTranscriptReadIntent{}, http.StatusBadRequest, fmt.Errorf("unsupported timeline anchor %q", anchor)
	}
}

func runSessionTranscriptRowRead(ctx context.Context, rowStore store.SessionTranscriptRowStore, sessionID string, intent sessionTranscriptReadIntent) (store.TranscriptRowPage, string, int, error) {
	if rowStore == nil {
		rowStore = store.StubSessionTranscriptRowStore{}
	}
	switch intent.kind {
	case sessionTranscriptReadTail:
		page, err := rowStore.ListLatest(ctx, sessionID, intent.rows)
		if err != nil {
			return store.TranscriptRowPage{}, "", http.StatusInternalServerError, err
		}
		return page, "", http.StatusOK, nil
	case sessionTranscriptReadHead:
		page, err := rowStore.ListOldest(ctx, sessionID, intent.rows)
		if err != nil {
			return store.TranscriptRowPage{}, "", http.StatusInternalServerError, err
		}
		return page, "", http.StatusOK, nil
	case sessionTranscriptReadBefore:
		page, err := rowStore.ListBefore(ctx, sessionID, intent.beforeCursor, intent.rows)
		if err != nil {
			return store.TranscriptRowPage{}, "", http.StatusInternalServerError, err
		}
		return page, "", http.StatusOK, nil
	case sessionTranscriptReadAround:
		targetCursor, err := rowStore.ResolveCursorForTimelineID(ctx, sessionID, intent.timelineID)
		if err != nil {
			return store.TranscriptRowPage{}, "", http.StatusInternalServerError, err
		}
		if targetCursor == "" {
			return store.TranscriptRowPage{}, "", http.StatusNotFound, fmt.Errorf("timeline target not found")
		}
		page, err := rowStore.ListAround(ctx, sessionID, targetCursor, intent.rowsBefore, intent.rowsAfter)
		if err != nil {
			return store.TranscriptRowPage{}, "", http.StatusInternalServerError, err
		}
		return page, targetCursor, http.StatusOK, nil
	default:
		page, err := rowStore.ListLatest(ctx, sessionID, sessionTranscriptRowsDefault)
		if err != nil {
			return store.TranscriptRowPage{}, "", http.StatusInternalServerError, err
		}
		return page, "", http.StatusOK, nil
	}
}

func parseSessionTranscriptIntParam(raw string, fallback, min, max int) int {
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
