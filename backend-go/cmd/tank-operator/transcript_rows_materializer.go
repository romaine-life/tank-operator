package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type transcriptRowsMaterializer struct {
	events store.SessionEventStore
	rows   store.SessionTranscriptRowStore
	turns  store.SessionTurnStore
}

type transcriptRowsMaterializationTxStore interface {
	WithTranscriptMaterializationTx(context.Context, string, func(context.Context, pgx.Tx) error) error
	ReplaceForTurnTx(context.Context, pgx.Tx, string, string, []map[string]any) error
	ReplaceForSessionTx(context.Context, pgx.Tx, string, []map[string]any) error
	UpsertRowsTx(context.Context, pgx.Tx, string, []map[string]any) error
	NeedsBackfillTx(context.Context, pgx.Tx, string) (bool, error)
}

type transcriptEventsTxStore interface {
	EventsForTurnAfterTx(context.Context, pgx.Tx, string, string, string, int) (store.SessionEventPage, error)
	ListBySessionTx(context.Context, pgx.Tx, string, store.SessionEventCursor, int) (store.SessionEventPage, error)
}

// readAllTurnEventsTx reads every event of a turn in ASC order inside a tx by
// paging the turn-scoped cursor to exhaustion. The materializer folds the
// COMPLETE turn so the stored turn-activity shell's terminal/active status can
// never be a casualty of a bounded read — the bug that made a finished long
// turn render as perpetually active. (Bounded-cost incremental re-projection of
// only the live page is the named follow-up; correctness comes first.)
func readAllTurnEventsTx(ctx context.Context, events transcriptEventsTxStore, tx pgx.Tx, sessionID, turnID string) ([]map[string]any, error) {
	var all []map[string]any
	cursor := ""
	for {
		page, err := events.EventsForTurnAfterTx(ctx, tx, sessionID, turnID, cursor, turnPageReadBatch)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Events...)
		if page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	return adoptLeadingSessionLifecycleTx(ctx, events, tx, sessionID, all)
}

