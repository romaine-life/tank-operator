package pgstore

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMigrationEngineRetiredPathStaysOut is the reintroduction guard for the
// crashloop class this engine replaced. The retired engine had no version
// table and re-ran every statement on every boot; this test fails if a future
// change reverts to that shape, so the durable-ledger contract can't silently
// regress.
func TestMigrationEngineRetiredPathStaysOut(t *testing.T) {
	src, err := os.ReadFile("migrations.go")
	if err != nil {
		t.Fatalf("read migrations.go: %v", err)
	}
	source := string(src)

	for _, forbidden := range []string{
		// The retired self-description.
		"there is no version table",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("migrations.go reintroduced the retired engine marker %q", forbidden)
		}
	}

	for _, required := range []string{
		// The durable ledger must be created, read, and consulted.
		"schema_migrations",
		"loadAppliedMigrations",
		"migrationChecksum",
		// Applied migrations must be skipped, not blindly re-executed.
		"RecordMigrationSkipped",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migrations.go is missing the ledger-engine anchor %q", required)
		}
	}
}

// TestMigrationIDsAreStableAndUnique pins the migration-identity contract: the
// engine keys the durable ledger on these IDs, so they must be non-empty,
// unique, and stable. They are sequential zero-padded ordinals in declaration
// order; a gap or duplicate means an edit re-keyed an already-applied
// migration, which would silently re-run one-shot backfills against live data.
func TestMigrationIDsAreStableAndUnique(t *testing.T) {
	seen := make(map[string]int, len(schemaMigrations))
	for i, m := range schemaMigrations {
		if strings.TrimSpace(m.ID) == "" {
			t.Fatalf("migration at index %d has an empty ID", i)
		}
		if m.SQL == "" {
			t.Fatalf("migration %q has empty SQL", m.ID)
		}
		if prev, dup := seen[m.ID]; dup {
			t.Fatalf("migration ID %q is duplicated (indexes %d and %d)", m.ID, prev, i)
		}
		seen[m.ID] = i
		if want := fmt.Sprintf("%04d", i+1); m.ID != want {
			t.Fatalf("migration at index %d has ID %q, want sequential %q", i, m.ID, want)
		}
	}
}

// TestMigrationChecksumGuardsImmutability proves the checksum the ledger stores
// is deterministic for identical SQL and changes when the SQL changes. This is
// what lets RunMigrations refuse to boot if an already-applied migration's SQL
// was edited in place instead of appended as a new migration.
func TestMigrationChecksumGuardsImmutability(t *testing.T) {
	const sql = `CREATE TABLE IF NOT EXISTS example (id text PRIMARY KEY)`
	if migrationChecksum(sql) != migrationChecksum(sql) {
		t.Fatal("checksum is not deterministic for identical SQL")
	}
	if migrationChecksum(sql) == migrationChecksum(sql+" -- edited") {
		t.Fatal("checksum did not change when SQL changed")
	}
}

func TestAppliedMigration0078ChecksumIsStable(t *testing.T) {
	const (
		id       = "0078"
		checksum = "78cab788b19fe45e654b518add42d0308531815c1a48124cb1b7e7499dd12f40"
	)

	for _, m := range schemaMigrations {
		if m.ID != id {
			continue
		}
		if got := migrationChecksum(m.SQL); got != checksum {
			t.Fatalf("migration %s checksum = %s, want %s", id, got, checksum)
		}
		if !strings.Contains(m.SQL, "discovered_repos") {
			t.Fatalf("migration %s no longer preserves the applied discovered_repos SQL", id)
		}
		return
	}
	t.Fatalf("migration %s not found", id)
}

func TestAppliedMigration0100LegacyChecksumIsAccepted(t *testing.T) {
	const (
		id              = "0100"
		legacyChecksum  = "306071cc8a62f897ea596b722c484115b537126eb0c570282f1b0df6049a994c"
		currentChecksum = "389133b9806e223de866d2a336669db41bae3adfbd68612641bbd44e78d43619"
	)

	for _, m := range schemaMigrations {
		if m.ID != id {
			continue
		}
		if got := migrationChecksum(m.SQL); got != currentChecksum {
			t.Fatalf("migration %s checksum = %s, want %s", id, got, currentChecksum)
		}
		if !migrationChecksumAccepted(id, legacyChecksum, currentChecksum) {
			t.Fatalf("migration %s legacy checksum is not accepted", id)
		}
		if migrationChecksumAccepted(id, "not-a-real-checksum", currentChecksum) {
			t.Fatalf("migration %s accepted an unknown checksum", id)
		}
		return
	}
	t.Fatalf("migration %s not found", id)
}

