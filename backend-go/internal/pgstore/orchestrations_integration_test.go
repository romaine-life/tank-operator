package pgstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func integrationPlan() []PlanPhase {
	return []PlanPhase{
		{Key: "schema", Brief: "design the schema", Target: PhaseTargetMain},
		{Key: "store", Brief: "build the store", DependsOn: []string{"schema"}, Target: PhaseTargetIntegration},
		{Key: "tests", Brief: "write the tests", DependsOn: []string{"schema", "store"}, Target: PhaseTargetIntegration},
	}
}

// TestOrchestrationStoreCreateAndRead exercises the orchestrations /
// orchestration_phases tables (migrations 0169-0173) against a real Postgres
// schema: the create-from-approved-plan round-trip, the frozen plan snapshot,
// and the materialized DAG. Skips locally unless TANK_TEST_POSTGRES_DSN is set;
// runs in CI against the postgres:16 service (see .github/workflows/go-backend.yml).
func TestOrchestrationStoreCreateAndRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orchestrations")
	store := NewOrchestrationStore(pool)

	orch, phases, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail:        "Owner@Example.test",
		ApproverEmail:     "Approver@Example.test",
		RepoOwner:         "Romaine-Life",
		RepoName:          "Tank-Operator",
		IntegrationBranch: "integration/orch-1",
		State:             OrchestrationApproved,
		Phases:            integrationPlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if orch.OwnerEmail != "owner@example.test" {
		t.Fatalf("owner_email = %q, want lowercased", orch.OwnerEmail)
	}
	if orch.ApproverEmail != "approver@example.test" {
		t.Fatalf("approver_email = %q, want lowercased", orch.ApproverEmail)
	}
	if orch.RepoOwner != "romaine-life" || orch.RepoName != "tank-operator" {
		t.Fatalf("repo = %q/%q, want lowercased", orch.RepoOwner, orch.RepoName)
	}
	if orch.IntegrationBranch != "integration/orch-1" {
		t.Fatalf("integration_branch = %q", orch.IntegrationBranch)
	}
	if orch.State != OrchestrationApproved {
		t.Fatalf("state = %q, want approved", orch.State)
	}
	if orch.ApprovedAt == nil {
		t.Fatalf("approved_at not stamped for a non-draft run")
	}
	if orch.PhaseCount != 3 || len(phases) != 3 {
		t.Fatalf("phase_count=%d len(phases)=%d, want 3", orch.PhaseCount, len(phases))
	}

	// plan_hash must content-address the canonical plan.
	_, wantHash, err := OrchestrationPlanHash("romaine-life", "tank-operator", "integration/orch-1", integrationPlan())
	if err != nil {
		t.Fatalf("recompute hash: %v", err)
	}
	if orch.PlanHash != wantHash {
		t.Fatalf("plan_hash = %q, want %q", orch.PlanHash, wantHash)
	}

	// Phases come back ordered, with their deps materialized.
	if phases[0].Key != "schema" || phases[1].Key != "store" || phases[2].Key != "tests" {
		t.Fatalf("phase order = %q/%q/%q", phases[0].Key, phases[1].Key, phases[2].Key)
	}
	if phases[0].Status != PhasePending {
		t.Fatalf("phase[0] status = %q, want pending", phases[0].Status)
	}
	if phases[1].Target != PhaseTargetIntegration {
		t.Fatalf("phase[1] target = %q, want integration", phases[1].Target)
	}
	if len(phases[2].DependsOn) != 2 || phases[2].DependsOn[0] != "schema" || phases[2].DependsOn[1] != "store" {
		t.Fatalf("phase[2] depends_on = %v, want [schema store]", phases[2].DependsOn)
	}
	// Phase identity is derived from (orchestration_id, phase_key).
	if phases[0].PhaseID != OrchestrationPhaseID(orch.OrchestrationID, "schema") {
		t.Fatalf("phase[0] id = %q, not derived from key", phases[0].PhaseID)
	}
}

