package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

func TestSessionEventCursorFromRequestAcceptsResumeAliases(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "timeline cursor", url: "/api/sessions/63/timeline?after_order_key=cursor-a", want: "cursor-a"},
		{name: "websocket last order key", url: "/api/sessions/63/agent-ws?last_order_key=cursor-b", want: "cursor-b"},
		{name: "generic cursor", url: "/api/sessions/63/agent-ws?cursor=cursor-c", want: "cursor-c"},
		{name: "last cursor", url: "/api/sessions/63/agent-ws?last_cursor=cursor-d", want: "cursor-d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)
			got := sessionEventCursorFromRequest(req)
			if got.AfterOrderKey != tt.want {
				t.Fatalf("AfterOrderKey = %q, want %q", got.AfterOrderKey, tt.want)
			}
		})
	}
}

type fakeSessionEventStore struct {
	pages map[string]store.SessionEventPage
}

func (s fakeSessionEventStore) ListBySession(_ context.Context, _ string, cursor store.SessionEventCursor, _ int) (store.SessionEventPage, error) {
	return s.pages[cursor.AfterOrderKey], nil
}

func TestAgentWSReplayUsesReconnectCursor(t *testing.T) {
	app := &appServer{
		sessionEvents: fakeSessionEventStore{
			pages: map[string]store.SessionEventPage{
				"cursor-1": {
					Events: []map[string]any{
						{"event_id": "e2", "order_key": "cursor-2", "type": "item.completed"},
						{"event_id": "e3", "order_key": "cursor-3", "type": "turn.completed"},
					},
					NextOrderKey: "cursor-3",
				},
				"cursor-2": {
					Events: []map[string]any{
						{"event_id": "e3", "order_key": "cursor-3", "type": "turn.completed"},
					},
					NextOrderKey: "cursor-3",
				},
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		replayed, lastOrderKey, err := app.replaySessionEventsToWebSocket(
			r.Context(),
			conn,
			"63",
			sessionEventCursorFromRequest(r),
		)
		if err != nil {
			t.Errorf("replay events: %v", err)
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, mustJSON(map[string]any{
			"type":           "tank.transport.subscribed",
			"replayed":       replayed,
			"last_order_key": lastOrderKey,
		}))
	}))
	defer server.Close()

	first := readReplayMessages(t, server.URL, "cursor-1")
	if got := eventIDs(first); strings.Join(got, ",") != "e2,e3" {
		t.Fatalf("first reconnect event ids = %#v, want e2,e3", got)
	}
	if got := first[len(first)-1]["last_order_key"]; got != "cursor-3" {
		t.Fatalf("first reconnect last_order_key = %v, want cursor-3", got)
	}

	second := readReplayMessages(t, server.URL, "cursor-2")
	if got := eventIDs(second); strings.Join(got, ",") != "e3" {
		t.Fatalf("second reconnect event ids = %#v, want e3", got)
	}
	if got := second[len(second)-1]["replayed"]; got != float64(1) {
		t.Fatalf("second reconnect replayed = %v, want 1", got)
	}
}

func readReplayMessages(t *testing.T, serverURL, cursor string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/?last_order_key=" + cursor
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var messages []map[string]any
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
		if msg["type"] == "tank.transport.subscribed" {
			return messages
		}
	}
}

func eventIDs(messages []map[string]any) []string {
	var ids []string
	for _, msg := range messages {
		id, _ := msg["event_id"].(string)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
