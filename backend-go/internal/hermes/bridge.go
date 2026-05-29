package hermes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// EventStore is the slice of internal/store's SessionEventStore that the
// bridge needs. Kept narrow so tests can stub it.
type EventStore interface {
	Upsert(ctx context.Context, event map[string]any) error
	FindTurnTerminal(ctx context.Context, tankSessionID, turnID string) (map[string]any, error)
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
	RunDuration(terminal string, seconds float64)
	RunEvent(eventType string)
	CapabilityCheck(result string)
	TranslatorError(reason string) // "decode" | "unhandled_type"
}

// ActiveRunStore persists the upstream Hermes run currently driving a Tank
// turn. Implemented by the Postgres session registry; nil is accepted in local
// development, but production wires it so restart recovery does not depend on
// process memory.
type ActiveRunStore interface {
	SetHermesActiveRun(ctx context.Context, owner, sessionID string, run sessionmodel.HermesActiveRun) error
	ClearHermesActiveRun(ctx context.Context, owner, sessionID, turnID, runID string) error
	GetHermesActiveRun(ctx context.Context, owner, sessionID, turnID string) (sessionmodel.HermesActiveRun, bool, error)
	ListHermesActiveRuns(ctx context.Context) ([]sessionmodel.HermesActiveRun, error)
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
// Lifecycle is bounded by the session's lifetime. Active run pointers are
// durable on the sessions row, so a replacement orchestrator can reattach to
// streams or synthesize terminal status from Hermes instead of abandoning
// accepted turns on process restart.
type Bridge struct {
	client   *Client
	store    EventStore
	active   ActiveRunStore
	rows     RowPublisher
	recorder Recorder
	scope    string

	mu          sync.Mutex
	activeTurns map[string]*activeTurn // keyed by sessionID:turnID
}

type activeTurn struct {
	runID     string
	cancel    context.CancelFunc
	startedAt time.Time
	mu        sync.Mutex
	terminal  string // set on goroutine exit or forced by stop/recovery failure
	// done closes when the bridge's runStream goroutine for this turn
	// returns — after the durable terminal event (translated terminal
	// or the hermes_stream_lost safety net) has landed in
	// session_events. WaitForTurn / WaitForActiveTurnsToSettle read
	// from this channel; production callers use the latter as the
	// graceful-shutdown drain, tests use the former to assert on the
	// post-goroutine ledger state without timing-based sleeps.
	done chan struct{}
}

func (a *activeTurn) setTerminal(terminal string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.terminal = terminal
}

func (a *activeTurn) forceTerminal(terminal string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminal == "" {
		a.terminal = terminal
	}
}

func (a *activeTurn) getTerminal() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminal
}

// BridgeOptions configures NewBridge. Scope defaults to "default".
type BridgeOptions struct {
	Client     *Client
	Store      EventStore
	ActiveRuns ActiveRunStore
	Rows       RowPublisher
	Recorder   Recorder
	Scope      string
}

