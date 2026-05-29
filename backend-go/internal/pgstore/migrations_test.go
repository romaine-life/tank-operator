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

func TestMigrationsPersistHermesActiveRunPointer(t *testing.T) {
	migrations := joinedMigrationSQL()
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS hermes_active_run jsonb",
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
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_events") > strings.Index(migrations, "session_events_turn_terminal_all") {
		t.Fatal("session_events table must exist before hermes terminal index")
	}
}
