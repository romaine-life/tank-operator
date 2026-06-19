package main

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// markPendingCall records a MarkTerminal invocation for assertion.
type markPendingCall struct {
	provisionID string
	status      pgstore.PendingTestProvisionStatus
	detail      string
	headSHA     string
}

// fakePendingProvisionStore is a thread-safe, behavior-faithful double for the
// durable pending-provision backstop store. It mirrors the real store's
// conditional-write semantics so the double-trigger guard and the reconcile
// backstop can be exercised without Postgres:
//   - Register is the atomic guard: a second kickoff for a provision_id still
//     'pending' returns created=false (no row), just like the real ON CONFLICT
//     ... WHERE status <> 'pending'.
//   - MarkTerminal / ClaimForRedrive are gated on status='pending' and return
//     ErrPendingTestProvisionStale on a lost race.
//   - ListStale returns only 'pending' rows past the cutoff (terminal rows are
//     excluded), and OldestPendingAgeSeconds reads the oldest 'pending' row.
type fakePendingProvisionStore struct {
	mu              sync.Mutex
	scope           string
	rows            map[string]*pgstore.PendingTestProvision
	registerCalls   int
	claimCalls      int
	markCalls       []markPendingCall
	forceClaimStale bool
	terminalCh      chan markPendingCall
}

func newFakePendingProvisionStore() *fakePendingProvisionStore {
	return &fakePendingProvisionStore{
		scope: "default",
		rows:  map[string]*pgstore.PendingTestProvision{},
	}
}

func (f *fakePendingProvisionStore) idFor(req pgstore.RegisterPendingTestProvisionRequest) string {
	scope := req.SessionScope
	if scope == "" {
		scope = f.scope
	}
	tankSessionID := sessionmodel.SessionStorageKey(scope, req.SessionID)
	return pgstore.PendingTestProvisionID(tankSessionID, req.RepoOwner, req.RepoName, req.Branch, req.Kind)
}

// seed inserts a pending row directly (the post-restart strand the backstop
// recovers). startedAtAgo backdates last activity so ListStale can match it.
func (f *fakePendingProvisionStore) seed(rec pgstore.PendingTestProvision, startedAtAgo time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec.Status == "" {
		rec.Status = pgstore.PendingTestProvisionPending
	}
	rec.StartedAt = time.Now().Add(-startedAtAgo)
	cp := rec
	f.rows[rec.ProvisionID] = &cp
}

func (f *fakePendingProvisionStore) Register(_ context.Context, req pgstore.RegisterPendingTestProvisionRequest) (pgstore.PendingTestProvision, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCalls++
	id := f.idFor(req)
	if existing, ok := f.rows[id]; ok && existing.Status == pgstore.PendingTestProvisionPending {
		// A provision for this target is already in flight: the conditional
		// re-arm matches no row.
		return pgstore.PendingTestProvision{}, false, nil
	}
	rec := pgstore.PendingTestProvision{
		ProvisionID: id, SessionScope: req.SessionScope, SessionID: req.SessionID,
		OwnerEmail: req.OwnerEmail, RepoOwner: req.RepoOwner, RepoName: req.RepoName,
		Branch: req.Branch, Project: req.Project, Workflow: req.Workflow,
		Kind: req.Kind, PRNumber: req.PRNumber, ExpectedSHA: req.ExpectedSHA,
		OrchestrationID: req.OrchestrationID, Status: pgstore.PendingTestProvisionPending,
		StartedAt: time.Now(),
	}
	cp := rec
	f.rows[id] = &cp
	return rec, true, nil
}

func (f *fakePendingProvisionStore) MarkTerminal(_ context.Context, provisionID string, status pgstore.PendingTestProvisionStatus, detail, headSHA string) (pgstore.PendingTestProvision, error) {
	f.mu.Lock()
	row, ok := f.rows[provisionID]
	if !ok || row.Status != pgstore.PendingTestProvisionPending {
		f.mu.Unlock()
		return pgstore.PendingTestProvision{}, pgstore.ErrPendingTestProvisionStale
	}
	row.Status = status
	row.Detail = detail
	if headSHA != "" {
		row.HeadSHA = headSHA
	}
	out := *row
	call := markPendingCall{provisionID: provisionID, status: status, detail: detail, headSHA: headSHA}
	f.markCalls = append(f.markCalls, call)
	ch := f.terminalCh
	f.mu.Unlock()
	if ch != nil {
		ch <- call
	}
	return out, nil
}

