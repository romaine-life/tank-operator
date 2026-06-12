package sessionbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionactivity"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// TranscriptRefresher is the projection hook the persister calls after a
// batch of events for one session is durably upserted. The implementation
// (cmd/tank-operator transcriptRowsMaterializer) coalesces the batch: N
// pending events for the same turn produce one re-projection, and a batch
// containing a session-scope trigger (turn.input_answered, a background-wake
// turn boundary) produces one session re-projection covering everything.
// Refresh errors NAK the whole batch — the event rows are already durable
// and idempotent, so redelivery retries the projection, never the ledger.
type TranscriptRefresher interface {
	RefreshEventBatch(ctx context.Context, events []map[string]any) error
}

type noopTranscriptRefresher struct{}

func (noopTranscriptRefresher) RefreshEventBatch(context.Context, []map[string]any) error {
	return nil
}

// persistableMessage is the narrow surface of jetstream.Msg used by the
// persist dispatcher. Defined here so unit tests can supply a stub without
// spinning up an in-process NATS server.
type persistableMessage interface {
	Subject() string
	Data() []byte
	Ack() error
	NakWithDelay(delay time.Duration) error
	TermWithReason(reason string) error
	InProgress() error
	Metadata() (*jetstream.MsgMetadata, error)
}

// inflightSessionEvent carries one delivered message plus its decoded event
// through the per-session queue. The event is decoded once at enqueue so
// routing, persistence, and refresh never re-unmarshal.
type inflightSessionEvent struct {
	msg        persistableMessage
	event      map[string]any
	storageKey string
	enqueuedAt time.Time
	inserted   bool
}

// persistDispatcherConfig bounds the dispatcher. Worker concurrency caps
// simultaneous Postgres batch transactions; batch size caps how many events
// one refresh coalesces. Total in-flight messages are already bounded by the
// consumer's MaxAckPending — the queues here never exceed it.
type persistDispatcherConfig struct {
	Workers  int
	MaxBatch int
}

func (c persistDispatcherConfig) withDefaults() persistDispatcherConfig {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = 64
	}
	return c
}

// persistDispatcher routes consumed bus messages to per-session serial
// queues processed by a bounded worker pool. Per-session ordering is
// preserved (the transcript projection folds events in order); sessions
// progress independently, so one flood session can saturate at most one
// worker instead of starving every session's durable delivery — the
// 2026-06-11 incident class (romaine-life/tank-operator#1051).
type persistDispatcher struct {
	bus       *Bus
	store     EventStore
	refresher TranscriptRefresher
	metrics   PersisterMetrics
	cfg       persistDispatcherConfig

	slots chan struct{}

	mu      sync.Mutex
	queues  map[string][]*inflightSessionEvent
	active  map[string]bool
	pending map[persistableMessage]struct{}
}

func newPersistDispatcher(b *Bus, store EventStore, refresher TranscriptRefresher, metrics PersisterMetrics, cfg persistDispatcherConfig) *persistDispatcher {
	cfg = cfg.withDefaults()
	return &persistDispatcher{
		bus:       b,
		store:     store,
		refresher: refresher,
		metrics:   metrics,
		cfg:       cfg,
		slots:     make(chan struct{}, cfg.Workers),
		queues:    make(map[string][]*inflightSessionEvent),
		active:    make(map[string]bool),
		pending:   make(map[persistableMessage]struct{}),
	}
}

// enqueue decodes one delivered message and routes it to its session queue,
// spawning a worker for the session if none is running. Invalid JSON is a
// producer bug that can never succeed on retry: it terminates immediately
// with the schema-rejected counter, same as the previous per-message path.
func (d *persistDispatcher) enqueue(ctx context.Context, msg persistableMessage) {
	var event map[string]any
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		d.metrics.RecordSchemaRejected()
		slog.Warn("session bus event terminated: invalid json",
			"subject", msg.Subject(),
			"error", err,
		)
		_ = msg.TermWithReason("schema rejected: invalid json")
		return
	}
	if meta, err := msg.Metadata(); err == nil && meta != nil && meta.NumDelivered > 1 {
		d.metrics.RecordRedelivered()
	}
	storageKey, _ := event["tank_session_id"].(string)
	if storageKey == "" {
		sessionID, _ := event["session_id"].(string)
		storageKey = sessionmodel.SessionStorageKey(d.bus.scope, sessionID)
	}
	in := &inflightSessionEvent{
		msg:        msg,
		event:      event,
		storageKey: storageKey,
		enqueuedAt: time.Now(),
	}
	d.mu.Lock()
	d.queues[storageKey] = append(d.queues[storageKey], in)
	d.pending[msg] = struct{}{}
	startWorker := !d.active[storageKey]
	if startWorker {
		d.active[storageKey] = true
	}
	d.mu.Unlock()
	if startWorker {
		go d.runWorker(ctx, storageKey)
	}
}

