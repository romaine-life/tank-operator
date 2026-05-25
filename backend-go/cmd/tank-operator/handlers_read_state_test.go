package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
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

func TestHandleUpdateSessionReadStateRefreshesDurableActivity(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	refresher := &recordingActivityRefresher{}
	app := readStateTestServer(t, readStates)
	app.activityRefresher = refresher

	putReadState(t, app, "63", "002")

	if len(refresher.calls) != 1 {
		t.Fatalf("refresh calls = %d, want 1", len(refresher.calls))
	}
	got := refresher.calls[0]
	if got.owner != "user@example.com" || got.scope != prodSessionScope || got.sessionID != "63" {
		t.Fatalf("refresh call = %#v, want owner/scope/session", got)
	}
}

func TestHandleUpdateSessionReadStateSkipsActivityRefreshForAdminCrossUserRead(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	refresher := &recordingActivityRefresher{}
	app := readStateTestServer(t, readStates)
	app.activityRefresher = refresher

	body := bytes.NewBufferString(`{"last_read_order_key":"002"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/sessions/63/read-state", body)
	request.SetPathValue("session_id", "63")
	request.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	response := httptest.NewRecorder()

	app.handleUpdateSessionReadState(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if len(refresher.calls) != 0 {
		t.Fatalf("refresh calls = %d, want 0", len(refresher.calls))
	}
}

func TestHandleUpdateSessionReadStateFailsWhenActivityRefreshFails(t *testing.T) {
	readStates := store.NewStubConversationReadStateStore()
	app := readStateTestServer(t, readStates)
	app.activityRefresher = &recordingActivityRefresher{err: errors.New("row update failed")}

	body := bytes.NewBufferString(`{"last_read_order_key":"002"}`)
	request := httptest.NewRequest(http.MethodPut, "/api/sessions/63/read-state", body)
	request.SetPathValue("session_id", "63")
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	app.handleUpdateSessionReadState(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s, want 500", response.Code, response.Body.String())
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
		verifier: auth.NewVerifier(testJWT(t)),
		mgr: sessions.NewManager(
			client,
			nil,
			sessionmodel.SessionsNamespace,
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

type recordingActivityRefresher struct {
	calls []activityRefreshCall
	err   error
}

type activityRefreshCall struct {
	owner     string
	scope     string
	sessionID string
}

func (r *recordingActivityRefresher) RefreshSessionActivity(_ context.Context, owner, scope, sessionID string) error {
	r.calls = append(r.calls, activityRefreshCall{
		owner:     owner,
		scope:     scope,
		sessionID: sessionID,
	})
	return r.err
}
