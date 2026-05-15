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
	return s.pages[cursor.AfterOrderKey], nil
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
