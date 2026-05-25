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
// `IF NOT EXISTS` semantics â€” there is no version table.
var schemaMigrations = []string{
	// `profiles` â€” single row per user, keyed by email.
	`CREATE TABLE IF NOT EXISTS profiles (
		email           text PRIMARY KEY,
		github_login    text,
		installation_id bigint,
		run_prefs       jsonb,
		updated_at      timestamptz NOT NULL DEFAULT now()
	)`,

	// github_install_states stores opaque, single-use nonces for the
	// GitHub App install callback. The callback records GitHub's
	// installation_id; the authenticated SPA completion consumes it.
	`CREATE TABLE IF NOT EXISTS github_install_states (
		state           text PRIMARY KEY,
		email           text NOT NULL,
		installation_id bigint,
		created_at      timestamptz NOT NULL DEFAULT now(),
		expires_at      timestamptz NOT NULL,
		callback_at     timestamptz,
		consumed_at     timestamptz
	)`,
	`CREATE INDEX IF NOT EXISTS github_install_states_expires_at
		ON github_install_states (expires_at)`,
	// `sessions` â€” the session registry. One row per (email, scope, session_id).
	// `visible` is the soft-delete flag the SPA's "delete session" toggles.
	// stream_auth_tickets stores short-lived opaque tickets for browser-native
	// streaming transports. The SPA mints these through a normal
	// Authorization-bearing fetch, then EventSource uses only the opaque
	// ticket in its URL because native EventSource cannot attach
	// Authorization headers.
	`CREATE TABLE IF NOT EXISTS stream_auth_tickets (
		ticket        text PRIMARY KEY,
		sub           text NOT NULL,
		email         text NOT NULL,
		name          text NOT NULL DEFAULT '',
		role          text NOT NULL,
		actor_email   text NOT NULL DEFAULT '',
		stream_kind   text NOT NULL,
		session_scope text NOT NULL,
		session_id    text NOT NULL DEFAULT '',
		created_at    timestamptz NOT NULL DEFAULT now(),
		expires_at    timestamptz NOT NULL,
		last_used_at  timestamptz
	)`,
	`CREATE INDEX IF NOT EXISTS stream_auth_tickets_expires_at
		ON stream_auth_tickets (expires_at)`,

	// `avatar_assets` stores administrator-curated avatar images. The
	// small circular crop (`avatar_bytes`) drives compact UI surfaces;
	// the original backing image is what the browser reveals in the
	// lightbox when a user clicks the avatar. Assets are private to
	// authenticated Tank callers, so bytes stay in Postgres instead of
	// being emitted as unauthenticated static files.
	`CREATE TABLE IF NOT EXISTS avatar_assets (
		id            text PRIMARY KEY,
		kind          text NOT NULL CHECK (kind IN ('agent', 'system')),
		name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
		avatar_mime   text NOT NULL,
		avatar_bytes  bytea NOT NULL CHECK (octet_length(avatar_bytes) <= 1048576),
		backing_mime  text NOT NULL,
		backing_bytes bytea NOT NULL CHECK (octet_length(backing_bytes) <= 8388608),
		crop          jsonb NOT NULL DEFAULT '{}'::jsonb,
		created_by    text NOT NULL,
		created_at    timestamptz NOT NULL DEFAULT now(),
		updated_at    timestamptz NOT NULL DEFAULT now(),
		deleted_at    timestamptz
	)`,
	`CREATE INDEX IF NOT EXISTS avatar_assets_kind_active_created
		ON avatar_assets (kind, created_at DESC)
		WHERE deleted_at IS NULL`,
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

	// Durable sidebar ordering. The pre-migration SPA stored manual
	// order in localStorage, which meant any row_version-sorted update
	// (test/rollout/activity) could reshuffle unpinned rows and every
	// browser tab had a different source of truth. Keep the user's
	// render order on the sessions row instead. Larger values render
	// earlier; existing rows backfill newest-first and future creates
	// get a sequence-backed value that naturally lands at the top until
	// the user drags rows into a custom order.
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS sidebar_position bigint`,
	`WITH ranked AS (
		SELECT email, session_scope, session_id,
			row_number() OVER (
				PARTITION BY email, session_scope
				ORDER BY created_at ASC, session_id ASC
			)::bigint AS position
		FROM sessions
	)
	UPDATE sessions
	SET sidebar_position = ranked.position
	FROM ranked
	WHERE sessions.email = ranked.email
	  AND sessions.session_scope = ranked.session_scope
	  AND sessions.session_id = ranked.session_id
	  AND sessions.sidebar_position IS NULL`,
	`ALTER TABLE sessions
		ALTER COLUMN sidebar_position SET DEFAULT nextval('sessions_row_version_seq')`,
	`ALTER TABLE sessions
		ALTER COLUMN sidebar_position SET NOT NULL`,
	`CREATE INDEX IF NOT EXISTS sessions_email_scope_visible_sidebar_position
		ON sessions (email, session_scope, visible, sidebar_position DESC, created_at DESC)`,

	// Phase 2 (docs/session-list-redesign.md) â€” test_state and
	// rollout_state move onto the row so Reader.List can build the
	// snapshot Info without a K8s pod read. Pod annotations are still
	// patched by Manager.SetTestState/SetRolloutState (the session-
	// agent reads them at runtime via the projected downward-API
	// volume); the column is the snapshot-facing replica.
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS test_state jsonb`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS rollout_state jsonb`,
	// Skill state is mutually exclusive at the durable row. The
	// frontend renders the row as canonical truth; it must not repair
	// both-active historical data locally. Prefer rollout when normalizing
	// ambiguous rows because rollout is the deployment flow whose sidebar
	// color was masked by the stale test marker.
	`UPDATE sessions
		SET test_state = NULL,
			updated_at = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE test_state @> '{"active": true}'::jsonb
		  AND rollout_state @> '{"active": true}'::jsonb`,
	`DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conname = 'sessions_skill_state_mutual_exclusion'
			  AND conrelid = 'sessions'::regclass
		) THEN
			ALTER TABLE sessions
				ADD CONSTRAINT sessions_skill_state_mutual_exclusion
				CHECK (NOT (
					test_state @> '{"active": true}'::jsonb
					AND rollout_state @> '{"active": true}'::jsonb
				));
		END IF;
	END $$`,

	// Repo-selection columns. `repos` is the durable list of
	// "owner/name" slugs the user picked at session creation; empty
	// array means "no auto-cloning, agent will mint clone tokens on
	// demand at runtime" (today's only shape). `clone_state` is the
	// per-repo init-container outcome the cloner writes back before
	// the agent starts â€” keyed by slug, value is
	// {status: pending|cloning|cloned|failed, error?: string,
	//  started_at?, finished_at?, path?}. Existing rows back-fill to
	// '{}'/NULL respectively, matching the
	// "0 repos selected" shape that has always been valid.
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS repos text[] NOT NULL DEFAULT '{}'`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS clone_state jsonb`,

	// Session-owned model configuration. `model`/`effort` are the
	// durable run options requested at session creation and used for
	// every SDK submit_turn; per-turn model overrides are ignored once
	// these are present. `runtime_*` is written back by the pod-side
	// runner after it has handed the options to the executable/SDK,
	// giving the UI an applied-config surface instead of echoing the
	// launch picker.
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS model text NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS effort text NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_model text NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_effort text NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_configured_at timestamptz`,

	// `session_events` â€” the durable transcript ledger. Partition key in
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
	`CREATE INDEX IF NOT EXISTS session_events_timeline_id_order_key
		ON session_events (tank_session_id, (payload ->> 'timeline_id'), order_key DESC)
		WHERE payload ? 'timeline_id'`,
	`CREATE INDEX IF NOT EXISTS session_events_created_at
		ON session_events (created_at)`,
	`CREATE OR REPLACE FUNCTION tank_upsert_session_status_event(
		p_email text,
		p_scope text,
		p_session_id text,
		p_status_key text,
		p_text text,
		p_session_status text,
		p_occurred_at timestamptz,
		p_reason text DEFAULT NULL
	) RETURNS void
	LANGUAGE plpgsql
	AS $$
	DECLARE
		v_storage_key text;
		v_event_id text;
		v_event_time timestamptz;
		v_event_iso text;
		v_order_key text;
		v_status_sequence text;
		v_doc jsonb;
	BEGIN
		IF trim(coalesce(p_session_id, '')) = ''
			OR trim(coalesce(p_status_key, '')) = ''
			OR trim(coalesce(p_text, '')) = '' THEN
			RETURN;
		END IF;

		v_event_time := coalesce(p_occurred_at, now());
		v_storage_key := CASE
			WHEN trim(coalesce(p_scope, '')) = '' OR trim(p_scope) = 'default' THEN trim(p_session_id)
			ELSE trim(p_scope) || ':' || trim(p_session_id)
		END;
		v_event_id := 'session:' || trim(p_session_id) || ':status:' || trim(p_status_key);
		v_event_iso := to_char(v_event_time AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"');
		v_status_sequence := CASE trim(p_status_key)
			WHEN 'loading' THEN '00000000'
			WHEN 'ready' THEN '00000001'
			WHEN 'failed' THEN '00000002'
			ELSE '00000099'
		END;
		v_order_key := lpad((floor(extract(epoch FROM v_event_time) * 1000)::bigint)::text, 13, '0')
			|| '-' || v_status_sequence || '-' || v_event_id;

		v_doc := jsonb_build_object(
			'event_id', v_event_id,
			'uuid', v_event_id,
			'id', v_event_id,
			'order_key', v_order_key,
			'conversation_id', trim(p_session_id),
			'session_id', trim(p_session_id),
			'tank_session_id', v_storage_key,
			'timeline_id', v_event_id,
			'email', trim(coalesce(p_email, '')),
			'actor', 'system',
			'source', 'tank',
			'type', 'session.status',
			'created_at', v_event_iso,
			'written_at', v_event_iso,
			'producer', jsonb_build_object('name', 'tank-operator'),
			'visibility', 'durable',
			'payload', jsonb_strip_nulls(jsonb_build_object(
				'status', trim(p_status_key),
				'text', trim(p_text),
				'session_status', nullif(trim(coalesce(p_session_status, '')), ''),
				'reason', nullif(trim(coalesce(p_reason, '')), '')
			))
		);

		IF trim(coalesce(p_email, '')) = '' THEN
			v_doc := v_doc - 'email';
		END IF;

		DELETE FROM session_events AS se
		WHERE se.tank_session_id = v_storage_key
		  AND se.event_id = v_event_id
		  AND se.order_key <> v_order_key;

		INSERT INTO session_events (
			tank_session_id, order_key, event_id, turn_id, event_type, payload
		) VALUES (
			v_storage_key, v_order_key, v_event_id, NULL, 'session.status', v_doc
		)
		ON CONFLICT (tank_session_id, order_key) DO UPDATE
		SET event_id = EXCLUDED.event_id,
			turn_id = EXCLUDED.turn_id,
			event_type = EXCLUDED.event_type,
			payload = EXCLUDED.payload;
	END
	$$`,
	`CREATE OR REPLACE FUNCTION tank_sessions_status_events_after_write()
	RETURNS trigger
	LANGUAGE plpgsql
	AS $$
	BEGIN
		IF TG_OP = 'INSERT' THEN
			PERFORM tank_upsert_session_status_event(
				NEW.email,
				NEW.session_scope,
				NEW.session_id,
				'loading',
				'Session is loading.',
				NEW.status,
				coalesce(NEW.requested_at, NEW.created_at),
				NULL
			);
			IF NEW.status = 'Active' THEN
				PERFORM tank_upsert_session_status_event(
					NEW.email,
					NEW.session_scope,
					NEW.session_id,
					'ready',
					'Session is ready.',
					NEW.status,
					coalesce(NEW.ready_at, NEW.created_at, NEW.requested_at),
					NULL
				);
			ELSIF NEW.status = 'Failed' THEN
				PERFORM tank_upsert_session_status_event(
					NEW.email,
					NEW.session_scope,
					NEW.session_id,
					'failed',
					'Session failed to start.',
					NEW.status,
					coalesce(NEW.terminating_at, NEW.updated_at, NEW.created_at, NEW.requested_at),
					NULL
				);
			END IF;
		ELSIF NEW.status IS DISTINCT FROM OLD.status
			OR NEW.ready_at IS DISTINCT FROM OLD.ready_at
			OR NEW.terminating_at IS DISTINCT FROM OLD.terminating_at THEN
			IF NEW.status = 'Active' THEN
				PERFORM tank_upsert_session_status_event(
					NEW.email,
					NEW.session_scope,
					NEW.session_id,
					'ready',
					'Session is ready.',
					NEW.status,
					coalesce(NEW.ready_at, NEW.created_at, NEW.requested_at),
					NULL
				);
			ELSIF NEW.status = 'Failed' THEN
				PERFORM tank_upsert_session_status_event(
					NEW.email,
					NEW.session_scope,
					NEW.session_id,
					'failed',
					'Session failed to start.',
					NEW.status,
					coalesce(NEW.terminating_at, NEW.updated_at, NEW.created_at, NEW.requested_at),
					NULL
				);
			END IF;
		END IF;
		RETURN NEW;
	END
	$$`,
	`DROP TRIGGER IF EXISTS tank_sessions_status_events_after_write ON sessions`,
	`CREATE TRIGGER tank_sessions_status_events_after_write
		AFTER INSERT OR UPDATE OF status, ready_at, terminating_at
		ON sessions
		FOR EACH ROW
		EXECUTE FUNCTION tank_sessions_status_events_after_write()`,
	`SELECT tank_upsert_session_status_event(
		email,
		session_scope,
		session_id,
		'loading',
		'Session is loading.',
		status,
		coalesce(requested_at, created_at),
		NULL
	)
	FROM sessions`,
	`SELECT tank_upsert_session_status_event(
		email,
		session_scope,
		session_id,
		'ready',
		'Session is ready.',
		status,
		coalesce(ready_at, created_at, requested_at),
		NULL
	)
	FROM sessions
	WHERE status = 'Active' OR ready_at IS NOT NULL`,
	`SELECT tank_upsert_session_status_event(
		email,
		session_scope,
		session_id,
		'failed',
		'Session failed to start.',
		status,
		coalesce(terminating_at, updated_at, created_at, requested_at),
		NULL
	)
	FROM sessions
	WHERE status = 'Failed'`,

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
		          'code', exit_code
		        ),
		        true
		      ),
		      '{payload,exit_code}',
		      to_jsonb(exit_code),
		      true
		    )
		FROM (
		  SELECT tank_session_id, order_key,
		         COALESCE(
		           payload->'payload'->>'exit_code',
		           payload->'payload'->'raw_item'->>'exit_code',
		           payload->'payload'->'raw_item'->>'exitCode',
		           payload->'payload'->'raw_item'->'result'->>'exit_code',
		           payload->'payload'->'raw_item'->'result'->>'exitCode'
		         )::int AS exit_code
		  FROM session_events
		  WHERE event_type = 'item.completed'
		    AND payload->>'source' = 'codex'
		    AND payload->'payload'->'outcome' IS NULL
		    AND COALESCE(
		          payload->'payload'->>'exit_code',
		          payload->'payload'->'raw_item'->>'exit_code',
		          payload->'payload'->'raw_item'->>'exitCode',
		          payload->'payload'->'raw_item'->'result'->>'exit_code',
		          payload->'payload'->'raw_item'->'result'->>'exitCode'
		        ) ~ '^-?[0-9]+$'
		) AS command_exit
		WHERE session_events.tank_session_id = command_exit.tank_session_id
		  AND session_events.order_key = command_exit.order_key
		  AND command_exit.exit_code <> 0`,

	// `session_counters` â€” monotonic session-id allocator, one row per scope.
	// Replaces the Cosmos `session-counter[:scope]` document the previous
	// store kept under a sentinel email. The atomic INCREMENT-AND-RETURN
	// happens via the UPSERT in sessionregistry.NextSessionID.
	`CREATE TABLE IF NOT EXISTS session_counters (
		session_scope       text PRIMARY KEY,
		next_session_number bigint NOT NULL DEFAULT 1,
		created_at          timestamptz NOT NULL DEFAULT now(),
		updated_at          timestamptz NOT NULL DEFAULT now()
	)`,

	// `conversation_read_state` â€” per-user, per-session render cursor.
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

	// provider_credential_health — Layer 1 durable model for the
	// transcript-surfaced "Codex / Claude sign-in expired" banner.
	// Source of truth for whether a provider's host-wide OAuth blob
	// (or, when per-user OAuth lands, a per-user blob) is currently
	// usable. Multi-provider and multi-scope from day one so the
	// claude/codex follow-up and the per-user OAuth follow-up don't
	// need schema migrations.
	//
	// status enum: "healthy" | "degraded" | "failed". "degraded" is a
	// reserved seat for a future "transient retries succeeding but
	// concerning" intermediate state — today the orchestrator's
	// debouncer flips healthy↔failed directly.
	//
	// action_label / action_href carry the user-facing affordance
	// (e.g. "Re-sign-in to Codex" → "/api/auth/codex/login"). Either
	// both are populated or both are NULL; a contract test on the
	// session.status emitter rejects content-free "failed" banners.
	//
	// row_version is the optimistic-concurrency counter the
	// orchestrator subscriber UPSERTs against so two replicas racing
	// on the same transition don't double-fan-out: only the writer
	// that wins the version bump publishes the session.status events.
	`CREATE TABLE IF NOT EXISTS provider_credential_health (
		provider          text NOT NULL,
		owner_scope       text NOT NULL,
		status            text NOT NULL,
		reason            text NOT NULL DEFAULT '',
		text              text NOT NULL DEFAULT '',
		action_label      text NOT NULL DEFAULT '',
		action_href       text NOT NULL DEFAULT '',
		detected_at       timestamptz NOT NULL,
		last_attempted_at timestamptz NOT NULL,
		last_succeeded_at timestamptz,
		row_version       bigint NOT NULL DEFAULT 0,
		PRIMARY KEY (provider, owner_scope)
	)`,
	`CREATE INDEX IF NOT EXISTS provider_credential_health_status
		ON provider_credential_health (status, provider)`,
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
