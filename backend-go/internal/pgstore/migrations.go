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

	// Row-centric architecture (docs/session-list-redesign.md). The
	// sidebar's status, ready_at, terminating_at, and activity_summary
	// move onto the sessions row so the snapshot is a single SELECT and
	// the wire (Phase 3) carries the row payload instead of typed
	// lifecycle events. row_version is a per-row monotonic counter
	// drawn from a globally-shared sequence so per-(email, scope)
	// cursor reads are well-ordered. Cold-start defaults are chosen so
	// existing rows render correctly the moment Phase 2 cuts the
	// snapshot read over to these columns:
	//   - status defaults to 'Pending' (matches Manager.Create's Info
	//     init and the prior LatestPodStatus fall-back for sessions
	//     with no ledger events yet).
	//   - row_version defaults to nextval() so every pre-existing row
	//     has a unique, monotonic value before any controller write.
	//
	// The accompanying sessions_email_scope_row_version index is the
	// catch-up read predicate for the row-update wire (Phase 3); add
	// it now so Phase 3 doesn't need a separate schema migration.
	`CREATE SEQUENCE IF NOT EXISTS sessions_row_version_seq`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'Pending'`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS ready_at timestamptz`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS terminating_at timestamptz`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS activity_summary jsonb`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS row_version bigint NOT NULL DEFAULT nextval('sessions_row_version_seq')`,
	`CREATE INDEX IF NOT EXISTS sessions_email_scope_row_version
		ON sessions (email, session_scope, row_version)`,

	// Phase 2 (docs/session-list-redesign.md) — test_state and
	// rollout_state move onto the row so Reader.List can build the
	// snapshot Info without a K8s pod read. Pod annotations are still
	// patched by Manager.SetTestState/SetRolloutState (the session-
	// agent reads them at runtime via the projected downward-API
	// volume); the column is the snapshot-facing replica.
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS test_state jsonb`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS rollout_state jsonb`,

	// Repo-selection columns. `repos` is the durable list of
	// "owner/name" slugs the user picked at session creation; empty
	// array means "no auto-cloning, agent will mint clone tokens on
	// demand at runtime" (today's only shape). `clone_state` is the
	// per-repo init-container outcome the cloner writes back before
	// the agent starts — keyed by slug, value is
	// {status: pending|cloning|cloned|failed, error?: string,
	//  started_at?, finished_at?}. Both columns are no-ops on the
	// pod path until stage 3 ships the init container; the schema
	// lands now so stage 1's row plumbing (Info, SSE wire payload,
	// recent-repos query) reads against a stable column set and the
	// stage 3 PR is purely additive on the runtime side. Existing
	// rows back-fill to '{}'/NULL respectively, matching the
	// "0 repos selected" shape that has always been valid.
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS repos text[] NOT NULL DEFAULT '{}'`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS clone_state jsonb`,

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

	// Normalize historical item outcomes after #561 split item-scoped
	// failures from session failure. Tool results that completed with a bad
	// provider result now stay item.completed and carry payload.outcome;
	// item.failed is reserved for adapter/provider execution failures.
	`UPDATE session_events
		SET event_type = 'item.completed',
		    payload = jsonb_set(
		      jsonb_set(payload, '{type}', to_jsonb('item.completed'::text), false),
		      '{payload,outcome}',
		      '{"kind":"result_failed","reason":"claude_tool_result_is_error"}'::jsonb,
		      true
		    )
		WHERE event_type = 'item.failed'
		  AND payload->>'source' = 'claude'
		  AND payload->'payload'->>'kind' = 'tool_result'
		  AND payload->'payload'->>'is_error' = 'true'`,
	`UPDATE session_events
		SET event_type = 'item.completed',
		    payload = jsonb_set(
		      jsonb_set(payload, '{type}', to_jsonb('item.completed'::text), false),
		      '{payload,outcome}',
		      '{"kind":"ok"}'::jsonb,
		      true
		    )
		WHERE event_type = 'item.failed'
		  AND payload->>'source' = 'codex'
		  AND payload->'payload'->>'kind' = 'mcp_tool_call'
		  AND jsonb_typeof(payload->'payload'->'error') = 'null'
		  AND payload->'payload'->'raw_item'->>'status' = 'completed'`,
	`UPDATE session_events
		SET event_type = 'item.completed',
		    payload = jsonb_set(
		      jsonb_set(payload, '{type}', to_jsonb('item.completed'::text), false),
		      '{payload,outcome}',
		      '{"kind":"result_failed","reason":"codex_item_status_failed"}'::jsonb,
		      true
		    )
		WHERE event_type = 'item.failed'
		  AND payload->>'source' = 'codex'
		  AND payload->'payload'->>'kind' = 'mcp_tool_call'
		  AND jsonb_typeof(payload->'payload'->'error') = 'null'
		  AND payload->'payload'->'raw_item'->>'status' = 'failed'`,
	`UPDATE session_events
		SET payload = jsonb_set(
		      jsonb_set(
		        payload,
		        '{payload,outcome}',
		        jsonb_build_object(
		          'kind', 'result_failed',
		          'reason', 'exit_code',
		          'code', (payload->'payload'->'raw_item'->>'exit_code')::int
		        ),
		        true
		      ),
		      '{payload,exit_code}',
		      to_jsonb((payload->'payload'->'raw_item'->>'exit_code')::int),
		      true
		    )
		WHERE event_type = 'item.completed'
		  AND payload->>'source' = 'codex'
		  AND payload->'payload'->'outcome' IS NULL
		  AND payload->'payload'->'raw_item'->>'exit_code' ~ '^-?[0-9]+$'
		  AND (payload->'payload'->'raw_item'->>'exit_code')::int <> 0`,

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

	// session_lifecycle_events was the durable per-owner ledger that
	// drove the sidebar before docs/session-list-redesign.md Phase 4.
	// Phase 4 dropped the ledger entirely: the sessions row is the
	// only persistent state on the sidebar path now (status, ready_at,
	// terminating_at, activity_summary all live in row columns), the
	// wire shape is per-row UPDATE on the (email, scope) row-update
	// NATS subject, and in-process dedup in the K8s watch's
	// transitionTracker replaces the ledger's unique (session_scope,
	// session_id, event_id) constraint. The DROP is idempotent: a
	// fresh database has nothing to drop; an upgraded database loses
	// the table on the first migrations pass after this PR rolls.
	`DROP TABLE IF EXISTS session_lifecycle_events`,
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
