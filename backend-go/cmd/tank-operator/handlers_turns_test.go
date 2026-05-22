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

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/hermes"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type recordingSessionBus struct {
	commands []sessionbus.Command
	wakes    []string
	err      error
}

// recordingSessionEventStore captures every backend-owned Upsert so tests
// can verify the durable-before-202 guarantee that handlers_turns.go
// makes. Wraps the no-op stub for the other SessionEventStore methods.
type recordingSessionEventStore struct {
	store.StubSessionEventStore
	upserts []map[string]any
	err     error
}

type staticHermesTokenSource struct{}

func (staticHermesTokenSource) Token(context.Context) (string, error) {
	return "test-token", nil
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

// PublishSessionRowUpdate + SubscribeSessionRowUpdates are the row-
// update wire surface (Phase 3 of docs/session-list-redesign.md).
// Tests don't drive the sidebar SSE so the recorder no-ops both.
func (*recordingSessionBus) PublishSessionRowUpdate(context.Context, string, string, []byte) error {
	return nil
}

func (*recordingSessionBus) SubscribeSessionRowUpdates(context.Context, string, string) (<-chan []byte, func(), error) {
	return make(chan []byte), func() {}, nil
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
		"prompt":"launch prompt\n\nAttachments (use the Read tool to load):\n- /workspace/.attachments/123-notes.txt",
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
	if got := bus.commands[0].Prompt; got != "launch prompt\n\nAttachments (use the Read tool to load):\n- /workspace/.attachments/123-notes.txt" {
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

func TestCreateHermesSessionInitialTurnSubmitsAtCreate(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus)
	var createRun hermes.CreateRunRequest
	hermesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			if err := json.NewDecoder(r.Body).Decode(&createRun); err != nil {
				t.Errorf("decode create run: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, hermes.CreateRunResponse{RunID: "run-1", Status: "started"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer hermesServer.Close()
	app.hermesBridge = hermes.NewBridge(hermes.BridgeOptions{
		Client: hermes.NewClient(hermes.Options{
			BaseURL: hermesServer.URL,
			Tokens:  staticHermesTokenSource{},
			Timeout: time.Second,
		}),
		Store: app.sessionEvents.(*recordingSessionEventStore),
		Scope: "default",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{
		"mode":"hermes_gui",
		"initial_turn":{"client_nonce":"turn-hermes_launch","prompt":"hello hermes"}
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
	if created.Mode != sessionmodel.HermesGUIMode || created.Status != "Active" {
		t.Fatalf("created hermes session = %#v", created)
	}
	if createRun.Input != "hello hermes" || createRun.SessionID != created.ID {
		t.Fatalf("hermes create run request = %#v", createRun)
	}
	es := app.sessionEvents.(*recordingSessionEventStore)
	if len(es.upserts) != 2 {
		t.Fatalf("hermes upserts = %d, want 2", len(es.upserts))
	}
	for i, want := range []string{"user_message.created", "turn.submitted"} {
		if got, _ := es.upserts[i]["type"].(string); got != want {
			t.Fatalf("upsert[%d].type = %q, want %q", i, got, want)
		}
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
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex-skill","prompt":"$test","skill_name":"test"}`)
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
	req := authedTurnRequest(t, "64", `{"client_nonce":"turn-codex","prompt":"hello"}`)
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

func TestInputReplySessionTurnPublishesControlCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{
		"provider_item_id": "toolu_123",
		"timeline_id": "turn-active_123:item:toolu_123",
		"answers": {
			"Which auth method should we use?": ["  OAuth  "]
		},
		"annotations": {
			"Which auth method should we use?": {"notes": "matches existing IdP"}
		}
	}`
	req := authedInputReplyRequest(t, "63", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleInputReplySessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandInputReply || got.Source != "input-reply" || got.Provider != "claude" || got.TargetTurnID != "turn-active_123" || got.ClientNonce != "turn-active_123" {
		t.Fatalf("input reply routing fields = %#v", got)
	}
	if got.TargetProviderItemID != "toolu_123" || got.TargetTimelineID != "turn-active_123:item:toolu_123" {
		t.Fatalf("input reply target fields = %#v", got)
	}
	if len(got.Answers) != 1 || len(got.Answers["Which auth method should we use?"]) != 1 || got.Answers["Which auth method should we use?"][0] != "OAuth" {
		t.Fatalf("input reply answers = %#v, want {<question>: [\"OAuth\"]}", got.Answers)
	}
	if got.Annotations["Which auth method should we use?"].Notes != "matches existing IdP" {
		t.Fatalf("input reply annotations = %#v", got.Annotations)
	}
}

func TestInputReplySessionTurnPublishesCodexControlCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{"q":["OAuth"]}}`
	req := authedInputReplyRequest(t, "64", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleInputReplySessionTurn(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(bus.commands))
	}
	got := bus.commands[0]
	if got.Type != sessionbus.CommandInputReply || got.Provider != "codex" || got.TargetTurnID != "turn-active_123" || got.ClientNonce != "turn-active_123" {
		t.Fatalf("input reply routing fields = %#v", got)
	}
	if got.TargetProviderItemID != "toolu_123" || got.TargetTimelineID != "turn-active_123:item:toolu_123" {
		t.Fatalf("input reply target fields = %#v", got)
	}
}

func TestInputReplySessionTurnRejectsCodexExecFallback(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexExecGUIMode, "codex-runner"))
	body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{"q":["OAuth"]}}`
	req := authedInputReplyRequest(t, "64", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleInputReplySessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestInputReplySessionTurnRejectsMissingTarget(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{"provider_item_id":"","timeline_id":"turn-active_123:item:toolu_123","answers":{"q":["OAuth"]}}`
	req := authedInputReplyRequest(t, "63", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleInputReplySessionTurn(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if len(bus.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(bus.commands))
	}
}

func TestInputReplySessionTurnRejectsEmptyAnswers(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","answers":{}}`
	req := authedInputReplyRequest(t, "63", "turn-active_123", body)
	resp := httptest.NewRecorder()

	app.handleInputReplySessionTurn(resp, req)

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

func authedInputReplyRequest(t *testing.T, sessionID, turnID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/turns/"+turnID+"/input-reply", strings.NewReader(body))
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("turn_id", turnID)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	req.Header.Set("Content-Type", "application/json")
	return req
}
