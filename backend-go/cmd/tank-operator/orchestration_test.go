package main

import (
	"context"
	"sync"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// fakeOrchStore is an in-memory model of OrchestrationStore that reproduces the
// guarded-UPDATE semantics of the real store exactly (MarkPhaseReady only flips
// pending, ClaimPhaseForSpawn only flips ready, RequeuePhaseForRespawn only
// flips running-with-empty-spoke). The engine's DAG / idempotency / reconcile
// logic is therefore exercised against the same conditional transitions
// production runs, without Postgres.
type fakeOrchStore struct {
	mu     sync.Mutex
	orch   pgstore.Orchestration
	phases map[string]*pgstore.OrchestrationPhase // by phase_id
	order  []string                               // phase_id in ordinal order
}

const (
	fakePROwner = "romaine-life"
	fakePRName  = "tank-operator"
)

type phaseSpec struct {
	key      string
	deps     []string
	status   pgstore.PhaseStatus
	prNumber int // PR coordinates pre-linked for the merged-PR reverse lookup
	spoke    string
}

func phaseID(key string) string { return "phase-" + key }

func newFakeOrchStore(state pgstore.OrchestrationState, specs ...phaseSpec) *fakeOrchStore {
	s := &fakeOrchStore{
		orch: pgstore.Orchestration{
			OrchestrationID: "orch-1",
			OwnerEmail:      "owner@example.test",
			RepoOwner:       fakePROwner,
			RepoName:        fakePRName,
			State:           state,
			PhaseCount:      len(specs),
		},
		phases: make(map[string]*pgstore.OrchestrationPhase, len(specs)),
	}
	for i, sp := range specs {
		id := phaseID(sp.key)
		p := &pgstore.OrchestrationPhase{
			PhaseID:         id,
			OrchestrationID: "orch-1",
			Ordinal:         i,
			Key:             sp.key,
			Brief:           "do " + sp.key,
			DependsOn:       sp.deps,
			Target:          pgstore.PhaseTargetMain,
			Status:          sp.status,
			SpokeSessionID:  sp.spoke,
		}
		if sp.prNumber > 0 {
			p.PROwner = fakePROwner
			p.PRName = fakePRName
			p.PRNumber = sp.prNumber
		}
		s.phases[id] = p
		s.order = append(s.order, id)
	}
	return s
}

func (s *fakeOrchStore) snapshot(id string) pgstore.OrchestrationPhase { return *s.phases[id] }

func (s *fakeOrchStore) GetWithPhases(_ context.Context, _ string) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pgstore.OrchestrationPhase, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, *s.phases[id])
	}
	return s.orch, out, nil
}

func (s *fakeOrchStore) GetPhase(_ context.Context, phaseID string) (pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.phases[phaseID]
	if !ok {
		return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
	}
	return *p, nil
}

func (s *fakeOrchStore) GetPhaseByPR(_ context.Context, prOwner, prName string, prNumber int) (pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		p := s.phases[id]
		if p.PROwner == prOwner && p.PRName == prName && p.PRNumber == prNumber {
			return *p, nil
		}
	}
	return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
}

func (s *fakeOrchStore) GetPhaseBySpokeSession(_ context.Context, spoke string) (pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if spoke == "" {
		return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
	}
	for _, id := range s.order {
		if s.phases[id].SpokeSessionID == spoke {
			return *s.phases[id], nil
		}
	}
	return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
}

func (s *fakeOrchStore) MarkPhaseReady(_ context.Context, phaseID string) (pgstore.OrchestrationPhase, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil || p.Status != pgstore.PhasePending {
		return pgstore.OrchestrationPhase{}, false, nil
	}
	p.Status = pgstore.PhaseReady
	return *p, true, nil
}

func (s *fakeOrchStore) ClaimPhaseForSpawn(_ context.Context, phaseID string) (pgstore.OrchestrationPhase, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil || p.Status != pgstore.PhaseReady {
		return pgstore.OrchestrationPhase{}, false, nil
	}
	p.Status = pgstore.PhaseRunning
	return *p, true, nil
}

