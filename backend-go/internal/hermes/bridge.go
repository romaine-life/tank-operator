package hermes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// EventStore is the slice of internal/store's SessionEventStore that the
// bridge needs. Kept narrow so tests can stub it.
type EventStore interface {
	Upsert(ctx context.Context, event map[string]any) error
}

// Recorder is an optional observability hook for the bridge. Wire it from
// the orchestrator's prometheus counters in cmd/tank-operator; nil is
// fine for tests and degraded boots. All recorder methods must be
// goroutine-safe and bounded — counters only, no labels that grow with
// session/turn cardinality.
type Recorder interface {
	RunCreated()
	RunCreateFailed()
	RunTerminal(terminal string) // "completed" | "failed" | "interrupted" | "command_failed" | "lost"
	TranslatorError(reason string) // "decode" | "unhandled_type"
}

// RowPublisher mirrors sessions.RowEmitter — the bridge calls it after
// turn-terminal events so the SPA's row stream picks up activity-summary
// updates on hermes_gui sessions. Optional; nil is fine.
type RowPublisher interface {
	PublishCurrentRow(ctx context.Context, owner, sessionID string)
}

// Bridge drives hermes_gui session turns: writes the user_message + turn.submitted
// pair, kicks off POST /v1/runs, tails the SSE stream, translates events into
// Tank schema, and lands them in session_events. One Bridge serves all
// hermes_gui sessions for the orchestrator — turns are concurrency-safe; each
// active turn runs in its own goroutine and tracks the run_id for cancel.
//
// Lifecycle is bounded by the session's lifetime. The bridge does not own a
// background reconcile loop today; if the orchestrator restarts mid-turn,
// active runs are abandoned and the user-visible state is whatever durable
// terminal event has landed in session_events (or, if none, a
// turn.command_failed emitted on the next stop/poll). Per-session reconcile
// is a follow-up — tracked in nelsong6/tank-operator#540's "out of scope" list.
type Bridge struct {
	client   *Client
	store    EventStore
	rows     RowPublisher
	recorder Recorder
	scope    string

	mu          sync.Mutex
	activeTurns map[string]*activeTurn // keyed by sessionID:turnID
}

type activeTurn struct {
	runID     string
	cancel    context.CancelFunc
	terminal  string // set on goroutine exit
	startedAt time.Time
}

// BridgeOptions configures NewBridge. Scope defaults to "default".
type BridgeOptions struct {
	Client   *Client
	Store    EventStore
	Rows     RowPublisher
	Recorder Recorder
	Scope    string
}

func NewBridge(opts BridgeOptions) *Bridge {
	scope := opts.Scope
	if scope == "" {
		scope = "default"
	}
	return &Bridge{
		client:      opts.Client,
		store:       opts.Store,
		rows:        opts.Rows,
		recorder:    opts.Recorder,
		scope:       scope,
		activeTurns: make(map[string]*activeTurn),
	}
}

func (b *Bridge) record(fn func(Recorder)) {
	if b.recorder != nil {
		fn(b.recorder)
	}
}

// SubmitTurn writes the durable user_message.created + turn.submitted pair,
// creates a Hermes run, and kicks off the SSE-tailing goroutine. Returns the
// turn id and the run id; the run id is opaque to callers but recorded on
// the activeTurns map for Stop.
type SubmitArgs struct {
	SessionID    string
	Email        string
	ClientNonce  string
	Text         string
	Instructions string // optional; layered on top of Hermes' core prompt
}

type SubmitResult struct {
	TurnID string
	RunID  string
}

