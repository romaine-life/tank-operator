package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// ---------------------------------------------------------------------------
// In-memory tx stores: faithful stand-ins for the Postgres row/event stores so
// the equivalence harness can drive the REAL RefreshEventBatch wiring —
// including fold-state load/save through []byte serialization, exactly the
// shape the durable checkpoint takes in production.
// ---------------------------------------------------------------------------

type memoryFoldEventStore struct {
	store.StubSessionEventStore
	events []map[string]any
}

func (s *memoryFoldEventStore) append(events ...map[string]any) {
	s.events = append(s.events, events...)
	sort.SliceStable(s.events, func(i, j int) bool {
		return transcriptString(s.events[i], "order_key") < transcriptString(s.events[j], "order_key")
	})
}

func (s *memoryFoldEventStore) all() []map[string]any {
	return append([]map[string]any(nil), s.events...)
}

func (s *memoryFoldEventStore) EventsForTurnAfterTx(_ context.Context, _ pgx.Tx, _ string, turnID, afterOrderKey string, _ int) (store.SessionEventPage, error) {
	var out []map[string]any
	for _, event := range s.events {
		if transcriptString(event, "turn_id") != turnID {
			continue
		}
		if afterOrderKey != "" && transcriptString(event, "order_key") <= afterOrderKey {
			continue
		}
		out = append(out, event)
	}
	return store.SessionEventPage{Events: out, FoundOldest: true, FoundNewest: true}, nil
}

func (s *memoryFoldEventStore) ListBySessionTx(_ context.Context, _ pgx.Tx, _ string, cursor store.SessionEventCursor, _ int) (store.SessionEventPage, error) {
	var out []map[string]any
	for _, event := range s.events {
		if cursor.AfterOrderKey != "" && transcriptString(event, "order_key") <= cursor.AfterOrderKey {
			continue
		}
		out = append(out, event)
	}
	return store.SessionEventPage{Events: out, FoundOldest: true, FoundNewest: true}, nil
}

type memoryFoldRowsStore struct {
	rows         map[string]map[string]any
	foldMemo     []byte
	foldTurns    map[string][]byte
	foldDisabled bool
	inTx         bool
}

func newMemoryFoldRowsStore() *memoryFoldRowsStore {
	return &memoryFoldRowsStore{rows: map[string]map[string]any{}}
}

func (s *memoryFoldRowsStore) WithTranscriptMaterializationTx(ctx context.Context, _ string, fn func(context.Context, pgx.Tx) error) error {
	s.inTx = true
	defer func() { s.inTx = false }()
	return fn(ctx, nil)
}

func (s *memoryFoldRowsStore) upsert(entries []map[string]any) {
	for _, entry := range entries {
		if id := transcriptMapString(entry, "id"); id != "" {
			s.rows[id] = entry
		}
	}
}

func (s *memoryFoldRowsStore) ReplaceForTurnTx(_ context.Context, _ pgx.Tx, _ string, turnID string, entries []map[string]any) error {
	for id, row := range s.rows {
		if transcriptMapString(row, "turnId") == turnID {
			delete(s.rows, id)
		}
	}
	s.upsert(entries)
	return nil
}

func (s *memoryFoldRowsStore) ReplaceForSessionTx(_ context.Context, _ pgx.Tx, _ string, entries []map[string]any) error {
	s.rows = map[string]map[string]any{}
	s.upsert(entries)
	return nil
}

func (s *memoryFoldRowsStore) UpsertRowsTx(_ context.Context, _ pgx.Tx, _ string, entries []map[string]any) error {
	s.upsert(entries)
	return nil
}

func (s *memoryFoldRowsStore) NeedsBackfillTx(context.Context, pgx.Tx, string) (bool, error) {
	return true, nil
}

func (s *memoryFoldRowsStore) LoadFoldStateTx(context.Context, pgx.Tx, string) ([]byte, bool, error) {
	return s.foldMemo, s.foldDisabled, nil
}

func (s *memoryFoldRowsStore) LoadFoldTurnsTx(_ context.Context, _ pgx.Tx, _ string, turnIDs []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, turnID := range turnIDs {
		if blob, ok := s.foldTurns[turnID]; ok {
			out[turnID] = blob
		}
	}
	return out, nil
}

