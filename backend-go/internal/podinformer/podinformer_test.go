package podinformer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// fakeStore is the test stand-in for lifecycleevents.Store. Records
// every Append in order; honors the unique (scope, session_id, event_id)
// contract by treating identical event_ids as "already exists" no-ops.
type fakeStore struct {
	mu       sync.Mutex
	events   []lifecycleevents.Event
	seenKeys map[string]struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{seenKeys: map[string]struct{}{}}
}

func (s *fakeStore) Append(_ context.Context, event lifecycleevents.Event) (lifecycleevents.Event, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := event.SessionScope + "|" + event.SessionID + "|" + event.EventID
	if _, ok := s.seenKeys[key]; ok {
		return event, true, nil
	}
	s.seenKeys[key] = struct{}{}
	event.OrderKey = nextOrderKey(len(s.events) + 1)
	if event.OccurredAt == "" {
		event.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	s.events = append(s.events, event)
	return event, false, nil
}

func (s *fakeStore) ListByOwner(_ context.Context, _, _ string, _ lifecycleevents.Cursor, _ int) (lifecycleevents.Page, error) {
	return lifecycleevents.Page{}, nil
}

func (s *fakeStore) HasOrderKey(_ context.Context, _, _, _ string) (bool, error) { return true, nil }

func (s *fakeStore) LatestActivity(_ context.Context, _, _ string) (*lifecycleevents.ActivitySummary, error) {
	return nil, nil
}

func (s *fakeStore) LatestPodStatus(_ context.Context, _, _ string) (*lifecycleevents.PodStatusSummary, error) {
	return nil, nil
}

func nextOrderKey(i int) string {
	return time.Now().Format("150405.000") + "-" + string(rune('a'+i%26))
}

// fakePublisher records each (owner, scope, payload) it sees. Used to
// assert that the informer only publishes on a fresh append (the
// "already exists" path must not re-publish, or stale rows would render
// on connected clients) and that the scope passed to the publisher
// matches the row's session_scope (the wire shape is keyed on (email,
// scope), so a stale scope here breaks delivery).
type fakePublisher struct {
	mu       sync.Mutex
	payloads []publishedEvent
}

type publishedEvent struct {
	owner string
	scope string
	raw   []byte
}

func (p *fakePublisher) PublishSessionListEvent(_ context.Context, owner, scope string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	p.payloads = append(p.payloads, publishedEvent{owner: owner, scope: scope, raw: cp})
	return nil
}

func TestHandleUpsertEmitsScheduledOnFirstSight(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	tracker := newTransitionTracker(store, pub, nil, "default")

	pod := newSessionPod("21", "u@example.com", corev1.PodPending, false)
	tracker.handleUpsert(context.Background(), nil, pod)

	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(store.events), store.events)
	}
	if got := store.events[0].Type; got != lifecycleevents.EventTypePodScheduled {
		t.Fatalf("first event type = %q, want %q", got, lifecycleevents.EventTypePodScheduled)
	}
}

func TestHandleUpsertEmitsReadyOnTransition(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	tracker := newTransitionTracker(store, pub, nil, "default")

	pending := newSessionPod("21", "u@example.com", corev1.PodPending, false)
	tracker.handleUpsert(context.Background(), nil, pending)

	ready := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	ready.UID = pending.UID
	tracker.handleUpsert(context.Background(), pending, ready)

	if got := lastEventType(store); got != lifecycleevents.EventTypePodReady {
		t.Fatalf("transition event type = %q, want %q", got, lifecycleevents.EventTypePodReady)
	}
}

func TestHandleUpsertEmitsFailedOnEviction(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	tracker := newTransitionTracker(store, pub, nil, "default")

	running := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	tracker.handleUpsert(context.Background(), nil, running)

	evicted := running.DeepCopy()
	evicted.Status.Phase = corev1.PodFailed
	evicted.Status.Reason = "Evicted"
	evicted.Status.Message = "The node was low on resource: memory."
	evicted.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "agent-runner",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Message: "OOMKilled"},
		},
	}}
	tracker.handleUpsert(context.Background(), running, evicted)

	last := store.events[len(store.events)-1]
	if last.Type != lifecycleevents.EventTypePodFailed {
		t.Fatalf("eviction event type = %q, want %q", last.Type, lifecycleevents.EventTypePodFailed)
	}
	if last.Payload["status"] != "Failed" {
		t.Fatalf("eviction payload.status = %v, want Failed", last.Payload["status"])
	}
	if last.Payload["reason"] != "Evicted" {
		t.Fatalf("eviction payload.reason = %v, want Evicted", last.Payload["reason"])
	}
	if last.Payload["exit_code"] != int32(137) {
		t.Fatalf("eviction payload.exit_code = %v, want 137", last.Payload["exit_code"])
	}
	if last.Payload["container"] != "agent-runner" {
		t.Fatalf("eviction payload.container = %v, want agent-runner", last.Payload["container"])
	}
}

