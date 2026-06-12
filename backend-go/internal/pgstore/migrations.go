package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migration is one immutable, identified schema change. Identity is the
// stable string `ID` carried in source, never the slice index: an entry's ID
// travels with it, so reordering or inserting never re-keys an already-applied
// migration. Once a migration has been applied to any database its `SQL` is
// frozen — RunMigrations refuses to start if the recorded checksum and the
// in-code SQL diverge (see applyMigration / the schema_migrations ledger).
type migration struct {
	ID  string
	SQL string
}

// schemaMigrations are applied in order at backend startup under a Postgres
// advisory lock so concurrent replicas don't race. The order is
// dependency-aware (no cross-table foreign keys today, so order is largely
// cosmetic, but kept stable so future FKs can be added in-place).
//
// Applied migrations are recorded in the durable `schema_migrations` ledger
// and skipped on subsequent boots. This is the load-bearing contract: the
// one-shot data backfills below (the `SELECT tank_upsert_session_status_event
// (...) FROM sessions` rows and the `UPDATE session_events ...` normalizers)
// run exactly once per database, not on every startup. New schema changes go
// in as new entries appended to this slice with the next sequential ID; their
// SQL must be idempotent on a fresh database (the `IF NOT EXISTS` semantics)
// but is never re-executed once the ledger records it.
var schemaMigrations = []migration{
	// `profiles` â€” single row per user, keyed by email.
	{ID: "0001", SQL: `CREATE TABLE IF NOT EXISTS profiles (
		email           text PRIMARY KEY,
		github_login    text,
		installation_id bigint,
		run_prefs       jsonb,
		updated_at      timestamptz NOT NULL DEFAULT now()
	)`},

	// github_install_states stores opaque, single-use nonces for the
	// GitHub App install callback. The callback records GitHub's
	// installation_id; the authenticated SPA completion consumes it.
	{ID: "0002", SQL: `CREATE TABLE IF NOT EXISTS github_install_states (
		state           text PRIMARY KEY,
		email           text NOT NULL,
		installation_id bigint,
		created_at      timestamptz NOT NULL DEFAULT now(),
		expires_at      timestamptz NOT NULL,
		callback_at     timestamptz,
		consumed_at     timestamptz
	)`},
	{ID: "0003", SQL: `CREATE INDEX IF NOT EXISTS github_install_states_expires_at
		ON github_install_states (expires_at)`},
	// `sessions` â€” the session registry. One row per (email, scope, session_id).
	// `visible` is the soft-delete flag the SPA's "delete session" toggles.
	// stream_auth_tickets stores short-lived opaque tickets for browser-native
	// streaming transports. The SPA mints these through a normal
	// Authorization-bearing fetch, then EventSource uses only the opaque
	// ticket in its URL because native EventSource cannot attach
	// Authorization headers.
	{ID: "0004", SQL: `CREATE TABLE IF NOT EXISTS stream_auth_tickets (
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
	)`},
	{ID: "0005", SQL: `CREATE INDEX IF NOT EXISTS stream_auth_tickets_expires_at
		ON stream_auth_tickets (expires_at)`},

	// `avatar_assets` stores administrator-curated avatar metadata. The
	// actual image bytes live in private blob storage; the backend reads
	// those blobs through authenticated API routes so uploaded backing
	// photos are not exposed as public static files.
	//
	// avatar_bytes/backing_bytes are nullable legacy columns kept only so
	// startup can migrate branch/test databases that already inserted
	// bytes before blob storage existed. New writes use *_blob_key.
	{ID: "0006", SQL: `CREATE TABLE IF NOT EXISTS avatar_assets (
		id            text PRIMARY KEY,
		kind          text NOT NULL CHECK (kind IN ('agent', 'system')),
		name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
		avatar_mime   text NOT NULL,
		avatar_blob_key text,
		avatar_bytes  bytea CHECK (octet_length(avatar_bytes) <= 1048576),
		backing_mime  text NOT NULL,
		backing_blob_key text,
		backing_bytes bytea CHECK (octet_length(backing_bytes) <= 8388608),
		crop          jsonb NOT NULL DEFAULT '{}'::jsonb,
		created_by    text NOT NULL,
		created_at    timestamptz NOT NULL DEFAULT now(),
		updated_at    timestamptz NOT NULL DEFAULT now(),
		deleted_at    timestamptz
	)`},
	{ID: "0007", SQL: `ALTER TABLE avatar_assets
		ADD COLUMN IF NOT EXISTS avatar_blob_key text`},
	{ID: "0008", SQL: `ALTER TABLE avatar_assets
		ADD COLUMN IF NOT EXISTS backing_blob_key text`},
	{ID: "0009", SQL: `DO $$
	BEGIN
		IF EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_name = 'avatar_assets'
			  AND column_name = 'avatar_bytes'
			  AND is_nullable = 'NO'
		) THEN
			ALTER TABLE avatar_assets
				ALTER COLUMN avatar_bytes DROP NOT NULL;
		END IF;
		IF EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_name = 'avatar_assets'
			  AND column_name = 'backing_bytes'
			  AND is_nullable = 'NO'
		) THEN
			ALTER TABLE avatar_assets
				ALTER COLUMN backing_bytes DROP NOT NULL;
		END IF;
	END $$`},
	{ID: "0010", SQL: `CREATE INDEX IF NOT EXISTS avatar_assets_kind_active_created
		ON avatar_assets (kind, created_at DESC)
		WHERE deleted_at IS NULL`},

	// avatar_upload_attempts is the durable support surface for avatar
	// upload failures. It lets an operator diagnose a failed browser
	// upload from an attempt id without asking the user for devtools.
	{ID: "0011", SQL: `CREATE TABLE IF NOT EXISTS avatar_upload_attempts (
		id                 text PRIMARY KEY,
		operation          text NOT NULL,
		actor_email        text NOT NULL,
		actor_role         text NOT NULL,
		method             text NOT NULL,
		route              text NOT NULL,
		content_type       text NOT NULL DEFAULT '',
		content_type_class text NOT NULL DEFAULT 'unknown',
		content_length     bigint NOT NULL DEFAULT -1,
		stage              text NOT NULL,
		result             text NOT NULL,
		detail             text NOT NULL DEFAULT '',
		kind               text NOT NULL DEFAULT '',
		avatar_id          text NOT NULL DEFAULT '',
		fields             jsonb NOT NULL DEFAULT '{}'::jsonb,
		diagnostics        jsonb NOT NULL DEFAULT '{}'::jsonb,
		created_at         timestamptz NOT NULL DEFAULT now(),
		updated_at         timestamptz NOT NULL DEFAULT now()
	)`},
	{ID: "0012", SQL: `CREATE INDEX IF NOT EXISTS avatar_upload_attempts_created_at
		ON avatar_upload_attempts (created_at DESC)`},
	{ID: "0013", SQL: `CREATE INDEX IF NOT EXISTS avatar_upload_attempts_actor_created
		ON avatar_upload_attempts (actor_email, created_at DESC)`},
	// session_list_debug_captures is the durable client-side counterpart
	// to /api/debug/session-list-state. The browser posts the bounded
	// session-list debug ring when the user or operator explicitly
	// captures the browser state or records a diagnostic window, so
	// operators can diagnose without asking for devtools.
	{ID: "0014", SQL: `CREATE TABLE IF NOT EXISTS session_list_debug_captures (
		id            text PRIMARY KEY,
		owner_email   text NOT NULL,
		session_scope text NOT NULL,
		session_id    text NOT NULL DEFAULT '',
		reason        text NOT NULL,
		source        text NOT NULL DEFAULT '',
		location      text NOT NULL DEFAULT '',
		active_id     text NOT NULL DEFAULT '',
		client_seq    bigint NOT NULL DEFAULT 0,
		snapshot      jsonb NOT NULL DEFAULT '{}'::jsonb,
		detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
		server_rows   jsonb NOT NULL DEFAULT '[]'::jsonb,
		created_at    timestamptz NOT NULL DEFAULT now()
	)`},
	{ID: "0015", SQL: `CREATE INDEX IF NOT EXISTS session_list_debug_captures_owner_created
		ON session_list_debug_captures (owner_email, session_scope, created_at DESC)`},
	{ID: "0016", SQL: `CREATE INDEX IF NOT EXISTS session_list_debug_captures_session_created
		ON session_list_debug_captures (owner_email, session_scope, session_id, created_at DESC)`},
	{ID: "0017", SQL: `CREATE TABLE IF NOT EXISTS sessions (
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
	)`},
	{ID: "0018", SQL: `CREATE INDEX IF NOT EXISTS sessions_email_scope_visible
		ON sessions (email, session_scope, visible, created_at)`},

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
	{ID: "0019", SQL: `CREATE SEQUENCE IF NOT EXISTS sessions_row_version_seq`},
	{ID: "0020", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'Pending'`},
	{ID: "0021", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS ready_at timestamptz`},
	{ID: "0022", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS terminating_at timestamptz`},
	{ID: "0023", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS activity_summary jsonb`},
	{ID: "0024", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS row_version bigint NOT NULL DEFAULT nextval('sessions_row_version_seq')`},
	{ID: "0025", SQL: `CREATE INDEX IF NOT EXISTS sessions_email_scope_row_version
		ON sessions (email, session_scope, row_version)`},

	// Durable sidebar ordering. The pre-migration SPA stored manual
	// order in localStorage, which meant any row_version-sorted update
	// (test/rollout/activity) could reshuffle unpinned rows and every
	// browser tab had a different source of truth. Keep the user's
	// render order on the sessions row instead. Larger values render
	// earlier; existing rows backfill newest-first and future creates
	// get a sequence-backed value that naturally lands at the top until
	// the user drags rows into a custom order.
	{ID: "0026", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS sidebar_position bigint`},
	{ID: "0027", SQL: `WITH ranked AS (
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
	  AND sessions.sidebar_position IS NULL`},
	{ID: "0028", SQL: `ALTER TABLE sessions
		ALTER COLUMN sidebar_position SET DEFAULT nextval('sessions_row_version_seq')`},
	{ID: "0029", SQL: `ALTER TABLE sessions
		ALTER COLUMN sidebar_position SET NOT NULL`},
	{ID: "0030", SQL: `CREATE INDEX IF NOT EXISTS sessions_email_scope_visible_sidebar_position
		ON sessions (email, session_scope, visible, sidebar_position DESC, created_at DESC)`},

	// Phase 2 (docs/session-list-redesign.md) â€” test_state and
	// rollout_state move onto the row so Reader.List can build the
	// snapshot Info without a K8s pod read. Pod annotations are still
	// patched by Manager.SetTestState/SetRolloutState (the session-
	// agent reads them at runtime via the projected downward-API
	// volume); the column is the snapshot-facing replica.
	{ID: "0031", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS test_state jsonb`},
	{ID: "0032", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS rollout_state jsonb`},
	// Skill state is mutually exclusive at the durable row. The
	// frontend renders the row as canonical truth; it must not repair
	// both-active historical data locally. Prefer rollout when normalizing
	// ambiguous rows because rollout is the deployment flow whose sidebar
	// color was masked by the stale test marker.
	{ID: "0033", SQL: `UPDATE sessions
		SET test_state = NULL,
			updated_at = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE test_state @> '{"active": true}'::jsonb
		  AND rollout_state @> '{"active": true}'::jsonb`},
	{ID: "0034", SQL: `DO $$
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
	END $$`},

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
	{ID: "0035", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS repos text[] NOT NULL DEFAULT '{}'`},
	{ID: "0036", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS clone_state jsonb`},

	// Session-owned model configuration. `model`/`effort` are the
	// durable run options requested at session creation and used for
	// every SDK submit_turn; per-turn model overrides are ignored once
	// these are present. `runtime_*` is written back by the pod-side
	// runner after it has handed the options to the executable/SDK,
	// giving the UI an applied-config surface instead of echoing the
	// launch picker.
	{ID: "0037", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS model text NOT NULL DEFAULT ''`},
	{ID: "0038", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS effort text NOT NULL DEFAULT ''`},
	{ID: "0039", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_model text NOT NULL DEFAULT ''`},
	{ID: "0040", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_effort text NOT NULL DEFAULT ''`},
	{ID: "0041", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_configured_at timestamptz`},

	// Retired external-backend active run pointer. Keep this migration
	// immutable for production ledger compatibility; migration 0084 removes
	// the unused column.
	{ID: "0042", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS hermes_active_run jsonb`},

	// Session-pinned avatar assignment. The avatar deck is mutable as
	// administrators add/delete assets, but an existing session's visible
	// identity should not reshuffle on refresh. These columns stay nullable so
	// no-avatar system pools can be represented explicitly; the frontend must
	// not synthesize a session identity when an assigned agent avatar is absent.
	{ID: "0043", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS agent_avatar_id text`},
	{ID: "0044", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS system_avatar_id text`},

	// avatar_deck_entries is the durable shuffled traversal state for avatar
	// assignment. A deck is scoped by owner + session scope + kind. Each cycle
	// snapshots the active avatar IDs at cycle creation time; new additions wait
	// until the next cycle by design.
	{ID: "0045", SQL: `CREATE TABLE IF NOT EXISTS avatar_deck_entries (
		email           text NOT NULL,
		session_scope   text NOT NULL,
		kind            text NOT NULL CHECK (kind IN ('agent', 'system')),
		cycle           bigint NOT NULL,
		position        integer NOT NULL,
		avatar_id       text NOT NULL,
		used_session_id text,
		used_at         timestamptz,
		created_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (email, session_scope, kind, cycle, position)
	)`},
	{ID: "0046", SQL: `CREATE UNIQUE INDEX IF NOT EXISTS avatar_deck_entries_avatar_once_per_cycle
		ON avatar_deck_entries (email, session_scope, kind, cycle, avatar_id)`},
	{ID: "0047", SQL: `CREATE INDEX IF NOT EXISTS avatar_deck_entries_current
		ON avatar_deck_entries (email, session_scope, kind, cycle DESC, position ASC)`},

	// `session_events` â€” the durable transcript ledger. Partition key in
	// Cosmos was `tank_session_id`; in Postgres the same field is the high
	// cardinality column we always filter and order by, so it leads the index.
	// `order_key` is the canonical render-order watermark each event ships
	// with; uniqueness is enforced per session.
	{ID: "0048", SQL: `CREATE TABLE IF NOT EXISTS session_events (
		tank_session_id text NOT NULL,
		order_key       text NOT NULL,
		event_id        text NOT NULL,
		turn_id         text,
		event_type      text,
		payload         jsonb NOT NULL,
		created_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tank_session_id, order_key)
	)`},
	{ID: "0049", SQL: `CREATE INDEX IF NOT EXISTS session_events_turn_terminal
		ON session_events (tank_session_id, turn_id, order_key DESC)
		WHERE event_type IN ('turn.completed', 'turn.failed', 'turn.interrupted')`},
	{ID: "0050", SQL: `CREATE INDEX IF NOT EXISTS session_events_turn_terminal_all
		ON session_events (tank_session_id, turn_id, order_key DESC)
		WHERE event_type IN ('turn.completed', 'turn.failed', 'turn.command_failed', 'turn.interrupted')`},
	{ID: "0051", SQL: `CREATE INDEX IF NOT EXISTS session_events_event_id
		ON session_events (tank_session_id, event_id)`},
	{ID: "0052", SQL: `CREATE INDEX IF NOT EXISTS session_events_timeline_id_order_key
		ON session_events (tank_session_id, (payload ->> 'timeline_id'), order_key DESC)
		WHERE payload ? 'timeline_id'`},
	{ID: "0053", SQL: `CREATE INDEX IF NOT EXISTS session_events_created_at
		ON session_events (created_at)`},
	{ID: "0054", SQL: `CREATE TABLE IF NOT EXISTS session_transcript_rows (
		tank_session_id text NOT NULL,
		row_cursor      text NOT NULL,
		row_id          text NOT NULL,
		row_kind        text NOT NULL,
		turn_id         text,
		start_order_key text NOT NULL,
		end_order_key   text NOT NULL,
		source_event_id text,
		payload         jsonb NOT NULL,
		updated_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tank_session_id, row_id)
	)`},
	{ID: "0055", SQL: `CREATE INDEX IF NOT EXISTS session_transcript_rows_cursor
			ON session_transcript_rows (tank_session_id, row_cursor)`},
	{ID: "0056", SQL: `CREATE INDEX IF NOT EXISTS session_transcript_rows_end_order
			ON session_transcript_rows (tank_session_id, end_order_key, row_cursor)`},
	{ID: "0057", SQL: `CREATE INDEX IF NOT EXISTS session_transcript_rows_turn
			ON session_transcript_rows (tank_session_id, turn_id)
			WHERE turn_id IS NOT NULL`},
	{ID: "0058", SQL: `CREATE INDEX IF NOT EXISTS session_transcript_rows_activity_ids
		ON session_transcript_rows USING gin ((payload -> 'activityIds'))
		WHERE payload ? 'activityIds'`},
	{ID: "0059", SQL: `CREATE TABLE IF NOT EXISTS session_transcript_row_backfills (
		tank_session_id    text PRIMARY KEY,
		projection_version integer NOT NULL,
		completed_at       timestamptz NOT NULL DEFAULT now()
	)`},
	// Exact per-session row locks for transcript materialization. A refresh
	// must serialize the event-ledger read, transcript projection, and row
	// replacement as one critical section; otherwise a stale projection can
	// survive after a terminal turn event.
	{ID: "0060", SQL: `CREATE TABLE IF NOT EXISTS session_transcript_materialization_locks (
		tank_session_id text PRIMARY KEY,
		created_at      timestamptz NOT NULL DEFAULT now()
	)`},
	{ID: "0061", SQL: `CREATE OR REPLACE FUNCTION tank_upsert_session_status_event(
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

		DELETE FROM session_transcript_rows AS tr
		WHERE tr.tank_session_id = v_storage_key
		  AND tr.source_event_id = v_event_id
		  AND tr.row_cursor <> (v_order_key || chr(31) || v_event_id);

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

		INSERT INTO session_transcript_rows (
			tank_session_id, row_cursor, row_id, row_kind, turn_id,
			start_order_key, end_order_key, source_event_id, payload, updated_at
		) VALUES (
			v_storage_key,
			v_order_key || chr(31) || v_event_id,
			v_event_id,
			'message',
			NULL,
			v_order_key,
			v_order_key,
			v_event_id,
			jsonb_build_object(
				'id', v_event_id,
				'kind', 'message',
				'role', 'system',
				'text', trim(p_text),
				'time', v_event_iso,
				'orderKey', v_order_key,
				'sourceEventId', v_event_id,
				'severity', CASE trim(p_status_key)
					WHEN 'failed' THEN 'error'
					ELSE 'info'
				END
			),
			now()
		)
		ON CONFLICT (tank_session_id, row_id) DO UPDATE
		SET row_cursor = EXCLUDED.row_cursor,
			row_kind = EXCLUDED.row_kind,
			turn_id = EXCLUDED.turn_id,
			start_order_key = EXCLUDED.start_order_key,
			end_order_key = EXCLUDED.end_order_key,
			source_event_id = EXCLUDED.source_event_id,
			payload = EXCLUDED.payload,
			updated_at = now();
	END
	$$`},
	{ID: "0062", SQL: `CREATE OR REPLACE FUNCTION tank_sessions_status_events_after_write()
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
	$$`},
	{ID: "0063", SQL: `DROP TRIGGER IF EXISTS tank_sessions_status_events_after_write ON sessions`},
	{ID: "0064", SQL: `CREATE TRIGGER tank_sessions_status_events_after_write
		AFTER INSERT OR UPDATE OF status, ready_at, terminating_at
		ON sessions
		FOR EACH ROW
		EXECUTE FUNCTION tank_sessions_status_events_after_write()`},
	{ID: "0065", SQL: `SELECT tank_upsert_session_status_event(
		email,
		session_scope,
		session_id,
		'loading',
		'Session is loading.',
		status,
		coalesce(requested_at, created_at),
		NULL
	)
	FROM sessions`},
	{ID: "0066", SQL: `SELECT tank_upsert_session_status_event(
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
	WHERE status = 'Active' OR ready_at IS NOT NULL`},
	{ID: "0067", SQL: `SELECT tank_upsert_session_status_event(
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
	WHERE status = 'Failed'`},

	// Normalize historical item outcomes after #561 split item-scoped
	// failures from session failure. Tool results that completed with a bad
	// provider result now stay item.completed and carry payload.outcome;
	// item.failed is reserved for adapter/provider execution failures.
	{ID: "0068", SQL: `UPDATE session_events
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
		  AND payload->'payload'->>'is_error' = 'true'`},
	{ID: "0069", SQL: `UPDATE session_events
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
		  AND payload->'payload'->'raw_item'->>'status' = 'completed'`},
	{ID: "0070", SQL: `UPDATE session_events
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
		  AND payload->'payload'->'raw_item'->>'status' = 'failed'`},
	{ID: "0071", SQL: `UPDATE session_events
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
		  AND command_exit.exit_code <> 0`},

	// Backfill the durable final-answer marker for completed historical turns
	// before the projection stopped inferring finality at read time. This is a
	// one-shot data transform: runtime projection intentionally has no
	// trailing-assistant fallback. The backfill marks the last assistant message
	// item before a completed terminal, grouping same-provider-event text
	// blocks for Claude-style multi-block final answers. If any non-final turn
	// activity landed after that assistant message, the turn is left unmarked.
	{ID: "0072", SQL: `WITH completed_terminals AS (
		  SELECT tank_session_id, order_key AS terminal_order_key, turn_id
		  FROM session_events
		  WHERE event_type = 'turn.completed'
		    AND turn_id IS NOT NULL
		    AND payload#>'{payload,final_answer}' IS NULL
		),
		last_assistant AS (
		  SELECT DISTINCT ON (c.tank_session_id, c.terminal_order_key)
		    c.tank_session_id,
		    c.terminal_order_key,
		    c.turn_id,
		    i.order_key AS assistant_order_key,
		    COALESCE(i.payload#>>'{producer,provider_event_id}', '') AS provider_event_id
		  FROM completed_terminals c
		  JOIN session_events i
		    ON i.tank_session_id = c.tank_session_id
		   AND i.turn_id = c.turn_id
		   AND i.order_key < c.terminal_order_key
		   AND i.event_type = 'item.completed'
		   AND i.payload->>'actor' = 'assistant'
		   AND i.payload->'payload'->>'kind' IN ('message', 'agent_message')
		   AND COALESCE(BTRIM(i.payload->'payload'->>'text'), '') <> ''
		   AND COALESCE(i.payload->>'timeline_id', '') <> ''
		  ORDER BY c.tank_session_id, c.terminal_order_key, i.order_key DESC
		),
		last_non_final_activity AS (
		  SELECT
		    c.tank_session_id,
		    c.terminal_order_key,
		    MAX(i.order_key) AS boundary_order_key
		  FROM completed_terminals c
		  LEFT JOIN session_events i
		    ON i.tank_session_id = c.tank_session_id
		   AND i.turn_id = c.turn_id
		   AND i.order_key < c.terminal_order_key
		   AND i.event_type <> 'user_message.created'
		   AND NOT (
		     i.event_type = 'item.completed'
		     AND i.payload->>'actor' = 'assistant'
		     AND i.payload->'payload'->>'kind' IN ('message', 'agent_message')
		     AND COALESCE(BTRIM(i.payload->'payload'->>'text'), '') <> ''
		     AND COALESCE(i.payload->>'timeline_id', '') <> ''
		   )
		  GROUP BY c.tank_session_id, c.terminal_order_key
		),
		final_items AS (
		  SELECT
		    la.tank_session_id,
		    la.terminal_order_key,
		    i.order_key,
		    i.payload->>'timeline_id' AS timeline_id,
		    COALESCE(i.payload->>'provider_item_id', '') AS provider_item_id
		  FROM last_assistant la
		  JOIN last_non_final_activity boundary
		    ON boundary.tank_session_id = la.tank_session_id
		   AND boundary.terminal_order_key = la.terminal_order_key
		  JOIN session_events i
		    ON i.tank_session_id = la.tank_session_id
		   AND i.turn_id = la.turn_id
		   AND i.order_key < la.terminal_order_key
		   AND i.event_type = 'item.completed'
		   AND i.payload->>'actor' = 'assistant'
		   AND i.payload->'payload'->>'kind' IN ('message', 'agent_message')
		   AND COALESCE(BTRIM(i.payload->'payload'->>'text'), '') <> ''
		   AND COALESCE(i.payload->>'timeline_id', '') <> ''
		   AND i.order_key > COALESCE(boundary.boundary_order_key, '')
		   AND (
		     (la.provider_event_id <> '' AND i.payload#>>'{producer,provider_event_id}' = la.provider_event_id)
		     OR (la.provider_event_id = '' AND i.order_key = la.assistant_order_key)
		   )
		  WHERE la.assistant_order_key > COALESCE(boundary.boundary_order_key, '')
		),
		final_answers AS (
		  SELECT
		    tank_session_id,
		    terminal_order_key,
		    jsonb_strip_nulls(jsonb_build_object(
		      'timeline_ids',
		      jsonb_agg(timeline_id ORDER BY order_key),
		      'provider_item_ids',
		      CASE
		        WHEN COUNT(NULLIF(provider_item_id, '')) > 0 THEN
		          jsonb_agg(provider_item_id ORDER BY order_key) FILTER (WHERE provider_item_id <> '')
		        ELSE NULL
		      END
		    )) AS final_answer
		  FROM final_items
		  GROUP BY tank_session_id, terminal_order_key
		)
		UPDATE session_events se
		SET payload = jsonb_set(
		  CASE
		    WHEN jsonb_typeof(se.payload->'payload') = 'object' THEN se.payload
		    ELSE jsonb_set(se.payload, '{payload}', '{}'::jsonb, true)
		  END,
		  '{payload,final_answer}',
		  fa.final_answer,
		  true
		)
		FROM final_answers fa
		WHERE se.tank_session_id = fa.tank_session_id
		  AND se.order_key = fa.terminal_order_key
		  AND se.payload#>'{payload,final_answer}' IS NULL`},

	// `session_counters` â€” monotonic session-id allocator, one row per scope.
	// Replaces the Cosmos `session-counter[:scope]` document the previous
	// store kept under a sentinel email. The atomic INCREMENT-AND-RETURN
	// happens via the UPSERT in sessionregistry.NextSessionID.
	{ID: "0073", SQL: `CREATE TABLE IF NOT EXISTS session_counters (
		session_scope       text PRIMARY KEY,
		next_session_number bigint NOT NULL DEFAULT 1,
		created_at          timestamptz NOT NULL DEFAULT now(),
		updated_at          timestamptz NOT NULL DEFAULT now()
	)`},

	// `conversation_read_state` â€” per-user, per-session render cursor.
	{ID: "0074", SQL: `CREATE TABLE IF NOT EXISTS conversation_read_state (
		email                text NOT NULL,
		session_scope        text NOT NULL,
		session_id           text NOT NULL,
		last_read_order_key  text NOT NULL,
		updated_at           timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (email, session_scope, session_id)
	)`},

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
	{ID: "0075", SQL: `DROP TABLE IF EXISTS session_lifecycle_events`},

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
	{ID: "0076", SQL: `CREATE TABLE IF NOT EXISTS provider_credential_health (
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
	)`},
	{ID: "0077", SQL: `CREATE INDEX IF NOT EXISTS provider_credential_health_status
		ON provider_credential_health (status, provider)`},

	// discovered_repos was applied to production under migration 0078 before
	// the feature branch that introduced it was reverted from main. Keep this
	// immutable SQL here so the durable migration ledger continues to match.
	{ID: "0078", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS discovered_repos text[] NOT NULL DEFAULT '{}'`},

	// Per-session capability opt-ins. Empty array is the default pod surface;
	// named values are rare create-time capabilities such as spirelens_mcp.
	// The list is persisted on the row so the pod manifest is not the only
	// place to inspect why a session joined extra infrastructure.
	{ID: "0079", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS capabilities text[] NOT NULL DEFAULT '{}'`},

	// message_link_shares stores durable bearer grants created by the
	// authenticated "copy link to message" action. Session ids are
	// monotonic and transcript timeline ids are not secrets, so public
	// unauthenticated transcript reads must validate one of these opaque
	// tokens instead of treating ?session=&message= as authority.
	{ID: "0080", SQL: `CREATE TABLE IF NOT EXISTS message_link_shares (
		token_hash      text PRIMARY KEY,
		created_by      text NOT NULL,
		owner_email     text NOT NULL,
		session_scope   text NOT NULL,
		session_id      text NOT NULL,
		timeline_id     text NOT NULL,
		created_at      timestamptz NOT NULL DEFAULT now(),
		last_used_at    timestamptz,
		revoked_at      timestamptz
	)`},
	{ID: "0081", SQL: `CREATE INDEX IF NOT EXISTS message_link_shares_session
		ON message_link_shares (owner_email, session_scope, session_id)
		WHERE revoked_at IS NULL`},

	// Per-user repository pins for the splash repo picker. Unlike
	// sessions.repos (create-time intent for one session), this is the
	// caller's durable cross-device shortcut list.
	{ID: "0082", SQL: `ALTER TABLE profiles
		ADD COLUMN IF NOT EXISTS pinned_repos text[] NOT NULL DEFAULT '{}'`},

	// Runtime repo discovery was retired after the polling-based reporter
	// proved misaligned with the product's durable/event-driven quality bar.
	// Keep migration 0078 immutable for production ledger compatibility, then
	// remove the unused column with a forward migration.
	{ID: "0083", SQL: `ALTER TABLE sessions
		DROP COLUMN IF EXISTS discovered_repos`},

	// Hermes was removed as a Tank session backend. Keep migration 0042
	// immutable for production ledger compatibility, then remove its durable
	// active-run pointer with a forward migration.
	{ID: "0084", SQL: `ALTER TABLE sessions
		DROP COLUMN IF EXISTS hermes_active_run`},

	// Hide any existing rows for the retired external-backend mode so the SPA
	// does not render sessions it can no longer drive.
	{ID: "0085", SQL: `UPDATE sessions
		SET visible     = false,
			updated_at  = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE mode = concat('hermes', '_gui') AND visible = true`},

	// session_image_overrides — durable, per-session-scope override of the
	// container images the orchestrator stamps onto NEW session pods. It backs
	// the test-slot "point this slot at a branch-built session image" flow
	// (docs/testing.md): a slot's orchestrator reads the row for its own scope
	// and stamps the override instead of the chart-pinned SESSION_IMAGE /
	// CODEX_SESSION_IMAGE / ANTIGRAVITY_SESSION_IMAGE, so newly-created sessions
	// boot the branch runner code the same way prod boots its pinned image. Keyed
	// by session_scope so the shared Postgres can never let a slot override bleed
	// into another slot or prod; the write path additionally refuses the
	// production scope.
	{ID: "0086", SQL: `CREATE TABLE IF NOT EXISTS session_image_overrides (
		session_scope text PRIMARY KEY,
		claude_image  text,
		codex_image   text,
		git_ref       text,
		set_by        text,
		set_at        timestamptz NOT NULL DEFAULT now()
	)`},

	// session_turns — durable per-session turn numbers. `turn_id`
	// (turn_<nonce>) stays the provider-neutral timeline identity that events,
	// timelines, idempotency, and the activity/interrupt/answer APIs key
	// on; turn_number is the human-facing, submission-ordered handle the public
	// route /sessions/{id}/turns/{n} resolves into, mirroring how
	// session_counters mints the session's own number. A turn was previously an
	// emergent grouping of session_events rows sharing turn_id; this is the
	// first table that makes a turn a first-class durable entity.
	{ID: "0087", SQL: `CREATE TABLE IF NOT EXISTS session_turns (
		tank_session_id text   NOT NULL,
		turn_id         text   NOT NULL,
		turn_number     bigint NOT NULL,
		first_order_key text   NOT NULL,
		created_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tank_session_id, turn_id),
		UNIQUE (tank_session_id, turn_number)
	)`},

	// Per-SESSION monotonic turn-number allocator. session_counters is
	// per-SCOPE; turn numbers restart at 1 within each session. The atomic
	// INCREMENT-AND-RETURN is driven from tank_allocate_session_turn_number
	// below (called by the session_events trigger), not from Go, so every
	// persisted turn is numbered regardless of write origin.
	{ID: "0088", SQL: `CREATE TABLE IF NOT EXISTS session_turn_counters (
		tank_session_id  text   PRIMARY KEY,
		next_turn_number bigint NOT NULL DEFAULT 1,
		created_at       timestamptz NOT NULL DEFAULT now(),
		updated_at       timestamptz NOT NULL DEFAULT now()
	)`},

	// tank_allocate_session_turn_number assigns a stable per-session number to a
	// turn on first sight, idempotent on (tank_session_id, turn_id). A re-run
	// for an already-numbered turn only lowers first_order_key if an
	// earlier-ordered event arrives. The allocation block is wrapped in an
	// EXCEPTION handler so a concurrent allocator racing the
	// (tank_session_id, turn_number) unique constraint can never roll back the
	// durable session_events write that fired the trigger: numbering is
	// best-effort-correct, the event ledger is sacred. An unnumbered turn is
	// surfaced by the projection's missing-number counter, not by a failed
	// write, and self-heals when the turn's next event retries allocation.
	{ID: "0089", SQL: `CREATE OR REPLACE FUNCTION tank_allocate_session_turn_number(
		p_tank_session_id text,
		p_turn_id text,
		p_order_key text
	) RETURNS void
	LANGUAGE plpgsql
	AS $$
	DECLARE
		v_next bigint;
	BEGIN
		IF trim(coalesce(p_tank_session_id, '')) = ''
			OR trim(coalesce(p_turn_id, '')) = '' THEN
			RETURN;
		END IF;

		PERFORM 1 FROM session_turns
		WHERE tank_session_id = p_tank_session_id AND turn_id = p_turn_id;
		IF FOUND THEN
			UPDATE session_turns
			SET first_order_key = p_order_key
			WHERE tank_session_id = p_tank_session_id
				AND turn_id = p_turn_id
				AND coalesce(p_order_key, '') <> ''
				AND (first_order_key = '' OR p_order_key < first_order_key);
			RETURN;
		END IF;

		BEGIN
			INSERT INTO session_turn_counters (tank_session_id, next_turn_number, updated_at)
			VALUES (p_tank_session_id, 2, now())
			ON CONFLICT (tank_session_id) DO UPDATE
			SET next_turn_number = session_turn_counters.next_turn_number + 1,
				updated_at = now()
			RETURNING next_turn_number - 1 INTO v_next;

			INSERT INTO session_turns (tank_session_id, turn_id, turn_number, first_order_key)
			VALUES (p_tank_session_id, p_turn_id, v_next, coalesce(p_order_key, ''))
			ON CONFLICT (tank_session_id, turn_id) DO NOTHING;
		EXCEPTION
			WHEN unique_violation THEN
				NULL;
		END;
	END
	$$`},

	{ID: "0090", SQL: `CREATE OR REPLACE FUNCTION tank_session_events_allocate_turn_number()
	RETURNS trigger
	LANGUAGE plpgsql
	AS $$
	BEGIN
		PERFORM tank_allocate_session_turn_number(NEW.tank_session_id, NEW.turn_id, NEW.order_key);
		RETURN NULL;
	END
	$$`},

	// Backfill BEFORE the trigger goes live (0093/0094): number every existing
	// turn deterministically by submission order (MIN order_key). Running first
	// guarantees the live trigger can never collide on
	// (tank_session_id, turn_number) with a backfilled row. Idempotent — safe to
	// re-run via ON CONFLICT DO NOTHING.
	{ID: "0091", SQL: `WITH ranked AS (
		SELECT tank_session_id,
			turn_id,
			MIN(order_key) AS first_order_key,
			row_number() OVER (
				PARTITION BY tank_session_id
				ORDER BY MIN(order_key)
			) AS turn_number
		FROM session_events
		WHERE turn_id IS NOT NULL AND turn_id <> '' AND order_key <> ''
		GROUP BY tank_session_id, turn_id
	)
	INSERT INTO session_turns (tank_session_id, turn_id, turn_number, first_order_key)
	SELECT tank_session_id, turn_id, turn_number, first_order_key
	FROM ranked
	ON CONFLICT (tank_session_id, turn_id) DO NOTHING`},

	// Prime each session's counter to max(turn_number)+1 so the live trigger
	// allocates the next number, never a backfilled one. GREATEST guards against
	// an old pod's trigger-allocated row (if one raced in) already advancing the
	// counter past the backfill max.
	{ID: "0092", SQL: `INSERT INTO session_turn_counters (tank_session_id, next_turn_number, updated_at)
	SELECT tank_session_id, MAX(turn_number) + 1, now()
	FROM session_turns
	GROUP BY tank_session_id
	ON CONFLICT (tank_session_id) DO UPDATE
	SET next_turn_number = GREATEST(session_turn_counters.next_turn_number, EXCLUDED.next_turn_number),
		updated_at = now()`},

	{ID: "0093", SQL: `DROP TRIGGER IF EXISTS tank_session_events_allocate_turn_number ON session_events`},

	{ID: "0094", SQL: `CREATE TRIGGER tank_session_events_allocate_turn_number
		AFTER INSERT ON session_events
		FOR EACH ROW
		WHEN (NEW.turn_id IS NOT NULL AND NEW.turn_id <> '')
		EXECUTE FUNCTION tank_session_events_allocate_turn_number()`},

	// session_scheduled_wakeups — durable backend-owned ScheduleWakeup state.
	// Claude emits ScheduleWakeup as a provider tool_use item; the runner
	// registers that item here and the orchestrator claims due rows, persists
	// normal user_message.created + turn.submitted boundary events, then
	// publishes the submit_turn command. The provider_item_id uniqueness is the
	// idempotency key for SDK replay and runner restart: one Claude tool_use can
	// produce at most one scheduled wakeup row.
	{ID: "0095", SQL: `CREATE TABLE IF NOT EXISTS session_scheduled_wakeups (
		wakeup_id         text PRIMARY KEY,
		session_scope     text NOT NULL,
		session_id        text NOT NULL,
		tank_session_id   text NOT NULL,
		owner_email       text NOT NULL,
		provider          text NOT NULL,
		prompt            text NOT NULL,
		client_nonce      text NOT NULL,
		scheduled_turn_id text NOT NULL DEFAULT '',
		provider_item_id  text NOT NULL,
		scheduled_at      timestamptz NOT NULL,
		due_at            timestamptz NOT NULL,
		status            text NOT NULL CHECK (status IN ('scheduled', 'claiming', 'fired', 'failed')),
		attempt_count     integer NOT NULL DEFAULT 0,
		locked_at         timestamptz,
		fired_at          timestamptz,
		fired_turn_id     text NOT NULL DEFAULT '',
		last_error        text NOT NULL DEFAULT '',
		created_at        timestamptz NOT NULL DEFAULT now(),
		updated_at        timestamptz NOT NULL DEFAULT now()
	)`},
	{ID: "0096", SQL: `CREATE UNIQUE INDEX IF NOT EXISTS session_scheduled_wakeups_provider_item
		ON session_scheduled_wakeups (tank_session_id, provider, provider_item_id)`},
	{ID: "0097", SQL: `CREATE UNIQUE INDEX IF NOT EXISTS session_scheduled_wakeups_client_nonce
		ON session_scheduled_wakeups (tank_session_id, client_nonce)`},
	{ID: "0098", SQL: `CREATE INDEX IF NOT EXISTS session_scheduled_wakeups_due
		ON session_scheduled_wakeups (session_scope, status, due_at, created_at)
		WHERE status IN ('scheduled', 'claiming')`},

	// Provider-observed runtime context window. The session's requested model is
	// immutable after create; this records the first concrete window reported by
	// the provider runtime (codex app-server token usage; Claude Agent SDK
	// per-turn modelUsage.contextWindow) so the composer context fraction
	// hydrates from durable row metadata instead of a frontend model table.
	{ID: "0099", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_context_window_tokens bigint NOT NULL DEFAULT 0`},
	{ID: "0100", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_context_window_source text NOT NULL DEFAULT ''`},
	{ID: "0101", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_context_window_observed_at timestamptz`},
	// Partial index backing the stranded-launch sweep
	// (store.FindStrandedLaunchTurns / cmd/tank-operator/stranded_launch_sweep.go).
	// That sweep scans for user_message.created rows in a created_at window;
	// the broad session_events_created_at index would force a scan across every
	// event type in the window, so this restricts the index to launch rows and
	// makes the periodic backstop a bounded range scan over launches alone.
	{ID: "0102", SQL: `CREATE INDEX IF NOT EXISTS session_events_user_message_created_at
		ON session_events (created_at)
		WHERE event_type = 'user_message.created'`},

	// control_action_events is the immutable audit ledger for privileged
	// cross-system effects initiated from session pods through MCP servers.
	// It intentionally records both caller identity (owner/session/scope) and
	// target identity (service/tool/action/target_ref) because GitHub, Loki,
	// and the chat transcript each own only part of that story. The payload is
	// bounded JSON evidence for the action, not a raw request dump.
	{ID: "0103", SQL: `CREATE TABLE IF NOT EXISTS control_action_events (
		event_id      text PRIMARY KEY,
		invocation_id text NOT NULL,
		created_at    timestamptz NOT NULL DEFAULT now(),
		owner_email   text NOT NULL,
		session_scope text NOT NULL,
		session_id    text NOT NULL,
		source_service text NOT NULL,
		source_tool    text NOT NULL,
		action         text NOT NULL,
		status         text NOT NULL,
		target_kind    text NOT NULL,
		target_ref     text NOT NULL,
		repo_owner     text NOT NULL DEFAULT '',
		repo_name      text NOT NULL DEFAULT '',
		pr_number      integer,
		result_sha     text NOT NULL DEFAULT '',
		error          text NOT NULL DEFAULT '',
		payload        jsonb NOT NULL DEFAULT '{}'::jsonb
	)`},
	{ID: "0104", SQL: `CREATE INDEX IF NOT EXISTS control_action_events_session_created
		ON control_action_events (owner_email, session_scope, session_id, created_at DESC)`},
	{ID: "0105", SQL: `CREATE INDEX IF NOT EXISTS control_action_events_target_created
		ON control_action_events (source_service, target_kind, target_ref, created_at DESC)`},

	// Repair guard for the runtime-context migrations after a branch image wrote
	// a production ledger checksum under the same IDs before the final main
	// migration text landed. These statements are idempotent and intentionally
	// use new IDs so the desired schema exists without editing the applied rows.
	{ID: "0106", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_context_window_tokens bigint NOT NULL DEFAULT 0`},
	{ID: "0107", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_context_window_source text NOT NULL DEFAULT ''`},
	{ID: "0108", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS runtime_context_window_observed_at timestamptz`},

	{ID: "0109", SQL: `CREATE TABLE IF NOT EXISTS bug_labels (
		id            bigserial PRIMARY KEY,
		owner_email   text NOT NULL,
		session_scope text NOT NULL,
		name          text NOT NULL,
		slug          text NOT NULL,
		created_at    timestamptz NOT NULL DEFAULT now(),
		updated_at    timestamptz NOT NULL DEFAULT now(),
		archived_at   timestamptz,
		UNIQUE (owner_email, session_scope, slug)
	)`},
	{ID: "0110", SQL: `CREATE TABLE IF NOT EXISTS session_bug_labels (
		owner_email   text NOT NULL,
		session_scope text NOT NULL,
		session_id    text NOT NULL,
		bug_label_id  bigint NOT NULL REFERENCES bug_labels(id),
		attached_at   timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (owner_email, session_scope, session_id),
		FOREIGN KEY (owner_email, session_scope, session_id)
			REFERENCES sessions(email, session_scope, session_id)
			ON DELETE CASCADE
	)`},
	{ID: "0111", SQL: `CREATE INDEX IF NOT EXISTS session_bug_labels_label
		ON session_bug_labels (bug_label_id, attached_at DESC)`},

	// Durable attachment launches (issue #865). A deferred attachment launch is
	// no longer a browser-owned, fire-and-forget phase two: the orchestrator
	// records the pending launch and its staged attachment bytes durably, then a
	// backend reconciler materializes the bytes into the pod and publishes
	// submit_turn once the pod is ready. session_pending_launch_turns is the
	// durable dispatch record; status moves awaiting_bytes -> ready (all bytes
	// staged) -> claiming -> dispatched, or -> failed. base_prompt/skill/model/
	// effort are the dispatch parameters the reconciler composes the runnable
	// turn from; the final workspace paths are stamped in at materialization.
	{ID: "0112", SQL: `CREATE TABLE IF NOT EXISTS session_pending_launch_turns (
		tank_session_id   text        NOT NULL,
		turn_id           text        NOT NULL,
		session_scope     text        NOT NULL,
		session_id        text        NOT NULL,
		client_nonce      text        NOT NULL,
		owner_email       text        NOT NULL,
		runtime           text        NOT NULL,
		skill_name        text        NOT NULL DEFAULT '',
		base_prompt       text        NOT NULL,
		display_text      text        NOT NULL DEFAULT '',
		model             text        NOT NULL DEFAULT '',
		effort            text        NOT NULL DEFAULT '',
		attachment_count  integer     NOT NULL DEFAULT 0,
		status            text        NOT NULL DEFAULT 'awaiting_bytes',
		attempt_count     integer     NOT NULL DEFAULT 0,
		last_error        text        NOT NULL DEFAULT '',
		locked_at         timestamptz,
		dispatched_turn_id text       NOT NULL DEFAULT '',
		created_at        timestamptz NOT NULL DEFAULT now(),
		updated_at        timestamptz NOT NULL DEFAULT now(),
		dispatched_at     timestamptz,
		PRIMARY KEY (tank_session_id, turn_id)
	)`},
	// Claim index: the reconciler scans for dispatchable launches by scope +
	// status, oldest first. Partial so it stays a tight working-set index that
	// never folds dispatched/failed rows.
	{ID: "0113", SQL: `CREATE INDEX IF NOT EXISTS session_pending_launch_turns_claim
		ON session_pending_launch_turns (session_scope, status, created_at)
		WHERE status IN ('awaiting_bytes', 'ready', 'claiming')`},
	// Staged attachment bytes for a pending launch. bytea is correct here: the
	// payloads are small (<= maxRawBytes, 8 MiB) and transient — deleted once
	// the reconciler has written them into the live pod workspace. Durable blob
	// artifacts that survive pod death are a separate feature (see #865).
	{ID: "0114", SQL: `CREATE TABLE IF NOT EXISTS session_launch_attachment_blobs (
		tank_session_id text        NOT NULL,
		turn_id         text        NOT NULL,
		ordinal         integer     NOT NULL,
		name            text        NOT NULL,
		content_type    text        NOT NULL DEFAULT '',
		size_bytes      bigint      NOT NULL DEFAULT 0,
		bytes           bytea       NOT NULL,
		created_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tank_session_id, turn_id, ordinal)
	)`},
	// Collision repair for PR #874/#877: branch/prod rollouts recorded
	// migrations 0115-0116 with checksums that did not match the final main
	// text. Keep 0115-0118 as harmless placeholders and create the desired
	// background-task wake schema with fresh IDs below. Do not put schema
	// changes in these IDs.
	{ID: "0115", SQL: `SELECT 1`},
	{ID: "0116", SQL: `SELECT 1`},
	{ID: "0117", SQL: `SELECT 1`},
	{ID: "0118", SQL: `SELECT 1`},

	{ID: "0119", SQL: `ALTER TABLE session_bug_labels
		DROP CONSTRAINT IF EXISTS session_bug_labels_pkey`},
	{ID: "0120", SQL: `ALTER TABLE session_bug_labels
		ADD CONSTRAINT session_bug_labels_pkey
		PRIMARY KEY (owner_email, session_scope, session_id, bug_label_id)`},

	// session_background_task_wakes — durable backend-owned "a background task
	// finished while the session was idle" wakes. The base Claude Bash tool
	// promises "run_in_background … re-invokes you when it exits", but a
	// task-lifecycle SDK message never starts a turn, so a task finishing while
	// the session is idle is a silent stranding. The runner registers the
	// natural terminal here and the orchestrator claims due rows, persists
	// normal user_message.created + turn.submitted boundary events, then
	// publishes the submit_turn command with source=background-task — the same
	// backend-owned turn boundary as a user turn and as ScheduleWakeup. The
	// (tank_session_id, task_id) uniqueness is the idempotency key for SDK frame
	// repeats and runner restart: one background task produces at most one wake
	// row per session.
	{ID: "0121", SQL: `CREATE TABLE IF NOT EXISTS session_background_task_wakes (
		wake_id           text PRIMARY KEY,
		session_scope     text NOT NULL,
		session_id        text NOT NULL,
		tank_session_id   text NOT NULL,
		owner_email       text NOT NULL,
		provider          text NOT NULL,
		task_id           text NOT NULL,
		task_status       text NOT NULL DEFAULT '',
		prompt            text NOT NULL,
		client_nonce      text NOT NULL,
		registered_at     timestamptz NOT NULL,
		due_at            timestamptz NOT NULL,
		status            text NOT NULL CHECK (status IN ('scheduled', 'claiming', 'fired', 'failed')),
		attempt_count     integer NOT NULL DEFAULT 0,
		locked_at         timestamptz,
		fired_at          timestamptz,
		fired_turn_id     text NOT NULL DEFAULT '',
		last_error        text NOT NULL DEFAULT '',
		created_at        timestamptz NOT NULL DEFAULT now(),
		updated_at        timestamptz NOT NULL DEFAULT now()
	)`},
	{ID: "0122", SQL: `CREATE UNIQUE INDEX IF NOT EXISTS session_background_task_wakes_task
		ON session_background_task_wakes (tank_session_id, task_id)`},
	{ID: "0123", SQL: `CREATE UNIQUE INDEX IF NOT EXISTS session_background_task_wakes_client_nonce
		ON session_background_task_wakes (tank_session_id, client_nonce)`},
	{ID: "0124", SQL: `CREATE INDEX IF NOT EXISTS session_background_task_wakes_due
		ON session_background_task_wakes (session_scope, status, due_at, created_at)
		WHERE status IN ('scheduled', 'claiming')`},

	// Durable per-session compaction count. context.compacted is an append-only
	// durable event in session_events; this column is its projection onto the
	// sessions row so the composer's compaction metric hydrates from durable row
	// metadata, stable across reload and identical in a fresh tab — exactly the
	// model runtime_context_window_tokens uses for the window denominator. The
	// chat-activity emitter recomputes it with a bounded COUNT over the partial
	// index below on each context.compacted upsert (recompute-and-compare, so an
	// at-least-once redelivery is a no-op rather than a double-count).
	{ID: "0125", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS compaction_count bigint NOT NULL DEFAULT 0`},
	{ID: "0126", SQL: `CREATE INDEX IF NOT EXISTS session_events_context_compacted
		ON session_events (tank_session_id)
		WHERE event_type = 'context.compacted'`},
	// Startup session.status events remain durable session_events, but they are
	// no longer transcript rows. Migration 0061's trigger function inserted
	// `Session is loading.` / `Session is ready.` directly into
	// session_transcript_rows, bypassing the server projection that now folds
	// those notices into the owning turn's Turn activity. Replace the function
	// forward, delete the stale direct rows, and leave failed startup banners
	// promoted because a failed session may never produce a turn event that can
	// invoke the materializer.
	{ID: "0127", SQL: `CREATE OR REPLACE FUNCTION tank_upsert_session_status_event(
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

		IF trim(p_status_key) IN ('loading', 'ready') THEN
			DELETE FROM session_transcript_rows
			WHERE tank_session_id = v_storage_key
			  AND source_event_id = v_event_id;
			RETURN;
		END IF;

		INSERT INTO session_transcript_rows (
			tank_session_id, row_cursor, row_id, row_kind, turn_id,
			start_order_key, end_order_key, source_event_id, payload, updated_at
		) VALUES (
			v_storage_key,
			v_order_key || chr(31) || v_event_id,
			v_event_id,
			'message',
			NULL,
			v_order_key,
			v_order_key,
			v_event_id,
			jsonb_build_object(
				'id', v_event_id,
				'kind', 'message',
				'role', 'system',
				'text', trim(p_text),
				'time', v_event_iso,
				'orderKey', v_order_key,
				'sourceEventId', v_event_id,
				'severity', CASE trim(p_status_key)
					WHEN 'failed' THEN 'error'
					ELSE 'info'
				END
			),
			now()
		)
		ON CONFLICT (tank_session_id, row_id) DO UPDATE
		SET row_cursor = EXCLUDED.row_cursor,
			row_kind = EXCLUDED.row_kind,
			turn_id = EXCLUDED.turn_id,
			start_order_key = EXCLUDED.start_order_key,
			end_order_key = EXCLUDED.end_order_key,
			source_event_id = EXCLUDED.source_event_id,
			payload = EXCLUDED.payload,
			updated_at = now();
	END
	$$;

	DELETE FROM session_transcript_rows AS tr
	USING session_events AS se
	WHERE tr.tank_session_id = se.tank_session_id
	  AND tr.source_event_id = se.event_id
	  AND se.event_type = 'session.status'
	  AND coalesce(se.payload -> 'payload' ->> 'status', '') IN ('loading', 'ready')
	  AND coalesce(se.payload ->> 'timeline_id', '') NOT LIKE '%:provider:%'`},
	// Latest provider rate-limit metadata reported by claude-runner. This keeps
	// the admin UI tied to the provider's own rate_limit_event payload instead
	// of forcing admins to infer current overage/retry state from transcript
	// failures alone.
	{ID: "0128", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS provider_rate_limit_info jsonb`},
	{ID: "0129", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS provider_rate_limit_observed_at timestamptz`},

	// static_page_snapshots — durable, time-boxed copies of agent-authored HTML
	// files rendered as sandboxed pages. The session pod is ephemeral; the
	// snapshot lets a rendered page (and a future shareable link) outlive the
	// pod for its TTL. Keyed by (scope, session, path); opening a page recaptures.
	{ID: "0130", SQL: `CREATE TABLE IF NOT EXISTS static_page_snapshots (
		session_scope   text NOT NULL,
		session_id      text NOT NULL,
		rel_path        text NOT NULL,
		owner_email     text NOT NULL,
		content_type    text NOT NULL,
		bytes           bytea NOT NULL,
		byte_size       integer NOT NULL,
		created_at      timestamptz NOT NULL DEFAULT now(),
		expires_at      timestamptz NOT NULL,
		PRIMARY KEY (session_scope, session_id, rel_path)
	)`},
	{ID: "0131", SQL: `CREATE INDEX IF NOT EXISTS static_page_snapshots_expires_at
		ON static_page_snapshots (expires_at)`},
	// Cancel state for self-scheduled work. A user prompt to a parked session
	// (prompt-mid-sleep take-over) or the explicit cancel control marks pending
	// wakes 'cancelled' — a terminal that leaves the wake non-pending without the
	// error semantics of 'failed' (cancel must not ring or paint red). DO block
	// so the constraint swap is one statement; idempotent via DROP ... IF EXISTS.
	// See docs/scheduled-turn-continuity.md.
	{ID: "0132", SQL: `DO $$ BEGIN
		ALTER TABLE session_scheduled_wakeups DROP CONSTRAINT IF EXISTS session_scheduled_wakeups_status_check;
		ALTER TABLE session_scheduled_wakeups ADD CONSTRAINT session_scheduled_wakeups_status_check CHECK (status IN ('scheduled', 'claiming', 'fired', 'failed', 'cancelled'));
	END $$`},
	{ID: "0133", SQL: `DO $$ BEGIN
		ALTER TABLE session_background_task_wakes DROP CONSTRAINT IF EXISTS session_background_task_wakes_status_check;
		ALTER TABLE session_background_task_wakes ADD CONSTRAINT session_background_task_wakes_status_check CHECK (status IN ('scheduled', 'claiming', 'fired', 'failed', 'cancelled'));
	END $$`},

	// sessions.name becomes a plain NON-NULL field (stage A of the
	// name/display_name inversion). Historically name was nullable and the
	// always-present display_name covered the null case; we invert that so
	// name is assigned at creation when the user gives none, and a later
	// stage deletes display_name (display_name stays on the wire this stage,
	// equal to name). Backfill every unnamed row with the same default
	// sessionmodel.SessionDisplayName derives — the short id from pod_name
	// (falling back to session_id) stripped of a leading "session-" and
	// truncated to 8 chars — then enforce NOT NULL. NULLIF(pod_name, '')
	// mirrors SessionDisplayName's "podName if set, else id" so an empty
	// pod_name falls through to session_id; the derived expression can never
	// yield NULL because session_id is NOT NULL. The row_version bump lets
	// any open row-update SSE catch up on the assigned label. One DO block so
	// the backfill and the NOT NULL flip are one statement; idempotent —
	// the WHERE no-ops once names exist and SET NOT NULL is a no-op when the
	// column is already NOT NULL. Executes at orchestrator startup via
	// RunMigrations; the go-backend CI job runs it against a real Postgres
	// service before merge.
	{ID: "0134", SQL: `DO $$ BEGIN
		UPDATE sessions
		SET name        = left(regexp_replace(coalesce(NULLIF(pod_name, ''), session_id), '^session-', ''), 8),
			updated_at  = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE name IS NULL OR btrim(name) = '';
		ALTER TABLE sessions ALTER COLUMN name SET NOT NULL;
	END $$`},

	// user_message_count: durable per-session count of user_message.created
	// events (one per human back-and-forth). Kept as durable row metadata for
	// diagnostics and compatibility. Mirrors compaction_count (0125/0126): the
	// chat-activity emitter recomputes it with a bounded COUNT over the partial
	// index below on each user_message.created upsert
	// (recompute-and-compare, so an at-least-once redelivery is a no-op rather
	// than a double-count). The index is keyed on (tank_session_id) for the
	// per-session count — distinct from the (created_at)-keyed
	// session_events_user_message_created_at index (0102) that the time-windowed
	// stranded-launch sweep uses.
	{ID: "0135", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS user_message_count bigint NOT NULL DEFAULT 0`},
	{ID: "0136", SQL: `CREATE INDEX IF NOT EXISTS session_events_user_message_by_session
		ON session_events (tank_session_id)
		WHERE event_type = 'user_message.created'`},

	// open_target: legacy durable per-session sidebar open-target preference
	// (''/chat/turns). The frontend no longer uses it for session-open defaults,
	// but the column remains on the row wire for compatibility with existing
	// clients and historical rows.
	{ID: "0137", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS open_target text NOT NULL DEFAULT ''`},

	{ID: "0138", SQL: `ALTER TABLE session_image_overrides
		ADD COLUMN IF NOT EXISTS antigravity_image text`},

	// Background-task wake continuation turns (turn_bgtask-<task>) are
	// continuation mechanics, not user-visible turns: the transcript projection
	// folds them into the originating real turn. Numbering them minted gapped,
	// separately navigable turn numbers — the session 655 turn 56/57 defect
	// where a wake-of-a-wake surfaced as its own /turns/{n}. This replaces the
	// allocator (migration 0089) so it skips wake turns going forward.
	// Already-numbered wake turns in history are left in place — renumbering
	// would break existing /turns/{n} deep links — and the turn-number resolver
	// folds a historical wake-turn number to its originating real turn instead.
	{ID: "0139", SQL: `CREATE OR REPLACE FUNCTION tank_allocate_session_turn_number(
		p_tank_session_id text,
		p_turn_id text,
		p_order_key text
	) RETURNS void
	LANGUAGE plpgsql
	AS $$
	DECLARE
		v_next bigint;
	BEGIN
		IF trim(coalesce(p_tank_session_id, '')) = ''
			OR trim(coalesce(p_turn_id, '')) = '' THEN
			RETURN;
		END IF;

		-- Wake continuation turns are not user-visible turns; never number them.
		IF starts_with(p_turn_id, 'turn_bgtask-') THEN
			RETURN;
		END IF;

		PERFORM 1 FROM session_turns
		WHERE tank_session_id = p_tank_session_id AND turn_id = p_turn_id;
		IF FOUND THEN
			UPDATE session_turns
			SET first_order_key = p_order_key
			WHERE tank_session_id = p_tank_session_id
				AND turn_id = p_turn_id
				AND coalesce(p_order_key, '') <> ''
				AND (first_order_key = '' OR p_order_key < first_order_key);
			RETURN;
		END IF;

		BEGIN
			INSERT INTO session_turn_counters (tank_session_id, next_turn_number, updated_at)
			VALUES (p_tank_session_id, 2, now())
			ON CONFLICT (tank_session_id) DO UPDATE
			SET next_turn_number = session_turn_counters.next_turn_number + 1,
				updated_at = now()
			RETURNING next_turn_number - 1 INTO v_next;

			INSERT INTO session_turns (tank_session_id, turn_id, turn_number, first_order_key)
			VALUES (p_tank_session_id, p_turn_id, v_next, coalesce(p_order_key, ''))
			ON CONFLICT (tank_session_id, turn_id) DO NOTHING;
		EXCEPTION
			WHEN unique_violation THEN
				NULL;
		END;
	END
	$$`},

	// Session-level Background screen feed: a partial index over the background
	// (run_in_background) shell-task lifecycle so the background-task list
	// endpoint is an indexed scan over only shell-task rows — bounded regardless
	// of how large the session ledger grows, never a full re-read of the event
	// ledger on each poll.
	{ID: "0140", SQL: `CREATE INDEX IF NOT EXISTS session_events_shell_task
		ON session_events (tank_session_id, order_key)
		WHERE event_type IN ('shell_task.started', 'shell_task.updated', 'shell_task.exited')`},

	// 0141-0142: background-task wake rework. The wake row stores the
	// STRUCTURED task facts and the identity of the durable observation
	// (shell_task.exited event id) that registered it; the agent-facing prompt
	// is composed provider-aware at FIRE time, so the stored-prompt column is
	// write-retired (its Claude-idiomatic text was sent verbatim to codex,
	// which produced zero fulfilled reports across every fired wake of the
	// session-161 bug museum). Generations make a wake re-armable: a premature
	// fire (wrong liveness observation) no longer permanently burns the task's
	// only wake — a later observation with a different event id arms the next
	// generation, capped to keep a flapping observer bounded.
	//
	// Both steps are deliberately OLD-BINARY-SAFE (additive columns, defaulted
	// prompt, index swap that old inserts don't depend on): migrations run at
	// new-binary startup while old replicas still serve (maxUnavailable=0
	// rolling update), and Glimmung test slots share this database with prod.
	// The prompt column DROP is the named follow-up migration for the release
	// AFTER this one ships, once no running binary reads the column — dropping
	// it in the same release would break the old binary's fire loop mid-rollout.
	{ID: "0141", SQL: `ALTER TABLE session_background_task_wakes
		ADD COLUMN IF NOT EXISTS task_description text NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS task_summary text NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS task_last_tool text NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS task_error text NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS observed_event_id text NOT NULL DEFAULT '',
		ADD COLUMN IF NOT EXISTS generation integer NOT NULL DEFAULT 1;
		ALTER TABLE session_background_task_wakes ALTER COLUMN prompt SET DEFAULT ''`},
	{ID: "0142", SQL: `DROP INDEX IF EXISTS session_background_task_wakes_task;
		CREATE UNIQUE INDEX IF NOT EXISTS session_background_task_wakes_task_generation
		ON session_background_task_wakes (tank_session_id, task_id, generation)`},

	// The resolved sandbox/session image is session-owned metadata. Store the
	// full image reference stamped at create time after any test-slot override
	// is applied so the Session data screen can explain what an existing
	// session booted from without reading a live pod.
	{ID: "0143", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS session_image text NOT NULL DEFAULT ''`},

	// Checkpointed transcript-fold state (tank-operator#1051 B3): the bounded
	// per-session fold memo the persister advances per flood-class event
	// instead of re-reading the session ledger. memo=NULL with disabled=true
	// durably opts a session out (memo over the size cap); a missing row just
	// means the fold seeds on the next session-scope re-projection. Owned by
	// backend-go/cmd/tank-operator/transcript_fold_checkpoint.go.
	{ID: "0144", SQL: `CREATE TABLE IF NOT EXISTS session_transcript_fold_state (
		tank_session_id text PRIMARY KEY,
		memo            jsonb,
		disabled        boolean NOT NULL DEFAULT false,
		updated_at      timestamptz NOT NULL DEFAULT now()
	)`},

	// Per-turn partition of the fold memo (tank-operator#1051 follow-up):
	// the session row keeps the small shared context; each turn's pruned
	// entry set lives in its own row so a fold batch loads and saves only
	// the turns it touches, and the size cap applies per part — the
	// monster sessions that exceeded the single-row cap (disabled_size=16
	// on first deploy) fit comfortably partitioned.
	{ID: "0145", SQL: `CREATE TABLE IF NOT EXISTS session_transcript_fold_turns (
		tank_session_id text NOT NULL,
		turn_id         text NOT NULL,
		entries         jsonb NOT NULL,
		updated_at      timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (tank_session_id, turn_id)
	)`},

	// Store the human-facing release metadata that describes the session image
	// stamped at create time. Kept separate from session_image so immutable
	// fingerprint tags stay machine-useful while the UI can show PR, commit,
	// workflow, and build timestamp context for existing sessions.
	{ID: "0146", SQL: `ALTER TABLE sessions
		ADD COLUMN IF NOT EXISTS session_image_metadata jsonb NOT NULL DEFAULT '{}'::jsonb`},

	// Deployment image/version observations. /api/admin/app-version is an
	// operator-facing diagnostic surface; its source of truth is the durable
	// ledger of what each orchestrator pod actually observed at boot, not only
	// the current process environment. Multiple pods may overlap during a
	// rolling update, so reads choose the latest observation per image kind
	// inside the session scope.
	{ID: "0147", SQL: `CREATE TABLE IF NOT EXISTS deployment_image_versions (
		session_scope  text NOT NULL,
		pod_name       text NOT NULL DEFAULT '',
		image_kind     text NOT NULL CHECK (image_kind IN (
			'app',
			'session_claude',
			'session_codex',
			'session_antigravity'
		)),
		image_ref      text NOT NULL DEFAULT '',
		image_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
		observed_at    timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (session_scope, pod_name, image_kind)
	)`},
	{ID: "0148", SQL: `CREATE INDEX IF NOT EXISTS deployment_image_versions_scope_kind_observed
		ON deployment_image_versions (session_scope, image_kind, observed_at DESC)`},

	// AskUserQuestion pause-event family, probed by the stranded-turn
	// sweep's pause-linkage exclusion (FindStrandedTurns): a turn linked to
	// a turn.awaiting_input / turn.input_answered row — riding it, or
	// referenced by the payload's asking_turn_id / question_turn_id — is a
	// legitimately terminal-less turn, never a strand. Partial: these are a
	// few rows per session (179 total at creation time), so the probe is an
	// index touch instead of a partition scan on flood-class sessions.
	{ID: "0149", SQL: `CREATE INDEX IF NOT EXISTS session_events_input_pause
		ON session_events (tank_session_id, turn_id)
		WHERE event_type IN ('turn.awaiting_input', 'turn.input_answered')`},

	// Remediation for the stranded-turn sweep's first-day false positives
	// (2026-06-11/12): the sweep wrote turn.command_failed terminals onto
	// AskUserQuestion turns that legitimately never carry a terminal under
	// their own id — destroying pending questions (the /answer endpoint
	// 409s once a terminal exists), painting healthy transcripts failed,
	// and ringing false summons. This deletes exactly those rows (sweep
	// reasons + pause linkage, the inverse of the FindStrandedTurns
	// exclusion added alongside migration 0149) and drops the derived
	// projection state for every affected session so the next read
	// rebuilds it from the corrected ledger: the backfills row makes
	// NeedsBackfill report stale, and a missing fold_state/fold_turns row
	// re-seeds the checkpointed fold by design. Durable activity summaries
	// for affected sessions self-correct on the next refresh (read-state
	// update or lifecycle event); the false 'error' pill on an idle
	// corrupted session clears the moment it is opened. Bounded: the
	// time floor matches the sweep's first production pass, and the row
	// population was ~164 events across ~50 sessions when written.
	{ID: "0150", SQL: falseSweepTerminalRemediationSQL},

	// Event-identity uniqueness. Multiple writers (the answer handler, both
	// sweeps, the launch dispatcher) build events with deterministic
	// event_ids and code comments asserted a UNIQUE
	// (tank_session_id, event_id) constraint collapsed replica-concurrent
	// rewrites — but the constraint never existed; session_events_event_id
	// was a plain btree index, and the PK (tank_session_id, order_key)
	// embeds producer wall-clock so every rebuild inserts. Production
	// grew 110 duplicate identity groups (replica-raced sweep terminals,
	// repeated interrupt requests, runner-restart re-claims) before the
	// 2026-06-12 audit caught it. One transaction: drop the late copies
	// (first durable observation wins), build the real unique index, drop
	// the now-redundant non-unique one. Atomic by design — a duplicate
	// committed between the DELETE's snapshot and the index build fails
	// the build, rolls back the whole migration, and the next boot's
	// retry re-deduplicates everything committed so far; the gap is
	// milliseconds, so this converges. The in-transaction build holds
	// ShareLock for the scan (~715k compact keys at creation time, well
	// inside the 120s budget) and briefly blocks the live replica's
	// persister upserts, which simply wait. If session_events ever grows
	// to where the build approaches the budget, migrate this to a
	// non-transactional CREATE INDEX CONCURRENTLY execution mode instead
	// of raising the timeout.
	{ID: "0151", SQL: eventIdentityUniquenessSQL},

	// platform_settings owns durable operator-controlled defaults that affect
	// product behavior across users, browsers, MCP callers, prod, and validation
	// slots. Rows are keyed by a stable setting name and carry a JSON value so
	// small admin controls can ship without inventing a table per setting.
	{ID: "0152", SQL: `CREATE TABLE IF NOT EXISTS platform_settings (
		key        text PRIMARY KEY,
		value      jsonb NOT NULL,
		updated_by text NOT NULL DEFAULT '',
		updated_at timestamptz NOT NULL DEFAULT now()
	)`},
	// Activity-derivation cost (issue #1077 item 7). Both indexes carry
	// LITERAL event-type lists because partial-index matching requires the
	// query predicate to imply the index predicate — a runtime
	// `= ANY($n)` parameter can never be proven, so the store queries
	// inline the same Go constants as SQL literals. If
	// store.LifecycleEventTypes / the unread type lists change, these
	// predicates must change in the same PR (the store comments point
	// here).
	//
	// 0153: LatestLifecycleEvents' DESC scan — previously walked every
	// trailing item/stream row of a flood turn to accumulate 50 lifecycle
	// rows; now an index range over only lifecycle rows.
	{ID: "0153", SQL: `CREATE INDEX IF NOT EXISTS session_events_lifecycle
		ON session_events (tank_session_id, order_key DESC)
		WHERE event_type IN ('turn.submitted', 'turn.claimed', 'turn.started', 'turn.completed', 'turn.failed', 'turn.command_failed', 'turn.interrupt_requested', 'turn.interrupted', 'turn.awaiting_input', 'turn.input_answered')`},

	// 0154: the unread-output scans (items + unread turn terminals). One
	// index whose predicate is the UNION of UnreadOutputItemTypes and
	// UnreadOutputTurnTypes — each query's literal list is a subset, so
	// implication holds for both.
	{ID: "0154", SQL: `CREATE INDEX IF NOT EXISTS session_events_unread_output
		ON session_events (tank_session_id, order_key)
		WHERE event_type IN ('item.started', 'item.completed', 'item.failed', 'shell_task.started', 'shell_task.updated', 'shell_task.exited', 'turn.failed', 'turn.command_failed', 'turn.interrupted', 'turn.awaiting_input')`},

	// 0155 (issue #1077 item 1): the general turn-scoped read index. Every
	// per-turn read (EventsForTurnAfter pagination, the turn-activity
	// endpoint, wake-chain adoption, the cache's freshness probe) filters
	// on (tank_session_id, turn_id) ordered by order_key — but the only
	// indexes carrying turn_id were terminal-filtered partials (0049/0050)
	// or missing order_key (0149), so a long turn's read scanned the whole
	// session partition. Unfiltered by design: it serves the hot path, not
	// a predicate subset.
	{ID: "0155", SQL: `CREATE INDEX IF NOT EXISTS session_events_turn_order
		ON session_events (tank_session_id, turn_id, order_key)`},
}

// eventIdentityUniquenessSQL is migration 0151, named so the integration
// test can re-exercise the exact production statement against seeded
// duplicates (a fresh test schema applies 0151 before any rows exist).
const eventIdentityUniquenessSQL = `
	DELETE FROM session_events e
	USING session_events keeper
	WHERE keeper.tank_session_id = e.tank_session_id
		AND keeper.event_id = e.event_id
		AND keeper.order_key < e.order_key;
	CREATE UNIQUE INDEX IF NOT EXISTS session_events_event_identity
		ON session_events (tank_session_id, event_id);
	DROP INDEX IF EXISTS session_events_event_id;
`

// falseSweepTerminalRemediationSQL is migration 0150, named so the
// integration test can exercise the exact production statement against
// seeded corruption (a fresh test schema applies 0150 before any rows
// exist, so the test re-runs the statement after seeding).
const falseSweepTerminalRemediationSQL = `
	WITH false_terminals AS (
		DELETE FROM session_events cf
		WHERE cf.event_type = 'turn.command_failed'
			AND cf.created_at >= timestamptz '2026-06-11 18:00+00'
			AND (
				cf.payload -> 'payload' ->> 'reason' LIKE 'submit_command_lost%'
				OR cf.payload -> 'payload' ->> 'reason' LIKE 'turn_progress_lost%'
				OR cf.payload -> 'payload' ->> 'reason' = 'stranded_continuation_swept'
			)
			AND EXISTS (
				SELECT 1
				FROM session_events pause
				WHERE pause.tank_session_id = cf.tank_session_id
					AND pause.event_type IN ('turn.awaiting_input', 'turn.input_answered')
					AND (
						pause.turn_id = cf.turn_id
						OR pause.payload -> 'payload' ->> 'asking_turn_id' = cf.turn_id
						OR pause.payload -> 'payload' ->> 'question_turn_id' = cf.turn_id
					)
			)
		RETURNING cf.tank_session_id
	),
	affected AS (
		SELECT DISTINCT tank_session_id FROM false_terminals
	),
	drop_backfills AS (
		DELETE FROM session_transcript_row_backfills b
		USING affected a
		WHERE b.tank_session_id = a.tank_session_id
	),
	drop_fold_state AS (
		DELETE FROM session_transcript_fold_state f
		USING affected a
		WHERE f.tank_session_id = a.tank_session_id
	),
	drop_fold_turns AS (
		DELETE FROM session_transcript_fold_turns f
		USING affected a
		WHERE f.tank_session_id = a.tank_session_id
	)
	SELECT count(*) FROM affected
`

// migrationsAdvisoryLockKey is an arbitrary stable 64-bit value used to
// serialize schema-migration runs across replicas via pg_advisory_lock. Any
// constant works as long as it doesn't collide with another caller's lock.
const migrationsAdvisoryLockKey int64 = 7164301728471038113

// perMigrationTimeout bounds each individual migration's apply transaction.
// The previous engine wrapped the entire run in one 30s budget, so a single
// slow one-shot backfill — re-executed on every boot because there was no
// ledger — could exhaust the whole budget and crashloop the pod. Each
// migration now gets its own budget, and steady-state boots apply zero
// migrations (one ledger SELECT), so this ceiling only ever applies to the
// rare boot that introduces a new migration.
const perMigrationTimeout = 120 * time.Second

// schemaMigrationsLedgerDDL creates the durable record of applied migrations.
// It is the one statement the engine runs unconditionally; everything else is
// gated on this ledger. It is intentionally not a member of schemaMigrations
// (it must exist before the ledger can be read).
const schemaMigrationsLedgerDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	id         text PRIMARY KEY,
	checksum   text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// MigrationMetrics receives counters from the migration engine. Optional;
// wired to prometheus in cmd/tank-operator/observability.go. Migration IDs are
// a small, bounded, slow-growing set, so emitting one only on the (rare)
// failure path keeps cardinality within the docs/observability.md budget.
type MigrationMetrics interface {
	SetMigrationsPending(n int)
	RecordMigrationApplied(seconds float64)
	RecordMigrationSkipped()
	RecordMigrationFailed(id string)
}

type noopMigrationMetrics struct{}

func (noopMigrationMetrics) SetMigrationsPending(int)       {}
func (noopMigrationMetrics) RecordMigrationApplied(float64) {}
func (noopMigrationMetrics) RecordMigrationSkipped()        {}
func (noopMigrationMetrics) RecordMigrationFailed(string)   {}

// migrationChecksum is the immutability fingerprint of a migration's SQL.
func migrationChecksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// acceptedAppliedMigrationChecksums is a narrow production repair map for
// migrations that were already recorded with a checksum from a branch image
// before final main migration text landed. Do not add entries here for routine
// edits; append a new migration instead.
var acceptedAppliedMigrationChecksums = map[string]map[string]struct{}{
	"0100": {
		"306071cc8a62f897ea596b722c484115b537126eb0c570282f1b0df6049a994c": {},
	},
	"0101": {
		"a3f7260f8d564113d6114c253079a239d11c17c3d1159829253db05fe6e09791": {},
	},
	"0102": {
		"3698dba005984cc9317a14fc9b9561ad228d55d5a8950110dc1c9e3fc2ed0bbf": {},
	},
	"0115": {
		"31f797615bbd4bfef55d14431881805ea425e15727c75267bb4a4563aabdb04e": {},
	},
	"0116": {
		"579fb5bd6d8fdab3d5799f2e7e3cfda07ea6498de9948f3641ee6389d4a79243": {},
	},
}

func migrationChecksumAccepted(id, recorded, current string) bool {
	if recorded == current {
		return true
	}
	acceptedForID, ok := acceptedAppliedMigrationChecksums[id]
	if !ok {
		return false
	}
	_, ok = acceptedForID[recorded]
	return ok
}

// RunMigrations applies the un-applied entries in schemaMigrations under a
// session-scoped advisory lock, recording each in the durable schema_migrations
// ledger so it never runs twice. Safe to invoke at backend startup.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	return RunMigrationsWithMetrics(ctx, pool, noopMigrationMetrics{})
}

