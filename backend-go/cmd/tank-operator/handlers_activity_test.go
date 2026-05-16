package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

func TestSummarizeSessionActivityFoldsLifecycleEventsToStatus(t *testing.T) {
	// Only lifecycle events reach the fold now — the store filters and
	// caps the input via LatestLifecycleEvents. user_message.created
	// and item.completed are correctly excluded; they're either user-
	// input or non-lifecycle content events.
	summary := summarizeSessionActivity(
		sessionActivitySummary{SessionID: "63", Status: "ready"},
		[]map[string]any{
			activityEvent("t1", "turn.started", "002", "runner", map[string]any{"turn_id": "turn-1"}),
			activityEvent("a1", "tool.approval_requested", "004", "tool", map[string]any{
				"turn_id":     "turn-1",
				"timeline_id": "ask-1",
			}),
		},
		2, // pre-computed unread count (from the store's COUNT query)
	)

	if summary.Status != "needs_input" || !summary.NeedsInput {
		t.Fatalf("status/needs = %q/%v, want needs_input/true", summary.Status, summary.NeedsInput)
	}
	if summary.ActiveTurnID == nil || *summary.ActiveTurnID != "turn-1" {
		t.Fatalf("active turn = %#v, want turn-1", summary.ActiveTurnID)
	}
	if summary.UnreadCount != 2 {
		t.Fatalf("unread = %d, want 2", summary.UnreadCount)
	}
	if summary.LastOrderKey == nil || *summary.LastOrderKey != "004" {
		t.Fatalf("last order = %#v, want 004", summary.LastOrderKey)
	}
}

func TestHandleSessionActivityReturnsOwnedSDKSessionSummaries(t *testing.T) {
	client := fake.NewSimpleClientset(activitySessionPod("63", "user@example.com"))
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t), "user@example.com"),
		mgr: sessions.NewManager(
			client,
			nil,
			sessionmodel.SessionsNamespace,
			nil,
			nil,
			sessions.ManagerOptions{},
		),
		sessionEvents: &activityEventStore{events: map[string][]map[string]any{
			"63": {
				activityEvent("started", "turn.started", "001", "runner", map[string]any{"turn_id": "turn-1"}),
			},
		}},
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/activity", nil)
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	app.handleSessionActivity(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body sessionActivityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want one", body.Sessions)
	}
	got := body.Sessions[0]
	if got.SessionID != "63" || got.Status != "streaming" {
		t.Fatalf("summary = %#v, want session 63 streaming", got)
	}
	if got.ActiveTurnID == nil || *got.ActiveTurnID != "turn-1" {
		t.Fatalf("active turn = %#v, want turn-1", got.ActiveTurnID)
	}
}

