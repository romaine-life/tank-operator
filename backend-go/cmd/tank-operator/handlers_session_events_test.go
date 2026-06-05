package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type fakeSessionEventStore struct {
	pages map[string]store.SessionEventPage
}

func (s fakeSessionEventStore) Upsert(_ context.Context, _ map[string]any) error {
	return nil
}

func (s fakeSessionEventStore) CountContextCompactions(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (s fakeSessionEventStore) FindStrandedLaunchTurns(context.Context, time.Time, time.Time, int) ([]store.StrandedLaunchTurn, error) {
	return nil, nil
}

func (s fakeSessionEventStore) ListBySession(_ context.Context, _ string, cursor store.SessionEventCursor, _ int) (store.SessionEventPage, error) {
	// Use whichever cursor field is set as the page key — tests target a
	// single shape per call and we don't need range semantics here.
	key := cursor.AfterOrderKey
	if cursor.BeforeOrderKey != "" {
		key = cursor.BeforeOrderKey
	}
	return s.pages[key], nil
}

func (s fakeSessionEventStore) LatestEvents(_ context.Context, _ string, _ int) (store.SessionEventPage, error) {
	// "" page key conventionally holds the tail for tests that exercise
	// anchor=newest. Mirrors the existing ListBySession("") fallback.
	return s.pages[""], nil
}

func (s fakeSessionEventStore) EventsForTurnAfter(_ context.Context, _ string, turnID string, afterOrderKey string, _ int) (store.SessionEventPage, error) {
	var events []map[string]any
	for _, page := range s.pages {
		for _, event := range page.Events {
			if event["turn_id"] != turnID {
				continue
			}
			if afterOrderKey != "" {
				if ok, _ := event["order_key"].(string); ok <= afterOrderKey {
					continue
				}
			}
			events = append(events, event)
		}
	}
	return store.SessionEventPage{
		Events:      events,
		FoundOldest: afterOrderKey == "",
		FoundNewest: true,
	}, nil
}

func (s fakeSessionEventStore) HasOrderKey(_ context.Context, _ string, orderKey string) (bool, error) {
	if orderKey == "" {
		return true, nil
	}
	for _, page := range s.pages {
		for _, event := range page.Events {
			if event["order_key"] == orderKey {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s fakeSessionEventStore) OrderKeyForTimelineID(_ context.Context, _ string, timelineID string) (string, error) {
	var newest string
	for _, page := range s.pages {
		for _, event := range page.Events {
			if event["timeline_id"] != timelineID {
				continue
			}
			orderKey, _ := event["order_key"].(string)
			if orderKey > newest {
				newest = orderKey
			}
		}
	}
	return newest, nil
}

func (s fakeSessionEventStore) FindTurnTerminal(_ context.Context, _ string, turnID string) (map[string]any, error) {
	for _, page := range s.pages {
		for _, event := range page.Events {
			if event["turn_id"] != turnID {
				continue
			}
			switch event["type"] {
			case "turn.completed", "turn.failed", "turn.interrupted":
				return event, nil
			}
		}
	}
	return nil, nil
}

func (s fakeSessionEventStore) LatestLifecycleEvents(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	return nil, nil
}

func (s fakeSessionEventStore) UnreadOutputCount(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}

type fakeSessionTranscriptRowStore struct {
	latestRows      int
	oldestRows      int
	changedAfter    string
	changedRows     int
	beforeCursor    string
	beforeRows      int
	aroundCursor    string
	aroundBefore    int
	aroundAfter     int
	resolveTimeline map[string]string
	pages           map[string]store.TranscriptRowPage
	deltaPages      map[string]store.TranscriptRowDeltaPage
	needsBackfill   bool
	needsErr        error
	needsCalls      int
	replaceSessions []string
}

func (s *fakeSessionTranscriptRowStore) ReplaceForTurn(context.Context, string, string, []map[string]any) error {
	return nil
}

func (s *fakeSessionTranscriptRowStore) ReplaceForSession(_ context.Context, sessionID string, entries []map[string]any) error {
	s.replaceSessions = append(s.replaceSessions, sessionID)
	s.needsBackfill = false
	if s.pages == nil {
		s.pages = map[string]store.TranscriptRowPage{}
	}
	s.pages["latest"] = store.TranscriptRowPage{
		Rows:        entries,
		FoundOldest: true,
		FoundNewest: true,
	}
	return nil
}

func (s *fakeSessionTranscriptRowStore) UpsertRows(context.Context, string, []map[string]any) error {
	return nil
}

func (s *fakeSessionTranscriptRowStore) ListChangedAfterOrderKey(_ context.Context, _ string, afterOrderKey string, rows int) (store.TranscriptRowDeltaPage, error) {
	s.changedAfter = afterOrderKey
	s.changedRows = rows
	return s.deltaPages[afterOrderKey], nil
}

func (s *fakeSessionTranscriptRowStore) ListLatest(_ context.Context, _ string, rows int) (store.TranscriptRowPage, error) {
	s.latestRows = rows
	return s.pages["latest"], nil
}

func (s *fakeSessionTranscriptRowStore) ListOldest(_ context.Context, _ string, rows int) (store.TranscriptRowPage, error) {
	s.oldestRows = rows
	return s.pages["oldest"], nil
}

func (s *fakeSessionTranscriptRowStore) ListBefore(_ context.Context, _ string, beforeCursor string, rows int) (store.TranscriptRowPage, error) {
	s.beforeCursor = beforeCursor
	s.beforeRows = rows
	return s.pages["before"], nil
}

func (s *fakeSessionTranscriptRowStore) ListAround(_ context.Context, _ string, rowCursor string, rowsBefore, rowsAfter int) (store.TranscriptRowPage, error) {
	s.aroundCursor = rowCursor
	s.aroundBefore = rowsBefore
	s.aroundAfter = rowsAfter
	return s.pages["around"], nil
}

func (s *fakeSessionTranscriptRowStore) ResolveCursorForTimelineID(_ context.Context, _ string, timelineID string) (string, error) {
	return s.resolveTimeline[timelineID], nil
}

func (s *fakeSessionTranscriptRowStore) NeedsBackfill(context.Context, string) (bool, error) {
	s.needsCalls++
	if s.needsErr != nil {
		return false, s.needsErr
	}
	return s.needsBackfill, nil
}

func TestSessionEventCursorFromRequestUsesOrderKeyOnly(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		header string
		want   string
	}{
		{name: "stream cursor", url: "/api/sessions/63/events?last_order_key=order-b", want: "order-b"},
		{name: "last event id wins", url: "/api/sessions/63/events?last_order_key=order-b", header: "order-c", want: "order-c"},
		{name: "old generic cursor ignored", url: "/api/sessions/63/events?cursor=order-d", want: ""},
		{name: "old document cursor ignored", url: "/api/sessions/63/events?after=event-id", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)
			if tt.header != "" {
				req.Header.Set("Last-Event-ID", tt.header)
			}
			got := sessionEventCursorFromRequest(req)
			if got.AfterOrderKey != tt.want {
				t.Fatalf("AfterOrderKey = %q, want %q", got.AfterOrderKey, tt.want)
			}
		})
	}
}

// The endpoint contract for the bug this fix targets: a turn with more than
// turnPageEventLimit events still reports a completed (not perpetually active)
// shell, splits into pages, and defaults to the last page.
func TestHandleSessionTurnActivityPaginatesOverLimitTurnWithTerminalShell(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"

	var events []map[string]any
	seq := 0
	next := func() string { seq++; return fmt.Sprintf("%08d", seq) }
	events = append(events,
		projectionTestEvent("u", next(), "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
			"text": "go", "display": map[string]any{"kind": "plain"},
		}),
		projectionTestEvent("submitted", next(), "turn.submitted", "runner", "tank", "turn-1", "", map[string]any{"status": "submitted"}),
	)
	var lastMsg string
	for i := 0; i < turnPageEventLimit+10; i++ {
		lastMsg = fmt.Sprintf("turn-1:item:m-%d", i)
		events = append(events, projectionTestEvent(fmt.Sprintf("m-%d", i), next(), "item.completed", "assistant", "claude", "turn-1", lastMsg,
			map[string]any{"kind": "message", "text": fmt.Sprintf("step %d", i)}))
	}
	events = append(events, projectionTestEvent("terminal", next(), "turn.completed", "runner", "claude", "turn-1", "", projectionFinalAnswerPayload(lastMsg)))

	app.sessionEvents = fakeSessionEventStore{pages: map[string]store.SessionEventPage{
		"": {Events: events, FoundOldest: true, FoundNewest: true},
	}}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/turn-1/activity", nil)
	req.SetPathValue("session_id", "63")
	req.SetPathValue("turn_id", "turn-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleSessionTurnActivity(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	summary, _ := body["summary"].(map[string]any)
	if got, _ := summary["status"].(string); got != "completed" {
		t.Fatalf("summary.status = %q, want completed (terminal must survive an over-limit turn)", got)
	}
	pageCount, _ := body["page_count"].(float64)
	if pageCount < 2 {
		t.Fatalf("page_count = %v, want >= 2", body["page_count"])
	}
	if page, _ := body["page"].(float64); page != pageCount {
		t.Fatalf("default page = %v, want last page %v", body["page"], pageCount)
	}
	entries, _ := body["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("last page entries empty, want the tail of the turn")
	}
}

func TestHandleSessionTurnActivityDefaultsNeedsInputToQuestionPage(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"

	events := []map[string]any{
		projectionTestEvent("submitted", "00000001", "turn.submitted", "runner", "tank", "turn-2", "", map[string]any{"status": "submitted"}),
		projectionTestEvent("await", "00000002", "turn.awaiting_input", "runner", "claude", "turn-2", "turn-2:item:ask", map[string]any{
			"asking_turn_id":       "turn-1",
			"question_turn_id":     "turn-2",
			"provider_item_id":     "toolu_ask",
			"timeline_id":          "turn-2:item:ask",
			"provider_timeline_id": "turn-1:item:ask",
			"questions": []any{
				map[string]any{
					"question": "Proceed?",
					"options":  []any{map[string]any{"label": "Yes"}, map[string]any{"label": "No"}},
				},
				map[string]any{
					"question":      "Anything else?",
					"allowFreeForm": true,
				},
			},
		}),
	}
	app.sessionEvents = fakeSessionEventStore{pages: map[string]store.SessionEventPage{
		"": {Events: events, FoundOldest: true, FoundNewest: true},
	}}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/turn-2/activity", nil)
	req.SetPathValue("session_id", "63")
	req.SetPathValue("turn_id", "turn-2")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleSessionTurnActivity(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := body["page"].(float64); got != 1 {
		t.Fatalf("default page = %v, want first pending question page 1", body["page"])
	}
	if got, _ := body["page_count"].(float64); got != 2 {
		t.Fatalf("page_count = %v, want two question pages", body["page_count"])
	}
	if got, _ := body["page_kind"].(string); got != "question" {
		t.Fatalf("page_kind = %q, want question", got)
	}
	if got, _ := body["question_index"].(float64); got != 1 {
		t.Fatalf("question_index = %v, want 1", body["question_index"])
	}
	if got, _ := body["question_set"].(float64); got != 1 {
		t.Fatalf("question_set = %v, want 1", body["question_set"])
	}
	if got, _ := body["question_count"].(float64); got != 2 {
		t.Fatalf("question_count = %v, want 2", body["question_count"])
	}
	if body["answered"] == true {
		t.Fatalf("answered = true, want false for pending question set")
	}
	entries, _ := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want one question card entry", len(entries))
	}
	entry, _ := entries[0].(map[string]any)
	if got, _ := entry["metaKind"].(string); got != "awaiting_input" {
		t.Fatalf("entry metaKind = %q, want awaiting_input", got)
	}
	awaiting, _ := entry["awaitingInput"].(map[string]any)
	if got, _ := awaiting["questionIndex"].(float64); got != 1 {
		t.Fatalf("entry awaitingInput.questionIndex = %v, want 1", awaiting["questionIndex"])
	}
	if got, _ := awaiting["questionSet"].(float64); got != 1 {
		t.Fatalf("entry awaitingInput.questionSet = %v, want 1", awaiting["questionSet"])
	}
}

func TestHandleSessionTurnActivityRejectsNonAdminProdScopeFromTestSlot(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "tank-operator-slot-1"

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/63/turns/turn-1/activity?session_scope=default",
		nil,
	)
	req.SetPathValue("session_id", "63")
	req.SetPathValue("turn_id", "turn-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	res := httptest.NewRecorder()

	app.handleSessionTurnActivity(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", res.Code, res.Body.String())
	}
}

func TestSessionTranscriptReadIntentFromRequestAnchorShapes(t *testing.T) {
	beforeCursor := store.EncodeTranscriptRowCursor("order-002\x1frow-2")
	tests := []struct {
		name           string
		url            string
		wantKind       sessionTranscriptReadKind
		wantLabel      string
		wantRows       int
		wantBeforeRows int
		wantAfterRows  int
		wantCursor     string
		wantTimelineID string
	}{
		{
			name:      "no params → tail",
			url:       "/api/sessions/s/timeline",
			wantKind:  sessionTranscriptReadTail,
			wantLabel: "newest",
			wantRows:  sessionTranscriptRowsDefault,
		},
		{
			name:      "anchor=newest → tail",
			url:       "/api/sessions/s/timeline?anchor=newest&rows=30",
			wantKind:  sessionTranscriptReadTail,
			wantLabel: "newest",
			wantRows:  30,
		},
		{
			name:      "anchor=oldest → head rows",
			url:       "/api/sessions/s/timeline?anchor=oldest",
			wantKind:  sessionTranscriptReadHead,
			wantLabel: "oldest",
			wantRows:  sessionTranscriptRowsDefault,
		},
		{
			name:           "timeline_id → deferred around lookup",
			url:            "/api/sessions/s/timeline?timeline_id=turn-1:item:msg-1&rows_before=7&rows_after=9",
			wantKind:       sessionTranscriptReadAround,
			wantLabel:      "timeline_id",
			wantBeforeRows: 7,
			wantAfterRows:  9,
			wantTimelineID: "turn-1:item:msg-1",
		},
		{
			name:           "message aliases timeline_id for copied links",
			url:            "/api/sessions/s/timeline?message=turn-1:item:msg-1",
			wantKind:       sessionTranscriptReadAround,
			wantLabel:      "timeline_id",
			wantBeforeRows: sessionTranscriptAroundRowsDefault,
			wantAfterRows:  sessionTranscriptAroundRowsDefault,
			wantTimelineID: "turn-1:item:msg-1",
		},
		{
			name:       "before_cursor → back-paginate rows",
			url:        "/api/sessions/s/timeline?before_cursor=" + beforeCursor + "&rows=6",
			wantKind:   sessionTranscriptReadBefore,
			wantLabel:  "before_cursor",
			wantRows:   6,
			wantCursor: beforeCursor,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.url, nil)
			got, status, err := sessionTranscriptReadIntentFromRequest(req)
			if err != nil || status != http.StatusOK {
				t.Fatalf("intent status=%d err=%v", status, err)
			}
			if got.kind != tc.wantKind {
				t.Fatalf("kind = %d, want %d", got.kind, tc.wantKind)
			}
			if got.metricLabel != tc.wantLabel {
				t.Fatalf("metricLabel = %q, want %q", got.metricLabel, tc.wantLabel)
			}
			if got.rows != tc.wantRows {
				t.Fatalf("rows = %d, want %d", got.rows, tc.wantRows)
			}
			if got.rowsBefore != tc.wantBeforeRows || got.rowsAfter != tc.wantAfterRows {
				t.Fatalf("rows around = (%d,%d), want (%d,%d)", got.rowsBefore, got.rowsAfter, tc.wantBeforeRows, tc.wantAfterRows)
			}
			if got.beforeCursor != tc.wantCursor {
				t.Fatalf("beforeCursor = %q, want %q", got.beforeCursor, tc.wantCursor)
			}
			if got.timelineID != tc.wantTimelineID {
				t.Fatalf("timelineID = %q, want %q", got.timelineID, tc.wantTimelineID)
			}
		})
	}
}

