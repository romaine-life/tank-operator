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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type recordingSessionBus struct {
	commands            []sessionbus.Command
	wakes               []string
	pinnedPublishEmails []string
	pinnedUpdateCh      <-chan struct{}
	err                 error
}

// recordingSessionEventStore captures every backend-owned Upsert so tests
// can verify the durable-before-202 guarantee that handlers_turns.go
// makes. Wraps the no-op stub for the other SessionEventStore methods.
type recordingSessionEventStore struct {
	store.StubSessionEventStore
	upserts       []map[string]any
	turnEvents    []map[string]any
	terminalTurns map[string]map[string]any
	terminal      map[string]any
	err           error
}

func (r *recordingSessionEventStore) Upsert(_ context.Context, event map[string]any) error {
	if r.err != nil {
		return r.err
	}
	r.upserts = append(r.upserts, event)
	return nil
}

func (r *recordingSessionEventStore) OrderKeyForTimelineID(_ context.Context, _, timelineID string) (string, error) {
	for i := len(r.upserts) - 1; i >= 0; i-- {
		if got, _ := r.upserts[i]["timeline_id"].(string); got == timelineID {
			orderKey, _ := r.upserts[i]["order_key"].(string)
			return orderKey, nil
		}
	}
	return "", nil
}

// FindTurnTerminal returns a seeded turn terminal. terminalTurns models a
// per-turn lookup keyed by turnID for lifecycle/interrupt tests.
func (r *recordingSessionEventStore) FindTurnTerminal(_ context.Context, _ string, turnID string) (map[string]any, error) {
	if r.terminalTurns != nil {
		return r.terminalTurns[turnID], nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.terminal, nil
}

func (r *recordingSessionEventStore) EventsForTurn(_ context.Context, _ string, _ string, _ int) (store.SessionEventPage, error) {
	if r.err != nil {
		return store.SessionEventPage{}, r.err
	}
	return store.SessionEventPage{Events: r.turnEvents, FoundNewest: true}, nil
}

func TestPersistBackendEventRefreshesActivityForLifecycleEvent(t *testing.T) {
	refresher := &recordingActivityRefresher{}
	app := &appServer{
		sessionEvents:     &recordingSessionEventStore{},
		sessionScope:      "tank-operator-slot-3",
		activityRefresher: refresher,
	}
	event := map[string]any{
		"type":            "turn.completed",
		"email":           "User@Example.COM",
		"session_id":      "17",
		"tank_session_id": "tank-operator-slot-3:17",
	}

	if err := app.persistBackendEvent(context.Background(), "tank-operator-slot-3:17", event); err != nil {
		t.Fatalf("persistBackendEvent returned error: %v", err)
	}
	if len(refresher.calls) != 1 {
		t.Fatalf("activity refresh calls = %d, want 1", len(refresher.calls))
	}
	call := refresher.calls[0]
	if call.owner != "user@example.com" || call.scope != "tank-operator-slot-3" || call.sessionID != "17" {
		t.Fatalf("activity refresh call = %+v, want owner=user@example.com scope=tank-operator-slot-3 sessionID=17", call)
	}
}

func TestPersistBackendEventSkipsActivityRefreshForNonLifecycleEvent(t *testing.T) {
	refresher := &recordingActivityRefresher{}
	app := &appServer{
		sessionEvents:     &recordingSessionEventStore{},
		sessionScope:      "tank-operator-slot-3",
		activityRefresher: refresher,
	}
	event := map[string]any{
		"type":            "user_message.created",
		"email":           "user@example.com",
		"session_id":      "17",
		"tank_session_id": "tank-operator-slot-3:17",
	}

	if err := app.persistBackendEvent(context.Background(), "tank-operator-slot-3:17", event); err != nil {
		t.Fatalf("persistBackendEvent returned error: %v", err)
	}
	if len(refresher.calls) != 0 {
		t.Fatalf("activity refresh calls = %d, want 0", len(refresher.calls))
	}
}

func (b *recordingSessionBus) PublishCommand(_ context.Context, command sessionbus.Command) error {
	if b.err != nil {
		return b.err
	}
	b.commands = append(b.commands, command)
	return nil
}

func (b *recordingSessionBus) PublishSessionEventWake(_ context.Context, storageKey string) error {
	b.wakes = append(b.wakes, storageKey)
	return nil
}

func (*recordingSessionBus) SubscribeWakes(context.Context, string) (<-chan struct{}, func(), error) {
	return make(chan struct{}), func() {}, nil
}

// SubscribeWakesWithRecorder is the per-stream-aware variant. Tests
// that drive the SSE handler observe the recorder through the
// registry surface; for the existing turns/list tests the recorder
// argument is unused.
func (*recordingSessionBus) SubscribeWakesWithRecorder(context.Context, string, sessionbus.WakeRecorder) (<-chan struct{}, func(), error) {
	return make(chan struct{}), func() {}, nil
}

func (*recordingSessionBus) SubscribeWakesForStorageKey(context.Context, string, sessionbus.WakeRecorder) (<-chan struct{}, func(), error) {
	return make(chan struct{}), func() {}, nil
}

// PublishSessionRowUpdate + SubscribeSessionRowUpdates are the row-
// update wire surface (Phase 3 of docs/session-list-redesign.md).
// Tests don't drive the sidebar SSE so the recorder no-ops both.
func (*recordingSessionBus) PublishSessionRowUpdate(context.Context, string, string, []byte) error {
	return nil
}

func (*recordingSessionBus) SubscribeSessionRowUpdates(context.Context, string, string) (<-chan []byte, func(), error) {
	return make(chan []byte), func() {}, nil
}

func (b *recordingSessionBus) PublishPinnedReposUpdate(_ context.Context, email string) error {
	if b.err != nil {
		return b.err
	}
	b.pinnedPublishEmails = append(b.pinnedPublishEmails, email)
	return nil
}

func (b *recordingSessionBus) SubscribePinnedReposUpdates(context.Context, string) (<-chan struct{}, func(), error) {
	if b.pinnedUpdateCh != nil {
		return b.pinnedUpdateCh, func() {}, nil
	}
	return make(chan struct{}), func() {}, nil
}

func TestEnqueueSessionTurnPublishesSDKCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{"client_nonce":"turn-abc_123","prompt":"  /test\n\nhello sdk  ","model":"claude-sonnet-4-6","effort":"xhigh","permission_mode":"bypassPermissions","skill_name":"test","follow_up":true}`
	req := authedTurnRequest(t, "63", body)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	// Boundary events are persisted to Postgres directly (handler must
	// guarantee durability before returning success), and each persist
	// publishes a NATS wake so SSE clients see them immediately.
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("session-event upserts = %d, want 2 (user_message.created + turn.submitted)", len(es.upserts))
	}
	wantTypes := []string{"user_message.created", "turn.submitted"}
	for i, want := range wantTypes {
		if got, _ := es.upserts[i]["type"].(string); got != want {
			t.Fatalf("upsert[%d].type = %q, want %q", i, got, want)
		}
		if _, ok := es.upserts[i]["event_id"].(string); !ok {
			t.Fatalf("upsert[%d] missing event_id; full event = %#v", i, es.upserts[i])
		}
	}
	if len(bus.wakes) < 2 {
		t.Fatalf("session-event wakes = %d, want >=2 (one per boundary event)", len(bus.wakes))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandSubmitTurn || got.ClientNonce != "turn-abc_123" {
		t.Fatalf("turn/client nonce = %q/%q", got.TurnID, got.ClientNonce)
	}
	if got.Source != "sdk" || got.Provider != "claude" || got.SessionID != "63" || got.Email != "user@example.com" {
		t.Fatalf("record routing fields = %#v", got)
	}
	if got.Prompt != "/test\n\nhello sdk" || got.Model != "claude-sonnet-4-6" || got.PermissionMode != "bypassPermissions" || got.SkillName != "test" || !got.FollowUp {
		t.Fatalf("record payload fields = %#v", got)
	}
	// Effort is a load-bearing field on submit_turn: the agent-runner pins
	// it into SDK Options at pod boot from the first turn, and we want the
	// allowlist value to survive the round-trip from request body to bus
	// command. Mirror of the model/permission_mode assertion above.
	if got.Effort != "xhigh" {
		t.Fatalf("effort = %q, want xhigh", got.Effort)
	}
}

func TestEnqueueSessionTurnSeparatesDisplayTextFromRunnerPrompt(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{
		"client_nonce":"turn-attach_123",
		"prompt":"compare these\n\nAttachments:\n- /workspace/screenshots/1.png",
		"display_text":"compare these",
		"display_attachments":[
			{"label":"Screenshot 1","name":"image.png","kind":"image","path":"screenshots/1.png","abs_path":"/workspace/screenshots/1.png","size":123}
		]
	}`
	req := authedTurnRequest(t, "63", body)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	if got := bus.commands[0].Prompt; got != "compare these\n\nAttachments:\n- /workspace/screenshots/1.png" {
		t.Fatalf("command prompt = %q", got)
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("session-event upserts = %d, want 2", len(es.upserts))
	}
	payload, ok := es.upserts[0]["payload"].(map[string]any)
	if !ok {
		t.Fatalf("user event payload missing: %#v", es.upserts[0])
	}
	if got, _ := payload["text"].(string); got != "compare these" {
		t.Fatalf("durable user text = %q", got)
	}
	attachments, ok := payload["attachments"].([]map[string]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("durable user attachments = %#v", payload["attachments"])
	}
	if got, _ := attachments[0]["label"].(string); got != "Screenshot 1" {
		t.Fatalf("attachment label = %q", got)
	}
	if got, _ := attachments[0]["absPath"].(string); got != "/workspace/screenshots/1.png" {
		t.Fatalf("attachment absPath = %q", got)
	}
}