func (s *memoryFoldRowsStore) SaveFoldStateTx(_ context.Context, _ pgx.Tx, _ string, memo []byte, turns map[string][]byte) error {
	s.foldMemo = append([]byte(nil), memo...)
	if s.foldTurns == nil {
		s.foldTurns = map[string][]byte{}
	}
	for turnID, blob := range turns {
		s.foldTurns[turnID] = append([]byte(nil), blob...)
	}
	s.foldDisabled = false
	return nil
}

func (s *memoryFoldRowsStore) ReplaceFoldStateTx(ctx context.Context, tx pgx.Tx, sessionID string, memo []byte, turns map[string][]byte) error {
	s.foldTurns = map[string][]byte{}
	return s.SaveFoldStateTx(ctx, tx, sessionID, memo, turns)
}

func (s *memoryFoldRowsStore) DeleteFoldStateTx(context.Context, pgx.Tx, string) error {
	if !s.foldDisabled {
		s.foldMemo = nil
		s.foldTurns = nil
	}
	return nil
}

func (s *memoryFoldRowsStore) DisableFoldTx(context.Context, pgx.Tx, string) error {
	s.foldMemo = nil
	s.foldTurns = nil
	s.foldDisabled = true
	return nil
}

// Non-tx SessionTranscriptRowStore surface — RefreshEventBatch type-asserts
// the tx interfaces, but the materializer struct field needs the base one.
func (s *memoryFoldRowsStore) ReplaceForTurn(ctx context.Context, sessionID, turnID string, entries []map[string]any) error {
	return s.ReplaceForTurnTx(ctx, nil, sessionID, turnID, entries)
}
func (s *memoryFoldRowsStore) ReplaceForSession(ctx context.Context, sessionID string, entries []map[string]any) error {
	return s.ReplaceForSessionTx(ctx, nil, sessionID, entries)
}
func (s *memoryFoldRowsStore) UpsertRows(ctx context.Context, sessionID string, entries []map[string]any) error {
	return s.UpsertRowsTx(ctx, nil, sessionID, entries)
}
func (s *memoryFoldRowsStore) ListChangedAfterOrderKey(context.Context, string, string, int) (store.TranscriptRowDeltaPage, error) {
	return store.TranscriptRowDeltaPage{}, nil
}
func (s *memoryFoldRowsStore) ListLatest(context.Context, string, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}
func (s *memoryFoldRowsStore) ListOldest(context.Context, string, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}
func (s *memoryFoldRowsStore) ListBefore(context.Context, string, string, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}
func (s *memoryFoldRowsStore) ListAround(context.Context, string, string, int, int) (store.TranscriptRowPage, error) {
	return store.TranscriptRowPage{}, nil
}
func (s *memoryFoldRowsStore) ResolveCursorForTimelineID(context.Context, string, string) (string, error) {
	return "", nil
}
func (s *memoryFoldRowsStore) NeedsBackfill(context.Context, string) (bool, error) {
	return true, nil
}

// ---------------------------------------------------------------------------
// The equivalence harness: replay each #1051 incident fixture through the
// real RefreshEventBatch in batches, and after EVERY batch require the row
// store to be byte-identical to a from-scratch batch projection of the same
// prefix. This is the acceptance gate for the checkpointed fold: any
// divergence between the fold fast path and the reference pipeline — summary
// math, membership, ordering, wake folding, promotion — fails here with the
// offending row named.
// ---------------------------------------------------------------------------

func foldFixtureEvents(t *testing.T, name string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var events []map[string]any
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return events
}

func groundTruthRows(events []map[string]any) map[string]map[string]any {
	projection := projectTranscriptEvents(events)
	out := make(map[string]map[string]any, len(projection.Entries))
	for _, entry := range projection.Entries {
		if id := transcriptMapString(entry, "id"); id != "" {
			out[id] = entry
		}
	}
	return out
}