func TestHandleSessionActivityUsesPersistedReadState(t *testing.T) {
	client := fake.NewSimpleClientset(activitySessionPod("63", "user@example.com"))
	readStates := store.NewStubConversationReadStateStore()
	if _, err := readStates.Set(context.Background(), "user@example.com", "63", "001"); err != nil {
		t.Fatal(err)
	}
	app := &appServer{
		verifier: auth.NewVerifier(testJWT(t), "user@example.com"),
		mgr: sessions.NewManager(
			client,
			nil,
			sessionmodel.SessionsNamespace,
			nil,
			nil,
			sessions.ManagerOptions{},
		),
		sessionEvents: &activityEventStore{events: map[string][]map[string]any{
			"63": {
				activityEvent("m1", "item.completed", "001", "assistant", map[string]any{"timeline_id": "msg-1"}),
				activityEvent("m2", "item.completed", "002", "assistant", map[string]any{"timeline_id": "msg-2"}),
			},
		}},
		readStates: readStates,
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/activity", nil)
	request.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	response := httptest.NewRecorder()

	app.handleSessionActivity(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body sessionActivityResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].UnreadCount != 1 {
		t.Fatalf("sessions = %#v, want one unread update", body.Sessions)
	}
}

// activityEventStore is an in-memory SessionEventStore for the activity
// handler tests. It implements LatestLifecycleEvents and
// UnreadOutputCount by filtering the test fixture, mirroring the
// behavior the Postgres implementation gets from server-side queries.
type activityEventStore struct {
	events map[string][]map[string]any
}

func (s *activityEventStore) Upsert(_ context.Context, _ map[string]any) error {
	return nil
}

func (s *activityEventStore) ListBySession(_ context.Context, sessionID string, cursor store.SessionEventCursor, _ int) (store.SessionEventPage, error) {
	if cursor.AfterOrderKey != "" {
		return store.SessionEventPage{Events: []map[string]any{}}, nil
	}
	events := s.events[sessionID]
	next := ""
	if len(events) > 0 {
		next, _ = events[len(events)-1]["order_key"].(string)
	}
	return store.SessionEventPage{Events: events, NextOrderKey: next, HasMore: false}, nil
}

func (s *activityEventStore) HasOrderKey(_ context.Context, sessionID, orderKey string) (bool, error) {
	if orderKey == "" {
		return true, nil
	}
	for _, event := range s.events[sessionID] {
		if event["order_key"] == orderKey {
			return true, nil
		}
	}
	return false, nil
}

func (s *activityEventStore) FindTurnTerminal(_ context.Context, sessionID, turnID string) (map[string]any, error) {
	for _, event := range s.events[sessionID] {
		if event["turn_id"] == turnID {
			switch event["type"] {
			case "turn.completed", "turn.failed", "turn.interrupted":
				return event, nil
			}
		}
	}
	return nil, nil
}

func (s *activityEventStore) LatestLifecycleEvents(_ context.Context, sessionID string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	lifecycle := make([]map[string]any, 0)
	allowed := map[string]bool{}
	for _, t := range store.LifecycleEventTypes {
		allowed[t] = true
	}
	for _, event := range s.events[sessionID] {
		eventType, _ := event["type"].(string)
		if allowed[eventType] {
			lifecycle = append(lifecycle, event)
		}
	}
	// Return ASC; the test fixture is already ASC by order_key.
	if len(lifecycle) > limit {
		lifecycle = lifecycle[len(lifecycle)-limit:]
	}
	return lifecycle, nil
}

func (s *activityEventStore) UnreadOutputCount(_ context.Context, sessionID, afterOrderKey string) (int, error) {
	itemTypes := map[string]bool{}
	for _, t := range store.UnreadOutputItemTypes {
		itemTypes[t] = true
	}
	turnTypes := map[string]bool{}
	for _, t := range store.UnreadOutputTurnTypes {
		turnTypes[t] = true
	}
	itemIDs := map[string]struct{}{}
	turnIDs := map[string]struct{}{}
	for _, event := range s.events[sessionID] {
		eventType, _ := event["type"].(string)
		orderKey, _ := event["order_key"].(string)
		if afterOrderKey != "" && orderKey <= afterOrderKey {
			continue
		}
		actor, _ := event["actor"].(string)
		if actor == "user" {
			continue
		}
		if itemTypes[eventType] {
			if id, _ := event["timeline_id"].(string); id != "" {
				itemIDs[id] = struct{}{}
			}
		}
		if turnTypes[eventType] {
			if id, _ := event["turn_id"].(string); id != "" {
				turnIDs[id] = struct{}{}
			}
		}
	}
	return len(itemIDs) + len(turnIDs), nil
}

// Ensure ASC ordering of the test fixture so the in-memory store mirrors
// the Postgres ORDER BY order_key result when callers add events out of
// order_key sequence.
func sortActivityEvents(events []map[string]any) {
	sort.SliceStable(events, func(i, j int) bool {
		left, _ := events[i]["order_key"].(string)
		right, _ := events[j]["order_key"].(string)
		return left < right
	})
}

func activityEvent(eventID, eventType, orderKey, actor string, fields map[string]any) map[string]any {
	event := map[string]any{
		"event_id":   eventID,
		"type":       eventType,
		"order_key":  orderKey,
		"actor":      actor,
		"source":     "tank",
		"session_id": "63",
		"created_at": "2026-05-12T00:00:00Z",
		"visibility": "durable",
	}
	switch eventType {
	case "user_message.created":
		event["turn_id"] = "turn-1"
		event["timeline_id"] = "turn-1:user"
		event["client_nonce"] = "client-1"
		event["payload"] = map[string]any{"text": "hello", "display": map[string]any{"kind": "plain"}}
	case "turn.submitted":
		event["turn_id"] = "turn-1"
		event["client_nonce"] = "client-1"
		event["payload"] = map[string]any{"status": "submitted"}
	case "turn.started", "turn.completed", "turn.failed", "turn.interrupted":
		event["turn_id"] = "turn-1"
	case "item.started", "item.completed", "item.failed":
		event["turn_id"] = "turn-1"
		event["timeline_id"] = "item-1"
		event["payload"] = map[string]any{"kind": "message"}
	case "tool.approval_requested", "tool.approval_resolved":
		event["turn_id"] = "turn-1"
		event["timeline_id"] = "approval-1"
		event["payload"] = map[string]any{"kind": "needs_input"}
	}
	for key, value := range fields {
		event[key] = value
	}
	return event
}

func activitySessionPod(id, owner string) *corev1.Pod {
	created := metav1.NewTime(time.Date(2026, 5, 12, 0, 0, 1, 0, time.UTC))
	ready := metav1.NewTime(time.Date(2026, 5, 12, 0, 0, 3, 0, time.UTC))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "session-" + id,
			Namespace:         sessionmodel.SessionsNamespace,
			CreationTimestamp: created,
			Labels: map[string]string{
				"tank-operator/owner":      sessionmodel.OwnerLabel(owner),
				"tank-operator/session-id": id,
				"tank-operator/mode":       sessionmodel.CodexGUIMode,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "mcp-auth-proxy"},
				{Name: "claude", Ports: []corev1.ContainerPort{{Name: "sandbox-agent", ContainerPort: 2468}}},
				{Name: "codex-runner"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: ready,
			}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "mcp-auth-proxy", Ready: true},
				{Name: "claude", Ready: true},
				{Name: "codex-runner", Ready: true},
			},
		},
	}
}

var _ = sortActivityEvents // referenced in case future tests need pre-sort