// TestOrchestrationStorePhaseTransitions covers the phase runtime mutations:
// spoke attach -> pr open -> merge, plus the run state machine.
func TestOrchestrationStorePhaseTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orchestrations_phases")
	store := NewOrchestrationStore(pool)

	orch, phases, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "o@x.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		State:      OrchestrationRunning,
		Phases:     integrationPlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	schema := phases[0]

	ready, err := store.UpdatePhaseStatus(ctx, schema.PhaseID, PhaseReady)
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	if ready.Status != PhaseReady {
		t.Fatalf("status = %q, want ready", ready.Status)
	}

	running, err := store.AttachPhaseSpoke(ctx, schema.PhaseID, "spoke-7")
	if err != nil {
		t.Fatalf("attach spoke: %v", err)
	}
	if running.SpokeSessionID != "spoke-7" || running.Status != PhaseRunning {
		t.Fatalf("after attach: spoke=%q status=%q", running.SpokeSessionID, running.Status)
	}

	opened, err := store.MarkPhasePROpen(ctx, schema.PhaseID, SetPhasePRRequest{
		PROwner:  "Romaine-Life",
		PRName:   "Tank-Operator",
		PRNumber: 42,
		PRURL:    "https://github.com/romaine-life/tank-operator/pull/42",
	})
	if err != nil {
		t.Fatalf("mark pr open: %v", err)
	}
	if opened.Status != PhasePROpen || opened.PRNumber != 42 || opened.PROwner != "romaine-life" {
		t.Fatalf("after pr open: status=%q pr=%q/%q#%d", opened.Status, opened.PROwner, opened.PRName, opened.PRNumber)
	}

	merged, err := store.MarkPhaseMerged(ctx, schema.PhaseID, "mergesha42")
	if err != nil {
		t.Fatalf("mark merged: %v", err)
	}
	if merged.Status != PhaseMerged || merged.MergeSHA != "mergesha42" {
		t.Fatalf("after merge: status=%q merge_sha=%q", merged.Status, merged.MergeSHA)
	}

	done, err := store.UpdateState(ctx, orch.OrchestrationID, OrchestrationDone)
	if err != nil {
		t.Fatalf("update state: %v", err)
	}
	if done.State != OrchestrationDone {
		t.Fatalf("state = %q, want done", done.State)
	}
}