func TestRuntimeContextWindowColumnsHaveForwardRepairMigrations(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, column := range []string{
		"runtime_context_window_tokens bigint NOT NULL DEFAULT 0",
		"runtime_context_window_source text NOT NULL DEFAULT ''",
		"runtime_context_window_observed_at timestamptz",
	} {
		if count := strings.Count(migrations, "ADD COLUMN IF NOT EXISTS "+column); count < 2 {
			t.Fatalf("runtime context column %q appears %d times, want original plus forward repair", column, count)
		}
	}
	for _, id := range []string{"0106", "0107", "0108"} {
		found := false
		for _, m := range schemaMigrations {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("forward repair migration %s not found", id)
		}
	}
}

func TestRuntimeRepoDiscoveryColumnIsDroppedAfterApplied0078(t *testing.T) {
	migrations := joinedMigrationSQL()
	addIndex := strings.Index(migrations, "ADD COLUMN IF NOT EXISTS discovered_repos")
	dropIndex := strings.Index(migrations, "DROP COLUMN IF EXISTS discovered_repos")
	if addIndex < 0 {
		t.Fatal("migration 0078 must remain in the ledger with the historical discovered_repos add")
	}
	if dropIndex < 0 {
		t.Fatal("a forward migration must drop retired discovered_repos")
	}
	if dropIndex < addIndex {
		t.Fatal("discovered_repos drop must occur after the historical 0078 add")
	}
}

// joinedMigrationSQL concatenates every migration's SQL in declaration order.
// The string-content tests below assert on the SQL bodies and their relative
// order, which is preserved by the []migration slice.
func joinedMigrationSQL() string {
	parts := make([]string, len(schemaMigrations))
	for i, m := range schemaMigrations {
		parts[i] = m.SQL
	}
	return strings.Join(parts, "\n")
}

func TestMigrationsEnforceMutualSkillState(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"test_state = NULL",
		"sessions_skill_state_mutual_exclusion",
		`test_state @> '{"active": true}'::jsonb`,
		`rollout_state @> '{"active": true}'::jsonb`,
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "ADD COLUMN IF NOT EXISTS test_state jsonb") > strings.Index(migrations, "sessions_skill_state_mutual_exclusion") {
		t.Fatal("skill-state constraint must be added after test_state exists")
	}
	if strings.Index(migrations, "ADD COLUMN IF NOT EXISTS rollout_state jsonb") > strings.Index(migrations, "sessions_skill_state_mutual_exclusion") {
		t.Fatal("skill-state constraint must be added after rollout_state exists")
	}
}

func TestMigrationsPersistSessionStatusEvents(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"tank_upsert_session_status_event",
		"tank_sessions_status_events_after_write",
		"session_transcript_rows",
		"v_order_key || chr(31) || v_event_id",
		"'sourceEventId', v_event_id",
		"'type', 'session.status'",
		"'visibility', 'durable'",
		"WHEN 'loading' THEN '00000000'",
		"WHEN 'ready' THEN '00000001'",
		"WHEN 'failed' THEN '00000002'",
		"se.event_id = v_event_id",
		"coalesce(NEW.requested_at, NEW.created_at)",
		"coalesce(NEW.ready_at, NEW.created_at, NEW.requested_at)",
		"FROM sessions",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_events") > strings.Index(migrations, "tank_upsert_session_status_event") {
		t.Fatal("session_events table must exist before session status events are written")
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_transcript_rows") > strings.Index(migrations, "tank_upsert_session_status_event") {
		t.Fatal("session_transcript_rows table must exist before session status rows are written")
	}
	if strings.Index(migrations, "CREATE TRIGGER tank_sessions_status_events_after_write") > strings.Index(migrations, "SELECT tank_upsert_session_status_event") {
		t.Fatal("session status trigger should be installed before backfill")
	}
	if strings.Index(migrations, "DROP TABLE IF EXISTS session_lifecycle_events") < strings.Index(migrations, "SELECT tank_upsert_session_status_event") {
		t.Fatal("session status transcript backfill must not depend on the retired lifecycle ledger")
	}
}

