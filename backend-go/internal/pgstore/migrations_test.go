package pgstore

import (
	"strings"
	"testing"
)

func TestMigrationsEnforceMutualSkillState(t *testing.T) {
	migrations := strings.Join(schemaMigrations, "\n")
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
	migrations := strings.Join(schemaMigrations, "\n")
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
	migrations := strings.Join(schemaMigrations, "\n")
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
	migrations := strings.Join(schemaMigrations, "\n")
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
	migrations := strings.Join(schemaMigrations, "\n")
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

func TestMigrationsPersistHermesActiveRunPointer(t *testing.T) {
	migrations := strings.Join(schemaMigrations, "\n")
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
