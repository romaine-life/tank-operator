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
			req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
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
		"note":"ok",
		"limit":50,
		"unlimited":true,
		"branch_names":["human-added"]
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

func TestHandleAutoApprovePRLanesPersistsSessionGrant(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/auto-approve", strings.NewReader(`{
		"repo": "romaine-life/tank-operator",
		"limit": 12,
		"branch_names": ["docs", "tank/session/47/tank-operator/backend"],
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
	names, ok := payload["branch_names"].([]any)
	if !ok || len(names) != 2 || names[0] != "docs" || names[1] != "backend" {
		t.Fatalf("branch_names = %#v", payload["branch_names"])
	}
}

func TestHandleInternalGetPRLaneAutoApprovalReturnsActiveGrant(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"limit": 7, "scope": "session"})
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
		"limit":        1,
		"branch_names": []string{"docs"},
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
				"lane_names":["docs","backend"],
				"requested_count":2,
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
	if payload["limit"] != float64(2) {
		t.Fatalf("limit = %#v", payload["limit"])
	}
	if payload["unlimited"] != false {
		t.Fatalf("unlimited = %#v", payload["unlimited"])
	}
	names, ok := payload["branch_names"].([]any)
	if !ok || len(names) != 2 || names[0] != "docs" || names[1] != "backend" {
		t.Fatalf("branch_names = %#v", payload["branch_names"])
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
				"lane_names":["docs","backend"],
				"requested_count":2,
				"reason":"split review"
			}`),
		}},
	}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/47/pr-lane-requests/lane-request-1/approve", strings.NewReader(`{
		"note":"override",
		"limit":10,
		"unlimited":true,
		"branch_names":["ops"]
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
	if payload["limit"] != float64(10) || payload["unlimited"] != true {
		t.Fatalf("override payload = %#v", payload)
	}
	names, ok := payload["branch_names"].([]any)
	if !ok || len(names) != 1 || names[0] != "ops" {
		t.Fatalf("branch_names = %#v", payload["branch_names"])
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

func TestHandleInternalGrantGitBreakGlassPersistsGrant(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/git-break-glass/grants", strings.NewReader(`{
		"repo": "romaine-life/tank-operator",
		"ttl_seconds": 900,
		"operations": ["mint_full_git_token"],
		"request_event_id": "request-1",
		"reason": "repair branch"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGrantGitBreakGlass(rec, req)

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
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["request_event_id"] != "request-1" {
		t.Fatalf("request_event_id = %v", payload["request_event_id"])
	}
}

func TestHandleInternalGetGitBreakGlassGrantReturnsActiveGrant(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"expires_at": expiresAt,
		"operations": []string{"mint_full_git_token"},
		"reason":     "repair branch",
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

func TestHandleInternalGrantAzureBreakGlassPersistsGrant(t *testing.T) {
	store := &fakeControlActionStore{}
	app := controlActionTestServer(t, store)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/sessions/47/azure-break-glass/grants", strings.NewReader(`{
		"ttl_seconds": 900,
		"request_event_id": "request-1",
		"reason": "inspect session_events ledger"
	}`))
	req.SetPathValue("session_id", "47")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-47@service.tank.romaine.life", "owner@example.test"))
	rec := httptest.NewRecorder()

	app.handleInternalGrantAzureBreakGlass(rec, req)

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
	ops, _ := payload["operations"].([]any)
	if len(ops) != 1 || ops[0] != "use_azure_personal_mcp" {
		t.Fatalf("operations = %v", payload["operations"])
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