func TestEnqueueSessionTurnUsesSessionOwnedRunConfig(t *testing.T) {
	bus := &recordingSessionBus{}
	registry := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "63",
		Email:   "user@example.com",
		Mode:    sessionmodel.ClaudeGUIMode,
		Visible: true,
		Model:   "claude-opus-4-7",
		Effort:  "high",
	})
	app := testTurnsAppWithRegistry(
		t,
		bus,
		registry,
		sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"),
	)
	req := authedTurnRequest(t, "63", `{
		"client_nonce":"turn-session-config",
		"prompt":"hello",
		"model":"claude-sonnet-4-6",
		"effort":"bogus"
	}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Model != "claude-opus-4-7" || got.Effort != "high" {
		t.Fatalf("command run config = model %q effort %q, want session-owned model/effort", got.Model, got.Effort)
	}
}

func TestCreateSessionInitialTurnPersistsBeforeStartupStatus(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"claude_gui",
		"initial_turn":{
			"client_nonce":"turn-launch_123",
			"prompt":"  /test\n\nlaunch prompt  ",
			"model":"claude-sonnet-4-6",
			"effort":"xhigh",
			"permission_mode":"bypassPermissions",
			"skill_name":"test"
		}
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleCreateSession(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var created sessions.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.RequestedAt == nil || *created.RequestedAt == "" {
		t.Fatalf("created session missing requested_at: %#v", created)
	}
	requestedAt, err := time.Parse(time.RFC3339Nano, *created.RequestedAt)
	if err != nil {
		t.Fatalf("parse requested_at %q: %v", *created.RequestedAt, err)
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("session-event upserts = %d, want 2 (user_message.created + turn.submitted)", len(es.upserts))
	}
	for i, want := range []string{"user_message.created", "turn.submitted"} {
		if got, _ := es.upserts[i]["type"].(string); got != want {
			t.Fatalf("upsert[%d].type = %q, want %q", i, got, want)
		}
		createdAt, err := time.Parse(time.RFC3339Nano, es.upserts[i]["created_at"].(string))
		if err != nil {
			t.Fatalf("parse upsert[%d].created_at: %v", i, err)
		}
		if !createdAt.Before(requestedAt) {
			t.Fatalf("upsert[%d].created_at = %s, want before session requested_at %s", i, createdAt, requestedAt)
		}
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandSubmitTurn || got.ClientNonce != "turn-launch_123" {
		t.Fatalf("command type/client nonce = %q/%q", got.Type, got.ClientNonce)
	}
	if got.SessionID != created.ID || got.Provider != "claude" || got.Prompt != "/test\n\nlaunch prompt" {
		t.Fatalf("command routing/prompt = %#v", got)
	}
	if got.Model != "claude-sonnet-4-6" || got.Effort != "xhigh" || got.PermissionMode != "bypassPermissions" || got.SkillName != "test" {
		t.Fatalf("command payload fields = %#v", got)
	}
}

func TestCreateSessionAcceptsBugLabel(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"claude_gui",
		"bug_label":"bug: repeated validation defect"
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleCreateSession(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var created sessions.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.BugLabel == nil || created.BugLabel.DisplayName != "bug: repeated validation defect" {
		t.Fatalf("bug_label = %#v, want normalized display label", created.BugLabel)
	}
}

func TestCreateSessionRejectsCodexWithoutExplicitModel(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"codex_gui",
		"effort":"xhigh"
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleCreateSession(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "model is required for Codex sessions") {
		t.Fatalf("body = %s, want explicit Codex model error", resp.Body.String())
	}
}

func TestCreateSessionRejectsCodexDefaultModelAlias(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"codex_gui",
		"model":"codex-account-default",
		"effort":"xhigh"
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleCreateSession(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "default is not accepted") {
		t.Fatalf("body = %s, want default rejection", resp.Body.String())
	}
}

func TestCreateSessionInitialTurnDeferredReusesUserMessage(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"claude_gui",
		"initial_turn":{
			"client_nonce":"turn-launch_attach",
			"prompt":"launch prompt\n\nAttachments:\n- notes.txt",
			"deferred":true
		}
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleCreateSession(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var created sessions.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 1 {
		t.Fatalf("session-event upserts after create = %d, want 1 deferred user message", len(es.upserts))
	}
	if got, _ := es.upserts[0]["type"].(string); got != "user_message.created" {
		t.Fatalf("create upsert type = %q, want user_message.created", got)
	}
	if len(bus.commands) != 0 {
		t.Fatalf("commands after deferred create = %d, want 0", len(bus.commands))
	}

	turnReq := authedTurnRequest(t, created.ID, `{
		"client_nonce":"turn-launch_attach",
		"prompt":"launch prompt\n\nAttachments:\n- /workspace/.attachments/123-notes.txt",
		"existing_user_message":true
	}`)
	turnResp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(turnResp, turnReq)

	if turnResp.Code != http.StatusAccepted {
		t.Fatalf("turn status = %d body = %s", turnResp.Code, turnResp.Body.String())
	}
	if len(es.upserts) != 2 {
		t.Fatalf("session-event upserts after turn = %d, want 2 total", len(es.upserts))
	}
	if got, _ := es.upserts[1]["type"].(string); got != "turn.submitted" {
		t.Fatalf("second upsert type = %q, want turn.submitted", got)
	}
	if len(bus.commands) != 1 {
		t.Fatalf("commands after deferred submit = %d, want 1", len(bus.commands))
	}
	if got := bus.commands[0].Prompt; got != "launch prompt\n\nAttachments:\n- /workspace/.attachments/123-notes.txt" {
		t.Fatalf("command prompt = %q", got)
	}
}

func TestEnqueueSessionTurnRejectsExistingUserMessageWithoutLaunchRow(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "63", `{
		"client_nonce":"turn-missing-user",
		"prompt":"hello",
		"existing_user_message":true
	}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("commands = %d, want 0", len(bus.commands))
	}
}

