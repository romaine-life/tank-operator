package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

type bridgeTestTokenSource struct{}

func (bridgeTestTokenSource) Token(context.Context) (string, error) {
	return "test-token", nil
}

type bridgeTestEventStore struct {
	mu     sync.Mutex
	events []map[string]any
}

func (s *bridgeTestEventStore) Upsert(_ context.Context, event map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *bridgeTestEventStore) FindTurnTerminal(_ context.Context, _, turnID string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		event := s.events[i]
		if got, _ := event["turn_id"].(string); got != turnID {
			continue
		}
		switch event["type"] {
		case string(conversation.EventTurnCompleted),
			string(conversation.EventTurnFailed),
			string(conversation.EventTurnCommandFailed),
			string(conversation.EventTurnInterrupted):
			return event, nil
		}
	}
	return nil, nil
}

func (s *bridgeTestEventStore) snapshot() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.events...)
}

type bridgeTestActiveRunStore struct {
	mu      sync.Mutex
	run     sessionmodel.HermesActiveRun
	hasRun  bool
	cleared int
}

func (s *bridgeTestActiveRunStore) SetHermesActiveRun(_ context.Context, owner, sessionID string, run sessionmodel.HermesActiveRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.Owner = owner
	run.SessionID = sessionID
	s.run = run
	s.hasRun = true
	return nil
}

func (s *bridgeTestActiveRunStore) ClearHermesActiveRun(_ context.Context, _, _, turnID, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasRun && s.run.TurnID == turnID && s.run.RunID == runID {
		s.hasRun = false
		s.cleared++
	}
	return nil
}

func (s *bridgeTestActiveRunStore) GetHermesActiveRun(_ context.Context, _, _, turnID string) (sessionmodel.HermesActiveRun, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasRun || s.run.TurnID != turnID {
		return sessionmodel.HermesActiveRun{}, false, nil
	}
	return s.run, true, nil
}

func (s *bridgeTestActiveRunStore) ListHermesActiveRuns(context.Context) ([]sessionmodel.HermesActiveRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasRun {
		return nil, nil
	}
	return []sessionmodel.HermesActiveRun{s.run}, nil
}

func (s *bridgeTestActiveRunStore) snapshot() (sessionmodel.HermesActiveRun, bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, s.hasRun, s.cleared
}

func TestBridgeSubmitPersistsAndClearsActiveRun(t *testing.T) {
	var created CreateRunRequest
	hermesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Fatalf("decode create run: %v", err)
			}
			writeBridgeJSON(t, w, CreateRunResponse{RunID: "run-1", Status: "started"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: run.completed\ndata: {\"output\":\"done\"}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer hermesServer.Close()

	events := &bridgeTestEventStore{}
	active := &bridgeTestActiveRunStore{}
	bridge := NewBridge(BridgeOptions{
		Client:     NewClient(Options{BaseURL: hermesServer.URL, Tokens: bridgeTestTokenSource{}}),
		Store:      events,
		ActiveRuns: active,
	})

	result, err := bridge.SubmitTurn(context.Background(), SubmitArgs{
		SessionID:   "session-1",
		Email:       "user@example.com",
		ClientNonce: "nonce-1",
		Text:        "hello",
	})
	if err != nil {
		t.Fatalf("SubmitTurn returned error: %v", err)
	}
	if result.RunID != "run-1" || created.Input != "hello" || created.SessionID != "session-1" {
		t.Fatalf("create/result mismatch: created=%#v result=%#v", created, result)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := bridge.WaitForTurn(waitCtx, "session-1", result.TurnID); err != nil {
		t.Fatalf("WaitForTurn returned error: %v", err)
	}

	run, hasRun, cleared := active.snapshot()
	if hasRun || cleared != 1 {
		t.Fatalf("active run store = run:%#v has:%v cleared:%d, want cleared once and absent", run, hasRun, cleared)
	}
	if run.RunID != "run-1" || run.TurnID != result.TurnID || run.ClientNonce != "nonce-1" {
		t.Fatalf("persisted active run = %#v", run)
	}
	assertBridgeEventType(t, events.snapshot(), string(conversation.EventTurnCompleted))
	assertBridgeMessageText(t, events.snapshot(), "done")
}

func TestBridgeRecoverActiveRunTranslatesTerminalStatus(t *testing.T) {
	hermesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-recover" {
			writeBridgeJSON(t, w, RunStatus{RunID: "run-recover", Status: "completed", SessionID: "session-1", Output: "recovered"})
			return
		}
		http.NotFound(w, r)
	}))
	defer hermesServer.Close()

	turnID := conversation.TurnIDForClientNonce("nonce-recover")
	events := &bridgeTestEventStore{}
	active := &bridgeTestActiveRunStore{
		run: sessionmodel.HermesActiveRun{
			Owner:       "user@example.com",
			SessionID:   "session-1",
			TurnID:      turnID,
			ClientNonce: "nonce-recover",
			RunID:       "run-recover",
			StartedAt:   time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano),
		},
		hasRun: true,
	}
	bridge := NewBridge(BridgeOptions{
		Client:     NewClient(Options{BaseURL: hermesServer.URL, Tokens: bridgeTestTokenSource{}}),
		Store:      events,
		ActiveRuns: active,
	})

	if err := bridge.RecoverActiveRuns(context.Background()); err != nil {
		t.Fatalf("RecoverActiveRuns returned error: %v", err)
	}
	_, hasRun, cleared := active.snapshot()
	if hasRun || cleared != 1 {
		t.Fatalf("active run after recover has=%v cleared=%d, want cleared terminal", hasRun, cleared)
	}
	assertBridgeEventType(t, events.snapshot(), string(conversation.EventTurnCompleted))
	assertBridgeMessageText(t, events.snapshot(), "recovered")
}

