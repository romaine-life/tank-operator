package sessionbus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
)

// These tests run the persister against an in-process JetStream server so
// the contracts that live in NATS semantics â€” delivery, ack floors,
// max-deliveries repair â€” are tested for real, not mocked. The 2026-06-11
// incident (tank-operator#1051) was precisely a failure of untested
// consumer-side semantics; this harness is the regression bed for them.

func startJetStreamServer(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{
		// Loopback-only: binding 0.0.0.0 makes Windows Firewall prompt
		// for every freshly built test binary.
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("embedded NATS server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func connectTestBus(t *testing.T, url string) *Bus {
	t.Helper()
	bus, err := Connect(context.Background(), Config{URL: url, Scope: "default", Replicas: 1})
	if err != nil {
		t.Fatalf("connect test bus: %v", err)
	}
	t.Cleanup(bus.Close)
	return bus
}

// dedupingStore is a concurrency-safe EventStore double with real
// inserted-vs-duplicate semantics keyed by order_key â€” the shape the
// dispatcher's dedupe contract depends on.
type dedupingStore struct {
	mu     sync.Mutex
	seen   map[string]map[string]any
	keyLog []string
}

func newDedupingStore() *dedupingStore {
	return &dedupingStore{seen: map[string]map[string]any{}}
}

func (s *dedupingStore) Upsert(_ context.Context, event map[string]any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, _ := event["order_key"].(string)
	_, existed := s.seen[key]
	s.seen[key] = event
	if !existed {
		s.keyLog = append(s.keyLog, key)
	}
	return !existed, nil
}

func (s *dedupingStore) has(orderKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[orderKey]
	return ok
}

func (s *dedupingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

type safeRefresher struct {
	mu      sync.Mutex
	batches [][]map[string]any
}

func (r *safeRefresher) RefreshEventBatch(_ context.Context, events []map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch := make([]map[string]any, len(events))
	copy(batch, events)
	r.batches = append(r.batches, batch)
	return nil
}

func busTestEvent(sessionID string, n int) map[string]any {
	return map[string]any{
		"event_id":   fmt.Sprintf("evt-%s-%03d", sessionID, n),
		"session_id": sessionID,
		"actor":      "runner",
		"source":     "codex",
		"type":       "item.completed",
		"created_at": "2026-06-11T00:00:00.000Z",
		"order_key":  fmt.Sprintf("178119%07d-%08d-evt-%s-%03d", n, n, sessionID, n),
		"visibility": "durable",
		"turn_id":    "turn-1",
		"payload":    map[string]any{"kind": "agent_message", "text": "ok"},
	}
}

func publishBusTestEvent(t *testing.T, b *Bus, event map[string]any) uint64 {
	t.Helper()
	sessionID, _ := event["session_id"].(string)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	ack, err := b.js.Publish(context.Background(), SessionEventSubject(sessionID), raw)
	if err != nil {
		t.Fatalf("publish event: %v", err)
	}
	return ack.Sequence
}

// TestRunEventPersisterPersistsPublishedEvents runs the full consume â†’
// dispatch â†’ upsert â†’ refresh loop against real JetStream delivery across
// multiple sessions.
func TestRunEventPersisterPersistsPublishedEvents(t *testing.T) {
	url := startJetStreamServer(t)
	bus := connectTestBus(t, url)
	store := newDedupingStore()
	refresher := &safeRefresher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- bus.RunEventPersister(ctx, store, refresher, &recordingMetrics{})
	}()

	var wantKeys []string
	for n := 1; n <= 3; n++ {
		event := busTestEvent("63", n)
		wantKeys = append(wantKeys, event["order_key"].(string))
		publishBusTestEvent(t, bus, event)
	}
	other := busTestEvent("64", 1)
	wantKeys = append(wantKeys, other["order_key"].(string))
	publishBusTestEvent(t, bus, other)

	deadline := time.Now().Add(10 * time.Second)
	for store.count() < len(wantKeys) {
		if time.Now().After(deadline) {
			t.Fatalf("persisted %d/%d events before deadline", store.count(), len(wantKeys))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, key := range wantKeys {
		if !store.has(key) {
			t.Fatalf("event %q not persisted", key)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("persister did not stop after context cancel")
	}
}

// TestReconcilePersisterGapsRepairsSkippedEvent pins the startup-repair
// contract: an event inside [ack floor + 1, last delivered] that was never
// persisted (the MaxDeliver-exhaustion shape â€” no future delivery will ever
// retry it) is found and persisted by the reconciler, while
// already-persisted neighbors dedupe to no-ops.
func TestReconcilePersisterGapsRepairsSkippedEvent(t *testing.T) {
	url := startJetStreamServer(t)
	bus := connectTestBus(t, url)
	ctx := context.Background()

	events := make([]map[string]any, 0, 5)
	for n := 1; n <= 5; n++ {
		event := busTestEvent("63", n)
		events = append(events, event)
		publishBusTestEvent(t, bus, event)
	}

	consumer, err := bus.js.CreateOrUpdateConsumer(ctx, bus.stream, jetstream.ConsumerConfig{
		Name:          EventPersisterConsumerName("default"),
		Durable:       EventPersisterConsumerName("default"),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       60 * time.Second,
		MaxAckPending: 200,
		FilterSubject: EventSubjectFilter("default"),
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	batch, err := consumer.Fetch(5, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	i := 0
	for msg := range batch.Messages() {
		i++
		// Ack everything except message 3 â€” the simulated exhausted
		// hole. Floor stops at 2 (contiguous acks), delivered is 5, so
		// the reconciler window is [3, 5].
		if i == 3 {
			continue
		}
		if err := msg.DoubleAck(ctx); err != nil {
			t.Fatalf("ack msg %d: %v", i, err)
		}
	}
	if i != 5 {
		t.Fatalf("fetched %d messages, want 5", i)
	}

	store := newDedupingStore()
	for n, event := range events {
		if n == 2 {
			continue // event 3 (index 2) was never persisted â€” the hole
		}
		if _, err := store.Upsert(ctx, event); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	metrics := &recordingMetrics{}
	d := newPersistDispatcher(bus, store, &safeRefresher{}, metrics, persistDispatcherConfig{})

	if err := bus.reconcilePersisterGaps(ctx, consumer, d); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	holeKey := events[2]["order_key"].(string)
	if !store.has(holeKey) {
		t.Fatalf("reconciler did not persist the hole event %q", holeKey)
	}
	if got := metrics.reconcilerRepairedCount(); got != 1 {
		t.Fatalf("reconciler repaired = %d, want exactly 1 (neighbors dedupe to no-ops)", got)
	}
}

// TestHandleMaxDeliveriesAdvisoryRepairsFromStream pins the exhaustion
// terminal: an advisory naming a stream sequence leads to the event being
// fetched from the stream and persisted out of band, counted as repaired.
// An unparseable advisory counts as failed â€” never silent.
func TestHandleMaxDeliveriesAdvisoryRepairsFromStream(t *testing.T) {
	url := startJetStreamServer(t)
	bus := connectTestBus(t, url)
	ctx := context.Background()

	event := busTestEvent("63", 1)
	seq := publishBusTestEvent(t, bus, event)

	store := newDedupingStore()
	metrics := &recordingMetrics{}
	d := newPersistDispatcher(bus, store, &safeRefresher{}, metrics, persistDispatcherConfig{})

	advisory, _ := json.Marshal(maxDeliveriesAdvisory{StreamSeq: seq, Deliveries: 20})
	bus.handleMaxDeliveriesAdvisory(ctx, d, advisory)

	if !store.has(event["order_key"].(string)) {
		t.Fatalf("advisory repair did not persist the exhausted event")
	}
	if got := metrics.exhaustedCount("repaired"); got != 1 {
		t.Fatalf("exhausted{repaired} = %d, want 1", got)
	}

	bus.handleMaxDeliveriesAdvisory(ctx, d, []byte("not-json"))
	if got := metrics.exhaustedCount("failed"); got != 1 {
		t.Fatalf("exhausted{failed} = %d, want 1 for unparseable advisory", got)
	}
}