func TestCreateSessionInitialTurnFailureRollsBackCreatedPod(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	app.sessionEvents = &recordingSessionEventStore{err: errors.New("postgres unavailable")}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"claude_gui",
		"initial_turn":{"client_nonce":"turn-launch_fail","prompt":"hello"}
	}`))
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	app.handleCreateSession(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	pods, err := app.k8s.CoreV1().Pods(sessionmodel.SessionsNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("pods after rollback = %d, want 0", len(pods.Items))
	}
}

func TestEnqueueSessionTurnStampsOriginSessionID(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{"client_nonce":"turn-origin","prompt":"forked prompt","origin_session_id":"42"}`
	req := authedTurnRequest(t, "63", body)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("session-event upserts = %d, want 2", len(es.upserts))
	}
	for _, event := range es.upserts {
		if got, _ := event["origin_session_id"].(string); got != "42" {
			t.Fatalf("event %q origin_session_id = %q, want 42", event["type"], got)
		}
	}
}

// TestAuthorKindForUser pins the principal taxonomy that maps an authenticated
// caller to the durable author_kind stamped on user_message.created. The two
// non-interactive principals — the k8s-exchange service identity that launches
// sessions (role=service) and human-minted break-glass tokens (purpose=bot) —
// resolve to "system"; ordinary interactive humans resolve to empty so their
// Gravatar still renders. A drift here is exactly the "user bubble borrows the
// wrong avatar" regression this feature exists to prevent.
func TestAuthorKindForUser(t *testing.T) {
	tests := []struct {
		name string
		user auth.User
		want string
	}{
		{name: "service principal", user: auth.User{Role: auth.RoleService, ActorEmail: "owner@example.com"}, want: string(conversation.AuthorKindSystem)},
		{name: "admin bot token", user: auth.User{Role: auth.RoleAdmin, Purpose: auth.PurposeBot}, want: string(conversation.AuthorKindSystem)},
		{name: "service bot token", user: auth.User{Role: auth.RoleService, Purpose: auth.PurposeBot, ActorEmail: "owner@example.com"}, want: string(conversation.AuthorKindSystem)},
		{name: "interactive user", user: auth.User{Role: auth.RoleUser, Email: "human@example.com"}, want: ""},
		{name: "interactive admin", user: auth.User{Role: auth.RoleAdmin, Email: "admin@example.com"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorKindForUser(tt.user); got != tt.want {
				t.Fatalf("authorKindForUser(%+v) = %q, want %q", tt.user, got, tt.want)
			}
		})
	}
}

