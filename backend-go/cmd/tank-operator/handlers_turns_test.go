package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
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

func (r *recordingSessionEventStore) Upsert(_ context.Context, event map[string]any) error {
	if r.err != nil {
		return r.err
	}
	r.upserts = append(r.upserts, event)
	return nil
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

// PublishSessionListEvent + SubscribeSessionListEvents are the typed-event
// successors of the prior opaque wake publish/subscribe pair retired in
// tank-operator#83. Tests don't drive the sidebar SSE so the recorder
// no-ops both; replace these stubs with capturing variants in dedicated
// session-list tests when those land.
func (*recordingSessionBus) PublishSessionListEvent(context.Context, string, []byte) error {
	return nil
}

func (*recordingSessionBus) SubscribeSessionListEvents(context.Context, string) (<-chan []byte, func(), error) {
	return make(chan []byte), func() {}, nil
}

func TestEnqueueSessionTurnPublishesSDKCommand(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-63", "63", "user@example.com", sessionmodel.ClaudeGUIMode, "agent-runner"))
	body := `{"client_nonce":"turn-abc_123","prompt":"  /test\n\nhello sdk  ","model":"claude-sonnet-4-6","permission_mode":"bypassPermissions","skill_name":"test","follow_up":true}`
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
	req := authedInputReplyRequest(t, "63", "turn-active_123", `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","text":"  Continue  "}`)
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
	if got.TargetProviderItemID != "toolu_123" || got.TargetTimelineID != "turn-active_123:item:toolu_123" || got.InputReply != "Continue" || got.Prompt != "Continue" {
		t.Fatalf("input reply payload fields = %#v", got)
	}
}

func TestInputReplySessionTurnRejectsCodex(t *testing.T) {
	bus := &recordingSessionBus{}
	app := testTurnsApp(t, bus, sdkSessionPod("session-64", "64", "user@example.com", sessionmodel.CodexGUIMode, "codex-runner"))
	req := authedInputReplyRequest(t, "64", "turn-active_123", `{"provider_item_id":"toolu_123","timeline_id":"turn-active_123:item:toolu_123","text":"Continue"}`)
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
	req := authedInputReplyRequest(t, "63", "turn-active_123", `{"provider_item_id":"","timeline_id":"turn-active_123:item:toolu_123","text":"Continue"}`)
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
		mgr:           sessions.NewManager(k8s, nil, ns, nil, nil, nil, sessions.ManagerOptions{}),
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
