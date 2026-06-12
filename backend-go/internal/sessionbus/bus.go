package sessionbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type Config struct {
	URL   string
	Token string
	// Stream is the legacy combined stream (events forever; command
	// subjects only for pre-split session pods). CommandStream is the
	// WorkQueue stream that owns durable commands (issue #1076 item 2).
	Stream        string
	CommandStream string
	Scope         string
	Replicas      int
	// WakeMetrics is optional. When set, publish failures inside
	// PublishSessionEventWake (chat per-session wake) and
	// PublishSessionRowUpdate (sidebar row-update wire) increment
	// the supplied counters before returning the error to the
	// caller, so silent fire-and-forget call sites still produce
	// telemetry on a NATS outage.
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
	// Upsert writes one event row and reports whether it was newly
	// inserted. False means the (tank_session_id, order_key) row already
	// existed — an at-least-once redelivery or producer republish — and
	// the persister skips duplicate side effects (per-event counters,
	// lifecycle emit) while still refreshing the projection.
	Upsert(context.Context, map[string]any) (bool, error)
}

// LifecycleEmitter is the hook the persister calls after a successful
// chat-event upsert so a session.activity_changed row update can be
// derived and published on the per-owner row-update subject. The
// implementation lives in internal/sessioncontroller and writes the
// activity_summary column through RowWriter + fans the post-write row
// out via RowPublisher — kept as an interface here so this package
// doesn't depend on sessioncontroller.
//
// Emit-or-skip is the emitter's decision (a no-op delta returns nil
// without writing). Errors are logged by the persister and otherwise
// ignored: the chat event is already durable and the per-session SSE
// wake already fired, so we don't NAK on a sidebar-only emit failure.
type LifecycleEmitter interface {
	EmitChatActivityDelta(ctx context.Context, event map[string]any) error
}

type noopLifecycleEmitter struct{}

func (noopLifecycleEmitter) EmitChatActivityDelta(_ context.Context, _ map[string]any) error {
	return nil
}