func (s *fakeOrchStore) RequeuePhaseForRespawn(_ context.Context, phaseID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil || p.Status != pgstore.PhaseRunning || p.SpokeSessionID != "" {
		return false, nil
	}
	p.Status = pgstore.PhaseReady
	return true, nil
}

func (s *fakeOrchStore) AttachPhaseSpoke(_ context.Context, phaseID, spoke string) (pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil {
		return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
	}
	p.SpokeSessionID = spoke
	p.Status = pgstore.PhaseRunning
	return *p, nil
}

func (s *fakeOrchStore) MarkPhasePROpen(_ context.Context, phaseID string, req pgstore.SetPhasePRRequest) (pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil {
		return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
	}
	p.PROwner = req.PROwner
	p.PRName = req.PRName
	p.PRNumber = req.PRNumber
	p.PRURL = req.PRURL
	p.Status = pgstore.PhasePROpen
	return *p, nil
}

func (s *fakeOrchStore) MarkPhaseMerged(_ context.Context, phaseID, mergeSHA string) (pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil {
		return pgstore.OrchestrationPhase{}, pgstore.ErrOrchestrationPhaseNotFound
	}
	p.MergeSHA = mergeSHA
	p.Status = pgstore.PhaseMerged
	return *p, nil
}

func (s *fakeOrchStore) UpdateState(_ context.Context, _ string, state pgstore.OrchestrationState) (pgstore.Orchestration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orch.State = state
	return s.orch, nil
}

func (s *fakeOrchStore) ListActiveOrchestrationIDs(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orch.State == pgstore.OrchestrationApproved || s.orch.State == pgstore.OrchestrationRunning {
		return []string{s.orch.OrchestrationID}, nil
	}
	return nil, nil
}

// recordingSpawner counts dispatch calls per phase key and assigns a spoke id.
type recordingSpawner struct {
	mu     sync.Mutex
	counts map[string]int
}

func newRecordingSpawner() *recordingSpawner { return &recordingSpawner{counts: map[string]int{}} }

func (r *recordingSpawner) spawn(_ context.Context, _ pgstore.Orchestration, phase pgstore.OrchestrationPhase) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[phase.Key]++
	return "spoke-" + phase.Key, nil
}

func (r *recordingSpawner) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[key]
}

func newTestEngine(store orchestrationStore, spawn phaseSpokeSpawnFunc) *orchestrationEngine {
	e := newOrchestrationEngine(store, spawn)
	return e
}

// TestAdvanceOnMergeSpawnsNextReadyPhase: merging A's PR marks A merged and
// dispatches B (which depends on A).
func TestAdvanceOnMergeSpawnsNextReadyPhase(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 100, "sha-a")

	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("phase a status = %q, want merged", got)
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase b status = %q, want running", got)
	}
	if store.snapshot(phaseID("b")).SpokeSessionID != "spoke-b" {
		t.Fatalf("phase b spoke = %q, want spoke-b", store.snapshot(phaseID("b")).SpokeSessionID)
	}
	if sp.count("b") != 1 {
		t.Fatalf("phase b spawned %d times, want 1", sp.count("b"))
	}
}

// TestAdvanceOnMergeIdempotent: a duplicate merged-PR delivery advances the run
// exactly once — B is spawned once, not twice.
func TestAdvanceOnMergeIdempotent(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 100, "sha-a")
	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 100, "sha-a") // duplicate

	if sp.count("b") != 1 {
		t.Fatalf("phase b spawned %d times across a double merge event, want 1", sp.count("b"))
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase b status = %q, want running", got)
	}
}

// TestReconcileRecoversMissedMerge: a phase whose PR actually merged but whose
// advance never ran (dropped webhook) is repaired by the reconcile backstop —
// the dependent phase gets dispatched without any webhook.
func TestReconcileRecoversMissedMerge(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseMerged, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	if err := e.reconcileAllActive(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase b status = %q, want running after reconcile", got)
	}
	if sp.count("b") != 1 {
		t.Fatalf("phase b spawned %d times, want 1", sp.count("b"))
	}
}

