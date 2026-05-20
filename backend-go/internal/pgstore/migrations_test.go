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