// settle removes messages from the heartbeat set once they are acked,
// NAKed, or terminated.
func (d *persistDispatcher) settle(msgs ...persistableMessage) {
	d.mu.Lock()
	for _, m := range msgs {
		delete(d.pending, m)
	}
	d.mu.Unlock()
}

func (d *persistDispatcher) runWorker(ctx context.Context, storageKey string) {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		d.mu.Lock()
		d.active[storageKey] = false
		d.mu.Unlock()
		return
	}
	defer func() { <-d.slots }()
	for {
		d.mu.Lock()
		queue := d.queues[storageKey]
		if len(queue) == 0 {
			d.active[storageKey] = false
			delete(d.queues, storageKey)
			d.mu.Unlock()
			return
		}
		n := len(queue)
		if n > d.cfg.MaxBatch {
			n = d.cfg.MaxBatch
		}
		batch := queue[:n]
		d.queues[storageKey] = queue[n:]
		d.mu.Unlock()
		d.processBatch(ctx, storageKey, batch)
		if ctx.Err() != nil {
			d.mu.Lock()
			d.active[storageKey] = false
			d.mu.Unlock()
			return
		}
	}
}

// processBatch is the per-session unit of work: upsert every event row,
// run one coalesced projection refresh, publish one SSE wake, emit
// lifecycle deltas for newly inserted events, then ack. A refresh failure
// NAKs the whole batch — rows are durable and idempotent, so redelivery
// retries the projection only (duplicates skip side effects but still
// mark the projection dirty, which is what a redelivered-after-failed-
// refresh event needs).
func (d *persistDispatcher) processBatch(ctx context.Context, storageKey string, batch []*inflightSessionEvent) {
	upsertStart := time.Now()
	persisted := batch[:0:0]
	for _, in := range batch {
		inserted, err := d.store.Upsert(ctx, in.event)
		if err != nil {
			var schemaErr *conversation.SchemaError
			if errors.As(err, &schemaErr) {
				d.metrics.RecordSchemaRejected()
				slog.Warn("session bus event terminated: schema rejected",
					"subject", in.msg.Subject(),
					"error", schemaErr.Error(),
					"event_type", stringField(in.event, "type"),
				)
				_ = in.msg.TermWithReason("schema rejected: " + schemaErr.Error())
			} else {
				d.metrics.RecordTransientFailure()
				slog.Warn("session bus event persist failed",
					"subject", in.msg.Subject(),
					"error", err,
				)
				_ = in.msg.NakWithDelay(5 * time.Second)
			}
			d.settle(in.msg)
			continue
		}
		in.inserted = inserted
		if inserted {
			d.recordInsertedEventMetrics(in.event)
		} else {
			d.metrics.RecordDuplicatePersisted()
		}
		persisted = append(persisted, in)
	}
	d.metrics.RecordPersistPhaseDuration("upsert", time.Since(upsertStart).Seconds())
	if len(persisted) == 0 {
		return
	}

	refreshStart := time.Now()
	events := make([]map[string]any, len(persisted))
	for i, in := range persisted {
		events[i] = in.event
	}
	if err := d.refresher.RefreshEventBatch(ctx, events); err != nil {
		d.metrics.RecordTransientFailure()
		slog.Warn("session bus transcript refresh failed; batch NAKed for redelivery",
			"storage_key", storageKey,
			"events", len(events),
			"error", err,
		)
		d.nakBatch(persisted)
		return
	}
	d.metrics.RecordPersistPhaseDuration("refresh", time.Since(refreshStart).Seconds())

	if storageKey != "" && d.bus != nil && d.bus.nc != nil {
		subject := WakeSubject(storageKey)
		if err := d.bus.nc.Publish(subject, nil); err != nil {
			d.bus.wakeMetrics.RecordSessionEventWakePublishFailed()
			slog.Warn("session event persister wake publish failed; batch NAKed for redelivery",
				"subject", subject,
				"storage_key", storageKey,
				"error", err,
			)
			d.nakBatch(persisted)
			return
		}
		d.bus.wakeMetrics.RecordSessionEventWakePublished()
		d.bus.wakeMetrics.RecordSessionEventPersistToWakeDuration(time.Since(upsertStart).Seconds())
		last := persisted[len(persisted)-1].event
		slog.Info("session event persister wake published",
			"subject", subject,
			"storage_key", storageKey,
			"events", len(persisted),
			"first_order_key", stringField(persisted[0].event, "order_key"),
			"last_order_key", stringField(last, "order_key"),
			"last_event_type", stringField(last, "type"),
			"tank_session_id", stringField(last, "tank_session_id"),
		)
	}

	if d.bus != nil && d.bus.lifecycle != nil {
		// Coalesce activity emits per refresh CLASS per batch (issue
		// #1077 item 7). The dispatcher's batches are per-session
		// (#1052), and each emit class recomputes its indicator from
		// durable state — so the LAST inserted event of each class
		// carries the whole batch for that class: emitting per event ran
		// the unread/lifecycle derivation queries up to batch-size times
		// per flood batch for identical results. Coalescing must be
		// per-class, not global: a batch of [context.compacted,
		// turn.completed] needs BOTH the compaction-count refresh and the
		// activity refresh, and the emitter dispatches on the event's
		// type. Duplicates (inserted=false) keep skipping side effects as
		// before. The classifier is shared with the emitter's gate
		// (sessionactivity.ChatActivityDeltaClass) so a class this filter
		// drops is exactly a class the emitter would no-op on.
		lastPerClass := map[string]map[string]any{}
		for _, in := range persisted {
			if !in.inserted {
				continue
			}
			if class := sessionactivity.ChatActivityDeltaClass(stringField(in.event, "type")); class != "" {
				lastPerClass[class] = in.event
			}
		}
		for _, class := range []string{
			sessionactivity.ActivityClassLifecycle,
			sessionactivity.ActivityClassCompaction,
			sessionactivity.ActivityClassUserMessage,
		} {
			event, ok := lastPerClass[class]
			if !ok {
				continue
			}
			if err := d.bus.lifecycle.EmitChatActivityDelta(ctx, event); err != nil {
				slog.Warn("lifecycle activity-delta emit failed",
					"class", class,
					"order_key", stringField(event, "order_key"),
					"error", err,
				)
			}
		}
	}

	if age, ok := orderKeyAgeSeconds(stringField(persisted[len(persisted)-1].event, "order_key"), time.Now()); ok {
		d.metrics.RecordProcessedEventAge(age)
	}
	d.metrics.RecordPersistBatchSize(len(persisted))

	for _, in := range persisted {
		if err := in.msg.Ack(); err != nil {
			slog.Warn("session bus event ack failed", "subject", in.msg.Subject(), "error", err)
		}
	}
	d.settleBatch(persisted)
}