// PersisterMetrics receives counters from the schema-rejection / transient-
// failure split in the persister. Steady-state expectation: zero schema
// rejections. Wired to prometheus in cmd/tank-operator/observability.go.
type PersisterMetrics interface {
	RecordSchemaRejected()
	RecordTransientFailure()
	// RecordTurnFailurePersisted increments when a durable turn-terminal
	// failure event (turn.failed / turn.command_failed) lands in the
	// session_events ledger. Labels carry the producer source ("claude",
	// "codex", "tank") and the failure reason from payload.reason (e.g.
	// "provider_failure", "command_failed"). Replaces the SPA pill as the
	// user-trust-failure observability surface: with the pill gone, this
	// counter is how we notice "every session is failing" without browser
	// devtools. Steady-state expectation: low and bursty.
	RecordTurnFailurePersisted(source string, reason string)
	// RecordTurnLifecyclePersisted increments for the five lifecycle
	// event types that bound a turn — turn.submitted (the open boundary)
	// plus the four terminal types (turn.completed / turn.failed /
	// turn.command_failed / turn.interrupted). The submitted-vs-terminal
	// divergence is the silent-stranding observability surface per
	// docs/features/claude-runners/contract.md → Observability ("Silent
	// strandings, where a requested action has no terminal event, are a
	// counted bug class"). The TankTurnSilentStranding alert in
	// k8s/templates/observability.yaml fires when submitted outruns
	// terminal for a window long enough to rule out a single long Codex
	// turn. ea70777 (romaine-life/tank-operator#652) was the prototypical
	// silent-stranding incident; this counter would have caught it within
	// minutes of deploy instead of a user bug report. Non-lifecycle event
	// types are dropped at the implementation; the label set is bounded.
	RecordTurnLifecyclePersisted(eventType string)
	// RecordTurnTerminalMissingClientNonce increments when a durable
	// terminal turn event lands without client_nonce. The terminal row still
	// closes the server-side lifecycle, but the browser's already-open tab
	// uses client_nonce to release local run state and queued follow-ups.
	// Missing nonce is therefore a producer contract violation that must be
	// visible even when no browser is open.
	RecordTurnTerminalMissingClientNonce(source string, eventType string)
	// RecordDuplicatePersisted increments when an upsert reports the row
	// already existed — an at-least-once redelivery or producer republish.
	// Sustained nonzero rate means messages are outliving AckWait or a
	// producer is double-publishing; pairs with RecordRedelivered.
	RecordDuplicatePersisted()
	// RecordRedelivered increments when a consumed message's NumDelivered
	// exceeds 1. The 2026-06-11 incident ground both replicas over the
	// same messages invisibly; this counter is that failure mode's direct
	// signal.
	RecordRedelivered()
	// RecordPersistPhaseDuration observes one batch's wall time in a
	// phase, "upsert" (ledger writes) or "refresh" (transcript-row
	// projection). The incident's 10s-per-event projection cost would
	// have been attributable from this split alone.
	RecordPersistPhaseDuration(phase string, seconds float64)
	// RecordPersistBatchSize observes how many events one coalesced batch
	// carried. Large sustained batches mean the persister is draining a
	// backlog.
	RecordPersistBatchSize(n int)
	// RecordProcessedEventAge sets the lag gauge: age (producer wall
	// clock) of the newest event in the last completed batch. This is the
	// user-trust number — "transcripts are N seconds behind".
	RecordProcessedEventAge(seconds float64)
	// RecordExhaustedRepair counts MAX_DELIVERIES advisory outcomes:
	// "repaired" (the event row exists after out-of-band persistence) or
	// "failed" (a durable ledger hole; pages).
	RecordExhaustedRepair(outcome string)
	// RecordStreamTruncationGap adds the number of stream sequences the
	// retention policy discarded past the persister's ack floor — events
	// lost before persistence, unrepairable, counted per the four-outcome
	// contract.
	RecordStreamTruncationGap(missing float64)
	// RecordReconcilerRepairedHole increments per event the startup
	// reconciler persisted that no delivery would ever have retried.
	RecordReconcilerRepairedHole()
	// RecordPersisterConsumerLag sets the sampled consumer gauges:
	// undelivered backlog and in-flight unacked counts, read from
	// JetStream consumer state — deliberately not from Postgres, so the
	// signal survives the persister itself failing.
	RecordPersisterConsumerLag(pending float64, ackPending float64)
	// RecordPersisterQueueDepth sets the sampled in-process queue gauge
	// (messages routed to per-session queues, not yet processed).
	RecordPersisterQueueDepth(depth int)
}

type noopPersisterMetrics struct{}

func (noopPersisterMetrics) RecordSchemaRejected()                     {}
func (noopPersisterMetrics) RecordTransientFailure()                   {}
func (noopPersisterMetrics) RecordTurnFailurePersisted(string, string) {}
func (noopPersisterMetrics) RecordTurnLifecyclePersisted(string)       {}
func (noopPersisterMetrics) RecordTurnTerminalMissingClientNonce(string, string) {
}
func (noopPersisterMetrics) RecordDuplicatePersisted()                  {}
func (noopPersisterMetrics) RecordRedelivered()                         {}
func (noopPersisterMetrics) RecordPersistPhaseDuration(string, float64) {}
func (noopPersisterMetrics) RecordPersistBatchSize(int)                 {}
func (noopPersisterMetrics) RecordProcessedEventAge(float64)            {}
func (noopPersisterMetrics) RecordExhaustedRepair(string)               {}
func (noopPersisterMetrics) RecordStreamTruncationGap(float64)          {}
func (noopPersisterMetrics) RecordReconcilerRepairedHole()              {}
func (noopPersisterMetrics) RecordPersisterConsumerLag(float64, float64) {
}
func (noopPersisterMetrics) RecordPersisterQueueDepth(int) {}

