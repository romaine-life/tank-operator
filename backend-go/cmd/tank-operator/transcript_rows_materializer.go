package main

import (
	"context"
	"fmt"
	"strings"

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
	if turnID == "" {
		projection := projectTranscriptEvents([]map[string]any{event})
		return m.rows.UpsertRows(ctx, sessionID, projection.Entries)
	}
	page, err := m.events.EventsForTurn(ctx, sessionID, turnID, turnActivityEventLimit)
	if err != nil {
		return err
	}
	projection := projectTranscriptEvents(page.Events)
	return m.rows.ReplaceForTurn(ctx, sessionID, turnID, projection.Entries)
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
	return m.rows.ReplaceForSession(ctx, sessionID, projection.Entries)
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