func (d *persistDispatcher) nakBatch(batch []*inflightSessionEvent) {
	for _, in := range batch {
		_ = in.msg.NakWithDelay(5 * time.Second)
	}
	d.settleBatch(batch)
}

func (d *persistDispatcher) settleBatch(batch []*inflightSessionEvent) {
	msgs := make([]persistableMessage, len(batch))
	for i, in := range batch {
		msgs[i] = in.msg
	}
	d.settle(msgs...)
}

// recordInsertedEventMetrics replicates the per-event observability the
// previous per-message persist path recorded — turn-terminal failures, the
// silent-stranding lifecycle counters, and the missing-client_nonce producer
// contract check. Recorded only for newly inserted rows so an at-least-once
// redelivery cannot double-count a turn boundary.
func (d *persistDispatcher) recordInsertedEventMetrics(event map[string]any) {
	eventType, _ := event["type"].(string)
	if eventType == string(conversation.EventTurnFailed) || eventType == string(conversation.EventTurnCommandFailed) {
		source, _ := event["source"].(string)
		reason := ""
		if payload, ok := event["payload"].(map[string]any); ok {
			reason, _ = payload["reason"].(string)
		}
		if source == "" {
			source = "unknown"
		}
		if reason == "" {
			reason = "unknown"
		}
		d.metrics.RecordTurnFailurePersisted(source, reason)
	}
	if conversation.IsTurnLifecycleEvent(conversation.EventType(eventType)) {
		d.metrics.RecordTurnLifecyclePersisted(eventType)
	}
	if conversation.IsTurnTerminalEvent(conversation.EventType(eventType)) && strings.TrimSpace(stringField(event, "client_nonce")) == "" {
		source := strings.TrimSpace(stringField(event, "source"))
		if source == "" {
			source = "unknown"
		}
		d.metrics.RecordTurnTerminalMissingClientNonce(source, eventType)
	}
}

