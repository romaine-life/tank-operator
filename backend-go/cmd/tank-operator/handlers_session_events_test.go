package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type fakeSessionEventStore struct {
	pages map[string]store.SessionEventPage
}

func (s fakeSessionEventStore) Upsert(_ context.Context, _ map[string]any) error {
	return nil
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

func (s fakeSessionEventStore) EventsForTurn(_ context.Context, _ string, turnID string, _ int) (store.SessionEventPage, error) {
	var events []map[string]any
	for _, page := range s.pages {
		for _, event := range page.Events {
			if event["turn_id"] == turnID {
				events = append(events, event)
			}
		}
	}
	return store.SessionEventPage{
		Events:      events,
		FoundOldest: true,
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
	beforeCursor    string
	beforeRows      int
	aroundCursor    string
	aroundBefore    int
	aroundAfter     int
	resolveTimeline map[string]string
	pages           map[string]store.TranscriptRowPage
}

func (s *fakeSessionTranscriptRowStore) ReplaceForTurn(context.Context, string, string, []map[string]any) error {
	return nil
}

func (s *fakeSessionTranscriptRowStore) ReplaceForSession(context.Context, string, []map[string]any) error {
	return nil
}

func (s *fakeSessionTranscriptRowStore) UpsertRows(context.Context, string, []map[string]any) error {
	return nil
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

func (s *fakeSessionTranscriptRowStore) BackfillSessionIDs(context.Context) ([]string, error) {
	return nil, nil
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
