package main

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// TestFoldShadowCompareMatchesAtFullSampling replays the background-task
// fixture with the shadow sampling at stride 1: every folded batch also runs
// the reference projection and diffs. The harness asserts the shadow engaged,
// matched every time, and never reported a divergence — the production net
// agreeing with the offline equivalence proof.
func TestFoldShadowCompareMatchesAtFullSampling(t *testing.T) {
	prior := transcriptFoldShadowSampleEvery
	transcriptFoldShadowSampleEvery = 1
	defer func() { transcriptFoldShadowSampleEvery = prior }()

	matchBefore := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("match"))
	divergenceBefore := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence"))

	events := foldFixtureEvents(t, "slot1_session_161_events.json")
	rows := newMemoryFoldRowsStore()
	eventsStore := &memoryFoldEventStore{}
	m := transcriptRowsMaterializer{events: eventsStore, rows: rows, turns: store.StubSessionTurnStore{}}

	const batchSize = 7
	for start := 0; start < len(events); start += batchSize {
		end := min(start+batchSize, len(events))
		batch := events[start:end]
		eventsStore.append(batch...)
		if start == 0 {
			if _, err := m.BackfillSession(context.Background(), transcriptMaterializerSessionID(batch[0])); err != nil {
				t.Fatalf("seed backfill: %v", err)
			}
		}
		if err := m.RefreshEventBatch(context.Background(), batch); err != nil {
			t.Fatalf("RefreshEventBatch at %d: %v", start, err)
		}
	}

	if got := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("match")) - matchBefore; got == 0 {
		t.Fatalf("shadow compare never engaged at stride 1")
	}
	if got := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence")) - divergenceBefore; got != 0 {
		t.Fatalf("shadow compare reported %v divergences on equivalent paths", got)
	}
}

// TestFoldShadowCompareIgnoresEventsPastFoldHorizon pins the #1130 fix. The
// persist pipeline commits events ahead of the async refresh queue, so when
// the shadow's in-transaction ledger scan runs, events NEWER than the fold's
// dequeued batch are routinely already visible. This replays the
// flood-heavy #1130 fixture with that race staged deliberately: before each
// RefreshEventBatch, the events store also holds the NEXT batch (committed
// but not yet dequeued). With the reference bounded at the memo's horizon
// the shadow must match every sampled batch; before the fix this fixture
// diverged on the live turn's shell (completedAt/endOrderKey lagging by
// exactly the racing tail) and burned an O(session) heal each time.
func TestFoldShadowCompareIgnoresEventsPastFoldHorizon(t *testing.T) {
	prior := transcriptFoldShadowSampleEvery
	transcriptFoldShadowSampleEvery = 1
	defer func() { transcriptFoldShadowSampleEvery = prior }()

	matchBefore := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("match"))
	divergenceBefore := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence"))

	events := foldFixtureEvents(t, "session_865_divergence_events.json")
	rows := newMemoryFoldRowsStore()
	eventsStore := &memoryFoldEventStore{}
	m := transcriptRowsMaterializer{events: eventsStore, rows: rows, turns: store.StubSessionTurnStore{}}

	const batchSize = 7
	appended := 0
	appendUpTo := func(end int) {
		if end > len(events) {
			end = len(events)
		}
		if end > appended {
			eventsStore.append(events[appended:end]...)
			appended = end
		}
	}
	for start := 0; start < len(events); start += batchSize {
		end := min(start+batchSize, len(events))
		batch := events[start:end]
		// The race: this batch AND the next are already committed to the
		// ledger before this batch is dequeued and folded.
		appendUpTo(end + batchSize)
		if start == 0 {
			if _, err := m.BackfillSession(context.Background(), transcriptMaterializerSessionID(batch[0])); err != nil {
				t.Fatalf("seed backfill: %v", err)
			}
		}
		if err := m.RefreshEventBatch(context.Background(), batch); err != nil {
			t.Fatalf("RefreshEventBatch at %d: %v", start, err)
		}
	}

	if got := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("match")) - matchBefore; got == 0 {
		t.Fatalf("shadow compare never engaged at stride 1")
	}
	if got := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence")) - divergenceBefore; got != 0 {
		t.Fatalf("shadow reported %v divergences for committed-but-undequeued tail events — the #1130 false positive", got)
	}
}

// TestFoldShadowHorizonStillCatchesTrueDefects pins that the horizon bound
// does not blind the shadow: a fold row that disagrees with the reference AT
// OR BELOW the horizon still diverges and heals, even while newer events sit
// past the bound.
func TestFoldShadowHorizonStillCatchesTrueDefects(t *testing.T) {
	divergenceBefore := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence"))

	events := foldFixtureEvents(t, "slot1_session_159_events.json")
	rows := newMemoryFoldRowsStore()
	eventsStore := &memoryFoldEventStore{}
	eventsStore.append(events...)
	m := transcriptRowsMaterializer{events: eventsStore, rows: rows, turns: store.StubSessionTurnStore{}}

	sessionID := transcriptMaterializerSessionID(events[0])
	horizon := transcriptString(events[len(events)-1], "order_key")
	tampered := map[string]any{
		"id":       "turn-activity-tampered",
		"kind":     "turn_activity",
		"turnId":   "turn-tampered",
		"orderKey": "000",
	}
	if err := m.shadowCompareFoldTx(context.Background(), nil, eventsStore, rows, sessionID, []map[string]any{tampered}, horizon); err != nil {
		t.Fatalf("shadowCompareFoldTx: %v", err)
	}
	if got := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence")) - divergenceBefore; got != 1 {
		t.Fatalf("divergence counter delta = %v, want 1: the horizon bound must not mask true defects", got)
	}
}

// TestFoldShadowCompareHealsDivergence pins the divergence path: a fold row
// that disagrees with the reference is counted, and the heal (reference
// re-projection in the same transaction) replaces the wrong rows and reseeds
// the memo.
func TestFoldShadowCompareHealsDivergence(t *testing.T) {
	divergenceBefore := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence"))

	events := foldFixtureEvents(t, "slot1_session_159_events.json")
	rows := newMemoryFoldRowsStore()
	eventsStore := &memoryFoldEventStore{}
	eventsStore.append(events...)
	m := transcriptRowsMaterializer{events: eventsStore, rows: rows, turns: store.StubSessionTurnStore{}}

	sessionID := transcriptMaterializerSessionID(events[0])
	// A row the reference projection cannot contain.
	tampered := map[string]any{
		"id":       "turn-activity-tampered",
		"kind":     "turn_activity",
		"turnId":   "turn-tampered",
		"orderKey": "zzz",
	}
	if err := m.shadowCompareFoldTx(context.Background(), nil, eventsStore, rows, sessionID, []map[string]any{tampered}, ""); err != nil {
		t.Fatalf("shadowCompareFoldTx: %v", err)
	}
	if got := testutil.ToFloat64(transcriptFoldShadowTotal.WithLabelValues("divergence")) - divergenceBefore; got != 1 {
		t.Fatalf("divergence counter delta = %v, want 1", got)
	}
	// The heal ran the reference projection: the row store now equals ground
	// truth and the memo is reseeded.
	want := groundTruthRows(eventsStore.all())
	if diff := diffRowSets(rows.rows, want); diff != "" {
		t.Fatalf("heal did not converge rows to reference:\n%s", diff)
	}
	if len(rows.foldMemo) == 0 && !rows.foldDisabled {
		t.Fatalf("heal did not reseed the fold memo")
	}
}