func NewBridge(opts BridgeOptions) *Bridge {
	scope := opts.Scope
	if scope == "" {
		scope = "default"
	}
	return &Bridge{
		client:      opts.Client,
		store:       opts.Store,
		active:      opts.ActiveRuns,
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
	SessionID       string
	Email           string
	ClientNonce     string
	Text            string
	DisplayText     string
	Attachments     []conversation.UserMessageAttachment
	Instructions    string // optional; layered on top of Hermes' core prompt
	SkillName       string
	OmitUserMessage bool
	// AuthorKind attributes the turn to a non-interactive principal (an
	// auth.romaine.life bot token) so the transcript renders the session's
	// system identity instead of the human owner. Empty for human turns.
	AuthorKind string
	Now        time.Time
	OrderBase  time.Time
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
	displayText := strings.TrimSpace(args.DisplayText)
	if displayText == "" {
		displayText = args.Text
	}
	now := args.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	storageKey := sessionmodel.SessionStorageKey(b.scope, args.SessionID)

	// 1. Land the user_message + turn.submitted pair. These are Tank-origin
	//    events; they exist whether or not the Hermes call succeeds, so the
	//    chat pane renders the user bubble even if /v1/runs 4xx's below.
	turnID, userEvents, err := conversation.UserSubmissionEventMaps(conversation.UserSubmissionArgs{
		SessionID:         args.SessionID,
		SessionStorageKey: storageKey,
		Email:             args.Email,
		Text:              displayText,
		Message:           map[string]any{"role": "user", "content": displayText},
		Attachments:       args.Attachments,
		ClientNonce:       args.ClientNonce,
		Runtime:           "hermes",
		SkillName:         args.SkillName,
		AuthorKind:        strings.TrimSpace(args.AuthorKind),
		Now:               now,
	})
	if err != nil {
		return SubmitResult{}, fmt.Errorf("user submission events: %w", err)
	}
	retimeTurnBoundaryEvents(userEvents, args.OrderBase)
	if args.OmitUserMessage {
		userEvents = omitUserMessageEvents(userEvents)
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

	startedAt := time.Now().UTC()
	activeRun := sessionmodel.HermesActiveRun{
		Owner:       args.Email,
		SessionID:   args.SessionID,
		TurnID:      turnID,
		ClientNonce: args.ClientNonce,
		RunID:       runResp.RunID,
		StartedAt:   startedAt.Format(time.RFC3339Nano),
	}
	if b.active != nil {
		if err := b.active.SetHermesActiveRun(ctx, args.Email, args.SessionID, activeRun); err != nil {
			_ = b.emitCommandFailed(ctx, args.SessionID, storageKey, args.Email, turnID, args.ClientNonce, "hermes_active_run_persist_failed", err.Error())
			b.record(func(r Recorder) { r.RunTerminal("command_failed") })
			b.record(func(r Recorder) { r.RunDuration("command_failed", durationSecondsSince(startedAt)) })
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = b.client.StopRun(stopCtx, runResp.RunID)
			cancel()
			return SubmitResult{}, fmt.Errorf("hermes active run persist: %w", err)
		}
	}

	// 3. Spawn the streaming goroutine. The context is owned by the bridge
	//    until terminal or cancel, not by the HTTP request that submitted it.
	b.startRunStream(activeRun)

	return SubmitResult{TurnID: turnID, RunID: runResp.RunID}, nil
}

func omitUserMessageEvents(events []map[string]any) []map[string]any {
	out := events[:0]
	for _, event := range events {
		if event["type"] == string(conversation.EventUserMessageCreated) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func retimeTurnBoundaryEvents(events []map[string]any, base time.Time) {
	if base.IsZero() {
		return
	}
	base = base.UTC()
	for i, event := range events {
		eventTime := base.Add(time.Duration(i) * time.Millisecond)
		event["created_at"] = eventTime.Format(time.RFC3339Nano)
		event["written_at"] = eventTime.Format(time.RFC3339Nano)
		event["order_key"] = orderKeyForEventTime(eventTime, i, eventIDForOrderKey(event))
	}
}

func orderKeyForEventTime(eventTime time.Time, sequence int, eventID string) string {
	return fmt.Sprintf("%013d-%08d-%s", eventTime.UTC().UnixMilli(), sequence, eventID)
}

func eventIDForOrderKey(event map[string]any) string {
	for _, key := range []string{"event_id", "id", "uuid"} {
		if value, ok := event[key].(string); ok && value != "" {
			return value
		}
	}
	return "missing-event-id"
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

func (b *Bridge) startRunStream(run sessionmodel.HermesActiveRun) *activeTurn {
	startedAt := parseHermesStartedAt(run.StartedAt)
	streamCtx, cancel := context.WithCancel(context.Background())
	at := &activeTurn{
		runID:     run.RunID,
		cancel:    cancel,
		startedAt: startedAt,
		done:      make(chan struct{}),
	}
	key := run.SessionID + ":" + run.TurnID
	b.mu.Lock()
	if existing, ok := b.activeTurns[key]; ok {
		b.mu.Unlock()
		cancel()
		return existing
	}
	b.activeTurns[key] = at
	b.mu.Unlock()

	go b.runStream(streamCtx, runStreamArgs{
		sessionID:   run.SessionID,
		storageKey:  sessionmodel.SessionStorageKey(b.scope, run.SessionID),
		email:       run.Owner,
		turnID:      run.TurnID,
		clientNonce: run.ClientNonce,
		runID:       run.RunID,
		owner:       run.Owner,
	}, at)
	return at
}

func parseHermesStartedAt(raw string) time.Time {
	if raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func (b *Bridge) runStream(ctx context.Context, args runStreamArgs, at *activeTurn) {
	defer func() {
		b.mu.Lock()
		delete(b.activeTurns, args.sessionID+":"+args.turnID)
		b.mu.Unlock()
		if b.active != nil {
			if err := b.active.ClearHermesActiveRun(context.Background(), args.owner, args.sessionID, args.turnID, args.runID); err != nil {
				slog.Warn("hermes bridge active-run clear failed",
					"session_id", args.sessionID, "turn_id", args.turnID,
					"run_id", args.runID, "error", err)
			}
		}
		if b.rows != nil {
			b.rows.PublishCurrentRow(context.Background(), args.owner, args.sessionID)
		}
		// Signal completion AFTER the activeTurns delete + row publish
		// so a caller awaiting on `done` sees a consistent post-state:
		// the map no longer lists the turn, the row publisher has
		// fired, and any durable terminal event the goroutine emitted
		// (translated terminal or hermes_stream_lost safety net) has
		// already landed via b.store.Upsert above. The close is the
		// only operation that happens after publish so it's safe even
		// if rows.PublishCurrentRow blocks briefly.
		close(at.done)
	}()

	translator := NewTranslator(TranslatorConfig{
		SessionID:         args.sessionID,
		SessionStorageKey: args.storageKey,
		Email:             args.email,
		TurnID:            args.turnID,
		ClientNonce:       args.clientNonce,
	})

	streamErr := b.client.StreamEvents(ctx, args.runID, func(evt RunEvent) error {
		b.record(func(r Recorder) { r.RunEvent(evt.Type) })
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

	if terminal := translator.Terminal(); terminal != "" {
		at.setTerminal(terminal)
	}
	terminal := at.getTerminal()

	// The "durable terminal contract" inherited from #532: every accepted
	// turn must produce a terminal event. If the stream ended without one,
	// emit turn.command_failed so the SPA's projection resolves.
	if terminal == "" {
		reason := "hermes_stream_lost"
		detail := ""
		if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
			detail = streamErr.Error()
		}
		_ = b.emitCommandFailed(context.Background(), args.sessionID, args.storageKey, args.email, args.turnID, args.clientNonce, reason, detail)
		terminal = "lost"
		at.setTerminal(terminal)
	}
	b.record(func(r Recorder) { r.RunTerminal(terminal) })
	b.record(func(r Recorder) { r.RunDuration(terminal, durationSecondsSince(at.startedAt)) })

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
// WaitForTurn blocks until the streaming goroutine for (sessionID, turnID)
// returns — which happens after the durable terminal event has landed
// in session_events (translated terminal from the upstream Hermes
// stream, or the hermes_stream_lost safety net the bridge emits when
// no upstream terminal arrived). Returns immediately when the turn is
// not (or no longer) active; returns ctx.Err() when the caller's
// context completes before the goroutine.
//
// Used by tests to assert on the post-goroutine ledger state without
// timing-based sleeps. The previous pattern — assert.Equal(len(upserts),
// 2) right after SubmitTurn — was a race against this same goroutine
// and was the cause of the intermittent TestCreateHermesSessionInitial
// TurnSubmitsAtCreate flake on nelsong6/tank-operator#638.
func (b *Bridge) WaitForTurn(ctx context.Context, sessionID, turnID string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	at, ok := b.activeTurns[sessionID+":"+turnID]
	b.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-at.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForActiveTurnsToSettle blocks until every in-flight bridge
// goroutine returns. This is the graceful-shutdown drain — call it
// from the orchestrator's shutdown hook with a bounded context (e.g.
// 30s) so a rolling pod gives in-flight Hermes turns time to emit
// their terminal events to session_events instead of getting killed
// mid-emit and leaving the SPA's projection stuck on "running" until
// the next stop/restart.
//
// Returns ctx.Err() when the deadline hits with turns still active.
// Returns nil when every goroutine has signaled completion (the
// activeTurns map will also be empty at that point, since the
// goroutine's defer deletes its entry before closing done).
func (b *Bridge) WaitForActiveTurnsToSettle(ctx context.Context) error {
	if b == nil {
		return nil
	}
	for {
		b.mu.Lock()
		// Snapshot the in-flight goroutines' done channels so we can
		// wait without holding the bridge mutex (which a returning
		// goroutine's defer needs to delete its activeTurns entry).
		channels := make([]chan struct{}, 0, len(b.activeTurns))
		for _, at := range b.activeTurns {
			channels = append(channels, at.done)
		}
		b.mu.Unlock()
		if len(channels) == 0 {
			return nil
		}
		for _, ch := range channels {
			select {
			case <-ch:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// Loop: a NEW turn may have started while we were waiting on
		// the snapshot's channels. Drain those too. Steady-state
		// shutdown has no new SubmitTurn callers (the HTTP server
		// already stopped accepting), so this terminates after one
		// or two iterations in practice.
	}
}

// RecoverActiveRuns reattaches process memory to durable hermes_active_run
// pointers after an orchestrator restart. Terminal Hermes statuses are
// translated immediately; non-terminal statuses restart the SSE tailer. The
// method keeps going after per-run failures and returns the first failure as
// boot diagnostics.
func (b *Bridge) RecoverActiveRuns(ctx context.Context) error {
	if b == nil || b.active == nil {
		return nil
	}
	runs, err := b.active.ListHermesActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("list hermes active runs: %w", err)
	}
	var firstErr error
	for _, run := range runs {
		if !run.Valid() {
			continue
		}
		storageKey := sessionmodel.SessionStorageKey(b.scope, run.SessionID)
		if terminal, err := b.store.FindTurnTerminal(ctx, run.SessionID, run.TurnID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("find terminal for hermes run %s: %w", run.RunID, err)
			}
		} else if terminal != nil {
			if clearErr := b.active.ClearHermesActiveRun(ctx, run.Owner, run.SessionID, run.TurnID, run.RunID); clearErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("clear completed hermes run %s: %w", run.RunID, clearErr)
			}
			continue
		}

		status, err := b.client.GetRun(ctx, run.RunID)
		if err != nil {
			_ = b.emitCommandFailed(ctx, run.SessionID, storageKey, run.Owner, run.TurnID, run.ClientNonce, "hermes_reconcile_failed", err.Error())
			b.record(func(r Recorder) { r.RunTerminal("command_failed") })
			b.recordRunDuration("command_failed", run.StartedAt)
			_ = b.active.ClearHermesActiveRun(ctx, run.Owner, run.SessionID, run.TurnID, run.RunID)
			if firstErr == nil {
				firstErr = fmt.Errorf("get hermes run %s: %w", run.RunID, err)
			}
			continue
		}

		switch normalizedHermesStatus(status.Status) {
		case "completed", "failed", "cancelled":
			if err := b.reconcileTerminalStatus(ctx, run, status); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if clearErr := b.active.ClearHermesActiveRun(ctx, run.Owner, run.SessionID, run.TurnID, run.RunID); clearErr != nil && firstErr == nil {
				firstErr = fmt.Errorf("clear terminal hermes run %s: %w", run.RunID, clearErr)
			}
		case "started", "running", "queued", "stopping", "":
			b.startRunStream(run)
		default:
			_ = b.emitCommandFailed(ctx, run.SessionID, storageKey, run.Owner, run.TurnID, run.ClientNonce, "hermes_unknown_run_status", status.Status)
			b.record(func(r Recorder) { r.RunTerminal("command_failed") })
			b.recordRunDuration("command_failed", run.StartedAt)
			_ = b.active.ClearHermesActiveRun(ctx, run.Owner, run.SessionID, run.TurnID, run.RunID)
			if firstErr == nil {
				firstErr = fmt.Errorf("hermes run %s has unknown status %q", run.RunID, status.Status)
			}
		}
	}
	return firstErr
}

func (b *Bridge) reconcileTerminalStatus(ctx context.Context, run sessionmodel.HermesActiveRun, status RunStatus) error {
	storageKey := sessionmodel.SessionStorageKey(b.scope, run.SessionID)
	translator := NewTranslator(TranslatorConfig{
		SessionID:         run.SessionID,
		SessionStorageKey: storageKey,
		Email:             run.Owner,
		TurnID:            run.TurnID,
		ClientNonce:       run.ClientNonce,
	})
	b.record(func(r Recorder) { r.RunEvent("run." + normalizedHermesStatus(status.Status)) })
	events := translator.TranslateRunStatus(status)
	for _, event := range events {
		if err := b.store.Upsert(ctx, event); err != nil {
			return fmt.Errorf("reconcile hermes run %s upsert: %w", run.RunID, err)
		}
	}
	terminal := translator.Terminal()
	if terminal == "" {
		terminal = "command_failed"
	}
	b.record(func(r Recorder) { r.RunTerminal(terminal) })
	b.recordRunDuration(terminal, run.StartedAt)
	if b.rows != nil {
		b.rows.PublishCurrentRow(context.Background(), run.Owner, run.SessionID)
	}
	return nil
}

func normalizedHermesStatus(status string) string {
	trimmed := strings.TrimSpace(status)
	switch trimmed {
	case "completed", "failed", "cancelled", "started", "running", "queued", "stopping":
		return trimmed
	case "canceled":
		return "cancelled"
	default:
		return trimmed
	}
}

func (b *Bridge) recordRunDuration(terminal, startedAt string) {
	started := parseHermesStartedAt(startedAt)
	b.record(func(r Recorder) { r.RunDuration(terminal, durationSecondsSince(started)) })
}

func durationSecondsSince(started time.Time) float64 {
	seconds := time.Since(started).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

// turn.command_failed is emitted directly so the UI's "stopping" state
// still resolves (durable-terminal contract from #532).
func (b *Bridge) StopTurn(ctx context.Context, sessionID, owner, turnID, clientNonce string) error {
	b.mu.Lock()
	at, ok := b.activeTurns[sessionID+":"+turnID]
	b.mu.Unlock()
	storageKey := sessionmodel.SessionStorageKey(b.scope, sessionID)
	runID := ""
	if ok {
		runID = at.runID
	}

	if !ok {
		if b.active != nil {
			run, found, err := b.active.GetHermesActiveRun(ctx, owner, sessionID, turnID)
			if err != nil {
				_ = b.emitCommandFailed(ctx, sessionID, storageKey, owner, turnID, clientNonce, "hermes_active_run_lookup_failed", err.Error())
				b.record(func(r Recorder) { r.RunTerminal("command_failed") })
				return fmt.Errorf("hermes active run lookup: %w", err)
			}
			if found {
				at = b.startRunStream(run)
				runID = run.RunID
			}
		}
	}
	if runID == "" {
		terminal, err := b.store.FindTurnTerminal(ctx, sessionID, turnID)
		if err != nil {
			_ = b.emitCommandFailed(ctx, sessionID, storageKey, owner, turnID, clientNonce, "hermes_terminal_lookup_failed", err.Error())
			b.record(func(r Recorder) { r.RunTerminal("command_failed") })
			return fmt.Errorf("hermes terminal lookup: %w", err)
		}
		if terminal != nil {
			// Race: the terminal event landed between client click and server
			// receipt. Preserve the explicit stop click as durable evidence.
			return b.emitInterruptRequested(ctx, sessionID, storageKey, owner, turnID, clientNonce)
		}
		if err := b.emitCommandFailed(ctx, sessionID, storageKey, owner, turnID, clientNonce, "hermes_active_run_missing", "no in-memory or durable Hermes run was found for this turn"); err != nil {
			return err
		}
		b.record(func(r Recorder) { r.RunTerminal("command_failed") })
		return nil
	}
	if _, err := b.client.StopRun(ctx, runID); err != nil {
		if at != nil {
			at.forceTerminal("command_failed")
			at.cancel()
		}
		_ = b.emitCommandFailed(ctx, sessionID, storageKey, owner, turnID, clientNonce, "hermes_stop_failed", err.Error())
		if b.active != nil {
			_ = b.active.ClearHermesActiveRun(context.Background(), owner, sessionID, turnID, runID)
		}
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