// TestEnqueueSessionTurnRejectsInvalidEffort pins the allowlist enforcement
// at the choke point. The agent-runner trusts whatever lands on the wire
// (it casts the string to EffortLevel without re-validating), so a typo
// or stale UI value MUST be rejected here loudly with a 400 — silently
// dropping it would either (a) hide a frontend regression or (b) get
// quietly stripped by Normalize and let the runner fall back to the
// baked-in default, which looks identical to "the dropdown didn't take
// effect" from a user's perspective.
func TestEnqueueSessionTurnRejectsInvalidEffort(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "63", `{"client_nonce":"turn-bad-effort","prompt":"hello","effort":"bogus"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestEnqueueSessionTurnForwardsCodexModelAndEffort(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex-strength","prompt":"hello","model":"gpt-5.5","effort":"xhigh"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	if got := bus.commands[0].Provider; got != "codex" {
		t.Fatalf("provider = %q, want codex", got)
	}
	if got := bus.commands[0].Model; got != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", got)
	}
	if got := bus.commands[0].Effort; got != "xhigh" {
		t.Fatalf("effort = %q, want xhigh", got)
	}
}

func TestEnqueueSessionTurnRejectsCodexWithoutExplicitModel(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex-no-model","prompt":"hello","effort":"xhigh"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "model is required for Codex turns") {
		t.Fatalf("body = %s, want explicit Codex model error", resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestEnqueueSessionTurnRejectsCodexDefaultModelAlias(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex-default-model","prompt":"hello","model":"default"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "default is not accepted") {
		t.Fatalf("body = %s, want default rejection", resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestEnqueueSessionTurnRejectsCodexMaxEffort(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex-max","prompt":"hello","model":"gpt-5.5","effort":"max"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

// TestEnqueueSessionTurnAllowsEmptyEffort pins the "empty means use the
// runner's baked-in default" mapping. Legacy clients may omit effort;
// enforce-but-don't-require keeps the wire shape additive across providers.
func TestEnqueueSessionTurnAllowsEmptyEffort(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "63", `{"client_nonce":"turn-no-effort","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	if got := bus.commands[0].Effort; got != "" {
		t.Fatalf("effort = %q, want empty (runner-default fallback)", got)
	}
}

