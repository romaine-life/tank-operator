package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
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

// shadowCompareFoldTx re-derives the rows the fold just wrote from a
// reference projection of the session ledger and diffs them. A match is
// counted; a divergence is counted, logged with the offending row ids, and
// healed in the same transaction by the reference re-projection (which also
// reseeds the memo) — so a fold defect costs one wrong-rows window of zero:
// the transaction that wrote them also corrects them.
//
// foldHorizon bounds the reference read at the fold's knowledge horizon (the
// memo's LastOrderKey after the batch). The persist pipeline is asynchronous:
// events COMMIT in the persister and only later reach the materializer via
// the per-session refresh queue, so by the time this transaction's
// read-committed ledger scan runs, flood events newer than the dequeued
// batch are routinely already visible — events the fold cannot know yet and
// the NEXT batch will fold. Comparing against an unbounded read made every
// sampled batch of a continuously-flooding turn a false divergence (#1130):
// the fold's shell lagged the reference by exactly the racing tail
// (completedAt/endOrderKey), and the "heal" re-ran the O(session) reference
// read the fold exists to avoid. Bounding at the horizon keeps the
// comparison apples-to-apples while still catching every true defect: an
// event AT OR BELOW the horizon that the fold mishandled still diffs, and a
// late-arriving old-key event the fold's ≤-guard skipped still diffs.
// foldHorizon == "" means unbounded (the pre-#1130 behavior, used by tests
// that stage the ledger and rows in lockstep).
func (m transcriptRowsMaterializer) shadowCompareFoldTx(
	ctx context.Context,
	tx pgx.Tx,
	txEvents transcriptEventsTxStore,
	txRows transcriptRowsMaterializationTxStore,
	sessionID string,
	foldRows []map[string]any,
	foldHorizon string,
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
		pastHorizon := false
		for _, event := range page.Events {
			if foldHorizon != "" && transcriptString(event, "order_key") > foldHorizon {
				// Committed after the fold's batch was dequeued; the next
				// batch folds it. Pages are ASC, so nothing below the
				// horizon remains.
				pastHorizon = true
				break
			}
			events = append(events, event)
		}
		if pastHorizon || page.FoundNewest || len(page.Events) == 0 || page.NextOrderKey == "" || page.NextOrderKey == cursor {
			break
		}
		cursor = page.NextOrderKey
	}
	projection := projectTranscriptEvents(events)
	if numbers, ok := m.turnNumbersForSession(ctx, tx, sessionID); ok {
		stampTurnNumbers(sessionID, numbers, projection.Entries)
	}
	reference := map[string]map[string]any{}
	for _, entry := range projection.Entries {
		if id := transcriptMapString(entry, "id"); id != "" {
			reference[id] = entry
		}
	}
	var diverged []string
	var diffs []string
	for _, row := range foldRows {
		id := transcriptMapString(row, "id")
		want, ok := reference[id]
		if !ok {
			diverged = append(diverged, id)
			diffs = append(diffs, id+": missing from reference")
			continue
		}
		if !reflect.DeepEqual(row, want) {
			diverged = append(diverged, id)
			diffs = append(diffs, id+": "+transcriptShadowRowDiff(row, want))
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
		"diff", strings.Join(diffs, " | "),
	)
	return m.resyncSessionTx(ctx, tx, txEvents, txRows, sessionID)
}

// transcriptShadowRowDiff names the top-level keys (descending one map level)
// where the fold-written row and the reference disagree, with bounded value
// snippets — the diagnostic the #1130 hunt was missing: pod logs survive
// rollouts far more often than the diverging batch does, and "which field"
// is the whole investigation.
func transcriptShadowRowDiff(got, want map[string]any) string {
	const maxKeys = 6
	const snippet = 160
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for k := range want {
		keys[k] = true
	}
	render := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		s := string(raw)
		if len(s) > snippet {
			s = s[:snippet] + "…"
		}
		return s
	}
	var parts []string
	for _, k := range sortedStringKeys(keys) {
		gv, gok := got[k]
		wv, wok := want[k]
		if gok && wok && reflect.DeepEqual(gv, wv) {
			continue
		}
		gm, gIsMap := gv.(map[string]any)
		wm, wIsMap := wv.(map[string]any)
		if gIsMap && wIsMap {
			// One level of descent so a giant nested struct names its
			// differing children instead of dumping both sides whole.
			inner := map[string]bool{}
			for ik := range gm {
				inner[ik] = true
			}
			for ik := range wm {
				inner[ik] = true
			}
			for _, ik := range sortedStringKeys(inner) {
				igv, igok := gm[ik]
				iwv, iwok := wm[ik]
				if igok && iwok && reflect.DeepEqual(igv, iwv) {
					continue
				}
				parts = append(parts, fmt.Sprintf("%s.%s fold=%s ref=%s", k, ik, render(igv), render(iwv)))
				if len(parts) >= maxKeys {
					break
				}
			}
		} else {
			parts = append(parts, fmt.Sprintf("%s fold=%s ref=%s", k, render(gv), render(wv)))
		}
		if len(parts) >= maxKeys {
			parts = append(parts, "…")
			break
		}
	}
	if len(parts) == 0 {
		return "rows differ but no key-level diff found (type-level mismatch?)"
	}
	return strings.Join(parts, "; ")
}

func sortedStringKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