func (b *Bridge) SubmitTurn(ctx context.Context, args SubmitArgs) (SubmitResult, error) {
	if b == nil {
		return SubmitResult{}, errors.New("hermes bridge not configured")
	}
	if args.SessionID == "" {
		return SubmitResult{}, errors.New("session_id is required")
	}
	if args.ClientNonce == "" {
		return SubmitResult{}, errors.New("client_nonce is required")
	}
	if args.Text == "" {
		return SubmitResult{}, errors.New("text is required")
	}
	storageKey := sessionmodel.SessionStorageKey(b.scope, args.SessionID)

	// 1. Land the user_message + turn.submitted pair. These are Tank-origin
	//    events; they exist whether or not the Hermes call succeeds, so the
	//    chat pane renders the user bubble even if /v1/runs 4xx's below.
	turnID, userEvents, err := conversation.UserSubmissionEventMaps(conversation.UserSubmissionArgs{
		SessionID:         args.SessionID,
		SessionStorageKey: storageKey,
		Email:             args.Email,
		Text:              args.Text,
		ClientNonce:       args.ClientNonce,
		Runtime:           "hermes",
	})
	if err != nil {
		return SubmitResult{}, fmt.Errorf("user submission events: %w", err)
	}
	for _, evt := range userEvents {
		if upErr := b.store.Upsert(ctx, evt); upErr != nil {
			return SubmitResult{}, fmt.Errorf("user submission upsert: %w", upErr)
		}
	}

	// 2. Create the Hermes run synchronously. Session id passed through so
	//    Hermes' dashboard correlates runs to Tank sessions.
	runResp, err := b.client.CreateRun(ctx, CreateRunRequest{
		Input:        args.Text,
		SessionID:    args.SessionID,
		Instructions: args.Instructions,
	})
	if err != nil {
		b.record(func(r Recorder) { r.RunCreateFailed() })
		// Write a durable turn.command_failed so the SPA's "stopping" /
		// "running" projection resolves to error rather than hangs.
		_ = b.emitCommandFailed(ctx, args.SessionID, storageKey, args.Email, turnID, args.ClientNonce, "hermes_create_failed", err.Error())
		return SubmitResult{}, fmt.Errorf("hermes create run: %w", err)
	}
	b.record(func(r Recorder) { r.RunCreated() })

	// 3. Spawn the streaming goroutine. The context derived from ctx survives
	//    the caller's request — bridge owns it until terminal or cancel.
	streamCtx, cancel := context.WithCancel(context.Background())
	at := &activeTurn{
		runID:     runResp.RunID,
		cancel:    cancel,
		startedAt: time.Now(),
	}
	b.mu.Lock()
	b.activeTurns[args.SessionID+":"+turnID] = at
	b.mu.Unlock()

	go b.runStream(streamCtx, runStreamArgs{
		sessionID:   args.SessionID,
		storageKey:  storageKey,
		email:       args.Email,
		turnID:      turnID,
		clientNonce: args.ClientNonce,
		runID:       runResp.RunID,
		owner:       args.Email,
	}, at)

	return SubmitResult{TurnID: turnID, RunID: runResp.RunID}, nil
}

type runStreamArgs struct {
	sessionID   string
	storageKey  string
	email       string
	turnID      string
	clientNonce string
	runID       string
	owner       string
}

