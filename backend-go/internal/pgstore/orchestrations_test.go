package pgstore

import "testing"

func samplePlanPhases() []PlanPhase {
	return []PlanPhase{
		{Key: "schema", Brief: "design the schema", Target: PhaseTargetMain},
		{Key: "store", Brief: "build the store", DependsOn: []string{"schema"}, Target: PhaseTargetIntegration},
		{Key: "tests", Brief: "write the tests", DependsOn: []string{"schema", "store"}, Target: PhaseTargetIntegration},
	}
}

// TestOrchestrationPlanHashDeterministic proves the freeze point is stable:
// the same plan hashes to the same value across calls.
func TestOrchestrationPlanHashDeterministic(t *testing.T) {
	_, h1, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration", samplePlanPhases())
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	_, h2, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration", samplePlanPhases())
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("plan hash not deterministic: %q vs %q", h1, h2)
	}
	if h1 == "" {
		t.Fatalf("plan hash is empty")
	}
}

// TestOrchestrationPlanHashDepOrderInvariant proves depends_on ordering does
// not change the hash (deps are normalized + sorted), so two equivalent plans
// freeze to the same ref.
func TestOrchestrationPlanHashDepOrderInvariant(t *testing.T) {
	a := samplePlanPhases()
	b := samplePlanPhases()
	b[2].DependsOn = []string{"store", "schema", "store"} // reordered + duplicated

	_, ha, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration", a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	_, hb, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration", b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if ha != hb {
		t.Fatalf("dep order changed the plan hash: %q vs %q", ha, hb)
	}
}

// TestOrchestrationPlanHashEditChangesHash proves a logical-plan edit produces
// a different content hash — the property that lets an edit never mutate an
// existing run's frozen history.
func TestOrchestrationPlanHashEditChangesHash(t *testing.T) {
	_, base, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration", samplePlanPhases())
	if err != nil {
		t.Fatalf("base hash: %v", err)
	}
	edited := samplePlanPhases()
	edited[1].Brief = "build the store, differently"
	_, after, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration", edited)
	if err != nil {
		t.Fatalf("edited hash: %v", err)
	}
	if base == after {
		t.Fatalf("editing a brief did not change the plan hash")
	}
}

// TestOrchestrationPlanHashRepoNormalized proves repo owner/name are
// lower-cased into the canonical plan so casing does not fork the hash.
func TestOrchestrationPlanHashRepoNormalized(t *testing.T) {
	_, lower, err := OrchestrationPlanHash("romaine-life", "tank-operator", "", samplePlanPhases())
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	_, mixed, err := OrchestrationPlanHash("Romaine-Life", "Tank-Operator", "", samplePlanPhases())
	if err != nil {
		t.Fatalf("mixed: %v", err)
	}
	if lower != mixed {
		t.Fatalf("repo casing changed the plan hash: %q vs %q", lower, mixed)
	}
}

func TestOrchestrationPlanHashRejectsBadPlans(t *testing.T) {
	cases := []struct {
		name   string
		phases []PlanPhase
	}{
		{"no phases", nil},
		{"empty key", []PlanPhase{{Key: "  ", Brief: "x", Target: PhaseTargetMain}}},
		{"empty brief", []PlanPhase{{Key: "a", Brief: "  ", Target: PhaseTargetMain}}},
		{"bad target", []PlanPhase{{Key: "a", Brief: "x", Target: PhaseTarget("trunk")}}},
		{"duplicate key", []PlanPhase{
			{Key: "a", Brief: "x", Target: PhaseTargetMain},
			{Key: "a", Brief: "y", Target: PhaseTargetMain},
		}},
		{"self dependency", []PlanPhase{{Key: "a", Brief: "x", DependsOn: []string{"a"}, Target: PhaseTargetMain}}},
		{"unknown dependency", []PlanPhase{{Key: "a", Brief: "x", DependsOn: []string{"ghost"}, Target: PhaseTargetMain}}},
		{"dependency cycle", []PlanPhase{
			{Key: "a", Brief: "x", DependsOn: []string{"b"}, Target: PhaseTargetMain},
			{Key: "b", Brief: "y", DependsOn: []string{"a"}, Target: PhaseTargetMain},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := OrchestrationPlanHash("o", "r", "", tc.phases); err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

// TestOrchestrationPhaseIDStable proves phase identity is derived from
// (orchestration_id, phase_key) and is reproducible.
func TestOrchestrationPhaseIDStable(t *testing.T) {
	a := OrchestrationPhaseID("orch_1", "schema")
	b := OrchestrationPhaseID("orch_1", "schema")
	if a != b {
		t.Fatalf("phase id not stable: %q vs %q", a, b)
	}
	if a == OrchestrationPhaseID("orch_1", "store") {
		t.Fatalf("distinct phase keys produced the same id")
	}
	if a == OrchestrationPhaseID("orch_2", "schema") {
		t.Fatalf("distinct orchestrations produced the same phase id")
	}
}