func (f *fakePendingProvisionStore) ClaimForRedrive(_ context.Context, provisionID string, knownAttempt int) (pgstore.PendingTestProvision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.forceClaimStale {
		return pgstore.PendingTestProvision{}, pgstore.ErrPendingTestProvisionStale
	}
	row, ok := f.rows[provisionID]
	if !ok || row.Status != pgstore.PendingTestProvisionPending || row.AttemptCount != knownAttempt {
		return pgstore.PendingTestProvision{}, pgstore.ErrPendingTestProvisionStale
	}
	row.AttemptCount++
	now := time.Now()
	row.LastEventAt = &now
	return *row, nil
}

func (f *fakePendingProvisionStore) ListStale(_ context.Context, olderThan time.Duration, limit int) ([]pgstore.PendingTestProvision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Add(-olderThan)
	var out []pgstore.PendingTestProvision
	for _, row := range f.rows {
		if row.Status != pgstore.PendingTestProvisionPending {
			continue
		}
		last := row.StartedAt
		if row.LastEventAt != nil {
			last = *row.LastEventAt
		}
		if last.Before(cutoff) {
			out = append(out, *row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakePendingProvisionStore) OldestPendingAgeSeconds(_ context.Context) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var oldest time.Time
	for _, row := range f.rows {
		if row.Status != pgstore.PendingTestProvisionPending {
			continue
		}
		if oldest.IsZero() || row.StartedAt.Before(oldest) {
			oldest = row.StartedAt
		}
	}
	if oldest.IsZero() {
		return 0, nil
	}
	return time.Since(oldest).Seconds(), nil
}

func (f *fakePendingProvisionStore) Get(_ context.Context, provisionID string) (pgstore.PendingTestProvision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.rows[provisionID]; ok {
		return *row, nil
	}
	return pgstore.PendingTestProvision{}, errors.New("not found")
}

func (f *fakePendingProvisionStore) status(id string) pgstore.PendingTestProvisionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.rows[id]; ok {
		return row.Status
	}
	return ""
}

func (f *fakePendingProvisionStore) markCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.markCalls)
}

// stalePendingRecord builds a 'pending' interactive record matching the
// testWorkflowApp session (owner@example.test / session 77 / default scope) so
// the reconcile backstop's idempotency + re-drive paths resolve against the real
// Manager and gate.
func stalePendingRecord(kind pgstore.PendingTestProvisionKind) pgstore.PendingTestProvision {
	tankSessionID := sessionmodel.SessionStorageKey("default", "77")
	branch := "tank/session/77/tank-operator"
	return pgstore.PendingTestProvision{
		ProvisionID:   pgstore.PendingTestProvisionID(tankSessionID, "romaine-life", "tank-operator", branch, kind),
		SessionScope:  "default",
		SessionID:     "77",
		TankSessionID: tankSessionID,
		OwnerEmail:    provisionTestOwner,
		RepoOwner:     "romaine-life",
		RepoName:      "tank-operator",
		Branch:        branch,
		Project:       "tank-operator",
		Workflow:      interactiveTestWorkflowLabel,
		Kind:          kind,
		Status:        pgstore.PendingTestProvisionPending,
	}
}