func TestSessionTranscriptReadIntentRejectsRawEventTimelineParams(t *testing.T) {
	for _, raw := range []string{
		"limit=200",
		"before_order_key=001",
		"after_order_key=001",
		"last_order_key=001",
		"num_before=100",
		"num_after=100",
		"min_transcript_entries=24",
		"anchor=001",
		"before_cursor=not-a-cursor",
		"anchor=newest&before_cursor=" + store.EncodeTranscriptRowCursor("001\x1frow"),
	} {
		t.Run(raw, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/sessions/s/timeline?"+raw, nil)
			_, status, err := sessionTranscriptReadIntentFromRequest(req)
			if err == nil || status != http.StatusBadRequest {
				t.Fatalf("status=%d err=%v, want 400", status, err)
			}
		})
	}
}

func TestRunSessionTranscriptRowReadUsesRowStore(t *testing.T) {
	targetCursor := store.EncodeTranscriptRowCursor("order-010\x1frow-10")
	rows := []map[string]any{{"id": "row-10", "kind": "message", "orderKey": "order-010"}}
	rowStore := &fakeSessionTranscriptRowStore{
		resolveTimeline: map[string]string{"turn-1:item:msg-1": targetCursor},
		pages: map[string]store.TranscriptRowPage{
			"around": {Rows: rows, PrevCursor: "prev", NextCursor: "next", FoundOldest: false, FoundNewest: false},
		},
	}

	intent := sessionTranscriptReadIntent{
		kind:       sessionTranscriptReadAround,
		rowsBefore: 7,
		rowsAfter:  9,
		timelineID: "turn-1:item:msg-1",
	}
	page, gotTargetCursor, status, err := runSessionTranscriptRowRead(context.Background(), rowStore, "63", intent)
	if err != nil || status != http.StatusOK {
		t.Fatalf("row read status=%d err=%v", status, err)
	}
	if gotTargetCursor != targetCursor {
		t.Fatalf("target cursor = %q, want %q", gotTargetCursor, targetCursor)
	}
	if rowStore.aroundCursor != targetCursor || rowStore.aroundBefore != 7 || rowStore.aroundAfter != 9 {
		t.Fatalf("around dispatch = cursor:%q before:%d after:%d", rowStore.aroundCursor, rowStore.aroundBefore, rowStore.aroundAfter)
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "row-10" {
		t.Fatalf("rows = %#v", page.Rows)
	}

	missing := sessionTranscriptReadIntent{
		kind:       sessionTranscriptReadAround,
		timelineID: "missing",
	}
	_, _, status, err = runSessionTranscriptRowRead(context.Background(), rowStore, "63", missing)
	if err == nil || status != http.StatusNotFound {
		t.Fatalf("missing status=%d err=%v, want 404", status, err)
	}
}

func TestSessionTimelineMaterializesStaleTranscriptRowsBeforeRead(t *testing.T) {
	app := adminTestServer(t)
	eventStore := fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {
				Events: []map[string]any{
					projectionTestEvent("turn-1:user", "001", "user_message.created", "user", "tank", "turn-1", "turn-1:user", map[string]any{
						"text": "hello",
					}),
				},
				FoundOldest: true,
				FoundNewest: true,
			},
		},
	}
	rowStore := &fakeSessionTranscriptRowStore{needsBackfill: true}
	app.sessionEvents = eventStore
	app.transcriptRows = rowStore
	app.sessionScope = "default"

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/timeline", nil)
	body, status, err := app.sessionTimelineBody(context.Background(), req, auth.User{
		Email: otherUser,
		Role:  auth.RoleUser,
	}, "63", "default")
	if err != nil || status != http.StatusOK {
		t.Fatalf("timeline status=%d err=%v", status, err)
	}
	if rowStore.needsCalls < 2 {
		t.Fatalf("NeedsBackfill calls = %d, want initial check and pre-replace recheck", rowStore.needsCalls)
	}
	if len(rowStore.replaceSessions) != 1 || rowStore.replaceSessions[0] != "63" {
		t.Fatalf("ReplaceForSession sessions = %#v, want [63]", rowStore.replaceSessions)
	}
	rowsBody, _ := body["rows"].([]map[string]any)
	if len(rowsBody) != 1 || rowsBody[0]["id"] != "turn-1:user" {
		t.Fatalf("timeline rows = %#v", body["rows"])
	}
}

