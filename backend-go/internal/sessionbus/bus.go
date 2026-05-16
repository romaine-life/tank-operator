package sessionbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

type Config struct {
	URL      string
	Token    string
	Stream   string
	Scope    string
	Replicas int
	// WakeMetrics is optional. When set, wake-publish failures inside
	// PublishSessionEventWake / PublishSessionListWake increment the
	// supplied counters before returning the error to the caller, so
	// silent `_ = b.PublishSessionListWake(...)` patterns in mutation
	// paths still produce telemetry.
	WakeMetrics WakeMetrics
	// ConnectionMetrics is optional. When set, the bus wires the NATS
	// disconnect / reconnect / async-error connection callbacks to the
	// supplied counters so an operator can tell whether session-bus
	// drops are happening (the failure mode behind both "SSE went
	// silent" and the wake-publish failures above).
	ConnectionMetrics ConnectionMetrics
}

// ConnectionMetrics receives counters for NATS connection lifecycle
// events. The bus wires these to nats.Options callbacks at Connect time.
type ConnectionMetrics interface {
	RecordDisconnect()
	RecordReconnect()
	RecordAsyncError()
}

type noopConnectionMetrics struct{}

func (noopConnectionMetrics) RecordDisconnect() {}
func (noopConnectionMetrics) RecordReconnect()  {}
func (noopConnectionMetrics) RecordAsyncError() {}

type EventStore interface {
	Upsert(context.Context, map[string]any) error
}

// PersisterMetrics receives counters from the schema-rejection / transient-
// failure split in the persister. Steady-state expectation: zero schema
// rejections. Wired to prometheus in cmd/tank-operator/observability.go.
type PersisterMetrics interface {
	RecordSchemaRejected()
	RecordTransientFailure()
}

type noopPersisterMetrics struct{}

func (noopPersisterMetrics) RecordSchemaRejected()   {}
func (noopPersisterMetrics) RecordTransientFailure() {}

// WakeMetrics receives counters for wake-publish failures. The bus records
// here before returning the error to the caller; callers that silently
// drop the error (Manager.PublishSessionListWake on lifecycle mutations)
// still get visibility into "NATS is down". Wired to prometheus in
// cmd/tank-operator/observability.go.
type WakeMetrics interface {
	RecordSessionEventWakePublishFailed()
	RecordSessionListWakePublishFailed()
}

type noopWakeMetrics struct{}

func (noopWakeMetrics) RecordSessionEventWakePublishFailed() {}
func (noopWakeMetrics) RecordSessionListWakePublishFailed()  {}

type Bus struct {
	nc          *nats.Conn
	js          jetstream.JetStream
	stream      string
	scope       string
	replicas    int
	wakeMetrics WakeMetrics
}