// WakeMetrics receives counters for wake/event publish failures, the
// success path, and the end-to-end persist→wake latency. The bus
// records here before returning the error to the caller; callers that
// silently drop the error (Manager mutations that just want
// fire-and-forget) still get visibility into "NATS is down". Wired to
// prometheus in cmd/tank-operator/observability.go.
//
// Published and received are separate throughput counters, not a delivery-loss
// ratio: one published wake can have zero open subscribers or can fan out to
// multiple open streams. Both counters are unlabeled aggregates — the
// per-session subject lives in the slog line and the admin endpoint's stream
// snapshot, not in metric labels.
type WakeMetrics interface {
	RecordSessionEventWakePublishFailed()
	RecordSessionListEventPublishFailed()
	RecordSessionEventWakePublished()
	RecordSessionEventWakeReceived()
	RecordSessionEventPersistToWakeDuration(seconds float64)
	// RecordCommandPublishFailed increments when js.Publish on a
	// session-bus command subject returns an error — submit_turn,
	// interrupt_turn, or stop_background_task commands that the orchestrator
	// could not hand to the runner because JetStream itself failed.
	// Steady-state expectation is zero. The 2026-05-25 NATS quorum
	// incident produced sustained `reason="no_response_from_stream"`
	// across `kind="submit_turn"` — every chat submission failed
	// silently until the SPA rendered the durable turn.command_failed
	// event the orchestrator wrote at handlers_turns.go:798. The
	// TankSessionBusPublishFailing alert pages on any non-zero rate;
	// `kind` and `reason` labels are bounded by the bus's own
	// classifyPublishError + the closed Command.Type set.
	RecordCommandPublishFailed(kind string, reason string)
}

type noopWakeMetrics struct{}

func (noopWakeMetrics) RecordSessionEventWakePublishFailed()            {}
func (noopWakeMetrics) RecordSessionListEventPublishFailed()            {}
func (noopWakeMetrics) RecordSessionEventWakePublished()                {}
func (noopWakeMetrics) RecordSessionEventWakeReceived()                 {}
func (noopWakeMetrics) RecordSessionEventPersistToWakeDuration(float64) {}
func (noopWakeMetrics) RecordCommandPublishFailed(string, string)       {}

// WakeRecorder is the optional per-stream hook SubscribeWakes calls
// from the NATS message callback. The SSE handler passes its
// sessionstream.StreamState, which records the wake's wall-clock
// timestamp + the subject the NATS payload arrived on. This is what
// powers the admin endpoint's per-stream `last_wake_at` /
// `last_wake_subject` fields — the only way to distinguish "no wake
// ever fired for this session" from "a wake fired but the page read
// returned nothing" without browser devtools.
type WakeRecorder interface {
	RecordWake(at time.Time, subject string)
}

type Bus struct {
	nc            *nats.Conn
	js            jetstream.JetStream
	stream        string
	commandStream string
	scope         string
	replicas      int
	wakeMetrics   WakeMetrics
	lifecycle     LifecycleEmitter

	persistMu sync.Mutex
	persist   *persistDispatcher
}

// SetLifecycleEmitter wires the chat→activity-delta hook the persister
// calls after each successful upsert. Optional: nil leaves the emitter at
// the no-op default. Set once at startup after the lifecycle store + the
// bus are both built.
func (b *Bus) SetLifecycleEmitter(emitter LifecycleEmitter) {
	if b == nil {
		return
	}
	if emitter == nil {
		emitter = noopLifecycleEmitter{}
	}
	b.lifecycle = emitter
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
		nc:            nc,
		js:            js,
		stream:        StreamName(cfg.Stream),
		commandStream: CommandStreamName(cfg.CommandStream),
		scope:         cfg.Scope,
		replicas:      cfg.Replicas,
		wakeMetrics:   cfg.WakeMetrics,
	}
	if b.scope == "" {
		b.scope = "default"
	}
	if b.replicas <= 0 {
		// JetStream Raft requires R ∈ {1, 3, 5}; R=2 has no tiebreaker
		// and halts on a single slow member. The production chart sets
		// sessionBus.streamReplicas: 3 and exports it as
		// NATS_STREAM_REPLICAS; this default is a defense-in-depth
		// safety net only — if the env is unset for any reason, the
		// stream is still created with a sane quorum size rather than
		// regressing to the 2026-05-25 incident shape.
		b.replicas = 3
	}
	if b.wakeMetrics == nil {
		b.wakeMetrics = noopWakeMetrics{}
	}
	if b.lifecycle == nil {
		b.lifecycle = noopLifecycleEmitter{}
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
	// Route by command type: interrupts go to the control-plane subject so a
	// runner-side max_ack_pending=1 on the data subject can't hold an
	// interrupt behind an in-flight submit_turn. SubjectForCommand is the
	// single decision point so the routing rule is unit-testable without
	// touching JetStream.
	// Dual-publish (issue #1076 item 2 cutover): the command stream is the
	// authoritative wire for post-split session pods; the legacy combined
	// stream keeps receiving every command so PRE-split pods' durable
	// consumers stay whole (the migration checklist's pre-deploy-pod
	// clause — their wire is byte-identical to before). Either leg failing
	// fails the call: a half-published command silently strands whichever
	// pod generation needed the missing leg, and both legs share the
	// 24h msg-id duplicate window so the caller's retry is idempotent.
	_, err = b.js.Publish(ctx, CommandStreamSubjectForCommand(command), raw, jetstream.WithMsgID(command.CommandID))
	if err != nil {
		b.wakeMetrics.RecordCommandPublishFailed(
			commandKindLabel(command.Type),
			"cmd_stream_"+classifyPublishError(err),
		)
		return err
	}
	_, err = b.js.Publish(ctx, SubjectForCommand(command), raw, jetstream.WithMsgID(command.CommandID))
	if err != nil {
		// The 2026-05-25 incident shape: JetStream lost quorum and
		// returned `nats: no response from stream`, every submit_turn
		// failed, and the only signal was the per-session
		// turn.command_failed event the handler writes below. The
		// counter here is the observability surface the
		// TankSessionBusPublishFailing alert reads — without it,
		// re-occurrence stays invisible to Grafana until a user
		// screenshots the failure.
		b.wakeMetrics.RecordCommandPublishFailed(
			commandKindLabel(command.Type),
			classifyPublishError(err),
		)
	}
	return err
}

