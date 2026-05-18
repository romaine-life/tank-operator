package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

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

func (s fakeSessionEventStore) EventsAround(_ context.Context, _ string, anchorOrderKey string, _ int, _ int) (store.SessionEventPage, error) {
	return s.pages[anchorOrderKey], nil
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

func TestSessionEventCursorFromRequestUsesOrderKeyOnly(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		header string
		want   string
	}{
		{name: "timeline cursor", url: "/api/sessions/63/timeline?after_order_key=order-a", want: "order-a"},
		{name: "stream cursor", url: "/api/sessions/63/events?last_order_key=order-b", want: "order-b"},
		{name: "last event id wins", url: "/api/sessions/63/events?last_order_key=order-b", header: "order-c", want: "order-c"},
		{name: "old generic cursor ignored", url: "/api/sessions/63/events?cursor=order-d", want: ""},
		{name: "old document cursor ignored", url: "/api/sessions/63/timeline?after=event-id", want: ""},
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

func TestSessionEventReadIntentFromRequestAnchorShapes(t *testing.T) {
	readState := &store.ConversationReadStateRecord{LastReadOrderKey: "order-r"}

	tests := []struct {
		name           string
		url            string
		readState      *store.ConversationReadStateRecord
		wantKind       sessionEventReadKind
		wantLabel      string
		wantValidate   string
		wantAnchorKey  string
		wantBeforeKey  string
		wantAfterKey   string
	}{
		{
			name:      "no params → legacy_forward (Stage 1 transitional)",
			url:       "/api/sessions/s/timeline",
			wantKind:  sessionEventReadLegacyForward,
			wantLabel: "legacy_forward",
		},
		{
			name:      "anchor=newest → tail",
			url:       "/api/sessions/s/timeline?anchor=newest",
			wantKind:  sessionEventReadTail,
			wantLabel: "newest",
		},
		{
			// Symmetric counterpart of anchor=newest. No cursor validation
			// because the head of the ledger is not a caller-supplied key.
			name:      "anchor=oldest → head",
			url:       "/api/sessions/s/timeline?anchor=oldest",
			wantKind:  sessionEventReadHead,
			wantLabel: "oldest",
		},
		{
			name:          "anchor=first_unread with read state → around",
			url:           "/api/sessions/s/timeline?anchor=first_unread",
			readState:     readState,
			wantKind:      sessionEventReadAround,
			wantLabel:     "first_unread",
			wantAnchorKey: "order-r",
		},
		{
			name:      "anchor=first_unread without read state → tail",
			url:       "/api/sessions/s/timeline?anchor=first_unread",
			wantKind:  sessionEventReadTail,
			wantLabel: "first_unread",
		},
		{
			name:          "anchor=<order_key> → around with validation",
			url:           "/api/sessions/s/timeline?anchor=order-x",
			wantKind:      sessionEventReadAround,
			wantLabel:     "around",
			wantValidate:  "order-x",
			wantAnchorKey: "order-x",
		},
		{
			name:          "before_order_key → back-paginate",
			url:           "/api/sessions/s/timeline?before_order_key=order-y",
			wantKind:      sessionEventReadBefore,
			wantLabel:     "before",
			wantValidate:  "order-y",
			wantBeforeKey: "order-y",
		},
		{
			name:         "after_order_key → forward catch-up",
			url:          "/api/sessions/s/timeline?after_order_key=order-z",
			wantKind:     sessionEventReadAfter,
			wantLabel:    "after",
			wantValidate: "order-z",
			wantAfterKey: "order-z",
		},
		{
			name:          "before wins over anchor (back-paginate is the most specific intent)",
			url:           "/api/sessions/s/timeline?anchor=newest&before_order_key=order-y",
			wantKind:      sessionEventReadBefore,
			wantLabel:     "before",
			wantValidate:  "order-y",
			wantBeforeKey: "order-y",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.url, nil)
			got := sessionEventReadIntentFromRequest(req, tc.readState)
			if got.kind != tc.wantKind {
				t.Fatalf("kind = %d, want %d", got.kind, tc.wantKind)
			}
			if got.metricLabel != tc.wantLabel {
				t.Fatalf("metricLabel = %q, want %q", got.metricLabel, tc.wantLabel)
			}
			if got.validateCursor != tc.wantValidate {
				t.Fatalf("validateCursor = %q, want %q", got.validateCursor, tc.wantValidate)
			}
			if got.anchorOrderKey != tc.wantAnchorKey {
				t.Fatalf("anchorOrderKey = %q, want %q", got.anchorOrderKey, tc.wantAnchorKey)
			}
			if got.beforeOrderKey != tc.wantBeforeKey {
				t.Fatalf("beforeOrderKey = %q, want %q", got.beforeOrderKey, tc.wantBeforeKey)
			}
			if got.afterOrderKey != tc.wantAfterKey {
				t.Fatalf("afterOrderKey = %q, want %q", got.afterOrderKey, tc.wantAfterKey)
			}
		})
	}
}