func Connect(ctx context.Context, cfg Config) (*Bus, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, fmt.Errorf("NATS_URL is required")
	}
	connMetrics := cfg.ConnectionMetrics
	if connMetrics == nil {
		connMetrics = noopConnectionMetrics{}
	}
	opts := []nats.Option{
		nats.Name("tank-operator"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		// Connection callbacks drive the tank_nats_* counters.
		// DisconnectErrHandler fires on every drop (with or without an
		// error attached); ReconnectHandler fires on the first
		// successful redial. ErrorHandler covers slow-consumer warnings
		// and permission errors that don't surface as Connect failures.
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			connMetrics.RecordDisconnect()
			if err != nil {
				slog.Warn("nats disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			connMetrics.RecordReconnect()
			slog.Info("nats reconnected")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			connMetrics.RecordAsyncError()
			slog.Warn("nats async error", "error", err)
		}),
	}
	if token := strings.TrimSpace(cfg.Token); token != "" {
		opts = append(opts, nats.Token(token))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	b := &Bus{
		nc:          nc,
		js:          js,
		stream:      StreamName(cfg.Stream),
		scope:       cfg.Scope,
		replicas:    cfg.Replicas,
		wakeMetrics: cfg.WakeMetrics,
	}
	if b.scope == "" {
		b.scope = "default"
	}
	if b.replicas <= 0 {
		b.replicas = 2
	}
	if b.wakeMetrics == nil {
		b.wakeMetrics = noopWakeMetrics{}
	}
	if err := b.ensureStream(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	return b, nil
}

func (b *Bus) Close() {
	if b == nil || b.nc == nil {
		return
	}
	b.nc.Close()
}

func (b *Bus) PublishCommand(ctx context.Context, command Command) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	command = command.Normalize()
	if command.CommandID == "" {
		return fmt.Errorf("command_id is required")
	}
	if command.Type == "" {
		return fmt.Errorf("command type is required")
	}
	if command.SessionStorageKey == "" {
		command.SessionStorageKey = sessionmodel.SessionStorageKey(b.scope, command.SessionID)
	}
	if command.SessionStorageKey == "" || command.Provider == "" {
		return fmt.Errorf("command routing is incomplete")
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(ctx, CommandSubject(command.SessionStorageKey, command.Provider), raw, jetstream.WithMsgID(command.CommandID))
	return err
}

// PublishSessionEventWake signals SSE subscribers on
// /api/sessions/{id}/events that new durable events landed in the
// session_events Postgres table for this session. The persister already
// publishes this after its own Upsert; backend code that direct-writes
// events to the table (e.g., boundary events on submit-turn,
// turn.command_failed when a command publish fails) must call this to
// keep the live SSE path consistent with the durable ledger. SSE clients
// otherwise wait up to one heartbeat interval before noticing — which is
// exactly the bug this fixes.
func (b *Bus) PublishSessionEventWake(_ context.Context, sessionStorageKey string) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	sessionStorageKey = strings.TrimSpace(sessionStorageKey)
	if sessionStorageKey == "" {
		return nil
	}
	if err := b.nc.Publish(WakeSubject(sessionStorageKey), nil); err != nil {
		b.wakeMetrics.RecordSessionEventWakePublishFailed()
		slog.Warn("session event wake publish failed",
			"storage_key", sessionStorageKey, "error", err)
		return err
	}
	return nil
}

// PublishSessionListWake signals to SSE subscribers on /api/sessions/events
// that the owner's session list has changed. Replaces the in-process
// EventBus fanout pattern with a NATS subject so the live path matches
// docs/product-inspirations.md ("Work delivery should use a real
// command/event fabric. Browser polling, process memory fanout, and
// database polling are not the normal live path for app-managed GUI chat.").
func (b *Bus) PublishSessionListWake(_ context.Context, email string) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	if strings.TrimSpace(email) == "" {
		return nil
	}
	if err := b.nc.Publish(SessionListWakeSubject(email), nil); err != nil {
		b.wakeMetrics.RecordSessionListWakePublishFailed()
		slog.Warn("session list wake publish failed",
			"email", email, "error", err)
		return err
	}
	return nil
}