// adoptLeadingSessionLifecycleTx is the in-transaction twin of
// adoptLeadingSessionLifecycle: it folds the session-startup lifecycle into the
// first turn's materialization so the durable /timeline rows match the lazy
// /activity body.
func adoptLeadingSessionLifecycleTx(ctx context.Context, events transcriptEventsTxStore, tx pgx.Tx, sessionID string, turnEvents []map[string]any) ([]map[string]any, error) {
	bound := firstEventOrderKey(turnEvents)
	if bound == "" {
		return turnEvents, nil
	}
	var lifecycle []map[string]any
	cursor := ""
	for {
		page, err := events.ListBySessionTx(ctx, tx, sessionID, store.SessionEventCursor{AfterOrderKey: cursor}, turnPageReadBatch)
		if err != nil {
			return nil, err
		}
		adopt, stop, prior := scanLeadingLifecycle(page.Events, bound)
		if prior {
			return turnEvents, nil
		}
		lifecycle = append(lifecycle, adopt...)
		if stop || page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	if len(lifecycle) == 0 {
		return turnEvents, nil
	}
	return append(lifecycle, turnEvents...), nil
}

// RefreshEventBatch implements sessionbus.TranscriptRefresher: one coalesced
// projection pass for a batch of just-persisted events. Within one session,
// session-scope triggers (turn.input_answered, a turn whose events contain a
// background-wake boundary) escalate the whole batch to a single session
// re-projection; otherwise each distinct turn re-projects exactly once
// regardless of how many of its events the batch carries, and turn-less
// events project individually. This coalescing is the PR-1 amortization from
// tank-operator#1051 — N flood events on one turn cost one full-turn read
// instead of N. The remaining O(turn) read goes away with the checkpointed
// projector (same issue, PR 2).
func (m transcriptRowsMaterializer) RefreshEventBatch(ctx context.Context, events []map[string]any) error {
	if m.events == nil || m.rows == nil || len(events) == 0 {
		return nil
	}
	// The persister batches per session, but out-of-band callers (advisory
	// repair, the startup reconciler) may hand a mixed batch — group
	// defensively, preserving first-seen session order.
	bySession := make(map[string][]map[string]any)
	var order []string
	for _, event := range events {
		sessionID := transcriptMaterializerSessionID(event)
		if sessionID == "" {
			continue
		}
		if _, ok := bySession[sessionID]; !ok {
			order = append(order, sessionID)
		}
		bySession[sessionID] = append(bySession[sessionID], event)
	}
	for _, sessionID := range order {
		if err := m.refreshSessionBatch(ctx, sessionID, bySession[sessionID]); err != nil {
			return err
		}
	}
	return nil
}

func (m transcriptRowsMaterializer) refreshSessionBatch(ctx context.Context, sessionID string, events []map[string]any) error {
	txRows, rowsOK := m.rows.(transcriptRowsMaterializationTxStore)
	txEvents, eventsOK := m.events.(transcriptEventsTxStore)
	if !rowsOK || !eventsOK {
		// Store doubles without tx support (unit-test seams) take the
		// per-event path; semantics are identical — coalescing is purely
		// a cost optimization.
		for _, event := range events {
			if err := m.RefreshEvent(ctx, event); err != nil {
				return err
			}
		}
		return nil
	}
	sessionScope := false
	var noTurn []map[string]any
	var turnOrder []string
	seenTurn := map[string]bool{}
	for _, event := range events {
		if transcriptString(event, "type") == "turn.input_answered" {
			sessionScope = true
		}
		turnID := transcriptString(event, "turn_id")
		if turnID == "" {
			noTurn = append(noTurn, event)
			continue
		}
		if !seenTurn[turnID] {
			seenTurn[turnID] = true
			turnOrder = append(turnOrder, turnID)
		}
	}
	return txRows.WithTranscriptMaterializationTx(ctx, sessionID, func(ctx context.Context, tx pgx.Tx) error {
		if sessionScope {
			// The session re-projection reads the whole ledger, so it
			// already covers every turn-less and per-turn event in the
			// batch.
			return m.backfillSessionTx(ctx, tx, txEvents, txRows, sessionID)
		}
		for _, event := range noTurn {
			projection := projectTranscriptEvents([]map[string]any{event})
			recordTranscriptProjectionInvariantViolations(sessionID, "", []map[string]any{event}, projection.Entries)
			if err := txRows.UpsertRowsTx(ctx, tx, sessionID, projection.Entries); err != nil {
				return err
			}
		}
		for _, turnID := range turnOrder {
			turnEvents, err := readAllTurnEventsTx(ctx, txEvents, tx, sessionID, turnID)
			if err != nil {
				return err
			}
			if turnEventsContainBackgroundWake(turnEvents) {
				// One session re-projection covers the remaining turns
				// in the batch too.
				return m.backfillSessionTx(ctx, tx, txEvents, txRows, sessionID)
			}
			projection := projectTranscriptEvents(turnEvents)
			recordTranscriptProjectionInvariantViolations(sessionID, turnID, turnEvents, projection.Entries)
			if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
				stampTurnNumbers(sessionID, numbers, projection.Entries)
			}
			if err := txRows.ReplaceForTurnTx(ctx, tx, sessionID, turnID, projection.Entries); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m transcriptRowsMaterializer) RefreshEvent(ctx context.Context, event map[string]any) error {
	if m.events == nil || m.rows == nil || event == nil {
		return nil
	}
	sessionID := transcriptMaterializerSessionID(event)
	if sessionID == "" {
		return nil
	}
	turnID := transcriptString(event, "turn_id")
	if txRows, ok := m.rows.(transcriptRowsMaterializationTxStore); ok {
		if txEvents, ok := m.events.(transcriptEventsTxStore); ok {
			return txRows.WithTranscriptMaterializationTx(ctx, sessionID, func(ctx context.Context, tx pgx.Tx) error {
				return m.refreshEventTx(ctx, tx, txEvents, txRows, sessionID, turnID, event)
			})
		}
	}
	if transcriptString(event, "type") == "turn.input_answered" {
		return m.refreshSession(ctx, sessionID)
	}
	if turnID == "" {
		projection := projectTranscriptEvents([]map[string]any{event})
		recordTranscriptProjectionInvariantViolations(sessionID, "", []map[string]any{event}, projection.Entries)
		return m.rows.UpsertRows(ctx, sessionID, projection.Entries)
	}
	turnEvents, err := readAllTurnEvents(ctx, m.events, sessionID, turnID)
	if err != nil {
		return err
	}
	if turnEventsContainBackgroundWake(turnEvents) {
		return m.refreshSession(ctx, sessionID)
	}
	projection := projectTranscriptEvents(turnEvents)
	recordTranscriptProjectionInvariantViolations(sessionID, turnID, turnEvents, projection.Entries)
	if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	return m.rows.ReplaceForTurn(ctx, sessionID, turnID, projection.Entries)
}

func (m transcriptRowsMaterializer) refreshEventTx(
	ctx context.Context,
	tx pgx.Tx,
	events transcriptEventsTxStore,
	rows transcriptRowsMaterializationTxStore,
	sessionID string,
	turnID string,
	event map[string]any,
) error {
	if turnID == "" {
		projection := projectTranscriptEvents([]map[string]any{event})
		recordTranscriptProjectionInvariantViolations(sessionID, "", []map[string]any{event}, projection.Entries)
		return rows.UpsertRowsTx(ctx, tx, sessionID, projection.Entries)
	}
	if transcriptString(event, "type") == "turn.input_answered" {
		return m.backfillSessionTx(ctx, tx, events, rows, sessionID)
	}
	turnEvents, err := readAllTurnEventsTx(ctx, events, tx, sessionID, turnID)
	if err != nil {
		return err
	}
	if turnEventsContainBackgroundWake(turnEvents) {
		return m.backfillSessionTx(ctx, tx, events, rows, sessionID)
	}
	projection := projectTranscriptEvents(turnEvents)
	recordTranscriptProjectionInvariantViolations(sessionID, turnID, turnEvents, projection.Entries)
	if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	return rows.ReplaceForTurnTx(ctx, tx, sessionID, turnID, projection.Entries)
}

func turnEventsContainBackgroundWake(events []map[string]any) bool {
	for _, event := range events {
		if isBackgroundTaskWakeTurnEvent(event) {
			return true
		}
	}
	return false
}

func (m transcriptRowsMaterializer) EnsureSession(ctx context.Context, sessionID string) error {
	if m.events == nil || m.rows == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	started := time.Now()
	needed, err := m.rows.NeedsBackfill(ctx, sessionID)
	if err != nil {
		recordTranscriptRowMaterialization("on_demand", transcriptRowMaterializationFailureResult(ctx, err), time.Since(started))
		return err
	}
	if !needed {
		recordTranscriptRowMaterialization("on_demand", "fresh", time.Since(started))
		return nil
	}
	backfilled, err := m.BackfillSession(ctx, sessionID)
	if err != nil {
		recordTranscriptRowMaterialization("on_demand", transcriptRowMaterializationFailureResult(ctx, err), time.Since(started))
		return fmt.Errorf("backfill transcript rows for session %s: %w", sessionID, err)
	}
	if backfilled {
		recordTranscriptRowMaterialization("on_demand", "backfilled", time.Since(started))
	} else {
		recordTranscriptRowMaterialization("on_demand", "fresh", time.Since(started))
	}
	return nil
}

func (m transcriptRowsMaterializer) BackfillSession(ctx context.Context, sessionID string) (bool, error) {
	if m.events == nil || m.rows == nil {
		return false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	if txRows, ok := m.rows.(transcriptRowsMaterializationTxStore); ok {
		if txEvents, ok := m.events.(transcriptEventsTxStore); ok {
			backfilled := false
			err := txRows.WithTranscriptMaterializationTx(ctx, sessionID, func(ctx context.Context, tx pgx.Tx) error {
				needed, err := txRows.NeedsBackfillTx(ctx, tx, sessionID)
				if err != nil || !needed {
					return err
				}
				if err := m.backfillSessionTx(ctx, tx, txEvents, txRows, sessionID); err != nil {
					return err
				}
				backfilled = true
				return nil
			})
			return backfilled, err
		}
	}
	needed, err := m.rows.NeedsBackfill(ctx, sessionID)
	if err != nil || !needed {
		return false, err
	}
	if err := m.refreshSession(ctx, sessionID); err != nil {
		return false, err
	}
	return true, nil
}

func (m transcriptRowsMaterializer) refreshSession(ctx context.Context, sessionID string) error {
	var events []map[string]any
	cursor := ""
	for {
		page, err := m.events.ListBySession(ctx, sessionID, store.SessionEventCursor{
			AfterOrderKey: cursor,
		}, 1000)
		if err != nil {
			return err
		}
		events = append(events, page.Events...)
		if page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	projection := projectTranscriptEvents(events)
	recordTranscriptProjectionInvariantViolations(sessionID, "", events, projection.Entries)
	if numbers, ok := m.turnNumbersForSession(ctx, sessionID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	if err := m.rows.ReplaceForSession(ctx, sessionID, projection.Entries); err != nil {
		return err
	}
	return nil
}

func (m transcriptRowsMaterializer) backfillSessionTx(
	ctx context.Context,
	tx pgx.Tx,
	eventsStore transcriptEventsTxStore,
	rowsStore transcriptRowsMaterializationTxStore,
	sessionID string,
) error {
	var events []map[string]any
	cursor := ""
	for {
		page, err := eventsStore.ListBySessionTx(ctx, tx, sessionID, store.SessionEventCursor{
			AfterOrderKey: cursor,
		}, 1000)
		if err != nil {
			return err
		}
		events = append(events, page.Events...)
		if page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	projection := projectTranscriptEvents(events)
	recordTranscriptProjectionInvariantViolations(sessionID, "", events, projection.Entries)
	if numbers, ok := m.turnNumbersForSession(ctx, sessionID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	return rowsStore.ReplaceForSessionTx(ctx, tx, sessionID, projection.Entries)
}

func transcriptRowMaterializationFailureResult(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return "failed"
}

func recordTranscriptProjectionInvariantViolations(sessionID, turnID string, events []map[string]any, entries []map[string]any) {
	terminalByTurn := map[string]string{}
	for _, event := range events {
		eventTurnID := transcriptString(event, "turn_id")
		if eventTurnID == "" {
			continue
		}
		switch transcriptString(event, "type") {
		case "turn.completed":
			terminalByTurn[eventTurnID] = "completed"
		case "turn.failed", "turn.command_failed":
			terminalByTurn[eventTurnID] = "failed"
		case "turn.interrupted":
			terminalByTurn[eventTurnID] = "interrupted"
		}
	}
	for _, entry := range entries {
		if transcriptMapString(entry, "kind") != "turn_activity" {
			continue
		}
		entryTurnID := transcriptMapString(entry, "turnId")
		if turnID != "" && entryTurnID != turnID {
			continue
		}
		terminalStatus := terminalByTurn[entryTurnID]
		if terminalStatus == "" {
			continue
		}
		activity := transcriptMap(entry, "activity")
		if activity["active"] != true && transcriptMapString(activity, "status") != "active" {
			continue
		}
		recordTranscriptMaterializationInvariantViolation("active_shell_after_terminal", terminalStatus)
		slog.Warn("transcript materialization invariant violation",
			"invariant", "active_shell_after_terminal",
			"session_id", sessionID,
			"turn_id", entryTurnID,
			"terminal_status", terminalStatus,
		)
	}
}

func transcriptMaterializerSessionID(event map[string]any) string {
	if sessionID := transcriptString(event, "session_id"); sessionID != "" {
		return sessionID
	}
	if storageKey := transcriptString(event, "tank_session_id"); storageKey != "" {
		_, sessionID := sessionbus.StorageScopeAndSessionID(storageKey)
		return strings.TrimSpace(sessionID)
	}
	return ""
}

// turnNumberingActive reports whether durable per-session turn numbering is
// available. In degraded/no-Postgres mode the store is the always-misses stub,
// so stamping is skipped and the missing-number counter is not spammed.
func turnNumberingActive(s store.SessionTurnStore) bool {
	if s == nil {
		return false
	}
	_, isStub := s.(store.StubSessionTurnStore)
	return !isStub
}

// turnNumbersForTurn returns the {turn_id: number} map for a single turn. ok is
// false when numbering is inactive or the read errored — in both cases the
// caller skips stamping for this round (the shell is re-stamped on the turn's
// next event) rather than recording a false miss. ok is true with an empty map
// only when the turn genuinely has no number yet, which the stamping pass then
// records as a missing-number invariant violation.
func (m transcriptRowsMaterializer) turnNumbersForTurn(ctx context.Context, sessionID, turnID string) (map[string]int64, bool) {
	if !turnNumberingActive(m.turns) || strings.TrimSpace(turnID) == "" {
		return nil, false
	}
	number, ok, err := m.turns.TurnNumberForTurnID(ctx, sessionID, turnID)
	if err != nil {
		slog.Warn("read durable turn number", "session_id", sessionID, "turn_id", turnID, "error", err)
		return nil, false
	}
	if !ok {
		return map[string]int64{}, true
	}
	return map[string]int64{turnID: number}, true
}

// turnNumbersForSession returns the whole-session {turn_id: number} map for the
// session/backfill projection paths. ok follows the same contract as
// turnNumbersForTurn.
func (m transcriptRowsMaterializer) turnNumbersForSession(ctx context.Context, sessionID string) (map[string]int64, bool) {
	if !turnNumberingActive(m.turns) {
		return nil, false
	}
	numbers, err := m.turns.TurnNumbersForSession(ctx, sessionID)
	if err != nil {
		slog.Warn("read durable turn numbers", "session_id", sessionID, "error", err)
		return nil, false
	}
	return numbers, true
}

// stampTurnNumbers sets turnNumber on every turn-tagged transcript row from
// the durable session_turns map. Turn activity shells are the primary consumer,
// and assistant AskUserQuestion messages also need the number for their linked
// question turn.
func stampTurnNumbers(sessionID string, numbers map[string]int64, entries []map[string]any) {
	for _, entry := range entries {
		turnID := transcriptMapString(entry, "turnId")
		if turnID == "" {
			continue
		}
		if number, ok := numbers[turnID]; ok {
			entry["turnNumber"] = number
		}
		if awaiting, _ := entry["awaitingInput"].(map[string]any); awaiting != nil {
			if questionTurnID := transcriptMapString(awaiting, "questionTurnId"); questionTurnID != "" {
				if number, ok := numbers[questionTurnID]; ok {
					awaiting["questionTurnNumber"] = number
				}
			}
		}
		if transcriptMapString(entry, "kind") == "turn_activity" {
			// Background-wake continuation turns are unnumbered BY DESIGN:
			// migration 0139 excludes them from the allocator because
			// numbering them minted separately navigable /turns/{n} for
			// continuation mechanics (the session-655 turn 56/57 defect).
			// Counting them here made TankTurnNumberMissing fire on intended
			// state — 12 standing false alerts during the 2026-06-11
			// incident, drowning the real signal the alert exists for
			// (allocation-trigger regressions on user-visible turns).
			if _, ok := numbers[turnID]; !ok && !isBackgroundWakeTurnID(turnID) {
				recordTurnNumberMissing("materialize")
				slog.Warn("durable turn number missing for materialized shell",
					"session_id", sessionID,
					"turn_id", turnID,
				)
			}
		}
	}
}
