package pgstore

// Integration coverage for migration 0150, the remediation of the
// stranded-turn sweep's first-day false positives. A fresh test schema
// applies 0150 before any rows exist, so the test seeds the production
// corruption shapes afterward and re-runs the exact statement
// (falseSweepTerminalRemediationSQL) to prove:
//   - false sweep terminals on pause-linked turns are deleted (both the
//     progress-lost asking-turn class and the command-lost question-shell
//     class, including replica-duplicate rows),
//   - genuine sweep terminals without pause linkage survive,
//   - non-sweep command_failed rows on question turns (the answer
//     handler's publish_input_reply_failed) survive,
//   - derived projection state (backfills row, fold state, fold turns) is
//     dropped for affected sessions only.

import (
	"context"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func TestFalseSweepTerminalRemediation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "remediation")

	scope := "default"
	eventStore := store.NewPostgresSessionEventStore(pool, scope)
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-3 * time.Hour)
	storage := func(sessionID string) string {
		return sessionmodel.SessionStorageKey(scope, sessionID)
	}

	commandFailed := func(sessionID, turnID, nonce, reason string) map[string]any {
		return conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storage(sessionID),
			Email:             "user@example.com",
			TurnID:            turnID,
			ClientNonce:       nonce,
			Runtime:           "claude",
			Reason:            reason,
			Now:               now,
		})
	}

	// c1: the corrupted session. An answered-shape AskUserQuestion chain
	// plus the false sweep terminals production wrote onto it.
	c1Asking := seedUserTurn(t, ctx, eventStore, "c1", storage("c1"), "c1-turn", "ask me", old, 0)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("c1", storage("c1"), c1Asking, "turn.claimed"), old.Add(time.Second), 10)
	seedEvent(t, ctx, eventStore, runnerTurnEvent("c1", storage("c1"), c1Asking, "turn.started"), old.Add(2*time.Second), 11)
	c1QuestionNonce := "question-c1c1c1"
	c1Question := conversation.TurnIDForClientNonce(c1QuestionNonce)
	seedEvent(t, ctx, eventStore, questionShellSubmitted("c1", storage("c1"), c1Question, c1QuestionNonce), old.Add(3*time.Second), 12)
	seedEvent(t, ctx, eventStore, awaitingInputEvent("c1", storage("c1"), c1Asking, c1Question), old.Add(4*time.Second), 13)

	// The false terminals: progress-lost on the asking turn, command-lost
	// on the question shell. The second shell copy models the two-replica
	// duplicate the missing event-id uniqueness allowed in production;
	// since migration 0151 the unique index absorbs it at insert time
	// (Upsert reports not-inserted), so it exercises the duplicate path
	// rather than landing a second row.
	falseAsking := commandFailed("c1", c1Asking, "c1-turn", "turn_progress_lost: a runner claimed this turn but stopped producing durable events and never wrote a terminal (runner process died mid-turn). Resubmit the message.")
	seedEvent(t, ctx, eventStore, falseAsking, now.Add(-time.Minute), 50)
	falseShellA := commandFailed("c1", c1Question, c1QuestionNonce, "submit_command_lost: the turn was durably submitted but no runner ever claimed it; the submit_turn command was lost (dead or wedged runner, or command-consumer exhaustion). Resubmit the message.")
	seedEvent(t, ctx, eventStore, falseShellA, now.Add(-time.Minute), 51)
	falseShellB := commandFailed("c1", c1Question, c1QuestionNonce, "submit_command_lost: the turn was durably submitted but no runner ever claimed it; the submit_turn command was lost (dead or wedged runner, or command-consumer exhaustion). Resubmit the message.")
	seedEvent(t, ctx, eventStore, falseShellB, now.Add(-59*time.Second), 52)

	// A legitimate non-sweep command_failed on the same question turn (the
	// answer handler's publish failure) — reason prefix differs, must
	// survive the remediation untouched.
	survivorReply := commandFailed("c1", c1Question, "answer-feedfeedfeedfeedfeedfeed", "publish_input_reply_failed: nats timeout")
	// Override the FULL stored identity, not just event_id: StampEventMap
	// mirrors event_id into uuid/id, Upsert keys the column on id, and
	// since migration 0151 the (tank_session_id, event_id) unique index
	// makes that identity load-bearing — an event_id-only override would
	// silently dedupe against the false shell terminal above instead of
	// inserting the survivor.
	survivorIdentity := c1Question + ":turn.command_failed:publish_input_reply"
	survivorReply["event_id"] = survivorIdentity
	survivorReply["id"] = survivorIdentity
	survivorReply["uuid"] = survivorIdentity
	seedEvent(t, ctx, eventStore, survivorReply, now.Add(-50*time.Second), 53)

	// Derived projection state for c1: all three must be dropped.
	for _, q := range []string{
		`INSERT INTO session_transcript_row_backfills (tank_session_id, projection_version) VALUES ($1, 10)`,
		`INSERT INTO session_transcript_fold_state (tank_session_id, memo) VALUES ($1, '{}'::jsonb)`,
	} {
		if _, err := pool.Exec(ctx, q, storage("c1")); err != nil {
			t.Fatalf("seed c1 projection state: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO session_transcript_fold_turns (tank_session_id, turn_id, entries) VALUES ($1, $2, '[]'::jsonb)`,
		storage("c1"), c1Asking); err != nil {
		t.Fatalf("seed c1 fold turn: %v", err)
	}

	// g1: a genuine strand correctly swept — no pause linkage anywhere.
	// Its terminal and projection state must survive.
	g1Turn := seedUserTurn(t, ctx, eventStore, "g1", storage("g1"), "g1-turn", "lost forever", old, 0)
	genuine := commandFailed("g1", g1Turn, "g1-turn", "submit_command_lost: the turn was durably submitted but no runner ever claimed it; the submit_turn command was lost (dead or wedged runner, or command-consumer exhaustion). Resubmit the message.")
	seedEvent(t, ctx, eventStore, genuine, now.Add(-time.Minute), 50)
	if _, err := pool.Exec(ctx,
		`INSERT INTO session_transcript_row_backfills (tank_session_id, projection_version) VALUES ($1, 10)`,
		storage("g1")); err != nil {
		t.Fatalf("seed g1 projection state: %v", err)
	}

	// Run the exact production remediation statement.
	var affected int
	if err := pool.QueryRow(ctx, falseSweepTerminalRemediationSQL).Scan(&affected); err != nil {
		t.Fatalf("run remediation: %v", err)
	}
	if affected != 1 {
		t.Fatalf("affected sessions = %d, want 1 (only c1)", affected)
	}

	countFailed := func(storageKey, turnID string) int {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM session_events
			WHERE tank_session_id = $1 AND turn_id = $2 AND event_type = 'turn.command_failed'
		`, storageKey, turnID).Scan(&n); err != nil {
			t.Fatalf("count terminals for %s/%s: %v", storageKey, turnID, err)
		}
		return n
	}

	if n := countFailed(storage("c1"), c1Asking); n != 0 {
		t.Fatalf("false asking-turn terminals remaining = %d, want 0", n)
	}
	// Only the publish_input_reply_failed survivor remains on the shell.
	if n := countFailed(storage("c1"), c1Question); n != 1 {
		t.Fatalf("question-shell terminals remaining = %d, want exactly the non-sweep survivor", n)
	}
	if n := countFailed(storage("g1"), g1Turn); n != 1 {
		t.Fatalf("genuine strand terminals remaining = %d, want 1", n)
	}

	countRows := func(table, storageKey string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tank_session_id = $1`, storageKey).Scan(&n); err != nil {
			t.Fatalf("count %s for %s: %v", table, storageKey, err)
		}
		return n
	}
	for _, table := range []string{"session_transcript_row_backfills", "session_transcript_fold_state", "session_transcript_fold_turns"} {
		if n := countRows(table, storage("c1")); n != 0 {
			t.Fatalf("%s rows for corrupted session = %d, want 0 (projection must rebuild)", table, n)
		}
	}
	if n := countRows("session_transcript_row_backfills", storage("g1")); n != 1 {
		t.Fatalf("g1 backfills rows = %d, want 1 (unaffected session keeps its projection)", n)
	}

	// Idempotent: a second run deletes nothing.
	if err := pool.QueryRow(ctx, falseSweepTerminalRemediationSQL).Scan(&affected); err != nil {
		t.Fatalf("re-run remediation: %v", err)
	}
	if affected != 0 {
		t.Fatalf("second run affected = %d, want 0", affected)
	}
}