// SubscribeSessionListWake returns a channel that receives a struct{} on
// every session-list-change signal for the owner. Channel cap is 1 so
// multiple wakes coalesce into one resync — same semantics as the prior
// in-process EventBus.
func (b *Bus) SubscribeSessionListWake(ctx context.Context, email string) (<-chan struct{}, func(), error) {
	if b == nil {
		return nil, func() {}, fmt.Errorf("session bus unavailable")
	}
	ch := make(chan struct{}, 1)
	sub, err := b.nc.Subscribe(SessionListWakeSubject(email), func(*nats.Msg) {
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return nil, func() {}, err
	}
	unsubscribe := func() {
		_ = sub.Unsubscribe()
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return ch, unsubscribe, nil
}

func (b *Bus) SubscribeWakes(ctx context.Context, sessionID string) (<-chan struct{}, func(), error) {
	if b == nil {
		return nil, func() {}, fmt.Errorf("session bus unavailable")
	}
	storageKey := sessionmodel.SessionStorageKey(b.scope, sessionID)
	ch := make(chan struct{}, 1)
	sub, err := b.nc.Subscribe(WakeSubject(storageKey), func(*nats.Msg) {
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return nil, func() {}, err
	}
	unsubscribe := func() {
		_ = sub.Unsubscribe()
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return ch, unsubscribe, nil
}

func (b *Bus) RunEventPersister(ctx context.Context, store EventStore, metrics PersisterMetrics) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	if metrics == nil {
		metrics = noopPersisterMetrics{}
	}
	consumer, err := b.js.CreateOrUpdateConsumer(ctx, b.stream, jetstream.ConsumerConfig{
		Name:          "tank-session-event-persister",
		Durable:       "tank-session-event-persister",
		Description:   "Persists session bus events to the Postgres session_events ledger",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       60 * time.Second,
		MaxDeliver:    20,
		MaxAckPending: 200,
		FilterSubject: subjectRoot + ".*.events",
	})
	if err != nil {
		return err
	}
	consumeCtx, err := consumer.Consume(func(msg jetstream.Msg) {
		b.handlePersistMessage(ctx, store, metrics, msg)
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	consumeCtx.Drain()
	<-consumeCtx.Closed()
	return nil
}

// persistableMessage is the narrow ack/term/data surface of jetstream.Msg
// used by handlePersistMessage. Defined here so unit tests can supply a
// stub without spinning up an in-process NATS server.
type persistableMessage interface {
	Subject() string
	Data() []byte
	Ack() error
	NakWithDelay(delay time.Duration) error
	TermWithReason(reason string) error
}

// handlePersistMessage routes one bus message through the store and acks /
// NAKs / terminates based on the outcome.
func (b *Bus) handlePersistMessage(ctx context.Context, store EventStore, metrics PersisterMetrics, msg persistableMessage) {
	err := b.persistOneEvent(ctx, store, msg)
	if err == nil {
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Warn("session bus event ack failed", "subject", msg.Subject(), "error", ackErr)
		}
		return
	}
	// Schema rejection is permanent — a retry would fail the same way.
	// Terminate the message so it doesn't burn 20 redeliveries + 200
	// ack-pending slots on something the persister can never accept.
	// The metric makes the producer-side regression visible.
	var schemaErr *conversation.SchemaError
	if errors.As(err, &schemaErr) {
		metrics.RecordSchemaRejected()
		slog.Warn("session bus event terminated: schema rejected",
			"subject", msg.Subject(),
			"error", schemaErr.Error(),
			"event_type", eventTypeForLog(msg.Data()),
		)
		_ = msg.TermWithReason("schema rejected: " + schemaErr.Error())
		return
	}
	metrics.RecordTransientFailure()
	slog.Warn("session bus event persist failed",
		"subject", msg.Subject(),
		"error", err,
	)
	_ = msg.NakWithDelay(5 * time.Second)
}

// persistOneEvent unmarshals + upserts + wakes for one message. Mirrors
// persistEventMessage but takes the narrow persistableMessage interface so
// it can be unit-tested without a live NATS server.
func (b *Bus) persistOneEvent(ctx context.Context, store EventStore, msg persistableMessage) error {
	var event map[string]any
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		// Invalid JSON is a producer-side bug that can never succeed on
		// retry. Surface it as a schema rejection so handlePersistMessage
		// terminates the message AND increments the producer-regression
		// counter — without this, an encoding bug at the producer would
		// silently terminate forever with no alert.
		return &conversation.SchemaError{Cause: fmt.Errorf("invalid json: %w", err)}
	}
	if store == nil {
		return fmt.Errorf("session event store unavailable")
	}
	if err := store.Upsert(ctx, event); err != nil {
		return err
	}
	storageKey, _ := event["tank_session_id"].(string)
	if storageKey == "" {
		sessionID, _ := event["session_id"].(string)
		storageKey = sessionmodel.SessionStorageKey(b.scope, sessionID)
	}
	if storageKey != "" && b.nc != nil {
		if err := b.nc.Publish(WakeSubject(storageKey), nil); err != nil {
			return err
		}
	}
	return nil
}

func eventTypeForLog(data []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return probe.Type
}

func (b *Bus) ensureStream(ctx context.Context) error {
	// Memory storage matches the infra-bootstrap NATS chart's
	// jetstream.fileStore.enabled=false config. The chart caps each
	// replica's JetStream RAM at 256Mi; the stream-level MaxBytes here
	// caps the stream within that budget so a runaway producer can't
	// fill memory and OOM the NATS pod. AllowMsgSchedules is no longer
	// needed since ScheduleWakeup is now a pod-local setTimeout.
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        b.stream,
		Description: "Tank session command and event delivery bus",
		Subjects:    []string{subjectRoot + ".>"},
		Retention:   jetstream.LimitsPolicy,
		Discard:     jetstream.DiscardOld,
		MaxAge:      7 * 24 * time.Hour,
		MaxBytes:    128 * 1024 * 1024,
		MaxMsgs:     100_000,
		MaxMsgSize:  2 * 1024 * 1024,
		Storage:     jetstream.MemoryStorage,
		Replicas:    b.replicas,
		Duplicates:  24 * time.Hour,
		AllowMsgTTL: true,
	})
	return err
}
