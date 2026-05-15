package sessionbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

type Config struct {
	URL      string
	Token    string
	Stream   string
	Scope    string
	Replicas int
}

type EventStore interface {
	Upsert(context.Context, map[string]any) error
}

type Bus struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	stream   string
	scope    string
	replicas int
}

func Connect(ctx context.Context, cfg Config) (*Bus, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, fmt.Errorf("NATS_URL is required")
	}
	opts := []nats.Option{
		nats.Name("tank-operator"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
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
		nc:       nc,
		js:       js,
		stream:   StreamName(cfg.Stream),
		scope:    cfg.Scope,
		replicas: cfg.Replicas,
	}
	if b.scope == "" {
		b.scope = "default"
	}
	if b.replicas <= 0 {
		b.replicas = 2
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
		command.SessionStorageKey = compat.SessionStorageKey(b.scope, command.SessionID)
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

func (b *Bus) PublishEvent(ctx context.Context, sessionStorageKey string, event map[string]any) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	sessionStorageKey = strings.TrimSpace(sessionStorageKey)
	if sessionStorageKey == "" {
		sessionID, _ := event["session_id"].(string)
		sessionStorageKey = compat.SessionStorageKey(b.scope, sessionID)
	}
	if sessionStorageKey == "" {
		return fmt.Errorf("event session storage key is required")
	}
	if _, ok := event["tank_session_id"]; !ok {
		event["tank_session_id"] = sessionStorageKey
	}
	msgID, _ := event["id"].(string)
	if msgID == "" {
		msgID, _ = event["uuid"].(string)
	}
	if msgID == "" {
		msgID, _ = event["event_id"].(string)
	}
	if msgID == "" {
		return fmt.Errorf("event id is required")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = b.js.Publish(ctx, EventSubject(sessionStorageKey), raw, jetstream.WithMsgID(msgID))
	return err
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
	return b.nc.Publish(SessionListWakeSubject(email), nil)
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
	storageKey := compat.SessionStorageKey(b.scope, sessionID)
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

func (b *Bus) RunEventPersister(ctx context.Context, store EventStore) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	consumer, err := b.js.CreateOrUpdateConsumer(ctx, b.stream, jetstream.ConsumerConfig{
		Name:          "tank-session-event-persister",
		Durable:       "tank-session-event-persister",
		Description:   "Persists session bus events to the Cosmos session-events ledger",
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
		if err := b.persistEventMessage(ctx, store, msg); err != nil {
			slog.Warn("session bus event persist failed",
				"subject", msg.Subject(),
				"error", err,
			)
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		if err := msg.Ack(); err != nil {
			slog.Warn("session bus event ack failed", "subject", msg.Subject(), "error", err)
		}
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	consumeCtx.Drain()
	<-consumeCtx.Closed()
	return nil
}

func (b *Bus) persistEventMessage(ctx context.Context, store EventStore, msg jetstream.Msg) error {
	var event map[string]any
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		_ = msg.TermWithReason("invalid json")
		return nil
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
		storageKey = compat.SessionStorageKey(b.scope, sessionID)
	}
	if storageKey != "" {
		if err := b.nc.Publish(WakeSubject(storageKey), nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bus) ensureStream(ctx context.Context) error {
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              b.stream,
		Description:       "Tank session command and event delivery bus",
		Subjects:          []string{subjectRoot + ".>"},
		Retention:         jetstream.LimitsPolicy,
		Discard:           jetstream.DiscardOld,
		MaxAge:            7 * 24 * time.Hour,
		MaxMsgSize:        2 * 1024 * 1024,
		Storage:           jetstream.FileStorage,
		Replicas:          b.replicas,
		Duplicates:        24 * time.Hour,
		AllowMsgTTL:       true,
		AllowMsgSchedules: true,
	})
	return err
}