func (b *Bridge) runStream(ctx context.Context, args runStreamArgs, at *activeTurn) {
	defer func() {
		b.mu.Lock()
		delete(b.activeTurns, args.sessionID+":"+args.turnID)
		b.mu.Unlock()
		if b.rows != nil {
			b.rows.PublishCurrentRow(context.Background(), args.owner, args.sessionID)
		}
	}()

	translator := NewTranslator(TranslatorConfig{
		SessionID:         args.sessionID,
		SessionStorageKey: args.storageKey,
		Email:             args.email,
		TurnID:            args.turnID,
		ClientNonce:       args.clientNonce,
	})

	streamErr := b.client.StreamEvents(ctx, args.runID, func(evt RunEvent) error {
		events := translator.Translate(evt)
		for _, e := range events {
			if err := b.store.Upsert(ctx, e); err != nil {
				slog.Error("hermes bridge upsert failed",
					"session_id", args.sessionID, "turn_id", args.turnID,
					"event_type", e["type"], "error", err)
				return err
			}
		}
		return nil
	})

	at.terminal = translator.Terminal()

	// The "durable terminal contract" inherited from #532: every accepted
	// turn must produce a terminal event. If the stream ended without one,
	// emit turn.command_failed so the SPA's projection resolves.
	if at.terminal == "" {
		reason := "hermes_stream_lost"
		detail := ""
		if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
			detail = streamErr.Error()
		}
		_ = b.emitCommandFailed(context.Background(), args.sessionID, args.storageKey, args.email, args.turnID, args.clientNonce, reason, detail)
		b.record(func(r Recorder) { r.RunTerminal("lost") })
	} else {
		b.record(func(r Recorder) { r.RunTerminal(at.terminal) })
	}

	if translator.UnhandledCount > 0 {
		slog.Warn("hermes bridge unhandled event types",
			"session_id", args.sessionID, "turn_id", args.turnID,
			"unhandled_count", translator.UnhandledCount,
			"unhandled_types", translator.UnhandledTypes)
		for i := 0; i < translator.UnhandledCount; i++ {
			b.record(func(r Recorder) { r.TranslatorError("unhandled_type") })
		}
	}
	if len(translator.TranslatorErrors) > 0 {
		slog.Warn("hermes bridge translator errors",
			"session_id", args.sessionID, "turn_id", args.turnID,
			"errors", len(translator.TranslatorErrors))
		for range translator.TranslatorErrors {
			b.record(func(r Recorder) { r.TranslatorError("decode") })
		}
	}
}

// StopTurn requests cancellation of an in-flight run. Mirrors the
// agent-runner / codex-runner contract: the call is non-blocking — Hermes
// returns {"status": "stopping"} immediately and the run's terminal SSE
// event is what actually resolves the UI. If no active turn matches, a
// turn.command_failed is emitted directly so the UI's "stopping" state
// still resolves (durable-terminal contract from #532).
func (b *Bridge) StopTurn(ctx context.Context, sessionID, owner, turnID, clientNonce string) error {
	b.mu.Lock()
	at, ok := b.activeTurns[sessionID+":"+turnID]
	b.mu.Unlock()
	storageKey := sessionmodel.SessionStorageKey(b.scope, sessionID)

	if !ok {
		// Race: the terminal event landed between client click and server
		// receipt. Emit a terminal-shaped marker for the "not found,
		// legitimately" bucket — UI was probably already at a terminal
		// projection but we don't want to silently strand.
		return b.emitInterruptRequested(ctx, sessionID, storageKey, owner, turnID, clientNonce)
	}
	if _, err := b.client.StopRun(ctx, at.runID); err != nil {
		return fmt.Errorf("hermes stop run: %w", err)
	}
	return b.emitInterruptRequested(ctx, sessionID, storageKey, owner, turnID, clientNonce)
}

func (b *Bridge) emitCommandFailed(ctx context.Context, sessionID, storageKey, email, turnID, clientNonce, reason, detail string) error {
	event := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             email,
		TurnID:            turnID,
		ClientNonce:       clientNonce,
		Reason:            reason,
		Runtime:           "hermes",
	})
	if detail != "" {
		if payload, ok := event["payload"].(map[string]any); ok {
			payload["detail"] = truncateForPayload(detail, 2000)
		}
	}
	return b.store.Upsert(ctx, event)
}

func (b *Bridge) emitInterruptRequested(ctx context.Context, sessionID, storageKey, email, turnID, clientNonce string) error {
	event := conversation.TurnInterruptRequestedEventMap(conversation.TurnInterruptRequestedArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             email,
		TurnID:            turnID,
		ClientNonce:       clientNonce,
		Runtime:           "hermes",
	})
	return b.store.Upsert(ctx, event)
}

func truncateForPayload(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// statsSnapshot is exposed for observability tests; production code wires
// these counters via the metrics package in observability.go.
func (b *Bridge) statsSnapshot() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]int{
		"active_turns": len(b.activeTurns),
	}
}

