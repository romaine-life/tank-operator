package main

import (
	"context"
	"strings"
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
	target   pgstore.PhaseTarget // defaults to main when empty
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
		target := sp.target
		if target == "" {
			target = pgstore.PhaseTargetMain
		}
		p := &pgstore.OrchestrationPhase{
			PhaseID:         id,
			OrchestrationID: "orch-1",
			Ordinal:         i,
			Key:             sp.key,
			Brief:           "do " + sp.key,
			DependsOn:       sp.deps,
			Target:          target,
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

func (s *fakeOrchStore) Get(_ context.Context, _ string) (pgstore.Orchestration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orch, nil
}

// Create models pgstore.OrchestrationStore.Create enough for the kickoff path:
// it materializes the request's phases as 'pending' and stores the run, so
// createAndStartOrchestration can be exercised against the same fake the engine
// drives.
func (s *fakeOrchStore) Create(_ context.Context, req pgstore.CreateOrchestrationRequest) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(req.OrchestrationID)
	if id == "" {
		id = "orch-1"
	}
	s.orch = pgstore.Orchestration{
		OrchestrationID:   id,
		OwnerEmail:        req.OwnerEmail,
		ApproverEmail:     req.ApproverEmail,
		RepoOwner:         req.RepoOwner,
		RepoName:          req.RepoName,
		IntegrationBranch: req.IntegrationBranch,
		State:             req.State,
		PhaseCount:        len(req.Phases),
	}
	s.phases = make(map[string]*pgstore.OrchestrationPhase, len(req.Phases))
	s.order = nil
	for i, p := range req.Phases {
		pid := pgstore.OrchestrationPhaseID(id, p.Key)
		target := p.Target
		if target == "" {
			target = pgstore.PhaseTargetMain
		}
		s.phases[pid] = &pgstore.OrchestrationPhase{
			PhaseID:         pid,
			OrchestrationID: id,
			Ordinal:         i,
			Key:             p.Key,
			Brief:           p.Brief,
			DependsOn:       p.DependsOn,
			Target:          target,
			Status:          pgstore.PhasePending,
		}
		s.order = append(s.order, pid)
	}
	out := make([]pgstore.OrchestrationPhase, 0, len(s.order))
	for _, pid := range s.order {
		out = append(out, *s.phases[pid])
	}
	return s.orch, out, nil
}

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

func (s *fakeOrchStore) BlockPhase(_ context.Context, phaseID, reason string) (pgstore.OrchestrationPhase, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil {
		return pgstore.OrchestrationPhase{}, false, nil
	}
	switch p.Status {
	case pgstore.PhasePending, pgstore.PhaseReady, pgstore.PhaseRunning, pgstore.PhasePROpen:
		p.Status = pgstore.PhaseBlocked
		p.Detail = reason
		return *p, true, nil
	default:
		return pgstore.OrchestrationPhase{}, false, nil
	}
}

func (s *fakeOrchStore) UnblockPhase(_ context.Context, phaseID string) (pgstore.OrchestrationPhase, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.phases[phaseID]
	if p == nil || p.Status != pgstore.PhaseBlocked {
		return pgstore.OrchestrationPhase{}, false, nil
	}
	p.Status = pgstore.PhasePending
	p.Detail = ""
	return *p, true, nil
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

// recordingMerger is a fake phaseMergeFunc: it merges the phases whose keys are
// configured (returning their merge SHA) and refuses the rest, modeling
// GitHub's green gate without any network.
type recordingMerger struct {
	mu        sync.Mutex
	mergeable map[string]string // phase key -> merge sha; absent => refused (not green)
	calls     map[string]int
}

func newRecordingMerger() *recordingMerger {
	return &recordingMerger{mergeable: map[string]string{}, calls: map[string]int{}}
}

func (m *recordingMerger) merge(_ context.Context, _ pgstore.Orchestration, phase pgstore.OrchestrationPhase) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[phase.Key]++
	if sha, ok := m.mergeable[phase.Key]; ok {
		return sha, true, nil
	}
	return "", false, nil
}

func (m *recordingMerger) count(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[key]
}

type notifyEvent struct {
	kind     orchestrationEventKind
	phaseKey string
	detail   string
}

type recordingNotifier struct {
	mu     sync.Mutex
	events []notifyEvent
}

func (n *recordingNotifier) notify(_ context.Context, _ pgstore.Orchestration, phase pgstore.OrchestrationPhase, kind orchestrationEventKind, detail string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, notifyEvent{kind: kind, phaseKey: phase.Key, detail: detail})
}

func (n *recordingNotifier) has(kind orchestrationEventKind) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, e := range n.events {
		if e.kind == kind {
			return true
		}
	}
	return false
}

