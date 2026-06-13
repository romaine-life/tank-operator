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
