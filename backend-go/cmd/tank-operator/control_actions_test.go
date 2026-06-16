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

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type fakeControlActionStore struct {
	appendCalls     []pgstore.ControlActionEvent
	appendErr       error
	listOwner       string
	listScope       string
	listSession     string
	listLimit       int
	listRows        []pgstore.ControlActionEvent
	listErr         error
	breakGlassScope string
	breakGlassLimit int
	breakGlassRows  []pgstore.ControlActionEvent
	breakGlassErr   error
	getScope        string
	getSession      string
	getEventID      string
	getRow          pgstore.ControlActionEvent
	getErr          error
	decisionScope   string
	decisionSession string
	decisionRequest string
	decisionRow     pgstore.ControlActionEvent
	decisionErr     error
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

func (s *fakeControlActionStore) ListBreakGlassRequests(_ context.Context, sessionScope string, limit int) ([]pgstore.ControlActionEvent, error) {
	s.breakGlassScope = sessionScope
	s.breakGlassLimit = limit
	if s.breakGlassErr != nil {
		return nil, s.breakGlassErr
	}
	if s.breakGlassRows != nil {
		return s.breakGlassRows, nil
	}
	var out []pgstore.ControlActionEvent
	for _, row := range s.listRows {
		if row.SessionScope == sessionScope && isBreakGlassRequestAction(row.Action) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *fakeControlActionStore) GetBySessionEvent(_ context.Context, sessionScope, sessionID, eventID string) (pgstore.ControlActionEvent, error) {
	s.getScope = sessionScope
	s.getSession = sessionID
	s.getEventID = eventID
	if s.getErr != nil {
		return pgstore.ControlActionEvent{}, s.getErr
	}
	if s.getRow.EventID != "" {
		return s.getRow, nil
	}
	for _, row := range s.listRows {
		if row.SessionScope == sessionScope && row.SessionID == sessionID && row.EventID == eventID {
			return row, nil
		}
	}
	return pgstore.ControlActionEvent{}, pgx.ErrNoRows
}

func (s *fakeControlActionStore) BreakGlassDecisionForRequest(_ context.Context, sessionScope, sessionID, requestEventID string) (pgstore.ControlActionEvent, error) {
	s.decisionScope = sessionScope
	s.decisionSession = sessionID
	s.decisionRequest = requestEventID
	if s.decisionErr != nil {
		return pgstore.ControlActionEvent{}, s.decisionErr
	}
	if s.decisionRow.EventID != "" {
		return s.decisionRow, nil
	}
	for _, row := range s.listRows {
		if row.SessionScope != sessionScope || row.SessionID != sessionID {
			continue
		}
		if !isBreakGlassDecisionAction(row.Action) {
			continue
		}
		if controlActionPayloadString(row.Payload, "request_event_id") == requestEventID {
			return row, nil
		}
	}
	return pgstore.ControlActionEvent{}, pgx.ErrNoRows
}

func (s *fakeControlActionStore) TestSlotModelDecisionForRequest(_ context.Context, sessionScope, sessionID, requestEventID string) (pgstore.ControlActionEvent, error) {
	s.decisionScope = sessionScope
	s.decisionSession = sessionID
	s.decisionRequest = requestEventID
	if s.decisionErr != nil {
		return pgstore.ControlActionEvent{}, s.decisionErr
	}
	if s.decisionRow.EventID != "" {
		return s.decisionRow, nil
	}
	for _, row := range s.listRows {
		if row.SessionScope != sessionScope || row.SessionID != sessionID {
			continue
		}
		if row.Action != testSlotModelGrantAction {
			continue
		}
		if controlActionPayloadString(row.Payload, "request_event_id") == requestEventID {
			return row, nil
		}
	}
	return pgstore.ControlActionEvent{}, pgx.ErrNoRows
}

func controlActionTestServer(t *testing.T, store controlActionStore) *appServer {
	t.Helper()
	app := testTurnsApp(
		t,
		&recordingSessionBus{},
		sdkSessionPod("session-47", "47", "owner@example.test", sessionmodel.ClaudeGUIMode, "claude-runner"),
	)
	app.verifier = auth.NewVerifier(testJWT(t))
	app.sessionScope = "tank-operator-slot-3"
	app.controlActions = store
	return app
}

func signedControlActionServiceToken(t *testing.T, sub string) string {
	t.Helper()
	tok, err := testJWT(t).MintJWT(context.Background(), jwt.MapClaims{
		"sub":         sub,
		"email":       "pod-47@service.tank.romaine.life",
		"iss":         "https://auth.romaine.life",
		"name":        "Service: tank pod-47",
		"role":        auth.RoleService,
		"actor_email": "owner@example.test",
		"iat":         time.Now().Unix(),
		"exp":         time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
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
	req.Header.Set("Authorization", "Bearer "+signedControlActionServiceToken(t, "svc:tank:slot-3-session-47"))
	req.Header.Set(callerSessionIDHeader, "47")
	req.Header.Set(callerSessionScopeHeader, "tank-operator-slot-3")
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

func TestHandleInternalAppendControlActionRejectsMissingCallerSession(t *testing.T) {
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
		"target_ref": "https://github.com/romaine-life/tank-operator/pull/857"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedControlActionServiceToken(t, "svc:tank-operator:orchestrator-slot-3"))
	rec := httptest.NewRecorder()

	app.handleInternalAppendControlAction(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleInternalAppendControlActionRejectsMismatchedCallerSession(t *testing.T) {
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
		"target_ref": "https://github.com/romaine-life/tank-operator/pull/857"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedControlActionServiceToken(t, "svc:tank:95"))
	req.Header.Set(callerSessionIDHeader, "95")
	req.Header.Set(callerSessionScopeHeader, "tank-operator-slot-3")
	rec := httptest.NewRecorder()

	app.handleInternalAppendControlAction(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleInternalAppendControlActionRejectsSpoofedCallerHeaders(t *testing.T) {
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
		"target_ref": "https://github.com/romaine-life/tank-operator/pull/857"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedControlActionServiceToken(t, "svc:tank-operator:orchestrator-slot-3"))
	req.Header.Set(callerSessionIDHeader, "47")
	req.Header.Set(callerSessionScopeHeader, "tank-operator-slot-3")
	rec := httptest.NewRecorder()

	app.handleInternalAppendControlAction(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
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
	req.Header.Set("Authorization", "Bearer "+signedControlActionServiceToken(t, "svc:tank:slot-3-session-47"))
	req.Header.Set(callerSessionIDHeader, "47")
	req.Header.Set(callerSessionScopeHeader, "tank-operator-slot-3")
	rec := httptest.NewRecorder()

	app.handleInternalAppendControlAction(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleInternalAppendControlActionAcceptsGitActivity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		action     string
		targetKind string
		targetRef  string
	}{
		{
			name:       "pull request opened",
			action:     "github.pull_request.open",
			targetKind: "github_pull_request",
			targetRef:  "https://github.com/romaine-life/tank-operator/pull/857",
		},
		{
			name:       "commit pushed",
			action:     "github.commit.push",
			targetKind: "github_commit",
			targetRef:  "https://github.com/romaine-life/tank-operator/commit/abcdef1234567890",
		},
		{
			name:       "commit ci",
			action:     "github.commit.ci",
			targetKind: "github_commit",
			targetRef:  "https://github.com/romaine-life/tank-operator/commit/abcdef1234567890",
		},
		{
			name:       "pull request mergeability",
			action:     "github.pull_request.mergeability",
			targetKind: "github_pull_request",
			targetRef:  "https://github.com/romaine-life/tank-operator/pull/857",
		},
		{
			name:       "pull request renamed",
			action:     "github.pull_request.rename",
			targetKind: "github_pull_request",
			targetRef:  "https://github.com/romaine-life/tank-operator/pull/857",
		},
		{
			name:       "pull request body updated",
			action:     "github.pull_request.update_body",
			targetKind: "github_pull_request",
			targetRef:  "https://github.com/romaine-life/tank-operator/pull/857",
		},
		{
			name:       "break glass requested",
			action:     "github.break_glass.request",
			targetKind: "github_repository",
			targetRef:  "https://github.com/romaine-life/tank-operator",
		},
		{
			name:       "PR lane requested",
			action:     "github.pr_lane.request",
			targetKind: "github_repository",
			targetRef:  "https://github.com/romaine-life/tank-operator",
		},
		{
			name:       "PR lane approved",
			action:     "github.pr_lane.approve",
			targetKind: "github_repository",
			targetRef:  "https://github.com/romaine-life/tank-operator",
		},
		{
			name:       "PR lane denied",
			action:     "github.pr_lane.deny",
			targetKind: "github_repository",
			targetRef:  "https://github.com/romaine-life/tank-operator",
		},
		{
			name:       "PR lane created",
			action:     "github.pr_lane.create",
			targetKind: "github_pull_request",
			targetRef:  "https://github.com/romaine-life/tank-operator/pull/999",
		},
		{
			name:       "azure break glass requested",
			action:     "azure.break_glass.request",
			targetKind: "azure_mcp",
			targetRef:  "azure-personal",
		},
		{
			name:       "azure break glass use",
			action:     "azure.break_glass.use",
			targetKind: "azure_mcp",
			targetRef:  "azure-personal",
		},
		{
			name:       "kubernetes break glass requested",
			action:     "kubernetes.break_glass.request",
			targetKind: "kubernetes_mcp",
			targetRef:  "kubernetes-break-glass",
		},
		{
			name:       "kubernetes break glass use",
			action:     "kubernetes.break_glass.use",
			targetKind: "kubernetes_mcp",
			targetRef:  "kubernetes-break-glass",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeControlActionStore{}
			app := controlActionTestServer(t, store)
			body := `{
				"event_id": "git_1",
				"invocation_id": "git_invocation_1",
				"source_service": "mcp-github",
				"source_tool": "create_pull_request",
				"action": "` + tc.action + `",
				"status": "succeeded",
				"target_kind": "` + tc.targetKind + `",
				"target_ref": "` + tc.targetRef + `",
				"repo_owner": "romaine-life",
				"repo_name": "tank-operator"
			}`
			req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/control-actions", strings.NewReader(body))
			req.SetPathValue("session_id", "47")
			req.Header.Set("Authorization", "Bearer "+signedControlActionServiceToken(t, "svc:tank:slot-3-session-47"))
			req.Header.Set(callerSessionIDHeader, "47")
			req.Header.Set(callerSessionScopeHeader, "tank-operator-slot-3")
			rec := httptest.NewRecorder()

			app.handleInternalAppendControlAction(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(store.appendCalls) != 1 {
				t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
			}
			if got := store.appendCalls[0].Action; got != tc.action {
				t.Fatalf("action = %q, want %q", got, tc.action)
			}
		})
	}
}

func TestHandleApprovePRLaneRequestRecordsDecision(t *testing.T) {
	requestPayload := []byte(`{"lane_name":"docs","relationship":"parallel","reason":"split docs review"}`)
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:      "lane-request-1",
			InvocationID: "lane-invocation-1",
			Action:       "github.pr_lane.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload:      requestPayload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/lane-request-1/approve", strings.NewReader(`{
		"note":"ok"
	}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "lane-request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleApprovePRLaneRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.listOwner != "owner@example.test" || store.listSession != "47" {
		t.Fatalf("list scope = owner %q session %q", store.listOwner, store.listSession)
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "github.pr_lane.approve" || got.Status != "succeeded" {
		t.Fatalf("decision action/status = %s/%s", got.Action, got.Status)
	}
	if got.InvocationID != "lane-invocation-1" || got.RepoOwner != "romaine-life" || got.RepoName != "tank-operator" {
		t.Fatalf("decision copied request identity: %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_event_id"] != "lane-request-1" || payload["note"] != "ok" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHandleApprovePRLaneRequestRejectsResolvedRequest(t *testing.T) {
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{
			{
				EventID:      "lane-request-1",
				InvocationID: "lane-invocation-1",
				Action:       "github.pr_lane.request",
				Status:       "started",
				TargetKind:   "github_repository",
				TargetRef:    "https://github.com/romaine-life/tank-operator",
				RepoOwner:    "romaine-life",
				RepoName:     "tank-operator",
				Payload:      []byte(`{}`),
			},
			{
				EventID:      "lane-approve-1",
				InvocationID: "lane-invocation-1",
				Action:       "github.pr_lane.approve",
				Status:       "succeeded",
			},
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/lane-request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "lane-request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleApprovePRLaneRequest(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleApprovePRLaneAllocationPersistsRepoScopeOverride(t *testing.T) {
	requestPayload := []byte(`{
		"allocation_request":true,
		"repo_scope":{"kind":"repos","repos":["romaine-life/tank-operator"]},
		"branch_scope":{"kind":"count","count":5},
		"reason":"split multi-repo work"
	}`)
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:      "lane-request-1",
			InvocationID: "lane-invocation-1",
			Action:       "github.pr_lane.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "tank://session/47/pr-lanes",
			Payload:      requestPayload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/lane-request-1/approve", strings.NewReader(`{
		"note":"broaden",
		"repo_scope":{"kind":"repos","repos":["romaine-life/auth","romaine-life/tank-operator"]},
		"branch_scope":{"kind":"count","count":10}
	}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "lane-request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleApprovePRLaneRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.appendCalls[0]
	if got.Action != "github.pr_lane.auto_approve" || got.RepoOwner != "" || got.TargetRef != "tank://session/47/pr-lanes/repos" {
		t.Fatalf("approval identity = %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	repoScope, ok := payload["repo_scope"].(map[string]any)
	if !ok {
		t.Fatalf("repo_scope = %#v", payload["repo_scope"])
	}
	repos, ok := repoScope["repos"].([]any)
	if !ok || len(repos) != 2 || repos[0] != "romaine-life/auth" || repos[1] != "romaine-life/tank-operator" {
		t.Fatalf("repo_scope.repos = %#v", repoScope["repos"])
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "count" || branchScope["count"] != float64(10) {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
}

func TestHandleAutoApprovePRLanesPersistsSessionGrant(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/auto-approve", strings.NewReader(`{
		"repo_scope": {"kind":"current_repo","repo":"romaine-life/tank-operator"},
		"branch_scope": {"kind":"named","branches":["docs", "tank/session/47/tank-operator/backend"]},
		"reason": "planned split"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleAutoApprovePRLanes(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "github.pr_lane.auto_approve" || got.Status != "succeeded" {
		t.Fatalf("auto action/status = %s/%s", got.Action, got.Status)
	}
	if got.RepoOwner != "romaine-life" || got.RepoName != "tank-operator" {
		t.Fatalf("repo = %s/%s", got.RepoOwner, got.RepoName)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "named" {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
	names, ok := branchScope["branches"].([]any)
	if !ok || len(names) != 2 || names[0] != "docs" || names[1] != "backend" {
		t.Fatalf("branch_scope.branches = %#v", branchScope["branches"])
	}
}

func TestHandleAutoApprovePRLanesRejectsConflictingBranchScope(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/auto-approve", strings.NewReader(`{
		"repo_scope": {"kind":"current_repo","repo":"romaine-life/tank-operator"},
		"branch_scope": {"kind":"unlimited","branches":["docs"]},
		"reason": "planned split"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleAutoApprovePRLanes(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleInternalGetPRLaneAutoApprovalReturnsActiveGrant(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"repo_scope":   map[string]any{"kind": "current_repo", "repo": "romaine-life/tank-operator"},
		"branch_scope": map[string]any{"kind": "count", "count": 7},
		"scope":        "session",
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:   "auto-1",
			Action:    "github.pr_lane.auto_approve",
			Status:    "succeeded",
			RepoOwner: "romaine-life",
			RepoName:  "tank-operator",
			Payload:   payload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/pr-lane-auto-approval?repo=romaine-life/tank-operator", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetPRLaneAutoApproval(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["event_id"] != "auto-1" || body["limit"] != float64(7) || body["remaining"] != float64(7) {
		t.Fatalf("body = %#v", body)
	}
	if store.listOwner != "owner@example.test" || store.listSession != "47" {
		t.Fatalf("list lookup = owner %q session %q", store.listOwner, store.listSession)
	}
}

func TestHandleInternalGetPRLaneAutoApprovalEnforcesBranchNamesAndLimit(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"repo_scope":   map[string]any{"kind": "current_repo", "repo": "romaine-life/tank-operator"},
		"branch_scope": map[string]any{"kind": "named", "branches": []string{"docs"}},
		"scope":        "session",
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:   "auto-1",
			SessionID: "47",
			Action:    "github.pr_lane.auto_approve",
			Status:    "succeeded",
			RepoOwner: "romaine-life",
			RepoName:  "tank-operator",
			Payload:   payload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/pr-lane-auto-approval?repo=romaine-life/tank-operator&lane_name=backend", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetPRLaneAutoApproval(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != false {
		t.Fatalf("backend branch unexpectedly allowed: %#v", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/pr-lane-auto-approval?repo=romaine-life/tank-operator&lane_name=docs", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec = httptest.NewRecorder()
	app.handleInternalGetPRLaneAutoApproval(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["remaining"] != float64(1) {
		t.Fatalf("docs branch not allowed: %#v", body)
	}
}

func TestHandleApprovePRLaneAllocationRequestCreatesAutoApproval(t *testing.T) {
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:      "lane-request-1",
			InvocationID: "lane-invocation-1",
			Action:       "github.pr_lane.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload: []byte(`{
				"allocation_request":true,
				"repo_scope":{"kind":"current_repo","repo":"romaine-life/tank-operator"},
				"branch_scope":{"kind":"named","branches":["docs","backend"]},
				"reason":"split review"
			}`),
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/lane-request-1/approve", strings.NewReader(`{"note":"ok"}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "lane-request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleApprovePRLaneRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "github.pr_lane.auto_approve" || got.InvocationID != "lane-invocation-1" {
		t.Fatalf("allocation approval = %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "named" {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
	names, ok := branchScope["branches"].([]any)
	if !ok || len(names) != 2 || names[0] != "docs" || names[1] != "backend" {
		t.Fatalf("branch_scope.branches = %#v", branchScope["branches"])
	}
}

func TestHandleApprovePRLaneAllocationRequestAllowsExplicitOverride(t *testing.T) {
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:      "lane-request-1",
			InvocationID: "lane-invocation-1",
			Action:       "github.pr_lane.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload: []byte(`{
				"allocation_request":true,
				"repo_scope":{"kind":"current_repo","repo":"romaine-life/tank-operator"},
				"branch_scope":{"kind":"named","branches":["docs","backend"]},
				"reason":"split review"
			}`),
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/lane-request-1/approve", strings.NewReader(`{
		"note":"override",
		"branch_scope":{"kind":"unlimited"}
	}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "lane-request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleApprovePRLaneRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.appendCalls[0]
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "unlimited" {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
}

func TestHandleInternalGetPRLaneAuthorizationAllowsApprovedRequest(t *testing.T) {
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{
			{
				EventID:      "lane-request-1",
				InvocationID: "lane-invocation-1",
				Action:       "github.pr_lane.request",
				Status:       "started",
				TargetKind:   "github_repository",
				TargetRef:    "https://github.com/romaine-life/tank-operator",
				RepoOwner:    "romaine-life",
				RepoName:     "tank-operator",
				Payload: []byte(`{
					"lane_name":"docs",
					"relationship":"parallel",
					"base":"main",
					"scope":"docs/",
					"reason":"split docs",
					"proposed_branch":"tank/session/47/tank-operator/docs"
				}`),
			},
			{
				EventID:      "lane-approve-1",
				InvocationID: "lane-invocation-1",
				Action:       "github.pr_lane.approve",
				Status:       "succeeded",
			},
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/pr-lane-requests/lane-request-1/authorization", nil)
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "lane-request-1")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetPRLaneAuthorization(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body prLaneAuthorizationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Allowed || body.ApprovalEventID != "lane-approve-1" {
		t.Fatalf("authorization = %#v", body)
	}
	if body.ProposedBranch != "tank/session/47/tank-operator/docs" || body.Repo != "romaine-life/tank-operator" {
		t.Fatalf("authorization metadata = %#v", body)
	}
}

func TestHandleInternalGetPRLaneAuthorizationBlocksDeniedOrCreatedRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		reason string
	}{
		{name: "denied", action: "github.pr_lane.deny", reason: "denied"},
		{name: "created", action: "github.pr_lane.create", reason: "already"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeControlActionStore{
				listRows: []pgstore.ControlActionEvent{
					{
						EventID:      "lane-request-1",
						InvocationID: "lane-invocation-1",
						Action:       "github.pr_lane.request",
						Status:       "started",
						TargetKind:   "github_repository",
						TargetRef:    "https://github.com/romaine-life/tank-operator",
						RepoOwner:    "romaine-life",
						RepoName:     "tank-operator",
						Payload:      []byte(`{"lane_name":"docs","proposed_branch":"tank/session/47/tank-operator/docs"}`),
					},
					{
						EventID:      "lane-terminal-1",
						InvocationID: "lane-invocation-1",
						Action:       tc.action,
						Status:       "succeeded",
					},
				},
			}
			app := controlActionTestServer(t, store)
			req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/pr-lane-requests/lane-request-1/authorization", nil)
			req.SetPathValue("session_id", "47")
			req.SetPathValue("request_event_id", "lane-request-1")
			req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
			rec := httptest.NewRecorder()

			app.handleInternalGetPRLaneAuthorization(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body prLaneAuthorizationResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Allowed || !strings.Contains(strings.Join(body.Reasons, "\n"), tc.reason) {
				t.Fatalf("authorization = %#v", body)
			}
		})
	}
}

func TestHandleApproveBreakGlassRequestPersistsGitGrantForRequestOwner(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload: []byte(`{
				"repo_scope": {"kind":"current_repo","repo":"romaine-life/tank-operator"},
				"branch_scope": {"kind":"unlimited"},
				"operations": ["mint_full_git_token"],
				"reason": "repair branch"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{"note":"ok"}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "github.break_glass.grant" || got.Status != "succeeded" {
		t.Fatalf("grant action/status = %s/%s", got.Action, got.Status)
	}
	if got.RepoOwner != "romaine-life" || got.RepoName != "tank-operator" {
		t.Fatalf("repo = %s/%s", got.RepoOwner, got.RepoName)
	}
	if got.OwnerEmail != "owner@example.test" {
		t.Fatalf("grant owner = %q, want request owner", got.OwnerEmail)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_event_id"] != "request-1" {
		t.Fatalf("request_event_id = %v", payload["request_event_id"])
	}
	if payload["approved_by"] != "admin@example.test" {
		t.Fatalf("approved_by = %v", payload["approved_by"])
	}
	if _, ok := payload["repo_scope"].(map[string]any); !ok {
		t.Fatalf("repo_scope = %#v", payload["repo_scope"])
	}
	if _, ok := payload["branch_scope"].(map[string]any); !ok {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
}

func TestHandleApproveBreakGlassRequestStartsSystemApprovalTurn(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-approval-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload: []byte(`{
				"repo_scope": {"kind":"current_repo","repo":"romaine-life/tank-operator"},
				"branch_scope": {"kind":"unlimited"},
				"operations": ["mint_full_git_token"],
				"reason": "repair branch"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-approval-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-approval-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	command := bus.commands[0]
	if command.Type != "submit_turn" || command.Source != "break-glass-approval" {
		t.Fatalf("command type/source = %s/%s", command.Type, command.Source)
	}
	if command.Email != "owner@example.test" || command.SessionID != "47" {
		t.Fatalf("command target = %s/%s", command.Email, command.SessionID)
	}
	if !strings.Contains(command.Prompt, "System message: Your GitHub break-glass request was approved by the user.") {
		t.Fatalf("prompt missing approval notice: %q", command.Prompt)
	}
	if !strings.Contains(command.Prompt, "Call request_git_break_glass again") {
		t.Fatalf("prompt missing activation instruction: %q", command.Prompt)
	}
	if want := gitBreakGlassApprovalTurnNonce("47:request-approval-1"); command.ClientNonce != want {
		t.Fatalf("client nonce = %q, want %q", command.ClientNonce, want)
	}
	events := app.sessionEvents.(*recordingSessionEventStore).upserts
	if len(events) < 2 {
		t.Fatalf("persisted events = %d, want at least 2", len(events))
	}
	userMessage := events[0]
	if userMessage["type"] != "user_message.created" || userMessage["author_kind"] != "system" {
		t.Fatalf("user message event = %#v", userMessage)
	}
	submitted := events[1]
	payload, _ := submitted["payload"].(map[string]any)
	if submitted["type"] != "turn.submitted" || payload["source"] != "break-glass-approval" {
		t.Fatalf("submitted event = %#v", submitted)
	}
	var body struct {
		AgentNotification struct {
			Delivered bool   `json:"delivered"`
			TurnID    string `json:"turn_id"`
		} `json:"agent_notification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.AgentNotification.Delivered || body.AgentNotification.TurnID == "" {
		t.Fatalf("agent notification response = %#v", body.AgentNotification)
	}
}

func TestHandleApproveBreakGlassRequestKeepsGrantWhenApprovalTurnFails(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-approval-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload: []byte(`{
				"repo_scope": {"kind":"current_repo","repo":"romaine-life/tank-operator"},
				"branch_scope": {"kind":"unlimited"},
				"operations": ["mint_full_git_token"],
				"reason": "repair branch"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	app.sessionBus = &recordingSessionBus{err: errors.New("nats down")}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-approval-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-approval-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want persisted grant before retryable notification failure", len(store.appendCalls))
	}
	var body struct {
		AgentNotification struct {
			Delivered bool   `json:"delivered"`
			Error     string `json:"error"`
		} `json:"agent_notification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AgentNotification.Delivered || body.AgentNotification.Error == "" {
		t.Fatalf("agent notification response = %#v", body.AgentNotification)
	}
}

func TestHandleApproveBreakGlassRequestPersistsAllReposBranchScope(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "tank://session/47/git-break-glass/all-repos",
			Payload: []byte(`{
				"repo_scope": {"kind":"all_repos"},
				"branch_scope": {"kind":"named","branches":["refs/heads/feature-a", "feature-b"]},
				"operations": ["push_current_head"],
				"reason": "repair planned branches"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.appendCalls[0]
	if got.RepoOwner != "" || got.RepoName != "" || got.TargetRef != "tank://session/47/git-break-glass/all-repos" {
		t.Fatalf("scope identity = owner %q repo %q target %q", got.RepoOwner, got.RepoName, got.TargetRef)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	repoScope, ok := payload["repo_scope"].(map[string]any)
	if !ok || repoScope["kind"] != "all_repos" {
		t.Fatalf("repo_scope = %#v", payload["repo_scope"])
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "named" {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
	names, ok := branchScope["branches"].([]any)
	if !ok || len(names) != 2 || names[0] != "feature-a" || names[1] != "feature-b" {
		t.Fatalf("branch_scope.branches = %#v", branchScope["branches"])
	}
}

func TestHandleApproveBreakGlassRequestPersistsScopeOverride(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload: []byte(`{
				"repo_scope": {"kind":"current_repo","repo":"romaine-life/tank-operator"},
				"branch_scope": {"kind":"unlimited"},
				"operations": ["push_current_head"],
				"reason": "repair branch"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{
		"note":"broaden to companion repo",
		"repo_scope":{"kind":"repos","repos":["romaine-life/tank-operator","romaine-life/auth"]},
		"branch_scope":{"kind":"named","branches":["feature-a","feature-b"]}
	}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.appendCalls[0]
	if got.RepoOwner != "" || got.RepoName != "" || got.TargetRef != "tank://session/47/git-break-glass/repos" {
		t.Fatalf("scope identity = owner %q repo %q target %q", got.RepoOwner, got.RepoName, got.TargetRef)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	repoScope, ok := payload["repo_scope"].(map[string]any)
	if !ok || repoScope["kind"] != "repos" {
		t.Fatalf("repo_scope = %#v", payload["repo_scope"])
	}
	repos, ok := repoScope["repos"].([]any)
	if !ok || len(repos) != 2 || repos[0] != "romaine-life/tank-operator" || repos[1] != "romaine-life/auth" {
		t.Fatalf("repo_scope.repos = %#v", repoScope["repos"])
	}
	branchScope, ok := payload["branch_scope"].(map[string]any)
	if !ok || branchScope["kind"] != "named" {
		t.Fatalf("branch_scope = %#v", payload["branch_scope"])
	}
	branches, ok := branchScope["branches"].([]any)
	if !ok || len(branches) != 2 || branches[0] != "feature-a" || branches[1] != "feature-b" {
		t.Fatalf("branch_scope.branches = %#v", branchScope["branches"])
	}
}

func TestHandleApproveBreakGlassRequestRejectsConflictingRepoScope(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			Payload: []byte(`{
				"repo_scope": {"kind":"all_repos","repo":"romaine-life/tank-operator"},
				"branch_scope": {"kind":"unlimited"},
				"operations": ["push_current_head"],
				"reason": "repair planned branches"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
}

func TestHandleInternalGetGitBreakGlassGrantReturnsActiveGrant(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"expires_at":   expiresAt,
		"operations":   []string{"mint_full_git_token"},
		"reason":       "repair branch",
		"repo_scope":   map[string]any{"kind": "current_repo", "repo": "romaine-life/tank-operator"},
		"branch_scope": map[string]any{"kind": "unlimited"},
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:   "grant-1",
			Action:    "github.break_glass.grant",
			Status:    "succeeded",
			RepoOwner: "romaine-life",
			RepoName:  "tank-operator",
			TargetRef: "https://github.com/romaine-life/tank-operator",
			Payload:   payload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/git-break-glass/grant?repo=romaine-life/tank-operator", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetGitBreakGlassGrant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["event_id"] != "grant-1" {
		t.Fatalf("body = %#v", body)
	}
	if store.listOwner != "owner@example.test" || store.listSession != "47" {
		t.Fatalf("list lookup = owner %q session %q", store.listOwner, store.listSession)
	}
}

func TestHandleAdminBreakGlassRequestsListsPendingAcrossSessions(t *testing.T) {
	store := &fakeControlActionStore{
		breakGlassRows: []pgstore.ControlActionEvent{
			{
				EventID:      "request-pending",
				InvocationID: "invocation-pending",
				CreatedAt:    time.Unix(1700000100, 0).UTC(),
				OwnerEmail:   "owner@example.test",
				SessionScope: "tank-operator-slot-3",
				SessionID:    "47",
				Action:       "github.break_glass.request",
				Status:       "started",
				TargetKind:   "github_repository",
				TargetRef:    "tank://session/47/git-break-glass/repos",
				RepoOwner:    "romaine-life",
				RepoName:     "auth",
				Payload: []byte(`{
					"reason": "open auth companion PR",
					"source": "agent",
					"request_event_id": "request-pending",
					"repo_scope": {"kind":"repos","repos":["romaine-life/auth"]},
					"branch_scope": {"kind":"named","branches":["auth"]}
				}`),
			},
			{
				EventID:      "request-decided",
				InvocationID: "invocation-decided",
				CreatedAt:    time.Unix(1700000000, 0).UTC(),
				OwnerEmail:   "owner@example.test",
				SessionScope: "tank-operator-slot-3",
				SessionID:    "48",
				Action:       "azure.break_glass.request",
				Status:       "started",
				TargetKind:   "azure_mcp",
				TargetRef:    "azure-personal",
				Payload:      []byte(`{"reason":"inspect azure","request_event_id":"request-decided"}`),
			},
		},
		listRows: []pgstore.ControlActionEvent{
			{
				EventID:      "deny-decided",
				InvocationID: "invocation-decided",
				OwnerEmail:   "owner@example.test",
				SessionScope: "tank-operator-slot-3",
				SessionID:    "48",
				Action:       "azure.break_glass.deny",
				Status:       "failed",
				TargetKind:   "azure_mcp",
				TargetRef:    "azure-personal",
				Payload:      []byte(`{"request_event_id":"request-decided"}`),
			},
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/break-glass-requests?status=pending", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleAdminBreakGlassRequests(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status   string `json:"status"`
		Requests []struct {
			Pending bool                   `json:"pending"`
			Request controlActionEventJSON `json:"request"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "pending" || len(body.Requests) != 1 {
		t.Fatalf("body = %+v", body)
	}
	if got := body.Requests[0].Request.EventID; got != "request-pending" {
		t.Fatalf("request event = %q", got)
	}
	if !body.Requests[0].Pending {
		t.Fatalf("pending = false")
	}
	if store.breakGlassScope != "tank-operator-slot-3" {
		t.Fatalf("breakGlassScope = %q", store.breakGlassScope)
	}
}

func TestHandleAdminBreakGlassRequestsRejectsNonAdmin(t *testing.T) {
	app := controlActionTestServer(t, &fakeControlActionStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/break-glass-requests", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleAdminBreakGlassRequests(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleApproveBreakGlassRequestPersistsAzureGrant(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "azure.break_glass.request",
			Status:       "started",
			TargetKind:   "azure_mcp",
			TargetRef:    "azure-personal",
			Payload: []byte(`{
				"operations": ["use_azure_personal_mcp"],
				"reason": "inspect session_events ledger"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "azure.break_glass.grant" || got.Status != "succeeded" {
		t.Fatalf("grant action/status = %s/%s", got.Action, got.Status)
	}
	if got.OwnerEmail != "owner@example.test" {
		t.Fatalf("grant owner = %q, want request owner", got.OwnerEmail)
	}
	if got.TargetKind != "azure_mcp" || got.TargetRef != "azure-personal" {
		t.Fatalf("target = %s/%s", got.TargetKind, got.TargetRef)
	}
	if got.RepoOwner != "" || got.RepoName != "" {
		t.Fatalf("azure grant should not be repo-scoped, got %s/%s", got.RepoOwner, got.RepoName)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_event_id"] != "request-1" {
		t.Fatalf("request_event_id = %v", payload["request_event_id"])
	}
	if payload["approved_by"] != "admin@example.test" {
		t.Fatalf("approved_by = %v", payload["approved_by"])
	}
	ops, _ := payload["operations"].([]any)
	if len(ops) != 1 || ops[0] != "use_azure_personal_mcp" {
		t.Fatalf("operations = %v", payload["operations"])
	}
}

func TestHandleDenyBreakGlassRequestPersistsDecision(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "github.break_glass.request",
			Status:       "started",
			TargetKind:   "github_repository",
			TargetRef:    "https://github.com/romaine-life/tank-operator",
			RepoOwner:    "romaine-life",
			RepoName:     "tank-operator",
			Payload:      []byte(`{"reason":"no context"}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/deny", strings.NewReader(`{"note":"too broad"}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleDenyBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "github.break_glass.deny" || got.Status != "failed" {
		t.Fatalf("decision action/status = %s/%s", got.Action, got.Status)
	}
	if got.OwnerEmail != "owner@example.test" || got.RepoOwner != "romaine-life" || got.RepoName != "tank-operator" {
		t.Fatalf("decision identity = %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_event_id"] != "request-1" || payload["decided_by"] != "admin@example.test" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHandleInternalGrantTestSlotModelApprovalPersistsGrant(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/test-slot-model-approvals/grants", strings.NewReader(`{
		"mode":"codex_gui",
		"model":"gpt-5.5",
		"effort":"xhigh",
		"request_event_id":"request-1",
		"reason":"need frontier model for this validation",
		"ttl_seconds":1800
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGrantTestSlotModelApproval(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls=%d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != testSlotModelGrantAction || got.SourceTool != "test_slot_model_approval" || got.SessionID != "47" {
		t.Fatalf("grant event = %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "gpt-5.5" || payload["effort"] != "xhigh" ||
		payload["low_model"] != "gpt-5.3-codex-spark" || payload["low_effort"] != "low" ||
		payload["request_event_id"] != "request-1" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestHandleApproveTestSlotModelApprovalRequestStartsSystemApprovalTurn(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "model-request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       testSlotModelRequestAction,
			Status:       "started",
			TargetKind:   "tank_session_model",
			TargetRef:    "tank://session-scope/tank-operator-slot-3/sessions/47/test-slot-model/codex_gui",
			Payload: []byte(`{
				"mode": "codex_gui",
				"provider": "codex",
				"model": "gpt-5.5",
				"effort": "xhigh",
				"low_model": "gpt-5.3-codex-spark",
				"low_effort": "low",
				"reason": "frontier validation"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/test-slot-model-requests/model-request-1/approve", strings.NewReader(`{"note":"ok"}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "model-request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveTestSlotModelApprovalRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls=%d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != testSlotModelGrantAction || got.OwnerEmail != "owner@example.test" {
		t.Fatalf("grant event = %#v", got)
	}
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	command := bus.commands[0]
	if command.Type != "submit_turn" || command.Source != "test-slot-model-approval" {
		t.Fatalf("command type/source = %s/%s", command.Type, command.Source)
	}
	if command.Email != "owner@example.test" || command.SessionID != "47" {
		t.Fatalf("command target = %s/%s", command.Email, command.SessionID)
	}
	if !strings.Contains(command.Prompt, "Your test-slot model request was approved") ||
		!strings.Contains(command.Prompt, "Retry the test-slot session creation") {
		t.Fatalf("prompt = %q", command.Prompt)
	}
}

func TestHandleApproveAzureBreakGlassRequestActivatesMcpViaApprovalTurn(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "azure.break_glass.request",
			Status:       "started",
			TargetKind:   "azure_mcp",
			TargetRef:    "azure-personal",
			Payload: []byte(`{
				"operations": ["use_azure_personal_mcp"],
				"reason": "inspect session_events ledger"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	command := bus.commands[0]
	if command.Type != "submit_turn" || command.Source != "break-glass-approval" {
		t.Fatalf("command type/source = %s/%s", command.Type, command.Source)
	}
	if command.SessionID != "47" || command.Email != "owner@example.test" {
		t.Fatalf("command target = %s/%s", command.SessionID, command.Email)
	}
	if command.MCPActivateName != "azure-personal" || command.MCPActivateURL != "http://127.0.0.1:9991/" {
		t.Fatalf("mcp activation = %q/%q", command.MCPActivateName, command.MCPActivateURL)
	}
	if !strings.Contains(command.Prompt, "Your Azure break-glass request was approved") {
		t.Fatalf("prompt missing azure approval notice: %q", command.Prompt)
	}
	var body struct {
		AgentNotification struct {
			Delivered bool   `json:"delivered"`
			TurnID    string `json:"turn_id"`
		} `json:"agent_notification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.AgentNotification.Delivered || body.AgentNotification.TurnID == "" {
		t.Fatalf("agent notification = %#v", body.AgentNotification)
	}
}

func TestHandleApproveKubernetesBreakGlassRequestActivatesMcpViaApprovalTurn(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "kubernetes.break_glass.request",
			Status:       "started",
			TargetKind:   "kubernetes_mcp",
			TargetRef:    "kubernetes-break-glass",
			Payload: []byte(`{
				"operations": ["use_kubernetes_break_glass_mcp"],
				"reason": "restart a wedged controller"
			}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(store.appendCalls))
	}
	got := store.appendCalls[0]
	if got.Action != "kubernetes.break_glass.grant" || got.Status != "succeeded" {
		t.Fatalf("grant action/status = %s/%s", got.Action, got.Status)
	}
	if got.TargetKind != "kubernetes_mcp" || got.TargetRef != "kubernetes-break-glass" {
		t.Fatalf("target = %s/%s", got.TargetKind, got.TargetRef)
	}
	bus := app.sessionBus.(*recordingSessionBus)
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	command := bus.commands[0]
	if command.MCPActivateName != "kubernetes-break-glass" || command.MCPActivateURL != "http://127.0.0.1:9993/" {
		t.Fatalf("mcp activation = %q/%q", command.MCPActivateName, command.MCPActivateURL)
	}
	if !strings.Contains(command.Prompt, "Your Kubernetes break-glass request was approved") {
		t.Fatalf("prompt missing kubernetes approval notice: %q", command.Prompt)
	}
}

func TestHandleBreakGlassRequestReturnsAlreadyDecided(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "azure.break_glass.request",
			Status:       "started",
			TargetKind:   "azure_mcp",
			TargetRef:    "azure-personal",
			Payload:      []byte(`{"reason":"inspect ledger"}`),
		},
		decisionRow: pgstore.ControlActionEvent{
			EventID:      "deny-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "azure.break_glass.deny",
			Status:       "failed",
			TargetKind:   "azure_mcp",
			TargetRef:    "azure-personal",
			Payload:      []byte(`{"request_event_id":"request-1"}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/break-glass-requests/request-1/approve", strings.NewReader(`{}`))
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleApproveBreakGlassRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.appendCalls) != 0 {
		t.Fatalf("append calls = %d, want 0", len(store.appendCalls))
	}
	if !strings.Contains(rec.Body.String(), "already_decided") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleGetBreakGlassRequestRequiresAdmin(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/47/break-glass-requests/request-1", nil)
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "owner@example.test", auth.RoleUser))
	rec := httptest.NewRecorder()

	app.handleGetBreakGlassRequest(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.getEventID != "" {
		t.Fatalf("non-admin should not load request, got event id %q", store.getEventID)
	}
}

func TestHandleGetBreakGlassRequestReturnsRequestAndDecision(t *testing.T) {
	store := &fakeControlActionStore{
		getRow: pgstore.ControlActionEvent{
			EventID:      "request-1",
			InvocationID: "invocation-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "azure.break_glass.request",
			Status:       "started",
			TargetKind:   "azure_mcp",
			TargetRef:    "azure-personal",
			Payload:      []byte(`{"reason":"inspect ledger"}`),
		},
		decisionRow: pgstore.ControlActionEvent{
			EventID:      "grant-1",
			OwnerEmail:   "owner@example.test",
			SessionScope: "tank-operator-slot-3",
			SessionID:    "47",
			Action:       "azure.break_glass.grant",
			Status:       "succeeded",
			TargetKind:   "azure_mcp",
			TargetRef:    "azure-personal",
			Payload:      []byte(`{"request_event_id":"request-1"}`),
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/47/break-glass-requests/request-1", nil)
	req.SetPathValue("session_id", "47")
	req.SetPathValue("request_event_id", "request-1")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, "admin@example.test", auth.RoleAdmin))
	rec := httptest.NewRecorder()

	app.handleGetBreakGlassRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Pending  bool                   `json:"pending"`
		Request  controlActionEventJSON `json:"request"`
		Decision controlActionEventJSON `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Pending || body.Request.EventID != "request-1" || body.Decision.EventID != "grant-1" {
		t.Fatalf("body = %#v", body)
	}
}

func TestHandleInternalGetAzureBreakGlassGrantReturnsActiveGrant(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"expires_at": expiresAt,
		"operations": []string{"use_azure_personal_mcp"},
		"reason":     "inspect ledger",
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:    "azure-grant-1",
			Action:     "azure.break_glass.grant",
			Status:     "succeeded",
			TargetKind: "azure_mcp",
			TargetRef:  "azure-personal",
			Payload:    payload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/azure-break-glass/grant", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetAzureBreakGlassGrant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["event_id"] != "azure-grant-1" {
		t.Fatalf("body = %#v", body)
	}
	if store.listOwner != "owner@example.test" || store.listSession != "47" {
		t.Fatalf("list lookup = owner %q session %q", store.listOwner, store.listSession)
	}
}

func TestHandleInternalGetAzureBreakGlassGrantInactiveWithoutGrant(t *testing.T) {
	// An expired grant must not count as active.
	expiredPayload, _ := json.Marshal(map[string]any{
		"expires_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		"operations": []string{"use_azure_personal_mcp"},
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:    "azure-grant-expired",
			Action:     "azure.break_glass.grant",
			Status:     "succeeded",
			TargetKind: "azure_mcp",
			TargetRef:  "azure-personal",
			Payload:    expiredPayload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/azure-break-glass/grant", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetAzureBreakGlassGrant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != false {
		t.Fatalf("expected inactive grant, body = %#v", body)
	}
}

func TestHandleInternalGetKubernetesBreakGlassGrantReturnsActiveGrant(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"expires_at": expiresAt,
		"operations": []string{"use_kubernetes_break_glass_mcp"},
		"reason":     "restart a wedged controller",
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			EventID:    "kubernetes-grant-1",
			Action:     "kubernetes.break_glass.grant",
			Status:     "succeeded",
			TargetKind: "kubernetes_mcp",
			TargetRef:  "kubernetes-break-glass",
			Payload:    payload,
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/kubernetes-break-glass/grant", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetKubernetesBreakGlassGrant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["event_id"] != "kubernetes-grant-1" || body["resource"] != "kubernetes-break-glass" {
		t.Fatalf("body = %#v", body)
	}
	if store.listOwner != "owner@example.test" || store.listSession != "47" {
		t.Fatalf("list lookup = owner %q session %q", store.listOwner, store.listSession)
	}
}

func TestHandleInternalGetGitBreakGlassGrantMatchesExplicitRepoListAndBranchLimit(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"expires_at":   expiresAt,
		"operations":   []string{"push_current_head"},
		"repo_scope":   map[string]any{"kind": "repos", "repos": []string{"romaine-life/tank-operator", "romaine-life/auth"}},
		"branch_scope": map[string]any{"kind": "count", "count": 2},
	})
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{
			{
				EventID:   "grant-1",
				Action:    "github.break_glass.grant",
				Status:    "succeeded",
				TargetRef: "tank://session/47/git-break-glass/repos",
				Payload:   payload,
			},
			{
				Action:  "github.break_glass.push",
				Status:  "succeeded",
				Payload: []byte(`{"grant_event_id":"grant-1","branch":"feature-a"}`),
			},
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/47/git-break-glass/grant?repo=romaine-life/auth", nil)
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGetGitBreakGlassGrant(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["remaining_branches"] != float64(1) {
		t.Fatalf("body = %#v", body)
	}
}

func TestHandleInternalVerifyHotSwapAllowsPublishedGreenMergeableHead(t *testing.T) {
	prNumber := 1113
	sha := "0123456789abcdef0123456789abcdef01234567"
	branch := "tank/session/47/tank-operator"
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{
			{
				Action:    "github.pull_request.mergeability",
				Status:    "succeeded",
				RepoOwner: "romaine-life",
				RepoName:  "tank-operator",
				PRNumber:  &prNumber,
				ResultSHA: sha,
				Payload:   []byte(`{"branch":"tank/session/47/tank-operator","mergeable":true,"mergeable_state":"clean"}`),
			},
			{
				Action:    "github.commit.ci",
				Status:    "succeeded",
				RepoOwner: "romaine-life",
				RepoName:  "tank-operator",
				ResultSHA: sha,
				Payload:   []byte(`{"completed":3}`),
			},
			{
				Action:    "github.commit.push",
				Status:    "succeeded",
				RepoOwner: "romaine-life",
				RepoName:  "tank-operator",
				ResultSHA: sha,
				Payload:   []byte(`{"branch":"tank/session/47/tank-operator"}`),
			},
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/hot-swap/verify", strings.NewReader(`{
		"repo": "romaine-life/tank-operator",
		"branch": "`+branch+`",
		"sha": "`+sha+`",
		"artifact_kind": "codex_runner",
		"validation_target": "existing_session",
		"source_tool": "apply_test_slot_hot_swap"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalVerifyHotSwap(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body hotSwapVerificationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Allowed || !body.PublishVerified || !body.CIVerified || !body.MergeVerified {
		t.Fatalf("verification body = %#v", body)
	}
	if body.PRNumber == nil || *body.PRNumber != prNumber {
		t.Fatalf("pr_number = %#v, want %d", body.PRNumber, prNumber)
	}
	if store.listOwner != "owner@example.test" || store.listSession != "47" || store.listLimit != 200 {
		t.Fatalf("list lookup = owner %q session %q limit %d", store.listOwner, store.listSession, store.listLimit)
	}
}

func TestHandleInternalVerifyHotSwapBlocksPendingCI(t *testing.T) {
	prNumber := 1113
	sha := "0123456789abcdef0123456789abcdef01234567"
	branch := "tank/session/47/tank-operator"
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{
			{
				Action:    "github.commit.ci",
				Status:    "started",
				RepoOwner: "romaine-life",
				RepoName:  "tank-operator",
				ResultSHA: sha,
				Error:     "checks are pending",
				Payload:   []byte(`{"pending":["build"]}`),
			},
			{
				Action:    "github.pull_request.mergeability",
				Status:    "succeeded",
				RepoOwner: "romaine-life",
				RepoName:  "tank-operator",
				PRNumber:  &prNumber,
				ResultSHA: sha,
				Payload:   []byte(`{"branch":"tank/session/47/tank-operator"}`),
			},
			{
				Action:    "github.commit.push",
				Status:    "succeeded",
				RepoOwner: "romaine-life",
				RepoName:  "tank-operator",
				ResultSHA: sha,
				Payload:   []byte(`{"branch":"tank/session/47/tank-operator"}`),
			},
		},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/hot-swap/verify", strings.NewReader(`{
		"repo": "romaine-life/tank-operator",
		"branch": "`+branch+`",
		"sha": "`+sha+`"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalVerifyHotSwap(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body hotSwapVerificationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Allowed || body.CIVerified {
		t.Fatalf("verification body = %#v", body)
	}
	if got := strings.Join(body.Reasons, "\n"); !strings.Contains(got, "latest CI observation") {
		t.Fatalf("reasons = %q", got)
	}
}

func TestHandleInternalVerifyHotSwapBlocksWrongBranchPublish(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	store := &fakeControlActionStore{
		listRows: []pgstore.ControlActionEvent{{
			Action:    "github.commit.push",
			Status:    "succeeded",
			RepoOwner: "romaine-life",
			RepoName:  "tank-operator",
			ResultSHA: sha,
			Payload:   []byte(`{"branch":"tank/session/48/tank-operator"}`),
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/hot-swap/verify", strings.NewReader(`{
		"repo": "romaine-life/tank-operator",
		"branch": "tank/session/47/tank-operator",
		"sha": "`+sha+`"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalVerifyHotSwap(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body hotSwapVerificationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.PublishVerified {
		t.Fatalf("wrong-branch publish was accepted: %#v", body)
	}
	if got := strings.Join(body.Reasons, "\n"); !strings.Contains(got, "no governed publish record") {
		t.Fatalf("reasons = %q", got)
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