// TestReconcilePendingProvisions_ReDrivesStaleInteractive proves the core
// backstop behavior: a record stranded in 'pending' past the staleness window is
// claimed and re-driven through the same gate the entry point uses, and a ready
// verdict provisions the slot and terminalizes the record 'done'.
func TestReconcilePendingProvisions_ReDrivesStaleInteractive(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, reg, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	store.terminalCh = make(chan markPendingCall, 1)
	app.pendingTestProvisions = store

	rec := stalePendingRecord(pgstore.PendingTestProvisionInteractive)
	store.seed(rec, 30*time.Minute)

	if err := app.reconcilePendingTestProvisions(context.Background(), 25*time.Minute); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}

	// The re-drive spawns the gate run in a goroutine that terminalizes the
	// record at its end; wait for that durable terminal.
	select {
	case call := <-store.terminalCh:
		if call.status != pgstore.PendingTestProvisionDone {
			t.Fatalf("re-drive terminalized %q, want done", call.status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-driven provision never terminalized its record")
	}

	if store.claimCalls != 1 {
		t.Fatalf("claim calls = %d, want exactly one before re-drive", store.claimCalls)
	}
	if glim.checkoutCalls != 1 || glim.deployCalls != 1 {
		t.Fatalf("re-drive did not provision: checkout=%d deploy=%d, want 1/1", glim.checkoutCalls, glim.deployCalls)
	}
	// The interactive re-drive runs the gate directly, not the injected launch
	// hook — so no second handler-style launch was captured.
	if len(*launched) != 0 {
		t.Fatalf("re-drive should not route through the launch hook; launched=%d", len(*launched))
	}
	recd, ok, _ := reg.Get(context.Background(), provisionTestOwner, "77")
	if !ok {
		t.Fatal("session record missing after re-drive")
	}
	if active, _ := recd.TestState["active"].(bool); !active {
		t.Fatalf("re-drive should mark test-state active: %#v", recd.TestState)
	}
}

// TestReconcilePendingProvisions_IdempotentWhenAlreadyProvisioned proves the
// backstop never double-provisions: when a test environment is already active
// for the session (the original provision succeeded; only the terminal mark was
// lost), the record is marked done WITHOUT touching glimmung.
func TestReconcilePendingProvisions_IdempotentWhenAlreadyProvisioned(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	record := testWorkflowSessionRecord("romaine-life/tank-operator")
	record.TestState = map[string]any{"active": true}
	app, _, _, launched := testWorkflowApp(t, record, gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	rec := stalePendingRecord(pgstore.PendingTestProvisionInteractive)
	store.seed(rec, 30*time.Minute)

	if err := app.reconcilePendingTestProvisions(context.Background(), 25*time.Minute); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}

	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("idempotent re-drive must not re-provision: checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if len(*launched) != 0 {
		t.Fatalf("idempotent re-drive must not launch a gate run; launched=%d", len(*launched))
	}
	if got := store.status(rec.ProvisionID); got != pgstore.PendingTestProvisionDone {
		t.Fatalf("already-provisioned record status = %q, want done", got)
	}
	if store.claimCalls != 1 {
		t.Fatalf("claim calls = %d, want one (claim then short-circuit)", store.claimCalls)
	}
}

// TestReconcilePendingProvisions_LosesClaimRaceSafely proves a concurrent
// reconcile that loses the conditional claim does nothing: no terminal mark, no
// provisioning. Mirrors the CI-watch lost-race guard.
func TestReconcilePendingProvisions_LosesClaimRaceSafely(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	store.forceClaimStale = true // a concurrent reconcile already owns the row
	app.pendingTestProvisions = store

	rec := stalePendingRecord(pgstore.PendingTestProvisionInteractive)
	store.seed(rec, 30*time.Minute)

	if err := app.reconcilePendingTestProvisions(context.Background(), 25*time.Minute); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}

	if store.markCount() != 0 {
		t.Fatalf("lost-race reconcile marked the record terminal: %d marks", store.markCount())
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 {
		t.Fatalf("lost-race reconcile must not provision: checkout=%d deploy=%d", glim.checkoutCalls, glim.deployCalls)
	}
	if len(*launched) != 0 {
		t.Fatalf("lost-race reconcile must not launch a gate run; launched=%d", len(*launched))
	}
	if got := store.status(rec.ProvisionID); got != pgstore.PendingTestProvisionPending {
		t.Fatalf("lost-race row status = %q, want it left pending for the winning reconcile", got)
	}
}

// TestReconcilePendingProvisions_TerminalRecordsUntouched proves a record that
// already reached a verdict ('done') is excluded from the stale scan, so the
// backstop never re-drives a legitimately-finished provision.
func TestReconcilePendingProvisions_TerminalRecordsUntouched(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, _, launched := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	done := stalePendingRecord(pgstore.PendingTestProvisionInteractive)
	done.Status = pgstore.PendingTestProvisionDone
	store.seed(done, 30*time.Minute)

	if err := app.reconcilePendingTestProvisions(context.Background(), 25*time.Minute); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}

	if store.claimCalls != 0 {
		t.Fatalf("terminal record was claimed for re-drive: %d claims", store.claimCalls)
	}
	if glim.checkoutCalls != 0 || glim.deployCalls != 0 || len(*launched) != 0 {
		t.Fatalf("terminal record triggered work: checkout=%d deploy=%d launched=%d", glim.checkoutCalls, glim.deployCalls, len(*launched))
	}
}

// TestReconcilePendingProvisions_FreshRecordNotYetStale proves a 'pending'
// record younger than the staleness window is left alone — a healthy provision
// still legitimately settling must not be raced by the backstop.
func TestReconcilePendingProvisions_FreshRecordNotYetStale(t *testing.T) {
	gh := &provisionFakeGitHub{states: []mcpgithub.PullRequestState{readyState("sha-ready")}}
	glim := &fakeGlimmungClient{}
	app, _, _, _ := testWorkflowApp(t, testWorkflowSessionRecord("romaine-life/tank-operator"), gh, glim)
	store := newFakePendingProvisionStore()
	app.pendingTestProvisions = store

	rec := stalePendingRecord(pgstore.PendingTestProvisionInteractive)
	store.seed(rec, 2*time.Minute) // well within the 25m window

	if err := app.reconcilePendingTestProvisions(context.Background(), 25*time.Minute); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}
	if store.claimCalls != 0 {
		t.Fatalf("a fresh in-flight record was claimed: %d claims", store.claimCalls)
	}
}