func TestEnqueueSessionTurnRejectsSkillPromptMismatch(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "63", `{"client_nonce":"turn-skill","prompt":"hello","skill_name":"test"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestEnqueueSessionTurnAcceptsCodexSkillTrigger(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex-skill","prompt":"$test","model":"gpt-5.5","skill_name":"test"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if got := bus.commands[0].SkillName; got != "test" {
		t.Fatalf("skill_name = %q, want test", got)
	}
}

func TestEnqueueSessionTurnRoutesCodexProvider(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex","prompt":"hello","model":"gpt-5.5"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if got := bus.commands[0].Provider; got != "codex" {
		t.Fatalf("provider = %q, want codex", got)
	}
}

func TestInterruptSessionTurnPublishesControlCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedInterruptRequest(t, "64", "run-abc_123")
	resp := httptest.NewRecorder()

	app.handleInterruptSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandInterrupt || got.Source != "interrupt" || got.Provider != "codex" || got.TargetTurnID != "run-abc_123" || got.ClientNonce != "run-abc_123" {
		t.Fatalf("interrupt record = %#v", got)
	}
	if got.Prompt != "" {
		t.Fatalf("interrupt prompt = %q, want empty", got.Prompt)
	}
	// Pin the load-bearing routing: interrupts MUST select the control
	// subject. If a future refactor sends them back through CommandSubject,
	// the runner-side max_ack_pending=1 on the command consumer will hold
	// the interrupt behind the in-flight submit_turn for the full duration
	// of the turn (the original "Stop doesn't stop deep tool-use loops"
	// regression). The unit test in internal/sessionbus pins the function;
	// this test pins that the handler's command Type + StorageKey + Provider
	// resolve to the same routing decision end-to-end.
	subject := sessionbus.SubjectForCommand(got)
	if subject != sessionbus.ControlSubject(got.SessionStorageKey, got.Provider) {
		t.Fatalf("interrupt subject = %q, want control subject for storage=%q provider=%q",
			subject, got.SessionStorageKey, got.Provider)
	}
	if subject == sessionbus.CommandSubject(got.SessionStorageKey, got.Provider) {
		t.Fatalf("interrupt MUST NOT publish on the data-plane command subject (%q)", subject)
	}
}

func TestInterruptSessionTurnRejectsBadTurnID(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedInterruptRequest(t, "64", "bad/slash")
	resp := httptest.NewRecorder()

	app.handleInterruptSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

// awaitingInputEvent builds the durable turn.awaiting_input pause that
// handleAnswerSessionTurn reads to confirm an answer targets a turn currently
// awaiting user input.
func awaitingInputEvent(turnID string, questions ...string) map[string]any {
	qs := make([]any, 0, len(questions))
	for _, q := range questions {
		qs = append(qs, map[string]any{"question": q})
	}
	return map[string]any{
		"type":        string(conversation.EventTurnAwaitingInput),
		"turn_id":     turnID,
		"timeline_id": turnID + ":item:toolu_123",
		"payload":     map[string]any{"questions": qs},
	}
}

func TestAnswerSessionTurnPublishesInputReply(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	app.sessionEvents = &recordingSessionEventStore{
		turnEvents: []map[string]any{awaitingInputEvent("turn-active_123", "Which auth method should we use?")},
	}
	body := `{
		"provider_item_id": "toolu_123",
		"timeline_id": "turn-active_123:item:toolu_123",
		"answers": {"Which auth method should we use?": ["  OAuth  "]},
		"annotations": {"Which auth method should we use?": {"notes": "matches existing IdP"}}
	}`
	req := authedAnswerRequest(t, "63", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleAnswerSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1 (input_reply)", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandInputReply || got.Provider != "claude" || got.Source != "answer" {
		t.Fatalf("answer routing fields = %#v", got)
	}
	if got.TargetTurnID != "turn-active_123" ||
		got.TargetTimelineID != "turn-active_123:item:toolu_123" ||
		got.TargetProviderItemID != "toolu_123" {
		t.Fatalf("answer target fields = %#v", got)
	}
	if got.Answers["Which auth method should we use?"][0] != "OAuth" {
		t.Fatalf("answers = %#v", got.Answers)
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 1 {
		t.Fatalf("session-event upserts = %d, want 1 (turn.input_answered)", len(es.upserts))
	}
	if gotType, _ := es.upserts[0]["type"].(string); gotType != string(conversation.EventTurnInputAnswered) {
		t.Fatalf("upsert[0].type = %q, want turn.input_answered", gotType)
	}
}

func TestAnswerSessionTurnPublishesCodexCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	registry := newTestSessionRegistry(sessionmodel.SessionRecord{
		ID:      "64",
		Email:   "user@example.com",
		Mode:    sessionmodel.CodexGUIMode,
		Visible: true,
		Model:   "gpt-5-codex",
	})
	app := testTurnsAppWithRegistry(t, bus, registry, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	app.sessionEvents = &recordingSessionEventStore{turnEvents: []map[string]any{awaitingInputEvent("turn-active_123", "Pick one")}}
	body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{"Pick one":["OAuth"]}}`
	req := authedAnswerRequest(t, "64", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleAnswerSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandInputReply || got.Provider != "codex" {
		t.Fatalf("codex answer command = %#v", got)
	}
	if !strings.HasPrefix(got.ClientNonce, "answer-") {
		t.Fatalf("client_nonce = %q, want answer-<hash> prefix", got.ClientNonce)
	}
}

// TestAnswerSessionTurnRejectsTurnNotAwaitingInput rejects an answer whose
// asking turn is not currently paused at turn.awaiting_input.
func TestAnswerSessionTurnRejectsTurnNotAwaitingInput(t *testing.T) {
	cases := []struct {
		name   string
		events []map[string]any
	}{
		{"completed turn", []map[string]any{
			awaitingInputEvent("turn-active_123", "Pick one"),
			{"type": string(conversation.EventTurnCompleted), "turn_id": "turn-active_123"},
		}},
		{"no awaiting input", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := &recordingSessionBus{}
			app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
			app.sessionEvents = &recordingSessionEventStore{turnEvents: tc.events}
			body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{"Pick one":["OAuth"]}}`
			req := authedAnswerRequest(t, "63", "turn-active_123", body)
			resp := httptest.NewRecorder()

			app.handleAnswerSessionTurn(resp, req)

			if resp.Code != http.StatusConflict {
				t.Fatalf("status = %d body = %s, want 409", resp.Code, resp.Body.String())
			}
			if len(bus.commands) != 0 {
				t.Fatalf("published commands = %d, want 0", len(bus.commands))
			}
		})
	}
}

// TestAnswerSessionTurnDoubleSubmitSharesDeterministicNonce proves the answered
// card cannot create divergent durable answers: two identical answers to the
// same asking turn derive the same client_nonce and command id.
func TestAnswerSessionTurnDoubleSubmitSharesDeterministicNonce(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	app.sessionEvents = &recordingSessionEventStore{turnEvents: []map[string]any{awaitingInputEvent("turn-active_123", "Pick one")}}
	body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{"Pick one":["OAuth"]}}`

	for i := 0; i < 2; i++ {
		req := authedAnswerRequest(t, "63", "turn-active_123", body)
		resp := httptest.NewRecorder()
		app.handleAnswerSessionTurn(resp, req)
		if resp.Code != http.StatusAccepted {
			t.Fatalf("submit %d: status = %d body = %s", i, resp.Code, resp.Body.String())
		}
	}
	if len(bus.commands) != 2 {
		t.Fatalf("published commands = %d, want 2", len(bus.commands))
	}
	if bus.commands[0].ClientNonce != bus.commands[1].ClientNonce {
		t.Fatalf("answer nonces differ: %q vs %q (must be deterministic for dedup)", bus.commands[0].ClientNonce, bus.commands[1].ClientNonce)
	}
	if bus.commands[0].CommandID != bus.commands[1].CommandID {
		t.Fatalf("answer command IDs differ: %q vs %q (dedup key must be stable)", bus.commands[0].CommandID, bus.commands[1].CommandID)
	}
}

func TestAnswerSessionTurnRejectsMissingTarget(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	app.sessionEvents = &recordingSessionEventStore{turnEvents: []map[string]any{awaitingInputEvent("turn-active_123", "Pick one")}}
	body := `{"provider_item_id":"","timeline_id":"turn-active_123:item:toolu_123","answers":{"Pick one":["OAuth"]}}`
	req := authedAnswerRequest(t, "63", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleAnswerSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestAnswerSessionTurnRejectsEmptyAnswers(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	app.sessionEvents = &recordingSessionEventStore{turnEvents: []map[string]any{awaitingInputEvent("turn-active_123", "Pick one")}}
	body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{}}`
	req := authedAnswerRequest(t, "63", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleAnswerSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestEnqueueSessionTurnRejectsMissingSDKRunner(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-65", "65", "user@example.com", sessionmodel.ClaudeGUIMode, "claude"))
	req := authedTurnRequest(t, "65", `{"client_nonce":"turn-no-runner","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published turn for pod without SDK runner: %#v", bus.commands)
	}
}

func TestEnqueueSessionTurnValidatesClientNonce(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-66", "66", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "66", `{"client_nonce":"bad/slash","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

func TestEnqueueSessionTurnSurfacesSessionBusFailure(t *testing.T) {
	bus := &recordingSessionBus{err: errors.New("nats down")}
	app := testTurnsApp(t, bus, sdkSessionPod("session-67", "67", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedTurnRequest(t, "67", `{"client_nonce":"turn-fail","prompt":"hello"}`)
	resp := httptest.NewRecorder()

	app.handleEnqueueSessionTurn(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}

// TestInterruptPersistsRequestedEventBeforeCommand pins the durable-first
// ordering: turn.interrupt_requested lands in session_events before the
// JetStream interrupt_turn command is published. This is the migration's
// load-bearing invariant — a future refactor that swapped the order would
// break the refresh-after-stop projection contract.
func TestInterruptPersistsRequestedEventBeforeCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-70", "70", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedInterruptRequest(t, "70", "turn-active_123")
	resp := httptest.NewRecorder()

	app.handleInterruptSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 1 {
		t.Fatalf("session-event upserts = %d, want 1 (turn.interrupt_requested)", len(es.upserts))
	}
	if got, _ := es.upserts[0]["type"].(string); got != "turn.interrupt_requested" {
		t.Fatalf("upsert[0].type = %q, want turn.interrupt_requested", got)
	}
	if got, _ := es.upserts[0]["turn_id"].(string); got != "turn-active_123" {
		t.Fatalf("upsert[0].turn_id = %q, want turn-active_123", got)
	}
	if got, _ := es.upserts[0]["actor"].(string); got != "system" {
		t.Fatalf("upsert[0].actor = %q, want system", got)
	}
	if got, _ := es.upserts[0]["source"].(string); got != "tank" {
		t.Fatalf("upsert[0].source = %q, want tank", got)
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	if bus.commands[0].Type != sessionbus.CommandInterrupt {
		t.Fatalf("command type = %q, want %q", bus.commands[0].Type, sessionbus.CommandInterrupt)
	}
}

func TestInterruptAlreadyTerminalRefreshesActivityWithoutStopRequest(t *testing.T) {
	bus := &recordingSessionBus{}
	refresher := &recordingActivityRefresher{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-70", "70", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	app.activityRefresher = refresher
	es := app.sessionEvents.(*recordingSessionEventStore)
	es.terminalTurns = map[string]map[string]any{
		"turn-done_123": {
			"type":    "turn.completed",
			"turn_id": "turn-done_123",
		},
	}
	req := authedInterruptRequest(t, "70", "turn-done_123")
	resp := httptest.NewRecorder()

	app.handleInterruptSessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "already_terminal" || body["target_terminal_type"] != "turn.completed" {
		t.Fatalf("response = %#v, want already_terminal turn.completed", body)
	}
	if len(es.upserts) != 0 {
		t.Fatalf("session-event upserts = %d, want 0 (late stop must not create turn.interrupt_requested)", len(es.upserts))
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0 (no runner work for already-terminal turn)", len(bus.commands))
	}
	if len(refresher.calls) != 1 {
		t.Fatalf("activity refresh calls = %d, want 1", len(refresher.calls))
	}
}

// TestInterruptPersistFailureBlocksCommand asserts the durable-first
// invariant under failure: if the persist of turn.interrupt_requested
// fails, NO interrupt_turn command is published. Otherwise the bus would
// carry a side-effect that the durable ledger doesn't record.
func TestInterruptPersistFailureBlocksCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-71", "71", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	app.sessionEvents = &recordingSessionEventStore{err: errors.New("postgres unavailable")}
	req := authedInterruptRequest(t, "71", "turn-active_123")
	resp := httptest.NewRecorder()

	app.handleInterruptSessionTurn(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0 (persist failed should block publish)", len(bus.commands))
	}
}

// TestInterruptPublishFailureLeavesRequestedEventAndCommandFailed: the
// durable record honestly reflects both the requested boundary and the
// publish failure. The reducer resolves this chain to error via
// turn.command_failed, and the chip from turn.interrupt_requested stays
// as transcript evidence that the user did press Stop.
func TestInterruptPublishFailureLeavesRequestedEventAndCommandFailed(t *testing.T) {
	bus := &recordingSessionBus{err: errors.New("nats down")}
	app := testTurnsApp(t, bus, sdkSessionPod("session-72", "72", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedInterruptRequest(t, "72", "turn-active_123")
	resp := httptest.NewRecorder()

	app.handleInterruptSessionTurn(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("session-event upserts = %d, want 2 (interrupt_requested + command_failed)", len(es.upserts))
	}
	wantTypes := []string{"turn.interrupt_requested", "turn.command_failed"}
	for i, want := range wantTypes {
		if got, _ := es.upserts[i]["type"].(string); got != want {
			t.Fatalf("upsert[%d].type = %q, want %q", i, got, want)
		}
	}
}

// TestInterruptIsIdempotentByEventID: deterministic event_id collapses a
// double-click POST to one durable row. The handler returns 202 on each
// call, but the reducer only sees one chip on replay.
func TestInterruptIsIdempotentByEventID(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-73", "73", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))

	for i := 0; i < 2; i++ {
		req := authedInterruptRequest(t, "73", "turn-active_123")
		resp := httptest.NewRecorder()
		app.handleInterruptSessionTurn(resp, req)
		if resp.Code != http.StatusAccepted {
			t.Fatalf("call %d: status = %d body = %s", i, resp.Code, resp.Body.String())
		}
	}

	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("handler upsert calls = %d, want 2 (the dedupe happens at the Postgres UNIQUE constraint, not the handler)", len(es.upserts))
	}
	if a, _ := es.upserts[0]["event_id"].(string); a == "" {
		t.Fatal("upsert[0].event_id missing")
	}
	a, _ := es.upserts[0]["event_id"].(string)
	b, _ := es.upserts[1]["event_id"].(string)
	if a != b {
		t.Fatalf("event_ids differ: %q vs %q; idempotency requires deterministic event_id keyed in turn_id", a, b)
	}
}

func TestStopBackgroundTaskPublishesControlCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedBackgroundStopRequest(t, "64", "proc:123", `{"turn_id":"turn-abc","timeline_id":"turn-abc:shell_task:proc-123","provider_item_id":"item-123","process_id":"proc:123"}`)
	resp := httptest.NewRecorder()

	app.handleStopBackgroundTask(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandStopBackgroundTask || got.Source != "background-stop" || got.Provider != "codex" || got.TargetTurnID != "turn-abc" || got.TargetTaskID != "proc:123" || got.TargetProcessID != "proc:123" {
		t.Fatalf("background stop record = %#v", got)
	}
	if got.TargetTimelineID != "turn-abc:shell_task:proc-123" || got.TargetProviderItemID != "item-123" {
		t.Fatalf("background stop target = %#v", got)
	}
	subject := sessionbus.SubjectForCommand(got)
	if subject != sessionbus.ControlSubject(got.SessionStorageKey, got.Provider) {
		t.Fatalf("background stop subject = %q, want control subject for storage=%q provider=%q",
			subject, got.SessionStorageKey, got.Provider)
	}
}

func TestStopBackgroundTaskRejectsClaudeSession(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	req := authedBackgroundStopRequest(t, "64", "task-123", `{"turn_id":"turn-abc"}`)
	resp := httptest.NewRecorder()

	app.handleStopBackgroundTask(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func testTurnsApp(t *testing.T, bus sessionCommandBus, pods ...*corev1.Pod) *appServer {
	t.Helper()
	clientObjects := make([]runtime.Object, 0, len(pods))
	for _, pod := range pods {
		clientObjects = append(clientObjects, pod)
	}
	k8s := fake.NewSimpleClientset(clientObjects...)
	ns := sessionmodel.SessionsNamespace
	return &appServer{
		k8s:           k8s,
		mgr:           sessions.NewManager(k8s, nil, ns, nil, nil, sessions.ManagerOptions{}),
		sessionBus:    bus,
		sessionEvents: &recordingSessionEventStore{},
		verifier:      auth.NewVerifier(testJWT(t)),
		namespace:     ns,
		sessionScope:  "default",
	}
}

func testTurnsAppWithRegistry(t *testing.T, bus sessionCommandBus, registry sessions.SessionRegistry, pods ...*corev1.Pod) *appServer {
	t.Helper()
	app := testTurnsApp(t, bus, pods...)
	app.mgr = sessions.NewManager(app.k8s, nil, app.namespace, registry, nil, sessions.ManagerOptions{})
	return app
}

func sdkSessionPod(name, sessionID, email, mode, runnerContainer string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: sessionmodel.SessionsNamespace,
			Labels: map[string]string{
				"tank-operator/session-id": sessionID,
				"tank-operator/owner":      sessionmodel.OwnerLabel(email),
				"tank-operator/mode":       mode,
			},
			Annotations: map[string]string{
				"tank-operator/owner-email": email,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sandbox-agent"}, {Name: runnerContainer}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func authedTurnRequest(t *testing.T, sessionID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/turns", strings.NewReader(body))
	req.SetPathValue("session_id", sessionID)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func authedInterruptRequest(t *testing.T, sessionID, turnID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/turns/"+turnID+"/interrupt", nil)
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("turn_id", turnID)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	return req
}

func authedBackgroundStopRequest(t *testing.T, sessionID, taskID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/background-tasks/"+taskID+"/stop", strings.NewReader(body))
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("task_id", taskID)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func authedAnswerRequest(t *testing.T, sessionID, turnID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/turns/"+turnID+"/answer", strings.NewReader(body))
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("turn_id", turnID)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	return req
}
