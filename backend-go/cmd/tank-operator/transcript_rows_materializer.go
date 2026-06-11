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
	// Checkpointed-fold state (tank-operator#1051 B3), advanced in the same
	// transaction as the row writes it describes.
	LoadFoldStateTx(context.Context, pgx.Tx, string) ([]byte, bool, error)
	SaveFoldStateTx(context.Context, pgx.Tx, string, []byte) error
	DeleteFoldStateTx(context.Context, pgx.Tx, string) error
	DisableFoldTx(context.Context, pgx.Tx, string) error
}

type transcriptEventsTxStore interface {
	EventsForTurnAfterTx(context.Context, pgx.Tx, string, string, string, int) (store.SessionEventPage, error)
	ListBySessionTx(context.Context, pgx.Tx, string, store.SessionEventCursor, int) (store.SessionEventPage, error)
}

// readAllTurnEventsTx reads every event of a turn in ASC order inside a tx by
// paging the turn-scoped cursor to exhaustion. The materializer folds the
// COMPLETE turn so the stored turn-activity shell's terminal/active status can
// never be a casualty of a bounded read — the bug that made a finished long
// turn render as perpetually active. This full read is the REFERENCE path:
// flood-class events take the checkpointed fold
// (transcript_fold_checkpoint.go) and never reach it.
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
		switch transcriptString(event, "type") {
		case "turn.input_answered":
			sessionScope = true
		case "shell_task.started", "shell_task.updated", "shell_task.exited":
			// Background-task lifecycle changes the wake forest, and a turn
			// that has absorbed wake content cannot be correctly re-projected
			// turn-scope: the per-turn read can't see the wake turns' folded
			// entries, so a per-turn replace would silently shed the merge.
			// (The checkpointed fold above handles flood-class shell_task
			// updates without this escalation; this is the REFERENCE path's
			// conservative rule.)
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
		// Checkpointed fold (tank-operator#1051 B2+B3): when the session has
		// a live memo and every event in the batch is flood-class, advance
		// the memo and rewrite only the changed shell rows — no ledger read
		// at all. The fold's own classifier rejects structure-class events
		// (terminals, boundaries, new/exiting tasks, answers), so it gets
		// first try unconditionally; anything outside its confident envelope
		// falls through to the reference projection below, which reseeds or
		// invalidates the memo. sessionScope only steers the reference path.
		if len(turnOrder) > 0 && len(noTurn) == 0 {
			if done, err := m.tryFoldBatchTx(ctx, tx, txRows, sessionID, events); done || err != nil {
				return err
			}
		}
		if sessionScope {
			// The session re-projection reads the whole ledger, so it
			// already covers every turn-less and per-turn event in the
			// batch.
			return m.resyncSessionTx(ctx, tx, txEvents, txRows, sessionID)
		}
		for _, event := range noTurn {
			projection := projectTranscriptEvents([]map[string]any{event})
			recordTranscriptProjectionInvariantViolations(sessionID, "", []map[string]any{event}, projection.Entries)
			if err := txRows.UpsertRowsTx(ctx, tx, sessionID, projection.Entries); err != nil {
				return err
			}
		}
		invalidatedMemo := false
		for _, turnID := range turnOrder {
			turnEvents, err := readAllTurnEventsTx(ctx, txEvents, tx, sessionID, turnID)
			if err != nil {
				return err
			}
			if turnEventsContainBackgroundWake(turnEvents) || turnEventsContainShellTask(turnEvents) {
				// One session re-projection covers the remaining turns
				// in the batch too.
				return m.resyncSessionTx(ctx, tx, txEvents, txRows, sessionID)
			}
			projection := projectTranscriptEvents(turnEvents)
			recordTranscriptProjectionInvariantViolations(sessionID, turnID, turnEvents, projection.Entries)
			if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
				stampTurnNumbers(sessionID, numbers, projection.Entries)
			}
			if err := txRows.ReplaceForTurnTx(ctx, tx, sessionID, turnID, projection.Entries); err != nil {
				return err
			}
			if !invalidatedMemo {
				// A turn-scope reference projection changes rows the memo
				// doesn't know about. Invalidate rather than rebuild: a
				// rebuild needs a full session read, which is exactly the
				// cost a turn-scope batch exists to avoid. The next
				// session-scope projection reseeds the memo.
				invalidatedMemo = true
				recordTranscriptFold("invalidated")
				if err := txRows.DeleteFoldStateTx(ctx, tx, sessionID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// tryFoldBatchTx attempts the checkpointed-fold fast path. done=true means
// the batch is fully handled (rows written, memo saved). done=false means the
// caller must run the reference projection; the memo, if any, is left intact
// for the reference path to invalidate or reseed.
func (m transcriptRowsMaterializer) tryFoldBatchTx(
	ctx context.Context,
	tx pgx.Tx,
	txRows transcriptRowsMaterializationTxStore,
	sessionID string,
	events []map[string]any,
) (bool, error) {
	raw, disabled, err := txRows.LoadFoldStateTx(ctx, tx, sessionID)
	if err != nil {
		// The fold is an optimization; a state-read failure must not block
		// the durable projection. The reference path still runs.
		slog.Warn("fold state load failed; using reference projection",
			"session_id", sessionID, "error", err)
		recordTranscriptFold("load_error")
		return false, nil
	}
	if disabled {
		recordTranscriptFold("disabled")
		return false, nil
	}
	memo := unmarshalSessionFoldMemo(raw)
	if memo == nil {
		return false, nil
	}
	// Turn-less events ride their own UpsertRows path and never touch
	// shells; their presence alongside foldable events is fine, but they are
	// handled by the caller, so only attempt the fold when every event in
	// the batch carries a turn.
	for _, event := range events {
		if transcriptString(event, "turn_id") == "" {
			return false, nil
		}
	}
	started := time.Now()
	changed, ok := memo.foldBatch(events)
	if !ok {
		recordTranscriptFold("resync")
		return false, nil
	}
	// Build every changed shell BEFORE writing anything: a turn whose merged
	// body includes a promotion-class entry sends the whole batch to the
	// reference path, and at that point no fold writes may have landed.
	var rows []map[string]any
	for _, turnID := range sortedFoldTurnIDs(changed) {
		row, emit, foldOK := memo.shellRowForTurn(turnID)
		if !foldOK {
			recordTranscriptFold("resync")
			return false, nil
		}
		if !emit {
			continue
		}
		if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
			stampTurnNumbers(sessionID, numbers, []map[string]any{row})
		}
		rows = append(rows, row)
	}
	for _, row := range rows {
		if err := txRows.UpsertRowsTx(ctx, tx, sessionID, []map[string]any{row}); err != nil {
			return false, err
		}
	}
	encoded, fits := marshalSessionFoldMemo(memo)
	if !fits {
		// The rows just written are correct; only the memo outgrew its cap.
		// Durably opt the session out so it batch-projects from here on.
		recordTranscriptFold("disabled_size")
		return true, txRows.DisableFoldTx(ctx, tx, sessionID)
	}
	if err := txRows.SaveFoldStateTx(ctx, tx, sessionID, encoded); err != nil {
		return false, err
	}
	recordTranscriptFold("folded")
	recordTranscriptFoldDuration(time.Since(started))
	// Production shadow-compare (tank-operator#1051 B5): on a sampled
	// fraction of folded batches, re-derive the written shells from a full
	// reference projection and diff. A divergence is counted, paged
	// (TankTranscriptFoldShadowDivergence), and healed in this same
	// transaction by the reference re-projection — the user never sees the
	// wrong rows beyond this batch. The fixture harness proves equivalence
	// for known shapes; the shadow is the net for shapes production invents.
	if transcriptFoldShadowDue() {
		if txEvents, ok := m.events.(transcriptEventsTxStore); ok {
			if err := m.shadowCompareFoldTx(ctx, tx, txEvents, txRows, sessionID, rows); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// sessionResyncSlots caps CONCURRENT session-scope re-projections per pod.
// The per-session worker isolation that keeps one session from starving
// another also means N cold sessions (post-deploy, no fold memos yet) can
// start N full-ledger reads simultaneously — five parallel multi-thousand-row
// reads wedged the B1ms instance outright on the 2026-06-12 post-#1051
// deploys: every Postgres caller timed out and the persister made zero
// progress. One heavy read at a time is proven fine on this instance class;
// queued workers wait here while flood-class folds (no ledger read) continue
// unimpeded on other sessions.
var sessionResyncSlots = make(chan struct{}, 1)

// resyncSessionTx is the reference projection: re-project the whole session
// and reseed the fold memo from the same events, in the same transaction.
func (m transcriptRowsMaterializer) resyncSessionTx(
	ctx context.Context,
	tx pgx.Tx,
	eventsStore transcriptEventsTxStore,
	rowsStore transcriptRowsMaterializationTxStore,
	sessionID string,
) error {
	select {
	case sessionResyncSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-sessionResyncSlots }()
	events, err := m.backfillSessionEventsTx(ctx, tx, eventsStore, rowsStore, sessionID)
	if err != nil {
		return err
	}
	memo := buildSessionFoldMemo(events)
	encoded, fits := marshalSessionFoldMemo(memo)
	if !fits {
		recordTranscriptFold("disabled_size")
		return rowsStore.DisableFoldTx(ctx, tx, sessionID)
	}
	recordTranscriptFold("reseeded")
	return rowsStore.SaveFoldStateTx(ctx, tx, sessionID, encoded)
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
	switch transcriptString(event, "type") {
	case "turn.input_answered", "shell_task.started", "shell_task.updated", "shell_task.exited":
		// Cross-turn structure: answers backpatch earlier turns, and
		// background-task lifecycle changes the wake forest a per-turn
		// replace cannot see (it would shed a parent's folded wake content).
		return m.resyncSessionTx(ctx, tx, events, rows, sessionID)
	}
	turnEvents, err := readAllTurnEventsTx(ctx, events, tx, sessionID, turnID)
	if err != nil {
		return err
	}
	if turnEventsContainBackgroundWake(turnEvents) || turnEventsContainShellTask(turnEvents) {
		return m.resyncSessionTx(ctx, tx, events, rows, sessionID)
	}
	projection := projectTranscriptEvents(turnEvents)
	recordTranscriptProjectionInvariantViolations(sessionID, turnID, turnEvents, projection.Entries)
	if numbers, ok := m.turnNumbersForTurn(ctx, sessionID, turnID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	if err := rows.ReplaceForTurnTx(ctx, tx, sessionID, turnID, projection.Entries); err != nil {
		return err
	}
	// A turn-scope reference projection changed rows the fold memo doesn't
	// know about; invalidate it (the next session-scope projection reseeds).
	return rows.DeleteFoldStateTx(ctx, tx, sessionID)
}

func turnEventsContainBackgroundWake(events []map[string]any) bool {
	for _, event := range events {
		if isBackgroundTaskWakeTurnEvent(event) {
			return true
		}
	}
	return false
}

// turnEventsContainShellTask reports whether the turn started background
// work. Such a turn may have wake turns folded into its shell, and a
// turn-scope replace cannot see those wake turns' entries — re-projecting it
// turn-scope would silently shed the merge, so callers escalate to a session
// re-projection.
func turnEventsContainShellTask(events []map[string]any) bool {
	for _, event := range events {
		switch transcriptString(event, "type") {
		case "shell_task.started", "shell_task.updated", "shell_task.exited":
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
				if err := m.resyncSessionTx(ctx, tx, txEvents, txRows, sessionID); err != nil {
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

// backfillSessionEventsTx re-projects the whole session and returns the
// events it read, so resyncSessionTx can reseed the fold memo from the same
// ledger snapshot without a second read.
func (m transcriptRowsMaterializer) backfillSessionEventsTx(
	ctx context.Context,
	tx pgx.Tx,
	eventsStore transcriptEventsTxStore,
	rowsStore transcriptRowsMaterializationTxStore,
	sessionID string,
) ([]map[string]any, error) {
	var events []map[string]any
	cursor := ""
	for {
		page, err := eventsStore.ListBySessionTx(ctx, tx, sessionID, store.SessionEventCursor{
			AfterOrderKey: cursor,
		}, 1000)
		if err != nil {
			return nil, err
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
	if err := rowsStore.ReplaceForSessionTx(ctx, tx, sessionID, projection.Entries); err != nil {
		return nil, err
	}
	return events, nil
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
