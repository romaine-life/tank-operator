package main

import (
	"context"
	"log/slog"
	"reflect"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// transcriptFoldShadowSampleEvery is the sampling stride for the production
// shadow-compare: one in every N successfully folded batches also runs the
// full reference projection and diffs the fold-written shells. The shadow
// read is O(session) — the very cost the fold removes — so it stays sampled;
// at 1-in-50 a flood session pays roughly 2% of the pre-fold read load for a
// continuous production equivalence net. A var (not const) so the harness can
// run at stride 1.
var transcriptFoldShadowSampleEvery uint64 = 50

var transcriptFoldShadowCounter atomic.Uint64

func transcriptFoldShadowDue() bool {
	every := transcriptFoldShadowSampleEvery
	if every == 0 {
		return false
	}
	return transcriptFoldShadowCounter.Add(1)%every == 0
}

// shadowCompareFoldTx re-derives the rows the fold just wrote from a full
// reference projection of the session ledger and diffs them. A match is
// counted; a divergence is counted, logged with the offending row ids, and
// healed in the same transaction by the reference re-projection (which also
// reseeds the memo) — so a fold defect costs one wrong-rows window of zero:
// the transaction that wrote them also corrects them.
func (m transcriptRowsMaterializer) shadowCompareFoldTx(
	ctx context.Context,
	tx pgx.Tx,
	txEvents transcriptEventsTxStore,
	txRows transcriptRowsMaterializationTxStore,
	sessionID string,
	foldRows []map[string]any,
) error {
	if len(foldRows) == 0 {
		recordTranscriptFoldShadow("match")
		return nil
	}
	var events []map[string]any
	cursor := ""
	for {
		page, err := txEvents.ListBySessionTx(ctx, tx, sessionID, store.SessionEventCursor{
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
	if numbers, ok := m.turnNumbersForSession(ctx, sessionID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	reference := map[string]map[string]any{}
	for _, entry := range projection.Entries {
		if id := transcriptMapString(entry, "id"); id != "" {
			reference[id] = entry
		}
	}
	var diverged []string
	for _, row := range foldRows {
		id := transcriptMapString(row, "id")
		want, ok := reference[id]
		if !ok || !reflect.DeepEqual(row, want) {
			diverged = append(diverged, id)
		}
	}
	if len(diverged) == 0 {
		recordTranscriptFoldShadow("match")
		return nil
	}
	recordTranscriptFoldShadow("divergence")
	slog.Error("transcript fold shadow divergence — healing via reference re-projection",
		"session_id", sessionID,
		"rows", diverged,
	)
	return m.resyncSessionTx(ctx, tx, txEvents, txRows, sessionID)
}