// heartbeatLoop extends the ack deadline of every queued or in-flight
// message on a cadence safely inside the consumer AckWait. This is the
// persister-side equivalent of the runner's working() heartbeats: a message
// waiting behind a slow batch must tell JetStream it is still owned, or the
// server redelivers it to the other replica and both grind the same work —
// the duplicate-processing amplification observed in the 2026-06-11
// incident.
func (d *persistDispatcher) heartbeatLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		d.mu.Lock()
		msgs := make([]persistableMessage, 0, len(d.pending))
		for m := range d.pending {
			msgs = append(msgs, m)
		}
		d.mu.Unlock()
		for _, m := range msgs {
			if err := m.InProgress(); err != nil {
				slog.Debug("session bus persist heartbeat failed", "subject", m.Subject(), "error", err)
			}
		}
	}
}

// queueDepth reports the total queued (not yet processed) messages across
// all sessions, for the sampled gauge and the debug endpoint.
func (d *persistDispatcher) queueDepth() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, q := range d.queues {
		total += len(q)
	}
	return total
}

// PersisterQueueSnapshot is the per-session view served by the
// /api/debug/persister endpoint — per-entity detail that the metric
// cardinality rules keep out of labels.
type PersisterQueueSnapshot struct {
	StorageKey string `json:"storage_key"`
	Queued     int    `json:"queued"`
	Active     bool   `json:"active"`
	OldestAge  string `json:"oldest_enqueued_age,omitempty"`
}

func (d *persistDispatcher) snapshot() []PersisterQueueSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]PersisterQueueSnapshot, 0, len(d.queues))
	now := time.Now()
	for key, q := range d.queues {
		s := PersisterQueueSnapshot{StorageKey: key, Queued: len(q), Active: d.active[key]}
		if len(q) > 0 {
			s.OldestAge = now.Sub(q[0].enqueuedAt).Truncate(time.Millisecond).String()
		}
		out = append(out, s)
	}
	return out
}

// orderKeyAgeSeconds parses the 13-digit epoch-millisecond prefix of a
// session-event order key and returns its age. The prefix is producer wall
// clock, so the age is approximate — good enough for the "transcripts are N
// seconds behind" lag gauge, which cares about minutes, not millisecond
// skew.
func orderKeyAgeSeconds(orderKey string, now time.Time) (float64, bool) {
	dash := strings.IndexByte(orderKey, '-')
	if dash <= 0 {
		return 0, false
	}
	ms, err := strconv.ParseInt(orderKey[:dash], 10, 64)
	if err != nil || ms <= 0 {
		return 0, false
	}
	age := now.Sub(time.UnixMilli(ms)).Seconds()
	if age < 0 {
		age = 0
	}
	return age, true
}

// maxDeliveriesAdvisorySubject names the JetStream advisory published when a
// consumer exhausts MaxDeliver for a message. Without a subscriber here,
// exhaustion is a silent ledger hole: the message stays in the stream but
// this durable never sees it again, no terminal, no counter — the bug class
// docs/diagnostic-discipline.md §8 names. See tank-operator#1051.
func maxDeliveriesAdvisorySubject(stream, consumer string) string {
	return fmt.Sprintf("$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.%s.%s", stream, consumer)
}

