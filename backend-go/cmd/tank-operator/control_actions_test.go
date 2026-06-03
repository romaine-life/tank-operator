package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type fakeControlActionStore struct {
	appendCalls []pgstore.ControlActionEvent
	appendErr   error
	listOwner   string
	listScope   string
	listSession string
	listLimit   int
	listRows    []pgstore.ControlActionEvent
	listErr     error
}

func (s *fakeControlActionStore) Append(_ context.Context, event pgstore.ControlActionEvent) (pgstore.ControlActionEvent, error) {
	if s.appendErr != nil {
		return pgstore.ControlActionEvent{}, s.appendErr
	}
	s.appendCalls = append(s.appendCalls, event)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Unix(1700000000, 0).UTC()
	}
	if len(event.Payload) == 0 {
		event.Payload = []byte(`{}`)
	}
	return event, nil
}

func (s *fakeControlActionStore) ListBySession(_ context.Context, ownerEmail, sessionScope, sessionID string, limit int) ([]pgstore.ControlActionEvent, error) {
	s.listOwner = ownerEmail
	s.listScope = sessionScope
	s.listSession = sessionID
	s.listLimit = limit
	return s.listRows, s.listErr
}

func controlActionTestServer(t *testing.T, store controlActionStore) *appServer {
	t.Helper()
	return &appServer{
		verifier:       auth.NewVerifier(testJWT(t)),
		sessionScope:   "tank-operator-slot-3",
		controlActions: store,
	}
}

func TestHandleInternalAppendControlActionPersistsServiceActorAudit(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/control-actions", strings.NewReader(`{
		"event_id": "ctrl_1_started",
		"invocation_id": "ctrl_1",
		"source_service": "mcp-github",
		"source_tool": "merge_pull_request",
		"action": "github.pull_request.merge",
		"status": "started",
		"target_kind": "github_pull_request",
		"target_ref": "https://github.com/romaine-life/tank-operator/pull/857",
		"repo_owner": "romaine-life",
		"repo_name": "tank-operator",
		"pr_number": 857,
		"payload": {"head_sha": "abc123"}
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalAppendControlAction(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.OwnerEmail != "owner@example.test" || got.SessionScope != "tank-operator-slot-3" || got.SessionID != "47" {
		t.Fatalf("audit owner/scope/session = (%q,%q,%q)", got.OwnerEmail, got.SessionScope, got.SessionID)
	}
	if got.SourceService != "mcp-github" || got.SourceTool != "merge_pull_request" || got.Action != "github.pull_request.merge" || got.Status != "started" {
		t.Fatalf("audit action fields = %#v", got)
	}
	if got.PRNumber == nil || *got.PRNumber != 857 {
		t.Fatalf("pr number = %#v, want 857", got.PRNumber)
	}
	if !json.Valid(got.Payload) {
		t.Fatalf("payload is invalid JSON: %s", string(got.Payload))
	}
}

func TestHandleInternalAppendControlActionRejectsUnsupportedActionBeforeStore(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/control-actions", strings.NewReader(`{
		"event_id": "ctrl_1_started",
		"invocation_id": "ctrl_1",
		"source_service": "mcp-github",
		"source_tool": "delete_branch",
		"action": "github.branch.delete",
		"status": "started",
		"target_kind": "github_branch",
		"target_ref": "https://github.com/romaine-life/tank-operator/tree/main"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalAppendControlAction(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleListControlActionsScopesBrowserRead(t *testing.T) {
	prNumber := 857
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:       "ctrl_1_succeeded",
			InvocationID:  "ctrl_1",
			CreatedAt:     time.Unix(1700000001, 0).UTC(),
			OwnerEmail:    "owner@example.test",
			SessionScope:  "tank-operator-slot-3",
			SessionID:     "47",
			SourceService: "mcp-github",
			SourceTool:    "merge_pull_request",
			Action:        "github.pull_request.merge",
			Status:        "succeeded",
			TargetKind:    "github_pull_request",
			TargetRef:     "https://github.com/romaine-life/tank-operator/pull/857",
			RepoOwner:     "romaine-life",
			RepoName:      "tank-operator",
			PRNumber:      &prNumber,
			ResultSHA:     "merge-sha",
			Payload:       []byte(`{"merge_method":"squash"}`),
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/47/control-actions?limit=25", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleListControlActions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.listOwner != "owner@example.test" || store.listScope != "tank-operator-slot-3" || store.listSession != "47" || store.listLimit != 25 {
		t.Fatalf("list scope = (%q,%q,%q,%d)", store.listOwner, store.listScope, store.listSession, store.listLimit)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if _, ok := rows[0]["owner_email"]; ok {
		t.Fatalf("browser response leaked owner_email: %#v", rows[0])
	}
	if rows[0]["result_sha"] != "merge-sha" {
		t.Fatalf("result_sha = %v, want merge-sha", rows[0]["result_sha"])
	}
}

func TestHandleListControlActionsReturnsStoreErrors(t *testing.T) {
	store := &fakeControlActionStore{listErr: errors.New("database down")}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/47/control-actions", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleListControlActions(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