func TestBridgeStopFailureEmitsCommandFailed(t *testing.T) {
	hermesServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			writeBridgeJSON(t, w, CreateRunResponse{RunID: "run-stop", Status: "started"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run-stop/events":
			w.Header().Set("Content-Type", "text/event-stream")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run-stop/stop":
			http.Error(w, "stop failed", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer hermesServer.Close()

	events := &bridgeTestEventStore{}
	active := &bridgeTestActiveRunStore{}
	bridge := NewBridge(BridgeOptions{
		Client:     NewClient(Options{BaseURL: hermesServer.URL, Tokens: bridgeTestTokenSource{}}),
		Store:      events,
		ActiveRuns: active,
	})
	result, err := bridge.SubmitTurn(context.Background(), SubmitArgs{
		SessionID:   "session-1",
		Email:       "user@example.com",
		ClientNonce: "nonce-stop",
		Text:        "stop me",
	})
	if err != nil {
		t.Fatalf("SubmitTurn returned error: %v", err)
	}
	if err := bridge.StopTurn(context.Background(), "session-1", "user@example.com", result.TurnID, "nonce-stop"); err == nil {
		t.Fatal("StopTurn returned nil, want stop error")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := bridge.WaitForTurn(waitCtx, "session-1", result.TurnID); err != nil {
		t.Fatalf("WaitForTurn returned error: %v", err)
	}

	var reasons []string
	for _, event := range events.snapshot() {
		if event["type"] != string(conversation.EventTurnCommandFailed) {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		reason, _ := payload["reason"].(string)
		reasons = append(reasons, reason)
	}
	if strings.Join(reasons, ",") != "hermes_stop_failed" {
		t.Fatalf("command_failed reasons = %v, want [hermes_stop_failed]", reasons)
	}
}

func writeBridgeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func assertBridgeEventType(t *testing.T, events []map[string]any, eventType string) {
	t.Helper()
	for _, event := range events {
		if event["type"] == eventType {
			return
		}
	}
	t.Fatalf("missing event type %s in %#v", eventType, events)
}

func assertBridgeMessageText(t *testing.T, events []map[string]any, text string) {
	t.Helper()
	for _, event := range events {
		payload, _ := event["payload"].(map[string]any)
		if payload == nil {
			continue
		}
		if got, _ := payload["text"].(string); got == text {
			return
		}
	}
	t.Fatalf("missing assistant text %q in %#v", text, events)
}