// RunMigrationsWithMetrics is RunMigrations with an observability sink.
func RunMigrationsWithMetrics(ctx context.Context, pool *pgxpool.Pool, metrics MigrationMetrics) error {
	if metrics == nil {
		metrics = noopMigrationMetrics{}
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: acquire migration conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationsAdvisoryLockKey); err != nil {
		return fmt.Errorf("pgstore: take migration lock: %w", err)
	}
	defer func() {
		// Unlock on a fresh context so a cancelled parent ctx can't strand
		// the session-scoped advisory lock.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationsAdvisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, schemaMigrationsLedgerDDL); err != nil {
		return fmt.Errorf("pgstore: ensure schema_migrations ledger: %w", err)
	}

	applied, err := loadAppliedMigrations(ctx, conn)
	if err != nil {
		return err
	}

	pending := 0
	for _, m := range schemaMigrations {
		if _, ok := applied[m.ID]; !ok {
			pending++
		}
	}
	metrics.SetMigrationsPending(pending)

	for _, m := range schemaMigrations {
		sum := migrationChecksum(m.SQL)
		if recorded, ok := applied[m.ID]; ok {
			if !migrationChecksumAccepted(m.ID, recorded, sum) {
				return fmt.Errorf(
					"pgstore: migration %s checksum mismatch (ledger=%s code=%s): applied migrations are immutable; add a new migration instead of editing this one",
					m.ID, recorded, sum,
				)
			}
			metrics.RecordMigrationSkipped()
			continue
		}
		if err := applyMigration(ctx, conn, m, sum, metrics); err != nil {
			return err
		}
	}
	return nil
}