func TestSessionEventReadIntentDefaultsAndCaps(t *testing.T) {
	// Defaults: limit=200, num_before=100, num_after=100.
	req := httptest.NewRequest("GET", "/api/sessions/s/timeline?anchor=first_unread", nil)
	got := sessionEventReadIntentFromRequest(req, &store.ConversationReadStateRecord{LastReadOrderKey: "x"})
	if got.numBefore != 100 || got.numAfter != 100 {
		t.Fatalf("default num_before/num_after = (%d,%d), want (100,100)", got.numBefore, got.numAfter)
	}

	// Caps: num_before > 250 clamps to 250; limit > 1000 clamps to 1000.
	req = httptest.NewRequest("GET", "/api/sessions/s/timeline?anchor=newest&limit=5000", nil)
	got = sessionEventReadIntentFromRequest(req, nil)
	if got.limit != 1000 {
		t.Fatalf("limit cap = %d, want 1000", got.limit)
	}

	req = httptest.NewRequest("GET", "/api/sessions/s/timeline?anchor=order-x&num_before=9999&num_after=9999", nil)
	got = sessionEventReadIntentFromRequest(req, nil)
	if got.numBefore != 250 || got.numAfter != 250 {
		t.Fatalf("num_before/num_after caps = (%d,%d), want (250,250)", got.numBefore, got.numAfter)
	}
}

// cursorRecordingFakeStore wraps fakeSessionEventStore but captures the
// cursor passed to ListBySession so dispatch tests can prove that a given
// read kind targets the right indexed scan (empty cursor = head/legacy,
// AfterOrderKey set = forward, BeforeOrderKey set = backward). Named
// distinctly from recordingSessionEventStore in handlers_turns_test.go,
// which captures Upserts for a different test surface.
type cursorRecordingFakeStore struct {
	fakeSessionEventStore
	lastCursor store.SessionEventCursor
	lastLimit  int
}

func (s *cursorRecordingFakeStore) ListBySession(ctx context.Context, sessionID string, cursor store.SessionEventCursor, limit int) (store.SessionEventPage, error) {
	s.lastCursor = cursor
	s.lastLimit = limit
	return s.fakeSessionEventStore.ListBySession(ctx, sessionID, cursor, limit)
}

func TestRunSessionEventReadAnchorOldestUsesEmptyCursor(t *testing.T) {
	// anchor=oldest must dispatch ListBySession with an empty cursor so the
	// store's ascending scan stamps FoundOldest=true (no AfterOrderKey /
	// BeforeOrderKey supplied — see sessionEventPageFromAscendingScan).
	rec := &cursorRecordingFakeStore{
		fakeSessionEventStore: fakeSessionEventStore{
			pages: map[string]store.SessionEventPage{
				"": {
					Events: []map[string]any{
						{"event_id": "e1", "order_key": "001", "type": "item.completed"},
					},
					FoundOldest:  true,
					FoundNewest:  true,
					NextOrderKey: "001",
					PrevOrderKey: "001",
				},
			},
		},
	}
	app := &appServer{sessionEvents: rec}

	intent := sessionEventReadIntent{
		kind:           sessionEventReadHead,
		limit:          200,
		metricLabel:    "oldest",
		responseAnchor: "oldest",
	}
	page, err := app.runSessionEventRead(context.Background(), rec, "63", intent)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if rec.lastCursor.AfterOrderKey != "" || rec.lastCursor.BeforeOrderKey != "" {
		t.Fatalf("anchor=oldest must pass empty cursor; got %+v", rec.lastCursor)
	}
	if rec.lastLimit != 200 {
		t.Fatalf("limit propagation: got %d, want 200", rec.lastLimit)
	}
	if !page.FoundOldest {
		t.Fatalf("anchor=oldest must surface FoundOldest=true; got %+v", page)
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
