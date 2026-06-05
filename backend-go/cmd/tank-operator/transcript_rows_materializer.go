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

type transcriptMaterializingEventStore struct {
	store.SessionEventStore
	materializer transcriptRowsMaterializer
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

func (s transcriptMaterializingEventStore) Upsert(ctx context.Context, event map[string]any) error {
	if err := s.SessionEventStore.Upsert(ctx, event); err != nil {
		return err
	}
	return s.materializer.RefreshEvent(ctx, event)
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
	if turnID == "" {
		projection := projectTranscriptEvents([]map[string]any{event})
		recordTranscriptProjectionInvariantViolations(sessionID, "", []map[string]any{event}, projection.Entries)
		return m.rows.UpsertRows(ctx, sessionID, projection.Entries)
	}
	turnEvents, err := readAllTurnEvents(ctx, m.events, sessionID, turnID)
	if err != nil {
		return err
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
	turnEvents, err := readAllTurnEventsTx(ctx, events, tx, sessionID, turnID)
	if err != nil {
		return err
	}
	projection := projectTranscriptEvents(turnEvents)
	recordTranscriptProjectionInvariantViolations(sessionID, turnID, turnEvents, projection.Entries)
	if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	return rows.ReplaceForTurnTx(ctx, tx, sessionID, turnID, projection.Entries)
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
	var events []map[string]any
	cursor := ""
	for {
		page, err := m.events.ListBySession(ctx, sessionID, store.SessionEventCursor{
			AfterOrderKey: cursor,
		}, 1000)
		if err != nil {
			return false, err
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
		return false, err
	}
	return true, nil
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

// stampTurnNumbers sets turnNumber on every turn_activity shell from the
// durable session_turns map. A shell whose turn_id has no number is the
// materialization-time analogue of the allocation invariant: it is recorded on
// the missing-number counter rather than failing the projection, so the
// durable transcript still renders while the regression is alerted on.
func stampTurnNumbers(sessionID string, numbers map[string]int64, entries []map[string]any) {
	for _, entry := range entries {
		if transcriptMapString(entry, "kind") != "turn_activity" {
			continue
		}
		turnID := transcriptMapString(entry, "turnId")
		if turnID == "" {
			continue
		}
		if number, ok := numbers[turnID]; ok {
			entry["turnNumber"] = number
			continue
		}
		recordTurnNumberMissing("materialize")
		slog.Warn("durable turn number missing for materialized shell",
			"session_id", sessionID,
			"turn_id", turnID,
		)
	}
}