// loadAppliedMigrations reads the ledger into an id->checksum map.
func loadAppliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx, "SELECT id, checksum FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("pgstore: read schema_migrations ledger: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]string)
	for rows.Next() {
		var id, checksum string
		if err := rows.Scan(&id, &checksum); err != nil {
			return nil, fmt.Errorf("pgstore: scan schema_migrations row: %w", err)
		}
		applied[id] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: iterate schema_migrations rows: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration and records it in the ledger atomically:
// the statement and its ledger row commit together, so a crash mid-migration
// leaves neither a half-applied change nor a phantom ledger entry. The
// advisory lock is held on the same session, so the per-migration transaction
// does not relinquish serialization.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration, sum string, metrics MigrationMetrics) error {
	start := time.Now()
	mctx, cancel := context.WithTimeout(ctx, perMigrationTimeout)
	defer cancel()

	tx, err := conn.Begin(mctx)
	if err != nil {
		metrics.RecordMigrationFailed(m.ID)
		return fmt.Errorf("pgstore: migration %s begin: %w", m.ID, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Migrations own their per-migration budget: the pool's server-side
	// statement_timeout (30s, an operational guard for request-path
	// queries) would abort long one-shot work like index builds. SET
	// LOCAL scopes the override to this transaction only.
	if _, err := tx.Exec(mctx, "SET LOCAL statement_timeout = '120s'"); err != nil {
		metrics.RecordMigrationFailed(m.ID)
		return fmt.Errorf("pgstore: migration %s set timeout: %w", m.ID, err)
	}

	if _, err := tx.Exec(mctx, m.SQL); err != nil {
		metrics.RecordMigrationFailed(m.ID)
		return fmt.Errorf("pgstore: migration %s failed: %w", m.ID, err)
	}
	if _, err := tx.Exec(mctx,
		"INSERT INTO schema_migrations (id, checksum) VALUES ($1, $2)", m.ID, sum,
	); err != nil {
		metrics.RecordMigrationFailed(m.ID)
		return fmt.Errorf("pgstore: record migration %s in ledger: %w", m.ID, err)
	}
	if err := tx.Commit(mctx); err != nil {
		metrics.RecordMigrationFailed(m.ID)
		return fmt.Errorf("pgstore: commit migration %s: %w", m.ID, err)
	}
	metrics.RecordMigrationApplied(time.Since(start).Seconds())
	return nil
}