// TestMigrationsAllocateDurableTurnNumbers pins the durable turn-number model:
// the session_turns table, the per-session counter, the idempotent allocation
// function, and the AFTER INSERT trigger that numbers every turn. The
// load-bearing ordering is that the one-shot backfill and counter-prime run
// BEFORE the trigger goes live, so the live allocator can never collide with a
// backfilled row on the (tank_session_id, turn_number) unique constraint.
func TestMigrationsAllocateDurableTurnNumbers(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS session_turns",
		"UNIQUE (tank_session_id, turn_number)",
		"CREATE TABLE IF NOT EXISTS session_turn_counters",
		"CREATE OR REPLACE FUNCTION tank_allocate_session_turn_number",
		"ON CONFLICT (tank_session_id, turn_id) DO NOTHING",
		// numbering must never roll back the durable event write
		"WHEN unique_violation THEN",
		"CREATE OR REPLACE FUNCTION tank_session_events_allocate_turn_number()",
		"row_number() OVER (",
		"CREATE TRIGGER tank_session_events_allocate_turn_number",
		"WHEN (NEW.turn_id IS NOT NULL AND NEW.turn_id <> '')",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}

	createTable := strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_turns")
	allocFn := strings.Index(migrations, "CREATE OR REPLACE FUNCTION tank_allocate_session_turn_number")
	backfill := strings.Index(migrations, "row_number() OVER (")
	prime := strings.Index(migrations, "GREATEST(session_turn_counters.next_turn_number")
	createTrigger := strings.Index(migrations, "CREATE TRIGGER tank_session_events_allocate_turn_number")

	if createTable > allocFn {
		t.Fatal("session_turns table must be declared before the allocation function references it")
	}
	if backfill < 0 || prime < 0 || createTrigger < 0 {
		t.Fatal("expected backfill, counter-prime, and trigger creation to all be present")
	}
	if backfill > createTrigger {
		t.Fatal("turn-number backfill must run before the trigger goes live, else it can collide on the (tank_session_id, turn_number) unique constraint")
	}
	if prime > createTrigger {
		t.Fatal("counter-prime must run before the trigger goes live so the live allocator never reissues a backfilled number")
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_events") > createTrigger {
		t.Fatal("session_events table must exist before the turn-number trigger is attached to it")
	}
}

func TestMigrationsPrepareAvatarBlobStorage(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"avatar_blob_key text",
		"backing_blob_key text",
		"ADD COLUMN IF NOT EXISTS avatar_blob_key",
		"ADD COLUMN IF NOT EXISTS backing_blob_key",
		"ALTER COLUMN avatar_bytes DROP NOT NULL",
		"ALTER COLUMN backing_bytes DROP NOT NULL",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
}

func TestMigrationsPersistAvatarDeckAssignments(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS agent_avatar_id text",
		"ADD COLUMN IF NOT EXISTS system_avatar_id text",
		"CREATE TABLE IF NOT EXISTS avatar_deck_entries",
		"used_session_id text",
		"avatar_deck_entries_avatar_once_per_cycle",
		"avatar_deck_entries_current",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS avatar_assets") > strings.Index(migrations, "CREATE TABLE IF NOT EXISTS avatar_deck_entries") {
		t.Fatal("avatar assets must exist before avatar deck entries")
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS sessions") > strings.Index(migrations, "ADD COLUMN IF NOT EXISTS agent_avatar_id text") {
		t.Fatal("sessions table must exist before avatar assignment columns")
	}
}

func TestMigrationsPersistAvatarUploadAttempts(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS avatar_upload_attempts",
		"content_type_class text NOT NULL",
		"fields             jsonb NOT NULL",
		"diagnostics        jsonb NOT NULL",
		"avatar_upload_attempts_created_at",
		"avatar_upload_attempts_actor_created",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS avatar_assets") > strings.Index(migrations, "CREATE TABLE IF NOT EXISTS avatar_upload_attempts") {
		t.Fatal("avatar assets should be declared before avatar upload attempt diagnostics")
	}
}

func TestMigrationsPersistSessionListDebugCaptures(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS session_list_debug_captures",
		"session_list_debug_captures_owner_created",
		"session_list_debug_captures_session_created",
		"snapshot      jsonb NOT NULL DEFAULT '{}'::jsonb",
		"server_rows   jsonb NOT NULL DEFAULT '[]'::jsonb",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_list_debug_captures") > strings.Index(migrations, "CREATE TABLE IF NOT EXISTS sessions") {
		t.Fatal("session-list debug capture storage should be declared before session rows")
	}
}

func TestMigrationsDropHermesActiveRunPointer(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS hermes_active_run jsonb",
		"DROP COLUMN IF EXISTS hermes_active_run",
		"session_events_turn_terminal_all",
		"'turn.command_failed'",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS sessions") > strings.Index(migrations, "ADD COLUMN IF NOT EXISTS hermes_active_run jsonb") {
		t.Fatal("sessions table must exist before hermes active-run column")
	}
	if strings.Index(migrations, "ADD COLUMN IF NOT EXISTS hermes_active_run jsonb") > strings.Index(migrations, "DROP COLUMN IF EXISTS hermes_active_run") {
		t.Fatal("hermes active-run column must be created before the teardown migration drops it")
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_events") > strings.Index(migrations, "session_events_turn_terminal_all") {
		t.Fatal("session_events table must exist before hermes terminal index")
	}
}