type maxDeliveriesAdvisory struct {
	StreamSeq  uint64 `json:"stream_seq"`
	Deliveries int    `json:"deliveries"`
}

// handleMaxDeliveriesAdvisory repairs one exhausted message out of band:
// fetch it from the stream by sequence, persist + refresh + wake through the
// normal path, and count the outcome either way. "repaired" means the ledger
// row exists after this call; "failed" pages via the
// TankSessionEventPersistExhausted alert.
func (b *Bus) handleMaxDeliveriesAdvisory(ctx context.Context, d *persistDispatcher, data []byte) {
	var adv maxDeliveriesAdvisory
	if err := json.Unmarshal(data, &adv); err != nil || adv.StreamSeq == 0 {
		d.metrics.RecordExhaustedRepair("failed")
		slog.Error("max-deliveries advisory unparseable", "error", err)
		return
	}
	stream, err := b.js.Stream(ctx, b.stream)
	if err != nil {
		d.metrics.RecordExhaustedRepair("failed")
		slog.Error("max-deliveries repair: stream handle failed", "stream_seq", adv.StreamSeq, "error", err)
		return
	}
	raw, err := stream.GetMsg(ctx, adv.StreamSeq)
	if err != nil {
		d.metrics.RecordExhaustedRepair("failed")
		slog.Error("max-deliveries repair: message no longer in stream — event lost before persistence",
			"stream_seq", adv.StreamSeq, "deliveries", adv.Deliveries, "error", err)
		return
	}
	var event map[string]any
	if err := json.Unmarshal(raw.Data, &event); err != nil {
		// Invalid JSON would have been terminated, not exhausted; count it
		// as failed so a surprise here still alerts.
		d.metrics.RecordExhaustedRepair("failed")
		slog.Error("max-deliveries repair: invalid event json", "stream_seq", adv.StreamSeq, "error", err)
		return
	}
	if err := d.persistOutOfBand(ctx, event); err != nil {
		d.metrics.RecordExhaustedRepair("failed")
		slog.Error("max-deliveries repair: persist failed",
			"stream_seq", adv.StreamSeq,
			"order_key", stringField(event, "order_key"),
			"error", err)
		return
	}
	d.metrics.RecordExhaustedRepair("repaired")
	slog.Warn("max-deliveries exhaustion repaired out of band",
		"stream_seq", adv.StreamSeq,
		"deliveries", adv.Deliveries,
		"order_key", stringField(event, "order_key"),
		"event_type", stringField(event, "type"),
	)
}

// persistOutOfBand runs the normal upsert + refresh + wake path for one
// event that is not riding a consumer delivery (advisory repair, startup
// reconciler). No ack semantics; idempotent by order_key.
func (d *persistDispatcher) persistOutOfBand(ctx context.Context, event map[string]any) error {
	inserted, err := d.store.Upsert(ctx, event)
	if err != nil {
		return err
	}
	if inserted {
		d.recordInsertedEventMetrics(event)
	} else {
		d.metrics.RecordDuplicatePersisted()
	}
	if err := d.refresher.RefreshEventBatch(ctx, []map[string]any{event}); err != nil {
		return err
	}
	storageKey, _ := event["tank_session_id"].(string)
	if storageKey == "" {
		sessionID, _ := event["session_id"].(string)
		storageKey = sessionmodel.SessionStorageKey(d.bus.scope, sessionID)
	}
	if storageKey != "" && d.bus != nil && d.bus.nc != nil {
		if err := d.bus.nc.Publish(WakeSubject(storageKey), nil); err != nil {
			d.bus.wakeMetrics.RecordSessionEventWakePublishFailed()
			return err
		}
		d.bus.wakeMetrics.RecordSessionEventWakePublished()
	}
	return nil
}

