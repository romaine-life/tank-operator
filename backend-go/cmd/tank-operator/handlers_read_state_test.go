package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

func TestHandleUpdateSessionReadStatePersistsMonotonicCursor(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	app := readStateTestServer(t, readStates)

	putReadState(t, app, "63", "002")
	putReadState(t, app, "63", "001")

	rec, err := readStates.Get(context.Background(), "user@example.com", "63")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.LastReadOrderKey != "002" {
		t.Fatalf("read state = %#v, want cursor 002", rec)
	}
}

func TestHandleListSessionEventsIncludesReadState(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	if _, err := readStates.Set(context.Background(), "user@example.com", "63", "cursor-read"); err != nil {
		t.Fatal(err)
	}
	app := readStateTestServer(t, readStates)
	app.sessionEvents = fakeSessionEventStore{
		pages: map[string]store.SessionEventPage{
			"": {
				Events: []map[string]any{
					{"event_id": "e1", "order_key": "001", "type": "item.completed"},
				},
				NextOrderKey: "001",
			},
		},
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/63/timeline", nil)
	request.SetPathValue("session_id", "63")
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	app.handleListSessionEvents(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body struct {
		ReadState *sessionReadStateResponseBody `json:"read_state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReadState == nil || body.ReadState.LastReadOrderKey != "cursor-read" {
		t.Fatalf("read_state = %#v, want cursor-read", body.ReadState)
	}
}

func readStateTestServer(t *testing.T, readStates store.ConversationReadStateStore) *appServer {
	t.Helper()
	client := fake.NewSimpleClientset(activitySessionPod("63", "user@example.com"))
	return &appServer{
		verifier: auth.NewVerifier(testJWT(t), "user@example.com"),
		mgr: sessions.NewManager(
			client,
			nil,
			compat.SessionsNamespace,
			nil,
			nil,
			sessions.ManagerOptions{},
		),
		sessionEvents: store.StubSessionEventStore{},
		readStates:    readStates,
	}
}

func putReadState(t *testing.T, app *appServer, sessionID, cursor string) {
	t.Helper()
	body := bytes.NewBufferString(`{"last_read_order_key":` + strconv.Quote(cursor) + `}`)
	request := httptest.NewRequest(http.MethodPut, "/api/sessions/"+sessionID+"/read-state", body)
	request.SetPathValue("session_id", sessionID)
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	app.handleUpdateSessionReadState(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}