// TestOrchestrationPhaseRejectsBadStatus proves the CHECK constraint on the
// phase status column rejects an out-of-set value.
func TestOrchestrationPhaseRejectsBadStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orchestrations_check")
	store := NewOrchestrationStore(pool)

	_, phases, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "o@x.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Phases:     integrationPlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE orchestration_phases SET status = 'bogus' WHERE phase_id = $1
	`, phases[0].PhaseID); err == nil {
		t.Fatalf("update to status='bogus' succeeded, want CHECK violation")
	}

	// And the run-level state CHECK.
	if _, err := pool.Exec(ctx, `
		UPDATE orchestrations SET state = 'paused' WHERE orchestration_id = $1
	`, phases[0].OrchestrationID); err == nil {
		t.Fatalf("update to state='paused' succeeded, want CHECK violation")
	}
}

// TestOrchestrationGetPhaseByPR covers the PR -> phase reverse lookup the
// merged-PR webhook handler depends on (matched case-insensitively on the
// GitHub slug), and proves a phase carries its orchestration_id back.
func TestOrchestrationGetPhaseByPR(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orchestrations_pr")
	store := NewOrchestrationStore(pool)

	orch, phases, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "o@x.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Phases:     integrationPlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.MarkPhasePROpen(ctx, phases[1].PhaseID, SetPhasePRRequest{
		PROwner:  "romaine-life",
		PRName:   "tank-operator",
		PRNumber: 99,
	}); err != nil {
		t.Fatalf("mark pr open: %v", err)
	}

	got, err := store.GetPhaseByPR(ctx, "Romaine-Life", "Tank-Operator", 99)
	if err != nil {
		t.Fatalf("GetPhaseByPR: %v", err)
	}
	if got.PhaseID != phases[1].PhaseID {
		t.Fatalf("GetPhaseByPR returned phase %q, want %q", got.PhaseID, phases[1].PhaseID)
	}
	if got.OrchestrationID != orch.OrchestrationID {
		t.Fatalf("phase carries orchestration_id %q, want %q", got.OrchestrationID, orch.OrchestrationID)
	}

	// A PR with no phase is a clean not-found.
	if _, err := store.GetPhaseByPR(ctx, "romaine-life", "tank-operator", 12345); err != ErrOrchestrationPhaseNotFound {
		t.Fatalf("GetPhaseByPR(unknown) = %v, want ErrOrchestrationPhaseNotFound", err)
	}
}

// TestOrchestrationFrozenPlanImmutability proves the core freeze invariant:
// runtime mutations never touch the frozen plan snapshot, and a later edit to
// the logical plan creates a distinct run without disturbing the first run's
// history.
func TestOrchestrationFrozenPlanImmutability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orchestrations_frozen")
	store := NewOrchestrationStore(pool)

	orch, phases, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "o@x.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		State:      OrchestrationRunning,
		Phases:     integrationPlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	frozenPlan := append([]byte(nil), orch.Plan...)
	frozenHash := orch.PlanHash

	// Drive the runtime hard: status, spoke, PR, merge, run state.
	if _, err := store.UpdatePhaseStatus(ctx, phases[0].PhaseID, PhaseReady); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if _, err := store.AttachPhaseSpoke(ctx, phases[0].PhaseID, "spoke-1"); err != nil {
		t.Fatalf("attach spoke: %v", err)
	}
	if _, err := store.MarkPhasePROpen(ctx, phases[0].PhaseID, SetPhasePRRequest{
		PROwner: "romaine-life", PRName: "tank-operator", PRNumber: 7,
	}); err != nil {
		t.Fatalf("mark pr open: %v", err)
	}
	if _, err := store.MarkPhaseMerged(ctx, phases[0].PhaseID, "sha7"); err != nil {
		t.Fatalf("mark merged: %v", err)
	}
	if _, err := store.UpdateState(ctx, orch.OrchestrationID, OrchestrationDone); err != nil {
		t.Fatalf("update state: %v", err)
	}

	// Re-read: the frozen plan snapshot + hash are byte-identical, and the
	// immutable plan columns on the phase rows are untouched.
	after, afterPhases, err := store.GetWithPhases(ctx, orch.OrchestrationID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !bytes.Equal(after.Plan, frozenPlan) {
		t.Fatalf("frozen plan jsonb changed after runtime mutations")
	}
	if after.PlanHash != frozenHash {
		t.Fatalf("plan_hash changed after runtime mutations: %q vs %q", after.PlanHash, frozenHash)
	}
	if afterPhases[0].Brief != "design the schema" || afterPhases[0].Target != PhaseTargetMain || afterPhases[0].Key != "schema" {
		t.Fatalf("frozen plan columns mutated on phase row: %+v", afterPhases[0])
	}

	// A later edit to the logical plan is a distinct run with a distinct hash;
	// the first run's frozen history is undisturbed.
	edited := integrationPlan()
	edited[0].Brief = "design the schema, v2"
	orch2, _, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "o@x.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		Phases:     edited,
	})
	if err != nil {
		t.Fatalf("create edited: %v", err)
	}
	if orch2.OrchestrationID == orch.OrchestrationID {
		t.Fatalf("edited plan reused the first run's id")
	}
	if orch2.PlanHash == frozenHash {
		t.Fatalf("edited plan produced the same plan_hash as the original")
	}

	original, _, err := store.GetWithPhases(ctx, orch.OrchestrationID)
	if err != nil {
		t.Fatalf("re-read original: %v", err)
	}
	if original.PlanHash != frozenHash || !bytes.Equal(original.Plan, frozenPlan) {
		t.Fatalf("creating an edited run mutated the original run's frozen plan")
	}
}