type recordingSyncer struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *recordingSyncer) sync(_ context.Context, _ pgstore.Orchestration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *recordingSyncer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestCreateAndStartOrchestrationDispatchesRoots: the create-and-start on-switch
// freezes a plan, flips the run approved→running, and dispatches only the root
// (no-dependency) phases — the dependent phase waits.
func TestCreateAndStartOrchestrationDispatchesRoots(t *testing.T) {
	fake := &fakeOrchStore{phases: map[string]*pgstore.OrchestrationPhase{}}
	sp := newRecordingSpawner()
	app := &appServer{orchStore: fake, orchestrations: newOrchestrationEngine(fake, sp.spawn)}

	doc := orchestrationPlanDoc{
		RepoOwner: "romaine-life",
		RepoName:  "tank-operator",
		Phases: []orchestrationPlanPhaseDoc{
			{PhaseKey: "root", Brief: "do root"},
			{PhaseKey: "child", Brief: "do child", DependsOn: []string{"root"}},
		},
	}
	orch, phases, err := app.createAndStartOrchestration(context.Background(), "owner@example.test", doc)
	if err != nil {
		t.Fatalf("createAndStartOrchestration: %v", err)
	}
	if orch.State != pgstore.OrchestrationRunning {
		t.Fatalf("run state = %q, want running after kickoff", orch.State)
	}
	byKey := map[string]pgstore.OrchestrationPhase{}
	for _, p := range phases {
		byKey[p.Key] = p
	}
	if got := byKey["root"].Status; got != pgstore.PhaseRunning {
		t.Fatalf("root status = %q, want running (dispatched at kickoff)", got)
	}
	if got := byKey["child"].Status; got != pgstore.PhasePending {
		t.Fatalf("child status = %q, want still pending", got)
	}
	if sp.count("root") != 1 || sp.count("child") != 0 {
		t.Fatalf("dispatch counts root=%d child=%d, want 1/0", sp.count("root"), sp.count("child"))
	}
}

// TestCreateAndStartOrchestrationRejectsIntegrationPlanWithoutBranch: an
// integration-target plan that omits the integration branch is rejected at the
// boundary, before any side effect.
func TestCreateAndStartOrchestrationRejectsIntegrationPlanWithoutBranch(t *testing.T) {
	fake := &fakeOrchStore{phases: map[string]*pgstore.OrchestrationPhase{}}
	sp := newRecordingSpawner()
	app := &appServer{orchStore: fake, orchestrations: newOrchestrationEngine(fake, sp.spawn)}

	doc := orchestrationPlanDoc{
		RepoOwner: "romaine-life",
		RepoName:  "tank-operator",
		Phases: []orchestrationPlanPhaseDoc{
			{PhaseKey: "i", Brief: "integration work", Target: string(pgstore.PhaseTargetIntegration)},
		},
	}
	if _, _, err := app.createAndStartOrchestration(context.Background(), "owner@example.test", doc); err == nil {
		t.Fatal("expected rejection for integration plan without integration_branch")
	}
}

// TestStartDispatchesInitialReadyPhases: the kickoff dispatches a run's root
// (no-dependency) phases immediately and flips approved -> running, while a
// dependent phase stays pending until its dep merges.
func TestStartDispatchesInitialReadyPhases(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationApproved,
		phaseSpec{key: "root", status: pgstore.PhasePending},
		phaseSpec{key: "child", deps: []string{"root"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	if err := e.Start(context.Background(), "orch-1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := store.snapshot(phaseID("root")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("root status = %q, want running", got)
	}
	if got := store.snapshot(phaseID("child")).Status; got != pgstore.PhasePending {
		t.Fatalf("child status = %q, want still pending", got)
	}
	if sp.count("root") != 1 || sp.count("child") != 0 {
		t.Fatalf("dispatch counts root=%d child=%d, want 1/0", sp.count("root"), sp.count("child"))
	}
	if store.orch.State != pgstore.OrchestrationRunning {
		t.Fatalf("run state = %q, want running", store.orch.State)
	}
}

// TestGreenWebhookAutoMergesAndAdvances: a green CI webhook for a pr_open phase
// auto-merges it (via the merge hook) and the merge advances the DAG — the
// dependent phase dispatches without any human merge.
func TestGreenWebhookAutoMergesAndAdvances(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhasePROpen, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	merger := newRecordingMerger()
	merger.mergeable["a"] = "sha-a"
	e.merge = merger.merge

	e.maybeAutoMergeOnCI(context.Background(), fakePROwner, fakePRName, 100)

	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("phase a status = %q, want merged", got)
	}
	if got := store.snapshot(phaseID("a")).MergeSHA; got != "sha-a" {
		t.Fatalf("phase a merge sha = %q, want sha-a", got)
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase b status = %q, want running (advanced after auto-merge)", got)
	}
	if sp.count("b") != 1 {
		t.Fatalf("phase b spawned %d times, want 1", sp.count("b"))
	}
}

// TestGreenMergeRefusedLeavesPhaseOpen: when GitHub refuses the merge (PR not
// yet green), the phase stays pr_open and nothing advances — and the attempt is
// idempotent across repeated CI events.
func TestGreenMergeRefusedLeavesPhaseOpen(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhasePROpen, prNumber: 100, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	merger := newRecordingMerger() // nothing mergeable -> always refused
	e.merge = merger.merge

	e.maybeAutoMergeOnCI(context.Background(), fakePROwner, fakePRName, 100)
	e.maybeAutoMergeOnCI(context.Background(), fakePROwner, fakePRName, 100)

	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhasePROpen {
		t.Fatalf("phase a status = %q, want still pr_open", got)
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhasePending {
		t.Fatalf("phase b status = %q, want still pending", got)
	}
	if merger.count("a") != 2 {
		t.Fatalf("merge attempted %d times, want 2 (one per CI event)", merger.count("a"))
	}
}

// TestReconcileAutoMergesGreenPrOpenPhase: the reconcile backstop also merges a
// green pr_open phase (a dropped green webhook degrades to a delay), then the
// single-phase main-only run reaches done.
func TestReconcileAutoMergesGreenPrOpenPhase(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhasePROpen, prNumber: 100, spoke: "spoke-a"},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	merger := newRecordingMerger()
	merger.mergeable["a"] = "sha-a"
	e.merge = merger.merge

	if err := e.reconcileAllActive(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseMerged {
		t.Fatalf("phase a status = %q, want merged via reconcile", got)
	}
	if store.orch.State != pgstore.OrchestrationDone {
		t.Fatalf("run state = %q, want done", store.orch.State)
	}
}

// TestTerminalGateAwaitsReviewForIntegrationRun: when every phase of a run with
// an integration branch is merged, the run parks on the human review gate
// (awaiting_review) and emits the review record — it does NOT auto-finish.
func TestTerminalGateAwaitsReviewForIntegrationRun(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", target: pgstore.PhaseTargetIntegration, status: pgstore.PhaseMerged, prNumber: 100, spoke: "spoke-a"},
	)
	store.orch.IntegrationBranch = "tank/orchestration/x/integration"
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	notifier := &recordingNotifier{}
	e.notify = notifier.notify

	if err := e.reconcileAllActive(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.orch.State != pgstore.OrchestrationAwaitingReview {
		t.Fatalf("run state = %q, want awaiting_review", store.orch.State)
	}
	if !notifier.has(orchestrationEventAwaitingReview) {
		t.Fatalf("expected an awaiting_review notification, got %+v", notifier.events)
	}
}

// TestMainOnlyRunDoneEmitsRecord: a main-only run (no integration branch) still
// finishes at done and emits the run_done record.
func TestMainOnlyRunDoneEmitsRecord(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseMerged, prNumber: 100, spoke: "spoke-a"},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	notifier := &recordingNotifier{}
	e.notify = notifier.notify

	if err := e.reconcileAllActive(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if store.orch.State != pgstore.OrchestrationDone {
		t.Fatalf("run state = %q, want done", store.orch.State)
	}
	if !notifier.has(orchestrationEventRunDone) {
		t.Fatalf("expected a run_done notification, got %+v", notifier.events)
	}
}

// TestBlockedPhaseEscalatesAndParks: a spoke's blocked signal moves the phase to
// blocked, notifies the human, pauses the dependent branch, and parks the run on
// the human gate rather than hanging in running with nothing in flight.
func TestBlockedPhaseEscalatesAndParks(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "a", status: pgstore.PhaseRunning, spoke: "spoke-a"},
		phaseSpec{key: "b", deps: []string{"a"}, status: pgstore.PhasePending},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	notifier := &recordingNotifier{}
	e.notify = notifier.notify

	blocked, err := e.signalBlocked(context.Background(), "spoke-a", "stuck on missing credential")
	if err != nil {
		t.Fatalf("signalBlocked: %v", err)
	}
	if !blocked {
		t.Fatal("signalBlocked returned false, want true")
	}
	if got := store.snapshot(phaseID("a")); got.Status != pgstore.PhaseBlocked || got.Detail != "stuck on missing credential" {
		t.Fatalf("phase a = {%q, %q}, want {blocked, reason}", got.Status, got.Detail)
	}
	if got := store.snapshot(phaseID("b")).Status; got != pgstore.PhasePending {
		t.Fatalf("phase b status = %q, want still pending (paused behind block)", got)
	}
	if sp.count("b") != 0 {
		t.Fatalf("phase b dispatched %d times, want 0", sp.count("b"))
	}
	if !notifier.has(orchestrationEventPhaseBlocked) {
		t.Fatalf("expected a phase_blocked notification, got %+v", notifier.events)
	}
	if store.orch.State != pgstore.OrchestrationAwaitingReview {
		t.Fatalf("run state = %q, want awaiting_review (parked, not hung)", store.orch.State)
	}
}

// TestUnblockResumesRun: unblocking an escalated phase returns the run from the
// human gate to running and re-dispatches the recovered branch.
func TestUnblockResumesRun(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationAwaitingReview,
		phaseSpec{key: "a", status: pgstore.PhaseBlocked, spoke: "spoke-a"},
	)
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)

	unblocked, err := e.signalUnblock(context.Background(), phaseID("a"))
	if err != nil {
		t.Fatalf("signalUnblock: %v", err)
	}
	if !unblocked {
		t.Fatal("signalUnblock returned false, want true")
	}
	// a had no deps, so unblocking -> pending -> ready -> re-dispatched, run running.
	if got := store.snapshot(phaseID("a")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("phase a status = %q, want running after unblock+redispatch", got)
	}
	if store.orch.State != pgstore.OrchestrationRunning {
		t.Fatalf("run state = %q, want running after unblock", store.orch.State)
	}
}