func TestSessionEventStreamFailsBeforeReadyWhenTranscriptMaterializationFails(t *testing.T) {
	app := adminTestServer(t)
	app.streamAuthTickets = &fakeStreamAuthTicketStore{
		validateResponse: pgstore.StreamAuthTicket{
			Sub:          "sub-" + otherUser,
			Email:        otherUser,
			Role:         auth.RoleUser,
			StreamKind:   streamKindSessionEvents,
			SessionScope: "default",
			SessionID:    "63",
		},
	}
	app.sessionScope = "default"
	app.sessionEvents = fakeSessionEventStore{pages: map[string]store.SessionEventPage{}}
	app.transcriptRows = &fakeSessionTranscriptRowStore{
		needsBackfill: true,
		needsErr:      errors.New("row marker unavailable"),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/events?stream_ticket=ticket-123", nil)
	req.SetPathValue("session_id", "63")
	rec := httptest.NewRecorder()

	app.handleSessionEventStream(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "transcript_materialization_failed") {
		t.Fatalf("SSE body missing materialization failure:\n%s", body)
	}
	if strings.Contains(body, "event: ready") {
		t.Fatalf("SSE wrote ready after materialization failure:\n%s", body)
	}
}

func TestWriteSessionEventStreamPageEmitsProjectedTranscriptRows(t *testing.T) {
	rowStore := &fakeSessionTranscriptRowStore{
		deltaPages: map[string]store.TranscriptRowDeltaPage{
			"order-001": {
				Rows: []store.TranscriptRowDelta{
					{
						OrderKey: "order-002",
						Row: map[string]any{
							"id":       "row-assistant",
							"kind":     "message",
							"role":     "assistant",
							"text":     "done",
							"orderKey": "order-002",
						},
					},
					{
						OrderKey: "order-002",
						Row: map[string]any{
							"id":       "turn-activity-turn-1",
							"kind":     "turn_activity",
							"turnId":   "turn-1",
							"orderKey": "order-001",
						},
					},
				},
				NextOrderKey: "order-002",
				HasMore:      true,
			},
		},
	}
	cursor := store.SessionEventCursor{AfterOrderKey: "order-001"}
	rec := httptest.NewRecorder()

	hasMore, count, err := (&appServer{}).writeSessionEventStreamPage(context.Background(), rec, rowStore, "63", &cursor, nil)
	if err != nil {
		t.Fatalf("write page failed: %v", err)
	}
	if !hasMore {
		t.Fatal("hasMore = false, want true")
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if rowStore.changedAfter != "order-001" || rowStore.changedRows != sessionEventStreamPageLimit {
		t.Fatalf("row store cursor=%q rows=%d", rowStore.changedAfter, rowStore.changedRows)
	}
	if cursor.AfterOrderKey != "order-002" {
		t.Fatalf("cursor.AfterOrderKey = %q, want order-002", cursor.AfterOrderKey)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"id: order-002\n",
		"event: transcript-rows\n",
		`"order_key":"order-002"`,
		`"id":"row-assistant"`,
		`"id":"turn-activity-turn-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "event: tank-event\n") {
		t.Fatalf("SSE body leaked raw tank-event:\n%s", body)
	}
}

func TestSessionEventStreamHeartbeatCatchupOnlyCountsHeartbeatEmits(t *testing.T) {
	tests := []struct {
		name       string
		wakeReason sessionEventStreamWakeReason
		emitCount  int
		want       bool
	}{
		{
			name:       "heartbeat emitted rows",
			wakeReason: sessionEventStreamWakeHeartbeat,
			emitCount:  1,
			want:       true,
		},
		{
			name:       "heartbeat empty read",
			wakeReason: sessionEventStreamWakeHeartbeat,
			emitCount:  0,
			want:       false,
		},
		{
			name:       "initial replay emitted rows",
			wakeReason: sessionEventStreamWakeInitial,
			emitCount:  1,
			want:       false,
		},
		{
			name:       "nats wake emitted rows",
			wakeReason: sessionEventStreamWakeNotify,
			emitCount:  1,
			want:       false,
		},
		{
			name:       "page drain emitted rows",
			wakeReason: sessionEventStreamWakeDrain,
			emitCount:  1,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSessionEventStreamHeartbeatCatchup(tt.wakeReason, tt.emitCount); got != tt.want {
				t.Fatalf("isSessionEventStreamHeartbeatCatchup(%q, %d) = %v, want %v", tt.wakeReason, tt.emitCount, got, tt.want)
			}
		})
	}
}

func TestWriteSSEJSONEventUsesOrderKeyAsEventID(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSEJSONEvent(rec, "tank-event", "001\nignored", map[string]any{
		"event_id":  "evt-1",
		"order_key": "001",
	})

	body := rec.Body.String()
	for _, want := range []string{
		"id: 001ignored\n",
		"event: tank-event\n",
		`data: {"event_id":"evt-1","order_key":"001"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q:\n%s", want, body)
		}
	}
}