func TestHandleDeleteEmitsDeletedOnce(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	tracker := newTransitionTracker(store, pub, nil, "default")

	pod := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	tracker.handleUpsert(context.Background(), nil, pod)

	tracker.handleDelete(context.Background(), pod)
	tracker.handleDelete(context.Background(), pod)

	deletedCount := 0
	for _, e := range store.events {
		if e.Type == lifecycleevents.EventTypeDeleted {
			deletedCount++
		}
	}
	if deletedCount != 1 {
		t.Fatalf("deleted events written = %d, want 1 (event_id uniqueness should collapse the second handleDelete to a no-op)", deletedCount)
	}
	deletedPublishes := 0
	for _, p := range pub.payloads {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(p.raw, &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Type == lifecycleevents.EventTypeDeleted {
			deletedPublishes++
		}
	}
	if deletedPublishes != 1 {
		t.Fatalf("session.deleted publishes = %d, want 1 (re-emit must not republish)", deletedPublishes)
	}
}

func TestRestartResyncDoesNotRepublish(t *testing.T) {
	// Simulate two informer "first-sight" passes against the same pod.
	// Real-world cause: orchestrator replica restart re-reads the
	// informer cache from scratch and reinvokes AddFunc. The unique
	// constraint dedupes the row write; the publisher must respect the
	// dedupe and skip the NATS publish.
	store := newFakeStore()
	pub := &fakePublisher{}
	pod := newSessionPod("21", "u@example.com", corev1.PodRunning, true)

	tracker1 := newTransitionTracker(store, pub, nil, "default")
	tracker1.handleUpsert(context.Background(), nil, pod)
	publishesAfterFirst := len(pub.payloads)

	tracker2 := newTransitionTracker(store, pub, nil, "default")
	tracker2.handleUpsert(context.Background(), nil, pod)

	if got := len(pub.payloads); got != publishesAfterFirst {
		t.Fatalf("restart re-publish count = %d, want %d (informer resync must not re-publish)",
			got-publishesAfterFirst, 0)
	}
}

func TestIgnoresUnrelatedPods(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	tracker := newTransitionTracker(store, pub, nil, "default")

	unrelated := newSessionPod("21", "u@example.com", corev1.PodRunning, true)
	unrelated.Labels = map[string]string{} // strip session/managed labels
	tracker.handleUpsert(context.Background(), nil, unrelated)
	if len(store.events) != 0 {
		t.Fatalf("unmanaged pod must not produce ledger rows")
	}
}

// --- helpers --------------------------------------------------------------

func lastEventType(s *fakeStore) string {
	if len(s.events) == 0 {
		return ""
	}
	return s.events[len(s.events)-1].Type
}

func newSessionPod(id, owner string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	created := metav1.NewTime(time.Date(2026, 5, 16, 0, 0, 1, 0, time.UTC))
	readyTime := metav1.NewTime(time.Date(2026, 5, 16, 0, 0, 3, 0, time.UTC))
	statuses := []corev1.ContainerStatus{
		{Name: "claude", Ready: ready},
		{Name: "agent-runner", Ready: ready},
		{Name: "mcp-auth-proxy", Ready: ready},
	}
	conditions := []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             condStatus(ready),
		LastTransitionTime: readyTime,
	}}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "session-" + id,
			Namespace:         sessionmodel.SessionsNamespace,
			UID:               types.UID("uid-" + id),
			CreationTimestamp: created,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tank-operator",
				"tank-operator/owner":          sessionmodel.OwnerLabel(owner),
				"tank-operator/session-id":     id,
			},
			Annotations: map[string]string{
				"tank-operator/owner-email": owner,
			},
		},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
			Conditions:        conditions,
		},
	}
}

func condStatus(ready bool) corev1.ConditionStatus {
	if ready {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}