func diffRowSets(got, want map[string]map[string]any) string {
	var problems []string
	for id, wantRow := range want {
		gotRow, ok := got[id]
		if !ok {
			problems = append(problems, fmt.Sprintf("missing row %s", id))
			continue
		}
		if !reflect.DeepEqual(gotRow, wantRow) {
			gotJSON, _ := json.Marshal(gotRow)
			wantJSON, _ := json.Marshal(wantRow)
			problems = append(problems, fmt.Sprintf("row %s diverged:\n  got:  %s\n  want: %s", id, gotJSON, wantJSON))
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			problems = append(problems, fmt.Sprintf("extra row %s", id))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	sort.Strings(problems)
	if len(problems) > 6 {
		problems = append(problems[:6], fmt.Sprintf("... and %d more", len(problems)-6))
	}
	return strings.Join(problems, "\n")
}

func TestFoldCheckpointEquivalenceOverFixtures(t *testing.T) {
	fixtures := []string{
		"slot1_session_161_events.json",
	}
	for _, fixture := range fixtures {
		for _, batchSize := range []int{1, 7, 64} {
			t.Run(fmt.Sprintf("%s/batch=%d", fixture, batchSize), func(t *testing.T) {
				events := foldFixtureEvents(t, fixture)
				rows := newMemoryFoldRowsStore()
				eventsStore := &memoryFoldEventStore{}
				m := transcriptRowsMaterializer{events: eventsStore, rows: rows, turns: store.StubSessionTurnStore{}}

				// Seed the memo the way production does on first read: an
				// on-demand backfill. From here the fold engages for every
				// flood-class batch and reseeds/invalidates per the wiring.
				foldedBefore := testutil.ToFloat64(transcriptFoldTotal.WithLabelValues("folded"))

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
					want := groundTruthRows(eventsStore.all())
					if diff := diffRowSets(rows.rows, want); diff != "" {
						t.Fatalf("rows diverged from reference after batch ending at %d:\n%s", end, diff)
					}
				}

				foldedAfter := testutil.ToFloat64(transcriptFoldTotal.WithLabelValues("folded"))
				// Engagement guard: at granular batch sizes the
				// background-task fixture must exercise the fold fast path;
				// without this, a regression that quietly routes everything
				// to the reference pipeline would still pass the equality
				// checks. Wide batches (64) legitimately resync almost every
				// window — they nearly always contain a structure event.
				if fixture == "slot1_session_161_events.json" && batchSize <= 7 && foldedAfter == foldedBefore {
					t.Fatalf("the fold fast path never engaged on the background-task fixture — the harness is not exercising the checkpointed path")
				}
			})
		}
	}
}

// TestFoldMemoSerializationRoundTrip pins that a memo survives the durable
// []byte round trip with its maps usable and version honored.
func TestFoldMemoSerializationRoundTrip(t *testing.T) {
	events := foldFixtureEvents(t, "slot1_session_161_events.json")
	memo := buildSessionFoldMemo(events)
	allTurns := make([]string, 0, len(memo.Turns))
	for turnID := range memo.Turns {
		allTurns = append(allTurns, turnID)
	}
	raw, turnBlobs, ok := marshalSessionFoldMemo(memo, allTurns)
	if !ok {
		t.Fatalf("memo did not marshal within the per-part size cap (%d)", sessionFoldMemoMaxBytes)
	}
	back := unmarshalSessionFoldMemo(raw)
	if back == nil {
		t.Fatalf("memo failed to unmarshal")
	}
	if !attachFoldTurnBlobs(back, turnBlobs) {
		t.Fatalf("turn blobs failed to attach")
	}
	if back.LastOrderKey != memo.LastOrderKey {
		t.Fatalf("LastOrderKey lost in round trip")
	}
	if len(back.Turns) != len(memo.Turns) {
		t.Fatalf("turn count changed in round trip: %d != %d", len(back.Turns), len(memo.Turns))
	}
	// Version gate: stale shapes never load.
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	generic["version"] = sessionFoldMemoVersion + 1
	stale, _ := json.Marshal(generic)
	if unmarshalSessionFoldMemo(stale) != nil {
		t.Fatalf("stale memo version must not load")
	}
}

func (s *memoryFoldRowsStore) RewriteEpoch(context.Context, string) (int64, error) {
	return 0, nil
}

func (s *memoryFoldRowsStore) MaxEndOrderKey(context.Context, string) (string, error) {
	return "", nil
}