// CommandStreamUsage samples the command stream's occupancy. With
// WorkQueue retention the steady state is near-zero (acked commands
// delete); growth means consumers are missing or pre-split copies are
// accumulating toward the DiscardNew rejection threshold.
func (b *Bus) CommandStreamUsage(ctx context.Context) (StreamUsage, error) {
	if b == nil {
		return StreamUsage{}, fmt.Errorf("session bus unavailable")
	}
	stream, err := b.js.Stream(ctx, b.commandStream)
	if err != nil {
		return StreamUsage{}, err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return StreamUsage{}, err
	}
	return StreamUsage{
		Messages:      info.State.Msgs,
		Bytes:         info.State.Bytes,
		MaxMsgs:       50_000,
		MaxBytes:      32 * 1024 * 1024,
		ConsumerCount: info.State.Consumers,
	}, nil
}

// classifyPublishError maps a js.Publish error into a bounded reason
// label for the publish-failure counter. The jetstream package exports
// the two transport-layer sentinels that map cleanly to operational
// causes; context errors are stdlib; everything else collapses to
// "other" so a future nats.go release can't quietly inflate the
// label cardinality.
func classifyPublishError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, jetstream.ErrNoStreamResponse):
		return "no_response_from_stream"
	case errors.Is(err, jetstream.ErrConnectionClosed),
		errors.Is(err, nats.ErrConnectionClosed):
		return "connection"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "other"
	}
}

// commandKindLabel buckets Command.Type against the closed set of
// commands this bus publishes today. The set is enumerated at
// internal/sessionbus/commands.go; any addition there should mirror
// here so the metric stays path-independent. Unknown types collapse
// to "other".
func commandKindLabel(commandType string) string {
	switch commandType {
	case CommandSubmitTurn,
		CommandInterrupt,
		CommandStopBackgroundTask:
		return commandType
	default:
		return "other"
	}
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

// PublishSessionRowUpdate publishes one sessions-row snapshot on the
// per-(owner, scope) row-update subject. The SSE handler on
// /api/sessions/events forwards the payload verbatim to subscribed
// clients; the SPA's SessionStore replaces its row cache by
// session_id. Per docs/session-list-redesign.md Phase 3, this
// replaces the typed-event publish path entirely; the wire is the
// row, not an event-type discriminator.
//
// Steady-state expectation: zero publish failures; a failure here
// means NATS is down, in which case SSE clients fall back to the
// durable Postgres replay (sessions table) on reconnect.
//
// Scope must match the sessions row's session_scope field. Passing
// the wrong scope here is the failure mode the (email, scope) subject
// shape is designed to make impossible — payloads publish to a
// subject no other-scope subscriber is listening on, so cross-scope
// leakage is a wire-shape impossibility.
func (b *Bus) PublishSessionRowUpdate(_ context.Context, email, scope string, payload []byte) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	if strings.TrimSpace(email) == "" {
		return nil
	}
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("session row update scope is required")
	}
	if len(payload) == 0 {
		return fmt.Errorf("session row update payload is empty")
	}
	if err := b.nc.Publish(SessionRowUpdateSubject(email, scope), payload); err != nil {
		b.wakeMetrics.RecordSessionListEventPublishFailed()
		slog.Warn("session row update publish failed",
			"email", email, "scope", scope, "error", err)
		return err
	}
	return nil
}

