package pgstore

// Integration coverage for the advance-loop store methods added on top of the
// #1264 data layer: the guarded status transitions the deterministic engine
// relies on (MarkPhaseReady / ClaimPhaseForSpawn / RequeuePhaseForRespawn), the
// session->phase reverse lookup, and the active-run enumeration. Runs against a
// real Postgres schema so the conditional UPDATE WHERE clauses — the actual
// concurrency choke points — are exercised exactly as production runs them.
// Skips locally unless TANK_TEST_POSTGRES_DSN is set.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func advancePlan() []PlanPhase {
	return []PlanPhase{
		{Key: "a", Brief: "phase a", Target: PhaseTargetMain},
		{Key: "b", Brief: "phase b", DependsOn: []string{"a"}, Target: PhaseTargetMain},
	}
}

func TestOrchestrationPhaseGuardedTransitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orch_advance")
	store := NewOrchestrationStore(pool)

	orch, _, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "owner@example.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		State:      OrchestrationApproved,
		Phases:     advancePlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	aID := OrchestrationPhaseID(orch.OrchestrationID, "a")

	// MarkPhaseReady is guarded on status='pending': the first call wins, a
	// repeat is a no-op (idempotent).
	if _, ok, err := store.MarkPhaseReady(ctx, aID); err != nil || !ok {
		t.Fatalf("first MarkPhaseReady: ok=%v err=%v, want true/nil", ok, err)
	}
	if _, ok, err := store.MarkPhaseReady(ctx, aID); err != nil || ok {
		t.Fatalf("second MarkPhaseReady: ok=%v err=%v, want false/nil", ok, err)
	}

	// ClaimPhaseForSpawn is guarded on status='ready': exactly one claim wins,
	// the second sees it already running and loses. This is the duplicate-webhook
	// / racing-replica guard.
	claimed, ok, err := store.ClaimPhaseForSpawn(ctx, aID)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v, want true/nil", ok, err)
	}
	if claimed.Status != PhaseRunning {
		t.Fatalf("claimed status = %q, want running", claimed.Status)
	}
	if claimed.SpokeSessionID != "" {
		t.Fatalf("claim must not set spoke; got %q", claimed.SpokeSessionID)
	}
	if _, ok, err := store.ClaimPhaseForSpawn(ctx, aID); err != nil || ok {
		t.Fatalf("second claim: ok=%v err=%v, want false/nil (already running)", ok, err)
	}

	// RequeuePhaseForRespawn recovers running-with-empty-spoke; once the spoke is
	// attached it must refuse to disturb the live phase.
	requeued, err := store.RequeuePhaseForRespawn(ctx, aID)
	if err != nil || !requeued {
		t.Fatalf("requeue running-empty-spoke: requeued=%v err=%v, want true/nil", requeued, err)
	}
	if p, _ := store.GetPhase(ctx, aID); p.Status != PhaseReady {
		t.Fatalf("post-requeue status = %q, want ready", p.Status)
	}
	// Re-claim and attach a spoke; now requeue must be a no-op.
	if _, ok, err := store.ClaimPhaseForSpawn(ctx, aID); err != nil || !ok {
		t.Fatalf("re-claim: ok=%v err=%v", ok, err)
	}
	if _, err := store.AttachPhaseSpoke(ctx, aID, "session-a"); err != nil {
		t.Fatalf("attach spoke: %v", err)
	}
	if requeued, err := store.RequeuePhaseForRespawn(ctx, aID); err != nil || requeued {
		t.Fatalf("requeue with live spoke: requeued=%v err=%v, want false/nil", requeued, err)
	}
}

func TestOrchestrationGetPhaseBySpokeSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orch_spoke")
	store := NewOrchestrationStore(pool)

	orch, _, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "owner@example.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		State:      OrchestrationApproved,
		Phases:     advancePlan(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	aID := OrchestrationPhaseID(orch.OrchestrationID, "a")

	// No spoke yet -> not found.
	if _, err := store.GetPhaseBySpokeSession(ctx, "session-a"); !errors.Is(err, ErrOrchestrationPhaseNotFound) {
		t.Fatalf("lookup before attach: err=%v, want ErrOrchestrationPhaseNotFound", err)
	}
	if _, err := store.GetPhaseBySpokeSession(ctx, ""); !errors.Is(err, ErrOrchestrationPhaseNotFound) {
		t.Fatalf("empty spoke lookup: err=%v, want ErrOrchestrationPhaseNotFound", err)
	}

	if _, _, err := store.MarkPhaseReady(ctx, aID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if _, _, err := store.ClaimPhaseForSpawn(ctx, aID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.AttachPhaseSpoke(ctx, aID, "session-a"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	got, err := store.GetPhaseBySpokeSession(ctx, "session-a")
	if err != nil {
		t.Fatalf("lookup after attach: %v", err)
	}
	if got.PhaseID != aID || got.Key != "a" {
		t.Fatalf("resolved phase = %q/%q, want %q/a", got.PhaseID, got.Key, aID)
	}
}

func TestOrchestrationListActiveOrchestrationIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "orch_active")
	store := NewOrchestrationStore(pool)

	approved, _, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "owner@example.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		State:      OrchestrationApproved,
		Phases:     advancePlan(),
	})
	if err != nil {
		t.Fatalf("create approved: %v", err)
	}
	draft, _, err := store.Create(ctx, CreateOrchestrationRequest{
		OwnerEmail: "owner@example.test",
		RepoOwner:  "romaine-life",
		RepoName:   "tank-operator",
		State:      OrchestrationDraft,
		Phases:     advancePlan(),
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	ids, err := store.ListActiveOrchestrationIDs(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if !containsString(ids, approved.OrchestrationID) {
		t.Fatalf("approved run %q missing from active set %v", approved.OrchestrationID, ids)
	}
	if containsString(ids, draft.OrchestrationID) {
		t.Fatalf("draft run %q must not be in active set %v", draft.OrchestrationID, ids)
	}

	// A run driven to done drops out of the active set.
	if _, err := store.UpdateState(ctx, approved.OrchestrationID, OrchestrationDone); err != nil {
		t.Fatalf("update state done: %v", err)
	}
	ids, err = store.ListActiveOrchestrationIDs(ctx)
	if err != nil {
		t.Fatalf("list active after done: %v", err)
	}
	if containsString(ids, approved.OrchestrationID) {
		t.Fatalf("done run %q must not be active: %v", approved.OrchestrationID, ids)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