// TestMergeForwardWhenIntegrationPending: while a main phase has landed and an
// integration phase is still pending, the reconcile pass merges main forward
// into the integration branch (via the sync hook) before dispatching the
// integration phase, so the integration spoke branches off current main.
func TestMergeForwardWhenIntegrationPending(t *testing.T) {
	store := newFakeOrchStore(pgstore.OrchestrationRunning,
		phaseSpec{key: "m", target: pgstore.PhaseTargetMain, status: pgstore.PhaseMerged, prNumber: 1, spoke: "spoke-m"},
		phaseSpec{key: "i", target: pgstore.PhaseTargetIntegration, deps: []string{"m"}, status: pgstore.PhasePending},
	)
	store.orch.IntegrationBranch = "tank/orchestration/x/integration"
	sp := newRecordingSpawner()
	e := newTestEngine(store, sp.spawn)
	syncer := &recordingSyncer{}
	e.syncForward = syncer.sync

	if err := e.reconcileAllActive(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if syncer.count() < 1 {
		t.Fatalf("merge-forward called %d times, want >= 1", syncer.count())
	}
	if got := store.snapshot(phaseID("i")).Status; got != pgstore.PhaseRunning {
		t.Fatalf("integration phase status = %q, want running (dispatched after forward)", got)
	}
	if sp.count("i") != 1 {
		t.Fatalf("integration phase spawned %d times, want 1", sp.count("i"))
	}
}

// TestIntegrationNeedsForward covers the bounded window the merge-forward fires
// in: only while a main phase has landed AND an integration phase is still
// non-terminal.
func TestIntegrationNeedsForward(t *testing.T) {
	mk := func(target pgstore.PhaseTarget, status pgstore.PhaseStatus) pgstore.OrchestrationPhase {
		return pgstore.OrchestrationPhase{Target: target, Status: status}
	}
	cases := []struct {
		name   string
		phases []pgstore.OrchestrationPhase
		want   bool
	}{
		{"main landed + integration pending", []pgstore.OrchestrationPhase{
			mk(pgstore.PhaseTargetMain, pgstore.PhaseMerged),
			mk(pgstore.PhaseTargetIntegration, pgstore.PhasePending),
		}, true},
		{"no main landed yet", []pgstore.OrchestrationPhase{
			mk(pgstore.PhaseTargetMain, pgstore.PhaseRunning),
			mk(pgstore.PhaseTargetIntegration, pgstore.PhasePending),
		}, false},
		{"integration all terminal", []pgstore.OrchestrationPhase{
			mk(pgstore.PhaseTargetMain, pgstore.PhaseMerged),
			mk(pgstore.PhaseTargetIntegration, pgstore.PhaseMerged),
		}, false},
		{"no integration phases", []pgstore.OrchestrationPhase{
			mk(pgstore.PhaseTargetMain, pgstore.PhaseMerged),
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := integrationNeedsForward(tc.phases); got != tc.want {
				t.Fatalf("integrationNeedsForward = %v, want %v", got, tc.want)
			}
		})
	}
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