// TestReconcileBootstrapsApprovedRun: an approved run's root phase (no deps,
// pending, no spoke) is dispatched by the reconcile loop, and the run flips to
// running.
func TestReconcileBootstrapsApprovedRun(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationApproved,
		phaseSpec{key: "root", status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	if err := e.reconcileAllActive(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := store.snapshot(phaseID("root")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("root phase status = %q, want running", got)
	}
	if got := store.orch.State; got != pgstore.OrchestrationRunning {
		t.Fatalf("run state = %q, want running", got)
	}
	if sp.count("root") != 1 {
		t.Fatalf("root spawned %d times, want 1", sp.count("root"))
	}
}

// TestFanInWaitsForAllDeps: a phase with two dependencies becomes ready only
// when both have merged.
func TestFanInWaitsForAllDeps(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", status: pgstore.PhaseRunning, prNumber: 200, spoke: "spoke-b"},
		phaseSpec{key: "c", deps: []string{"a", "b"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	// Merge only A: C still has an unmerged dep (B), so it must not dispatch.
	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 100, "sha-a")
	if got := store.snapshot(phaseID("c")).Status; got != pgstore.PhasePending {
		t.Fatalf("phase c status = %q after one dep merged, want still pending", got)
	}
	if sp.count("c") != 0 {
		t.Fatalf("phase c spawned %d times before both deps merged, want 0", sp.count("c"))
	}

	// Merge B: now both deps are merged, C becomes ready and dispatches.
	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 200, "sha-b")
	if got := store.snapshot(phaseID("c")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase c status = %q after both deps merged, want running", got)
	}
	if sp.count("c") != 1 {
		t.Fatalf("phase c spawned %d times, want 1", sp.count("c"))
	}
}

// TestRunReachesDone: when the final phase merges and no phase remains active,
// the run transitions to done.
func TestRunReachesDone(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseMerged, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhaseRunning, prNumber: 200, spoke: "spoke-b"},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 200, "sha-b")

	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("phase b status = %q, want merged", got)
	}
	if got := store.orch.State; got != pgstore.OrchestrationDone {
		t.Fatalf("run state = %q, want done", got)
	}
}

// TestAdvanceOnMergeNonPhaseNoop: a merged PR that is not an orchestration phase
// does nothing (and does not error).
func TestAdvanceOnMergeNonPhaseNoop(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, prNumber: 100, spoke: "spoke-a"},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	e.advanceOnMerge(context.Background(), fakePROwner, fakePRName, 999, "sha-x") // no phase has PR 999

	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase a disturbed by unrelated merge: %q", got)
	}
}

// TestLinkPhasePRStampsCoordinates: when a phase's spoke registers a PR, the
// coordinates land on the phase and it moves to pr_open; a non-spoke session is
// a no-op.
func TestLinkPhasePRStampsCoordinates(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, spoke: "spoke-a"},
	)
	e := newTestEngine(store, newRecordingSpawner().spawn)

	e.linkPhasePR(context.Background(), "spoke-a", pgstore.SetPhasePRRequest{
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 321,
		PRURL: "https://github.com/romaine-life/tank-operator/pull/321",
	})
	got := store.snapshot(phaseID("a"))
	if got.Status != pgstore.PhasePROpen {
		t.Fatalf("phase a status = %q, want pr_open", got.Status)
	}
	if got.PRNumber != 321 {
		t.Fatalf("phase a pr_number = %d, want 321", got.PRNumber)
	}

	// A session that is not any phase's spoke must not error or mutate anything.
	e.linkPhasePR(context.Background(), "some-random-session", pgstore.SetPhasePRRequest{
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 555,
	})
	if store.snapshot(phaseID("a")).PRNumber != 321 {
		t.Fatalf("unrelated session disturbed phase a PR linkage")
	}
}

// TestLinkPhasePRSkipsMergedPhase: a re-registration after merge must not drag a
// merged phase back to pr_open.
func TestLinkPhasePRSkipsMergedPhase(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseMerged, prNumber: 100, spoke: "spoke-a"},
	)
	e := newTestEngine(store, newRecordingSpawner().spawn)

	e.linkPhasePR(context.Background(), "spoke-a", pgstore.SetPhasePRRequest{
		PROwner: fakePROwner, PRName: fakePRName, PRNumber: 100,
	})
	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("merged phase dragged back to %q", got)
	}
}
