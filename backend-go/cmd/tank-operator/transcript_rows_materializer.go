package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

type transcriptRowsMaterializer struct {
	events store.SessionEventStore
	rows   store.SessionTranscriptRowStore
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
}

type transcriptEventsTxStore interface {
	EventsForTurnTx(context.Context, pgx.Tx, string, string, int) (store.SessionEventPage, error)
	ListBySessionTx(context.Context, pgx.Tx, string, store.SessionEventCursor, int) (store.SessionEventPage, error)
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
	page, err := m.events.EventsForTurn(ctx, sessionID, turnID, turnActivityEventLimit)
	if err != nil {
		return err
	}
	projection := projectTranscriptEvents(page.Events)
	recordTranscriptProjectionInvariantViolations(sessionID, turnID, page.Events, projection.Entries)
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
	page, err := events.EventsForTurnTx(ctx, tx, sessionID, turnID, turnActivityEventLimit)
	if err != nil {
		return err
	}
	projection := projectTranscriptEvents(page.Events)
	recordTranscriptProjectionInvariantViolations(sessionID, turnID, page.Events, projection.Entries)
	return rows.ReplaceForTurnTx(ctx, tx, sessionID, turnID, projection.Entries)
}

func (m transcriptRowsMaterializer) Backfill(ctx context.Context) error {
	if m.events == nil || m.rows == nil {
		return nil
	}
	sessionIDs, err := m.rows.BackfillSessionIDs(ctx)
	if err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		if err := m.BackfillSession(ctx, sessionID); err != nil {
			return fmt.Errorf("backfill transcript rows for session %s: %w", sessionID, err)
		}
	}
	return nil
}

func (m transcriptRowsMaterializer) BackfillSession(ctx context.Context, sessionID string) error {
	if txRows, ok := m.rows.(transcriptRowsMaterializationTxStore); ok {
		if txEvents, ok := m.events.(transcriptEventsTxStore); ok {
			return txRows.WithTranscriptMaterializationTx(ctx, sessionID, func(ctx context.Context, tx pgx.Tx) error {
				return m.backfillSessionTx(ctx, tx, txEvents, txRows, sessionID)
			})
		}
	}
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
	return m.rows.ReplaceForSession(ctx, sessionID, projection.Entries)
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
	return rowsStore.ReplaceForSessionTx(ctx, tx, sessionID, projection.Entries)
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