// reconcilePersisterGaps repairs the window where MaxDeliver exhaustion can
// have silently skipped events: [ack floor + 1, last delivered]. Messages in
// that window that are merely unacked will redeliver normally and dedupe to
// a no-op; messages that exhausted their deliveries exist nowhere else and
// are persisted here. Also detects stream truncation past the ack floor
// (discard=old outrunning a stalled consumer), which is unrepairable and
// therefore counted. Runs once per persister start; in a healthy boot the
// window is empty and this returns immediately.
func (b *Bus) reconcilePersisterGaps(ctx context.Context, consumer jetstream.Consumer, d *persistDispatcher) error {
	info, err := consumer.Info(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: consumer info: %w", err)
	}
	stream, err := b.js.Stream(ctx, b.stream)
	if err != nil {
		return fmt.Errorf("reconciler: stream handle: %w", err)
	}
	sinfo, err := stream.Info(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: stream info: %w", err)
	}
	ackFloor := info.AckFloor.Stream
	delivered := info.Delivered.Stream
	firstSeq := sinfo.State.FirstSeq
	if ackFloor > 0 && firstSeq > ackFloor+1 {
		missing := firstSeq - ackFloor - 1
		d.metrics.RecordStreamTruncationGap(float64(missing))
		slog.Error("session bus stream truncated past persister ack floor — events discarded before persistence",
			"ack_floor", ackFloor,
			"stream_first_seq", firstSeq,
			"missing", missing,
		)
	}
	lo := ackFloor + 1
	if firstSeq > lo {
		lo = firstSeq
	}
	hi := delivered
	if hi < lo {
		return nil
	}
	slog.Info("session bus persister reconciler scanning exhaustion window",
		"from_seq", lo, "to_seq", hi, "window", hi-lo+1)
	oc, err := b.js.OrderedConsumer(ctx, b.stream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{EventSubjectFilter(b.scope)},
		DeliverPolicy:  jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:    lo,
	})
	if err != nil {
		return fmt.Errorf("reconciler: ordered consumer: %w", err)
	}
	repaired := 0
	scanned := uint64(0)
	maxScan := hi - lo + 1
	for scanned < maxScan {
		msg, err := oc.Next(jetstream.FetchMaxWait(5 * time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			return fmt.Errorf("reconciler: next: %w", err)
		}
		meta, err := msg.Metadata()
		if err != nil || meta == nil {
			continue
		}
		if meta.Sequence.Stream > hi {
			break
		}
		scanned++
		var event map[string]any
		if err := json.Unmarshal(msg.Data(), &event); err != nil {
			continue
		}
		inserted, err := d.store.Upsert(ctx, event)
		if err != nil {
			slog.Warn("reconciler: upsert failed", "stream_seq", meta.Sequence.Stream, "error", err)
			continue
		}
		if !inserted {
			continue
		}
		repaired++
		d.metrics.RecordReconcilerRepairedHole()
		if err := d.refresher.RefreshEventBatch(ctx, []map[string]any{event}); err != nil {
			slog.Warn("reconciler: refresh failed", "order_key", stringField(event, "order_key"), "error", err)
			continue
		}
		if storageKey := stringField(event, "tank_session_id"); storageKey != "" && b.nc != nil {
			_ = b.nc.Publish(WakeSubject(storageKey), nil)
		}
	}
	if repaired > 0 {
		slog.Warn("session bus persister reconciler repaired ledger holes",
			"repaired", repaired, "scanned", scanned, "from_seq", lo, "to_seq", hi)
	} else {
		slog.Info("session bus persister reconciler found no holes",
			"scanned", scanned, "from_seq", lo, "to_seq", hi)
	}
	return nil
}

// sampleLoop publishes the consumer-level lag gauges on a fixed cadence.
// These read JetStream consumer state, not Postgres rows — the persister's
// health signal must not depend on the persister working (the 2026-06-11
// incident blinded every persist-fed alert).
func (d *persistDispatcher) sampleLoop(ctx context.Context, consumer jetstream.Consumer, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		info, err := consumer.Info(ctx)
		if err != nil {
			slog.Debug("persister consumer info sample failed", "error", err)
			continue
		}
		d.metrics.RecordPersisterConsumerLag(float64(info.NumPending), float64(info.NumAckPending))
		queueDepth := d.queueDepth()
		d.metrics.RecordPersisterQueueDepth(queueDepth)
		// The processed-event-age gauge is set per completed batch, so with
		// zero traffic it freezes at the last batch's value — after a
		// backlog drain that frozen value is hours, and the backlog alert
		// pages on a healthy idle persister (observed on the #1051 deploy
		// itself). Nothing pending anywhere means transcripts are current:
		// say so.
		if info.NumPending == 0 && info.NumAckPending == 0 && queueDepth == 0 {
			d.metrics.RecordProcessedEventAge(0)
		}
	}
}

