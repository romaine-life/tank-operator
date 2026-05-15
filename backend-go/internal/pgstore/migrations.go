package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaMigrations are run idempotently at backend startup under a Postgres
// advisory lock so concurrent replicas don't race on CREATE statements. The
// order is dependency-aware (no cross-table foreign keys today, so order is
// largely cosmetic, but kept stable so future FKs can be added in-place).
//
// All schema definitions use `IF NOT EXISTS` so a re-run is a no-op. Schema
// changes go in as new entries appended to this slice with their own
// `IF NOT EXISTS` semantics — there is no version table.
var schemaMigrations = []string{
	// `profiles` — single row per user, keyed by email.
	`CREATE TABLE IF NOT EXISTS profiles (
		email           text PRIMARY KEY,
		github_login    text,
		installation_id bigint,
		run_prefs       jsonb,
		updated_at      timestamptz NOT NULL DEFAULT now()
	)`,

	// `sessions` — the session registry. One row per (email, scope, session_id).
	// `visible` is the soft-delete flag the SPA's "delete session" toggles.
	`CREATE TABLE IF NOT EXISTS sessions (
		email           text NOT NULL,
		session_scope   text NOT NULL,
		session_id      text NOT NULL,
		mode            text NOT NULL,
		pod_name        text NOT NULL DEFAULT '',
		name            text,
		visible         boolean NOT NULL DEFAULT true,
		requested_at    timestamptz,
		created_at      timestamptz NOT NULL DEFAULT now(),
		updated_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (email, session_scope, session_id)
	)`,
	`CREATE INDEX IF NOT EXISTS sessions_email_scope_visible
		ON sessions (email, session_scope, visible, created_at)`,

	// `session_events` — the durable transcript ledger. Partition key in
	// Cosmos was `tank_session_id`; in Postgres the same field is the high
	// cardinality column we always filter and order by, so it leads the index.
	// `order_key` is the canonical render-order watermark each event ships
	// with; uniqueness is enforced per session.
	`CREATE TABLE IF NOT EXISTS session_events (
		tank_session_id text NOT NULL,
		order_key       text NOT NULL,
		event_id        text NOT NULL,
		turn_id         text,
		event_type      text,
		payload         jsonb NOT NULL,
		created_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tank_session_id, order_key)
	)`,
	`CREATE INDEX IF NOT EXISTS session_events_turn_terminal
		ON session_events (tank_session_id, turn_id, order_key DESC)
		WHERE event_type IN ('turn.completed', 'turn.failed', 'turn.interrupted')`,
	`CREATE INDEX IF NOT EXISTS session_events_event_id
		ON session_events (tank_session_id, event_id)`,
	`CREATE INDEX IF NOT EXISTS session_events_created_at
		ON session_events (created_at)`,

	// `session_counters` — monotonic session-id allocator, one row per scope.
	// Replaces the Cosmos `session-counter[:scope]` document the previous
	// store kept under a sentinel email. The atomic INCREMENT-AND-RETURN
	// happens via the UPSERT in sessionregistry.NextSessionID.
	`CREATE TABLE IF NOT EXISTS session_counters (
		session_scope       text PRIMARY KEY,
		next_session_number bigint NOT NULL DEFAULT 1,
		created_at          timestamptz NOT NULL DEFAULT now(),
		updated_at          timestamptz NOT NULL DEFAULT now()
	)`,

	// `conversation_read_state` — per-user, per-session render cursor.
	`CREATE TABLE IF NOT EXISTS conversation_read_state (
		email                text NOT NULL,
		session_scope        text NOT NULL,
		session_id           text NOT NULL,
		last_read_order_key  text NOT NULL,
		updated_at           timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (email, session_scope, session_id)
	)`,
}

// migrationsAdvisoryLockKey is an arbitrary stable 64-bit value used to
// serialize schema-migration runs across replicas via pg_advisory_lock. Any
// constant works as long as it doesn't collide with another caller's lock.
const migrationsAdvisoryLockKey int64 = 7164301728471038113

// RunMigrations applies every entry in schemaMigrations under a session-scoped
// advisory lock. Safe to invoke at backend startup; idempotent on re-run.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: acquire migration conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationsAdvisoryLockKey); err != nil {
		return fmt.Errorf("pgstore: take migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationsAdvisoryLockKey)
	}()

	for i, stmt := range schemaMigrations {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("pgstore: migration %d failed: %w", i, err)
		}
	}
	return nil
}
