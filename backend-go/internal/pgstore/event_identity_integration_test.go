package pgstore

// DSN-gated integration coverage for migration 0151 (event-identity
// uniqueness) and the Upsert duplicate-identity handling. Before 0151,
// code comments asserted a UNIQUE (tank_session_id, event_id) constraint
// that never existed — replica-concurrent writers rebuilt deterministic-id
// events under fresh order_keys and both landed (110 duplicate identity
// groups in production at audit time). The test proves: the migration
// keeps exactly the earliest copy per identity, installs the unique
// index, drops the redundant non-unique one, and the store's Upsert
// reports a rebuilt duplicate as not-inserted instead of erroring.

import (
	"context"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

func TestEventIdentityUniqueness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "event_identity")

	scope := "default"
	eventStore := store.NewPostgresSessionEventStore(pool, scope)
	now := time.Now().UTC().Truncate(time.Millisecond)
	storageKey := sessionmodel.SessionStorageKey(scope, "u1")

	buildTerminal := func(reason string, at time.Time) map[string]any {
		return conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         "u1",
			SessionStorageKey: storageKey,
			Email:             "user@example.com",
			TurnID:            "turn_dup",
			ClientNonce:       "dup",
			Runtime:           "claude",
			Reason:            reason,
			Now:               at,
		})
	}

	t.Run("upsert drops a rebuilt duplicate identity", func(t *testing.T) {
		first := buildTerminal("first copy", now)
		seedEvent(t, ctx, eventStore, first, now, 0)

		// Same deterministic event_id, different order_key — the
		// replica-race shape the audit observed in production.
		second := buildTerminal("second copy", now.Add(time.Second))
		second["created_at"] = now.Add(time.Second).Format(time.RFC3339Nano)
		second["order_key"] = "9999999999999-00000099-" + second["event_id"].(string)
		inserted, err := eventStore.Upsert(ctx, second)
		if err != nil {
			t.Fatalf("duplicate-identity upsert errored instead of dropping: %v", err)
		}
		if inserted {
			t.Fatalf("duplicate-identity upsert reported inserted = true")
		}

		var count int
		var storedReason string
		if err := pool.QueryRow(ctx, `
			SELECT count(*), min(payload -> 'payload' ->> 'reason')
			FROM session_events WHERE tank_session_id = $1 AND turn_id = 'turn_dup'
		`, storageKey).Scan(&count, &storedReason); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if count != 1 || storedReason != "first copy" {
			t.Fatalf("rows = %d reason = %q, want the first durable observation only", count, storedReason)
		}

		// The same-order_key republish path is unchanged: overwrite in
		// place, reported as not-inserted.
		again := buildTerminal("first copy revised", now)
		again["order_key"] = first["order_key"]
		again["created_at"] = first["created_at"]
		inserted, err = eventStore.Upsert(ctx, again)
		if err != nil {
			t.Fatalf("same-order_key republish: %v", err)
		}
		if inserted {
			t.Fatalf("same-order_key republish reported inserted = true")
		}
	})

	t.Run("migration dedups keeping the earliest copy", func(t *testing.T) {
		// Recreate the pre-0151 world: no unique index, the old plain
		// btree in its place, and raced duplicate rows on disk.
		for _, q := range []string{
			`DROP INDEX IF EXISTS session_events_event_identity`,
			`CREATE INDEX IF NOT EXISTS session_events_event_id ON session_events (tank_session_id, event_id)`,
		} {
			if _, err := pool.Exec(ctx, q); err != nil {
				t.Fatalf("restore pre-0151 indexes: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO session_events (tank_session_id, order_key, event_id, turn_id, event_type, payload)
			VALUES
			  ($1, '0000000000100-00000001-turn_x:turn.command_failed', 'turn_x:turn.command_failed', 'turn_x', 'turn.command_failed', '{"payload":{"reason":"earliest"}}'::jsonb),
			  ($1, '0000000000200-00000002-turn_x:turn.command_failed', 'turn_x:turn.command_failed', 'turn_x', 'turn.command_failed', '{"payload":{"reason":"middle"}}'::jsonb),
			  ($1, '0000000000300-00000003-turn_x:turn.command_failed', 'turn_x:turn.command_failed', 'turn_x', 'turn.command_failed', '{"payload":{"reason":"latest"}}'::jsonb),
			  ($1, '0000000000400-00000004-turn_y:turn.claimed', 'turn_y:turn.claimed', 'turn_y', 'turn.claimed', '{"payload":{}}'::jsonb)
		`, sessionmodel.SessionStorageKey(scope, "u2")); err != nil {
			t.Fatalf("seed pre-0151 duplicates: %v", err)
		}

		if _, err := pool.Exec(ctx, eventIdentityUniquenessSQL); err != nil {
			t.Fatalf("re-run 0151: %v", err)
		}

		var count int
		var reason string
		if err := pool.QueryRow(ctx, `
			SELECT count(*), min(payload -> 'payload' ->> 'reason')
			FROM session_events WHERE tank_session_id = $1 AND turn_id = 'turn_x'
		`, sessionmodel.SessionStorageKey(scope, "u2")).Scan(&count, &reason); err != nil {
			t.Fatalf("count deduped rows: %v", err)
		}
		if count != 1 || reason != "earliest" {
			t.Fatalf("dedup kept %d rows / reason %q, want 1 / earliest", count, reason)
		}

		var unique, old int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE indexname = 'session_events_event_identity'),
			       count(*) FILTER (WHERE indexname = 'session_events_event_id')
			FROM pg_indexes WHERE tablename = 'session_events'
		`).Scan(&unique, &old); err != nil {
			t.Fatalf("inspect indexes: %v", err)
		}
		if unique != 1 || old != 0 {
			t.Fatalf("indexes after 0151: unique=%d old=%d, want 1/0", unique, old)
		}
	})
}