// RunEventPersister consumes runner/backend events from the session bus and
// owns their Postgres persistence, transcript-row projection, SSE wakes, and
// sidebar activity deltas. Architecture per tank-operator#1051: per-session
// serial queues over a bounded worker pool (cross-session isolation), batch
// coalescing (one projection refresh per session batch), ack-extension
// heartbeats, MAX_DELIVERIES advisory repair, and a startup reconciler for
// exhaustion holes.
func (b *Bus) RunEventPersister(ctx context.Context, store EventStore, refresher TranscriptRefresher, metrics PersisterMetrics) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	if metrics == nil {
		metrics = noopPersisterMetrics{}
	}
	if refresher == nil {
		refresher = noopTranscriptRefresher{}
	}
	if store == nil {
		return fmt.Errorf("session event store unavailable")
	}
	// Re-assert the stream on every (re)start: JetStream here is
	// memory-only, so a full NATS restart erases the stream AND every
	// durable consumer; the boot-time ensure alone could never heal that
	// (issue #1076 item 3). With the supervised restart loop in main, a
	// persister death from "stream not found" re-creates the stream and
	// the consumer on the next attempt instead of staying dead for the
	// pod's lifetime.
	if err := b.ensureStream(ctx); err != nil {
		return fmt.Errorf("ensure stream: %w", err)
	}
	consumer, err := b.js.CreateOrUpdateConsumer(ctx, b.stream, jetstream.ConsumerConfig{
		Name:          EventPersisterConsumerName(b.scope),
		Durable:       EventPersisterConsumerName(b.scope),
		Description:   "Persists session bus events to the Postgres session_events ledger",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       60 * time.Second,
		MaxDeliver:    20,
		MaxAckPending: 200,
		FilterSubject: EventSubjectFilter(b.scope),
	})
	if err != nil {
		return err
	}
	d := newPersistDispatcher(b, store, refresher, metrics, persistDispatcherConfig{})
	b.setPersistDispatcher(d)

	// Repair before consuming: exhaustion holes predate this process and
	// the live consumer will never redeliver them. A reconciler failure is
	// logged but does not block the live persister — degraded repair beats
	// no persistence.
	if err := b.reconcilePersisterGaps(ctx, consumer, d); err != nil {
		slog.Error("session bus persister reconciler failed", "error", err)
	}

	advSub, err := b.nc.Subscribe(
		maxDeliveriesAdvisorySubject(b.stream, EventPersisterConsumerName(b.scope)),
		func(msg *nats.Msg) {
			b.handleMaxDeliveriesAdvisory(ctx, d, msg.Data)
		},
	)
	if err != nil {
		return fmt.Errorf("max-deliveries advisory subscribe: %w", err)
	}
	defer func() { _ = advSub.Unsubscribe() }()

	go d.heartbeatLoop(ctx, 20*time.Second)
	go d.sampleLoop(ctx, consumer, 10*time.Second)

	consumeCtx, err := consumer.Consume(func(msg jetstream.Msg) {
		d.enqueue(ctx, msg)
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	consumeCtx.Drain()
	<-consumeCtx.Closed()
	return nil
}

// setPersistDispatcher / PersisterDebugSnapshot expose the dispatcher's
// per-session queue state to the /api/debug/persister endpoint.
func (b *Bus) setPersistDispatcher(d *persistDispatcher) {
	b.persistMu.Lock()
	defer b.persistMu.Unlock()
	b.persist = d
}

func (b *Bus) PersisterDebugSnapshot() []PersisterQueueSnapshot {
	if b == nil {
		return nil
	}
	b.persistMu.Lock()
	d := b.persist
	b.persistMu.Unlock()
	if d == nil {
		return nil
	}
	return d.snapshot()
}
