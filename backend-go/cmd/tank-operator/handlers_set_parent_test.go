package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
)

// Hermetic coverage for PUT /api/sessions/{id}/parent (drag-to-nest / un-nest).
// The registry-backed fake exercises the manager guards that keep the durable
// parent_session_id tree acyclic — self-parent, cycle, missing/cross-scope
// target, unknown child — which live in Manager.SetParentSession, not in SQL, so
// they are covered without a Postgres DSN.

func nestableRecord(id, parentID string) sessionmodel.SessionRecord {
	return sessionmodel.SessionRecord{
		ID:              id,
		Email:           otherUser,
		Scope:           prodSessionScope,
		Mode:            sessionmodel.ClaudeGUIMode,
		Visible:         true,
		Status:          "Active",
		ParentSessionID: parentID,
	}
}

func setParentRequest(t *testing.T, id, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+id+"/parent", strings.NewReader(body))
	req.SetPathValue("session_id", id)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	return httptest.NewRecorder(), req
}

func TestHandleSetSessionParent_NestReturns200(t *testing.T) {
	app := registryOnlyAuthTestServer(t, nestableRecord("71", ""), nestableRecord("72", ""))
	res, req := setParentRequest(t, "71", `{"parent_session_id":"72"}`)

	app.handleSetSessionParent(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ID != "71" || info.ParentSessionID != "72" {
		t.Fatalf("info = %+v, want id=71 parent=72", info)
	}
}

func TestHandleSetSessionParent_UnnestReturns200(t *testing.T) {
	app := registryOnlyAuthTestServer(t, nestableRecord("71", "72"), nestableRecord("72", ""))
	res, req := setParentRequest(t, "71", `{"parent_session_id":null}`)

	app.handleSetSessionParent(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var info sessions.Info
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ParentSessionID != "" {
		t.Fatalf("parent_session_id = %q, want empty after un-nest", info.ParentSessionID)
	}
}

func TestHandleSetSessionParent_SelfParentReturns400(t *testing.T) {
	app := registryOnlyAuthTestServer(t, nestableRecord("71", ""))
	res, req := setParentRequest(t, "71", `{"parent_session_id":"71"}`)

	app.handleSetSessionParent(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 (self-parent)", res.Code, res.Body.String())
	}
}

func TestHandleSetSessionParent_MissingParentReturns400(t *testing.T) {
	app := registryOnlyAuthTestServer(t, nestableRecord("71", ""))
	res, req := setParentRequest(t, "71", `{"parent_session_id":"999"}`)

	app.handleSetSessionParent(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 (missing parent)", res.Code, res.Body.String())
	}
}

func TestHandleSetSessionParent_CycleReturns400(t *testing.T) {
	// 72 is already a child of 71; nesting 71 under 72 would close a cycle.
	app := registryOnlyAuthTestServer(t, nestableRecord("71", ""), nestableRecord("72", "71"))
	res, req := setParentRequest(t, "71", `{"parent_session_id":"72"}`)

	app.handleSetSessionParent(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400 (cycle)", res.Code, res.Body.String())
	}
}

func TestHandleSetSessionParent_UnknownChildReturns404(t *testing.T) {
	app := registryOnlyAuthTestServer(t, nestableRecord("72", ""))
	res, req := setParentRequest(t, "999", `{"parent_session_id":"72"}`)

	app.handleSetSessionParent(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404", res.Code, res.Body.String())
	}
}
