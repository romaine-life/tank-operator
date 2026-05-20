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
		"'type', 'session.status'",
		"'visibility', 'durable'",
		"WHEN 'loading' THEN '00000000'",
		"WHEN 'ready' THEN '00000001'",
		"WHEN 'failed' THEN '00000002'",
		"se.event_id = v_event_id",
		"FROM sessions",
	} {
		if !strings.Contains(migrations, want) {
			t.Fatalf("schema migrations missing %q", want)
		}
	}
	if strings.Index(migrations, "CREATE TABLE IF NOT EXISTS session_events") > strings.Index(migrations, "tank_upsert_session_status_event") {
		t.Fatal("session_events table must exist before session status events are written")
	}
	if strings.Index(migrations, "CREATE TRIGGER tank_sessions_status_events_after_write") > strings.Index(migrations, "SELECT tank_upsert_session_status_event") {
		t.Fatal("session status trigger should be installed before backfill")
	}
	if strings.Index(migrations, "DROP TABLE IF EXISTS session_lifecycle_events") < strings.Index(migrations, "SELECT tank_upsert_session_status_event") {
		t.Fatal("session status transcript backfill must not depend on the retired lifecycle ledger")
	}
}