// SubscribeSessionRowUpdates returns a channel that receives each
// sessions-row snapshot published for the (owner, scope) pair.
// Channel cap is 64 to absorb short bursts; if the consumer falls
// further behind, payloads are dropped at the NATS-subscription
// callback and the consumer's next reconnect cursor-resume catches
// up from the sessions table.
func (b *Bus) SubscribeSessionRowUpdates(ctx context.Context, email, scope string) (<-chan []byte, func(), error) {
	if b == nil {
		return nil, func() {}, fmt.Errorf("session bus unavailable")
	}
	if strings.TrimSpace(scope) == "" {
		return nil, func() {}, fmt.Errorf("session row update scope is required")
	}
	ch := make(chan []byte, 64)
	sub, err := b.nc.Subscribe(SessionRowUpdateSubject(email, scope), func(msg *nats.Msg) {
		// Copy the data slice — the NATS client reuses the underlying
		// buffer across deliveries.
		payload := make([]byte, len(msg.Data))
		copy(payload, msg.Data)
		select {
		case ch <- payload:
		default:
			// Drop. SSE consumer will resync from the durable ledger on
			// the next reconnect; better than a slow-consumer stall.
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

func (b *Bus) PublishPinnedReposUpdate(_ context.Context, email string) error {
	if b == nil {
		return fmt.Errorf("session bus unavailable")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	if err := b.nc.Publish(PinnedReposUpdateSubject(email), nil); err != nil {
		slog.Warn("pinned repos update publish failed", "email", email, "error", err)
		return err
	}
	return nil
}

func (b *Bus) SubscribePinnedReposUpdates(ctx context.Context, email string) (<-chan struct{}, func(), error) {
	if b == nil {
		return nil, func() {}, fmt.Errorf("session bus unavailable")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, func() {}, fmt.Errorf("email is required")
	}
	ch := make(chan struct{}, 1)
	sub, err := b.nc.Subscribe(PinnedReposUpdateSubject(email), func(_ *nats.Msg) {
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
	return b.SubscribeWakesWithRecorder(ctx, sessionID, nil)
}

// SubscribeWakesWithRecorder is the per-stream-aware variant of
// SubscribeWakes. The optional recorder is invoked from the NATS
// message callback so the per-session SSE stream's last_wake_at /
// last_wake_subject / wakes_received state stays accurate even when
// the buffered notify channel is already full. The metrics counter
// fires once per NATS delivery (not once per noticed wake). That makes the
// received counter a subscriber-delivery throughput metric, not a direct
// counterpart to the one-per-event published counter.
//
// SubscribeWakes (no recorder) is preserved for tests and any
// caller that doesn't need per-stream attribution.
func (b *Bus) SubscribeWakesWithRecorder(ctx context.Context, sessionID string, recorder WakeRecorder) (<-chan struct{}, func(), error) {
	storageKey := sessionmodel.SessionStorageKey(b.scope, sessionID)
	return b.SubscribeWakesForStorageKey(ctx, storageKey, recorder)
}

// SubscribeWakesForStorageKey subscribes to a fully-resolved Tank session
// storage key. Most callers should use SubscribeWakesWithRecorder; this is
// for read-only cross-scope views where the public session id belongs to a
// different registry scope than this process writes to.
func (b *Bus) SubscribeWakesForStorageKey(ctx context.Context, sessionStorageKey string, recorder WakeRecorder) (<-chan struct{}, func(), error) {
	if b == nil {
		return nil, func() {}, fmt.Errorf("session bus unavailable")
	}
	storageKey := strings.TrimSpace(sessionStorageKey)
	if storageKey == "" {
		return nil, func() {}, fmt.Errorf("session storage key is required")
	}
	ch := make(chan struct{}, 1)
	sub, err := b.nc.Subscribe(WakeSubject(storageKey), func(msg *nats.Msg) {
		b.wakeMetrics.RecordSessionEventWakeReceived()
		if recorder != nil {
			subject := ""
			if msg != nil {
				subject = msg.Subject
			}
			recorder.RecordWake(time.Now(), subject)
		}
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

// stringField is a defensive accessor for the persister's slog lines.
// Returns "" instead of panicking when the field is missing or not a
// string — the persister already validates schema, so this is purely
// for the diagnostic log boundary.
func stringField(event map[string]any, key string) string {
	if event == nil {
		return ""
	}
	v, _ := event[key].(string)
	return v
}

func (b *Bus) ensureStream(ctx context.Context) error {
	// Memory storage matches the infra-bootstrap NATS chart's
	// jetstream.fileStore.enabled=false config. The chart caps each
	// replica's JetStream RAM at 256Mi; the stream-level MaxBytes here
	// caps the stream within that budget so a runaway producer can't
	// fill memory and OOM the NATS pod. ScheduleWakeup is backend-owned
	// durable Postgres state, so JetStream message scheduling stays off.
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
	if err != nil {
		return err
	}
	// The command stream (issue #1076 item 2). WorkQueue retention: a
	// message lives until the (single) matching consumer acks it, so a
	// flood session's EVENTS can never evict another session's undelivered
	// submit_turn — the failure class the combined LimitsPolicy/DiscardOld
	// stream carried. Consumer filters are per-session/per-provider and
	// non-overlapping (commands.* vs control.*), satisfying WorkQueue's
	// exclusivity rule. DiscardNew: when limits fill, NEW publishes are
	// REJECTED (loud — PublishCommand's error path writes a durable
	// turn.command_failed and trips the publish-failure alert) instead of
	// silently evicting someone else's command. MaxAge 24h bounds copies
	// dual-published for pre-split pods that will never consume here.
	_, err = b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        b.commandStream,
		Description: "Tank session durable command delivery (WorkQueue; events stay on the session bus stream)",
		Subjects:    []string{cmdRoot + ".>"},
		Retention:   jetstream.WorkQueuePolicy,
		Discard:     jetstream.DiscardNew,
		MaxAge:      24 * time.Hour,
		MaxBytes:    32 * 1024 * 1024,
		MaxMsgs:     50_000,
		MaxMsgSize:  2 * 1024 * 1024,
		Storage:     jetstream.MemoryStorage,
		Replicas:    b.replicas,
		Duplicates:  24 * time.Hour,
	})
	return err
}

// StreamUsage is one sample of the bus stream's occupancy against its
// configured limits. The stream is LimitsPolicy/DiscardOld over BOTH
// durable commands and events (issue #1076 item 2): when MaxBytes/MaxMsgs
// fill, the OLDEST messages are silently evicted regardless of consumer
// interest — including other sessions' undelivered submit_turn commands.
// Until commands move to their own interest-retained stream (a wire
// migration; see the issue), occupancy visibility + an approach alert are
// the guardrail: evictions of unconsumed events are detectable after the
// fact (RecordStreamTruncationGap), evicted commands are not detectable
// at all, so the alert has to fire BEFORE the limit.
type StreamUsage struct {
	Messages      uint64
	Bytes         uint64
	MaxMsgs       int64
	MaxBytes      int64
	ConsumerCount int
}

func (b *Bus) StreamUsage(ctx context.Context) (StreamUsage, error) {
	if b == nil || b.js == nil {
		return StreamUsage{}, fmt.Errorf("session bus unavailable")
	}
	stream, err := b.js.Stream(ctx, b.stream)
	if err != nil {
		return StreamUsage{}, err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return StreamUsage{}, err
	}
	return StreamUsage{
		Messages:      info.State.Msgs,
		Bytes:         info.State.Bytes,
		MaxMsgs:       info.Config.MaxMsgs,
		MaxBytes:      info.Config.MaxBytes,
		ConsumerCount: info.State.Consumers,
	}, nil
}
