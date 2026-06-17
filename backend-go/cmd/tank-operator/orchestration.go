package main

// The orchestration advance engine — the deterministic core that walks a
// multi-phase orchestration DAG. There is no LLM in this loop: when a phase's
// PR merges, the engine marks the node done, recomputes which downstream phases
// are now ready (every depends_on satisfied), and dispatches a spoke session
// for each, off the run's repo `main`. See docs/event-driven-rollout.md for the
// sibling CI-watch bridge this extends; the durable store landed in #1264
// (internal/pgstore/orchestrations.go).
//
// Determinism comes from two places: the readiness computation is a pure
// function of the durable DAG state, and every state-moving store call is an
// atomically-guarded conditional UPDATE. A merged-PR webhook can fire more than
// once and two orchestrator replicas can race; the claim/requeue guards make
// "advance the phase and spawn the next one" safe to repeat. The reconcile
// backstop re-drives the same logic on an interval so a dropped webhook degrades
// to a delay, never a hung run.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

// orchestrationStore is the durable-store slice the advance engine needs. It is
// satisfied by *pgstore.OrchestrationStore; an interface so the engine's DAG /
// idempotency / reconcile logic is unit-testable against a fake without
// Postgres.
type orchestrationStore interface {
	Get(ctx context.Context, orchestrationID string) (pgstore.Orchestration, error)
	GetWithPhases(ctx context.Context, orchestrationID string) (pgstore.Orchestration, []pgstore.OrchestrationPhase, error)
	GetPhase(ctx context.Context, phaseID string) (pgstore.OrchestrationPhase, error)
	GetPhaseByPR(ctx context.Context, prOwner, prName string, prNumber int) (pgstore.OrchestrationPhase, error)
	GetPhaseBySpokeSession(ctx context.Context, spokeSessionID string) (pgstore.OrchestrationPhase, error)
	MarkPhaseReady(ctx context.Context, phaseID string) (pgstore.OrchestrationPhase, bool, error)
	ClaimPhaseForSpawn(ctx context.Context, phaseID string) (pgstore.OrchestrationPhase, bool, error)
	RequeuePhaseForRespawn(ctx context.Context, phaseID string) (bool, error)
	AttachPhaseSpoke(ctx context.Context, phaseID, spokeSessionID string) (pgstore.OrchestrationPhase, error)
	MarkPhasePROpen(ctx context.Context, phaseID string, req pgstore.SetPhasePRRequest) (pgstore.OrchestrationPhase, error)
	MarkPhaseMerged(ctx context.Context, phaseID, mergeSHA string) (pgstore.OrchestrationPhase, error)
	BlockPhase(ctx context.Context, phaseID, reason string) (pgstore.OrchestrationPhase, bool, error)
	UnblockPhase(ctx context.Context, phaseID string) (pgstore.OrchestrationPhase, bool, error)
	UpdateState(ctx context.Context, orchestrationID string, state pgstore.OrchestrationState) (pgstore.Orchestration, error)
	ListActiveOrchestrationIDs(ctx context.Context) ([]string, error)
}

// phaseSpokeSpawnFunc creates the spoke session that works a phase and returns
// its session id. The production implementation is appServer.spawnPhaseSpoke
// (mgr.Create + enqueueSDKTurn — the same machinery spawn_run_session and
// POST /api/internal/sessions/{id}/turns use); tests pass a fake.
type phaseSpokeSpawnFunc func(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) (string, error)

// phaseMergeFunc attempts the autonomous, green-gated merge of a phase's PR. It
// resolves the PR head GitHub verified green and calls mcp-github's
// merge_pull_request with that head as the expected_head_sha guard; GitHub
// itself enforces the required-checks-green-and-mergeable gate, so a not-yet-
// green PR is simply refused (merged=false, no error worth surfacing) and
// retried on the next CI event or reconcile pass. Returns the merge commit SHA
// when the merge lands. Production impl is appServer.mergePhasePR; tests pass a
// fake. nil disables autonomous merge (phases then wait for a human merge, as
// before this slice).
type phaseMergeFunc func(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) (mergeSHA string, merged bool, err error)

// branchSyncFunc brings the run's integration branch current with main (merges
// main forward) so integration-target phases build on the latest landed main
// work. Idempotent and a no-op when the branches are already in sync.
// Production impl is appServer.syncIntegrationForward; nil disables merge-
// forward.
type branchSyncFunc func(ctx context.Context, orch pgstore.Orchestration) error

// orchestrationEventKind tags the display-plane records the engine emits so a
// human is never left guessing about an autonomous run's state.
type orchestrationEventKind string

const (
	orchestrationEventPhaseBlocked  orchestrationEventKind = "phase_blocked"
	orchestrationEventAwaitingReview orchestrationEventKind = "awaiting_review"
	orchestrationEventRunDone        orchestrationEventKind = "run_done"
)

// orchestrationNotifyFunc emits a display-only, no-agent-invoked record (the
// same record kind the rollout work surfaces on green) plus its wake, so an
// autonomous run's terminal-review gate and blocked-phase escalations reach the
// human. phase is the zero value for run-level events. Production impl is
// appServer.emitOrchestrationRecord; nil makes notification a no-op (the state
// transition still happens durably).
type orchestrationNotifyFunc func(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase, kind orchestrationEventKind, detail string)

// reviewEnvFunc brings up a test environment from the run's integration branch
// when it reaches the terminal review gate, returning a human-facing locator
// (e.g. a slot URL) on success. Best-effort: a failure degrades the gate to
// "ready for review, env unavailable", never blocks it. nil skips env bring-up.
type reviewEnvFunc func(ctx context.Context, orch pgstore.Orchestration) (string, error)

// orchestrationEngine owns the advance loop. It holds no mutable state of its
// own — every decision reads the durable DAG and every effect is a guarded
// store write — so the webhook hot path and the reconcile loop can both call it
// concurrently. The capability hooks (spawn/merge/syncForward/notify/reviewEnv)
// are injected from appServer in main.go and faked in tests, keeping the
// DAG/idempotency logic Postgres- and GitHub-free under test.
type orchestrationEngine struct {
	store       orchestrationStore
	spawn       phaseSpokeSpawnFunc
	merge       phaseMergeFunc
	syncForward branchSyncFunc
	notify      orchestrationNotifyFunc
	reviewEnv   reviewEnvFunc
	log         *slog.Logger
}

func newOrchestrationEngine(store orchestrationStore, spawn phaseSpokeSpawnFunc) *orchestrationEngine {
	if store == nil {
		return nil
	}
	return &orchestrationEngine{store: store, spawn: spawn, log: slog.Default()}
}

// advanceOnMerge is the webhook hot path: a merged PR (repo + number) maps to
// its owning phase, the phase is marked merged, and the run is re-driven so any
// phase the merge unblocked is dispatched. Idempotent — a duplicate merged
// delivery finds the phase already merged and the downstream phase already
// claimed, so the run advances exactly once. A PR that is not an orchestration
// phase is a quiet no-op.
func (e *orchestrationEngine) advanceOnMerge(ctx context.Context, prOwner, prName string, prNumber int, mergeSHA string) {
	if e == nil || e.store == nil {
		return
	}
	phase, err := e.store.GetPhaseByPR(ctx, prOwner, prName, prNumber)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationPhaseNotFound) {
			recordOrchestrationAdvance("not_phase")
			return
		}
		recordOrchestrationAdvance("error")
		e.log.Warn("orchestration advance: phase lookup failed",
			"pr", prOwner+"/"+prName+"#"+strconv.Itoa(prNumber), "error", err)
		return
	}
	if phase.Status != pgstore.PhaseMerged {
		if _, err := e.store.MarkPhaseMerged(ctx, phase.PhaseID, mergeSHA); err != nil {
			recordOrchestrationAdvance("error")
			e.log.Warn("orchestration advance: mark merged failed",
				"phase_id", phase.PhaseID, "error", err)
			return
		}
		recordOrchestrationAdvance("merged")
	} else {
		// Duplicate/late merged delivery. Still re-drive: a prior advance that
		// crashed after the merge write but before dispatch is recovered here.
		recordOrchestrationAdvance("already_merged")
	}
	if err := e.reconcileRun(ctx, phase.OrchestrationID); err != nil && !errors.Is(err, context.Canceled) {
		e.log.Warn("orchestration advance: reconcile after merge failed",
			"orchestration_id", phase.OrchestrationID, "error", err)
	}
}

// Start is the run kickoff the advance loop never triggers on its own: it
// drives a freshly-approved run once, immediately, so its root phases (no deps)
// dispatch without waiting up to a reconcile interval. The advance loop drives
// everything after the first merge; this is the on-switch. Idempotent and safe
// to call repeatedly — reconcileRun only acts on guarded transitions, so a
// double kickoff dispatches each root phase exactly once.
func (e *orchestrationEngine) Start(ctx context.Context, orchestrationID string) error {
	if e == nil || e.store == nil {
		return errors.New("orchestration engine unavailable")
	}
	return e.reconcileRun(ctx, orchestrationID)
}

// maybeAutoMergeOnCI is the green→merge fast path off the CI webhook: a non-
// failing CI completion for a watched PR that maps to a still-open
// orchestration phase triggers an immediate green-gated merge attempt, so a
// phase self-completes within seconds of going green instead of waiting for the
// reconcile backstop. A PR that is not an orchestration phase, or a phase past
// pr_open, is a quiet no-op. GitHub is the merge gate (the attempt is refused
// unless the PR is truly green and mergeable), so firing on every passing check
// is safe and idempotent.
func (e *orchestrationEngine) maybeAutoMergeOnCI(ctx context.Context, prOwner, prName string, prNumber int) {
	if e == nil || e.store == nil || e.merge == nil || prNumber <= 0 {
		return
	}
	phase, err := e.store.GetPhaseByPR(ctx, prOwner, prName, prNumber)
	if err != nil {
		if !errors.Is(err, pgstore.ErrOrchestrationPhaseNotFound) {
			e.log.Warn("orchestration auto-merge: phase lookup failed",
				"pr", prOwner+"/"+prName+"#"+strconv.Itoa(prNumber), "error", err)
		}
		return
	}
	if phase.Status != pgstore.PhasePROpen {
		return
	}
	orch, err := e.store.Get(ctx, phase.OrchestrationID)
	if err != nil {
		e.log.Warn("orchestration auto-merge: run lookup failed",
			"orchestration_id", phase.OrchestrationID, "error", err)
		return
	}
	if orch.State != pgstore.OrchestrationRunning && orch.State != pgstore.OrchestrationApproved {
		return
	}
	if e.tryAutoMergePhase(ctx, orch, phase) {
		if err := e.reconcileRun(ctx, phase.OrchestrationID); err != nil && !errors.Is(err, context.Canceled) {
			e.log.Warn("orchestration auto-merge: reconcile after merge failed",
				"orchestration_id", phase.OrchestrationID, "error", err)
		}
	}
}

// tryAutoMergePhase attempts the green-gated autonomous merge of one pr_open
// phase and, on success, records the merge durably (MarkPhaseMerged) — the same
// terminal the merged-PR webhook records, so the advance loop walks the DAG
// identically whether the merge was Tank-driven or human-driven. Returns
// whether the phase merged. A refused merge (PR not yet green/mergeable) is the
// common, expected case and is left for a later CI event or reconcile pass.
func (e *orchestrationEngine) tryAutoMergePhase(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) bool {
	if e.merge == nil || phase.Status != pgstore.PhasePROpen {
		return false
	}
	mergeSHA, merged, err := e.merge(ctx, orch, phase)
	if err != nil {
		recordOrchestrationMerge("error")
		e.log.Warn("orchestration auto-merge: merge attempt failed",
			"phase_id", phase.PhaseID, "phase_key", phase.Key, "error", err)
		return false
	}
	if !merged {
		recordOrchestrationMerge("not_ready")
		return false
	}
	if _, err := e.store.MarkPhaseMerged(ctx, phase.PhaseID, mergeSHA); err != nil {
		recordOrchestrationMerge("mark_failed")
		e.log.Warn("orchestration auto-merge: mark merged failed",
			"phase_id", phase.PhaseID, "error", err)
		return false
	}
	recordOrchestrationMerge("merged")
	e.log.Info("orchestration phase auto-merged",
		"orchestration_id", orch.OrchestrationID, "phase_id", phase.PhaseID,
		"phase_key", phase.Key, "merge_sha", mergeSHA)
	return true
}

// signalBlocked records that a phase's spoke reported it is genuinely stuck:
// the phase moves to the terminal-failure 'blocked' status with the reason, its
// DAG subtree pauses (dependents never satisfy their depends_on), and the human
// is notified immediately. The run is then re-settled so it parks on the human
// gate rather than appearing to hang once the unblocked branches finish. A
// session that is not any phase's spoke, or a phase already terminal, is a quiet
// no-op. Returns whether the phase was newly blocked.
func (e *orchestrationEngine) signalBlocked(ctx context.Context, spokeSessionID, reason string) (bool, error) {
	if e == nil || e.store == nil {
		return false, errors.New("orchestration engine unavailable")
	}
	phase, err := e.store.GetPhaseBySpokeSession(ctx, spokeSessionID)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationPhaseNotFound) {
			return false, nil
		}
		return false, err
	}
	blocked, ok, err := e.store.BlockPhase(ctx, phase.PhaseID, reason)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	recordOrchestrationBlocked()
	e.log.Warn("orchestration phase blocked",
		"orchestration_id", blocked.OrchestrationID, "phase_id", blocked.PhaseID,
		"phase_key", blocked.Key, "reason", reason)
	if orch, gerr := e.store.Get(ctx, blocked.OrchestrationID); gerr == nil {
		e.emit(ctx, orch, blocked, orchestrationEventPhaseBlocked, reason)
	}
	if err := e.settleRunState(ctx, blocked.OrchestrationID); err != nil && !errors.Is(err, context.Canceled) {
		e.log.Warn("orchestration block: settle failed",
			"orchestration_id", blocked.OrchestrationID, "error", err)
	}
	return true, nil
}

// signalUnblock is the human's "I resolved the blocker, resume that branch"
// lever: it clears a blocked phase back to pending and, if the run had parked on
// the human gate, returns it to running so the advance loop re-drives it. Safe
// to call on a non-blocked phase (no-op). Returns whether a phase was unblocked.
func (e *orchestrationEngine) signalUnblock(ctx context.Context, phaseID string) (bool, error) {
	if e == nil || e.store == nil {
		return false, errors.New("orchestration engine unavailable")
	}
	unblocked, ok, err := e.store.UnblockPhase(ctx, phaseID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	// A run parked in awaiting_review because this branch was blocked must
	// return to running so the reconcile loop / Start picks it up again.
	if orch, gerr := e.store.Get(ctx, unblocked.OrchestrationID); gerr == nil &&
		orch.State == pgstore.OrchestrationAwaitingReview {
		if _, uerr := e.store.UpdateState(ctx, unblocked.OrchestrationID, pgstore.OrchestrationRunning); uerr != nil {
			e.log.Warn("orchestration unblock: state restore failed",
				"orchestration_id", unblocked.OrchestrationID, "error", uerr)
		}
	}
	if err := e.reconcileRun(ctx, unblocked.OrchestrationID); err != nil && !errors.Is(err, context.Canceled) {
		e.log.Warn("orchestration unblock: reconcile failed",
			"orchestration_id", unblocked.OrchestrationID, "error", err)
	}
	return true, nil
}

// emit fans a display-plane record out through the notify hook when one is
// wired; a nil hook leaves the durable state move intact and only skips the
// human-facing surface.
func (e *orchestrationEngine) emit(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase, kind orchestrationEventKind, detail string) {
	if e.notify == nil {
		return
	}
	e.notify(ctx, orch, phase, kind, detail)
}

// linkPhasePR joins a session that just registered a PR (the CI-watch handoff)
// back to its owning phase and stamps the PR coordinates onto it, so the
// PR->phase reverse lookup resolves at merge time. A session that is not any
// phase's spoke is a quiet no-op; a phase already merged/terminal is left alone
// (never dragged back to pr_open).
func (e *orchestrationEngine) linkPhasePR(ctx context.Context, spokeSessionID string, pr pgstore.SetPhasePRRequest) {
	if e == nil || e.store == nil {
		return
	}
	phase, err := e.store.GetPhaseBySpokeSession(ctx, spokeSessionID)
	if err != nil {
		if errors.Is(err, pgstore.ErrOrchestrationPhaseNotFound) {
			recordOrchestrationPRLink("not_phase")
			return
		}
		recordOrchestrationPRLink("error")
		e.log.Warn("orchestration pr-link: phase lookup failed",
			"session_id", spokeSessionID, "error", err)
		return
	}
	switch phase.Status {
	case pgstore.PhaseRunning, pgstore.PhaseReady, pgstore.PhasePROpen:
		// still working its PR — safe to (re)stamp coordinates.
	default:
		recordOrchestrationPRLink("skipped")
		return
	}
	if _, err := e.store.MarkPhasePROpen(ctx, phase.PhaseID, pr); err != nil {
		recordOrchestrationPRLink("error")
		e.log.Warn("orchestration pr-link: mark pr_open failed",
			"phase_id", phase.PhaseID, "error", err)
		return
	}
	recordOrchestrationPRLink("linked")
}

// reconcileAllActive re-drives every non-terminal run. It is the dropped-webhook
// backstop: a phase whose PR actually merged but whose advance never landed, and
// any ready/pending phase that should have a spoke but doesn't, are repaired
// here. It also bootstraps a freshly-approved run whose root phases have not
// been dispatched yet. Per-replica idempotent — every effect is a guarded write.
func (e *orchestrationEngine) reconcileAllActive(ctx context.Context) error {
	if e == nil || e.store == nil {
		return nil
	}
	ids, err := e.store.ListActiveOrchestrationIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.reconcileRun(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
			recordOrchestrationReconcile("error")
			e.log.Warn("orchestration reconcile: run failed", "orchestration_id", id, "error", err)
			continue
		}
		recordOrchestrationReconcile("ok")
	}
	return nil
}

// reconcileRun is the single drive-forward step shared by advanceOnMerge and the
// reconcile loop: load the DAG, promote newly-ready phases, dispatch a spoke for
// each ready phase, recover any claimed-but-unspawned phase, and settle the run
// state. Only 'approved'/'running' runs are driven; a terminal or draft run is a
// no-op.
func (e *orchestrationEngine) reconcileRun(ctx context.Context, orchestrationID string) error {
	orch, phases, err := e.store.GetWithPhases(ctx, orchestrationID)
	if err != nil {
		return err
	}
	if orch.State != pgstore.OrchestrationApproved && orch.State != pgstore.OrchestrationRunning {
		return nil
	}

	// Pass 1: autonomous green→merge. Any pr_open phase whose PR GitHub will
	// accept (verified green + mergeable) is merged now, so the readiness pass
	// below sees it merged and dispatches what it unblocked within this same
	// reconcile. GitHub is the gate; a not-yet-green PR is a no-op.
	if e.merge != nil {
		mergedAny := false
		for _, p := range phases {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if p.Status == pgstore.PhasePROpen && e.tryAutoMergePhase(ctx, orch, p) {
				mergedAny = true
			}
		}
		if mergedAny {
			if orch, phases, err = e.store.GetWithPhases(ctx, orchestrationID); err != nil {
				return err
			}
		}
	}

	// Pass 2: bring the integration branch current with main before dispatching
	// integration-target phases, so an integration phase that depends on a
	// landed main phase branches off main's latest work, not a stale base.
	e.maybeSyncForward(ctx, orch, phases)

	byKey := make(map[string]pgstore.OrchestrationPhase, len(phases))
	for _, p := range phases {
		byKey[p.Key] = p
	}

	for _, p := range phases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 1. pending + deps satisfied -> ready.
		if p.Status == pgstore.PhasePending && phaseDepsSatisfied(p, byKey) {
			updated, ok, err := e.store.MarkPhaseReady(ctx, p.PhaseID)
			if err != nil {
				e.log.Warn("orchestration reconcile: mark ready failed", "phase_id", p.PhaseID, "error", err)
				continue
			}
			if ok {
				p = updated
			} else if rp, gerr := e.store.GetPhase(ctx, p.PhaseID); gerr == nil {
				p = rp // another writer moved it; pick up its current state
			}
		}
		// 2. claimed-but-unspawned (crash/spawn-failure recovery) -> ready.
		if p.Status == pgstore.PhaseRunning && strings.TrimSpace(p.SpokeSessionID) == "" {
			ok, err := e.store.RequeuePhaseForRespawn(ctx, p.PhaseID)
			if err != nil {
				e.log.Warn("orchestration reconcile: requeue failed", "phase_id", p.PhaseID, "error", err)
				continue
			}
			if !ok {
				continue // lost the recovery race; leave it to the winner
			}
			if rp, gerr := e.store.GetPhase(ctx, p.PhaseID); gerr == nil {
				p = rp
			}
		}
		// 3. ready -> claim + spawn.
		if p.Status == pgstore.PhaseReady {
			e.dispatchReadyPhase(ctx, orch, p)
		}
	}

	return e.settleRunState(ctx, orchestrationID)
}

// dispatchReadyPhase atomically claims a ready phase and spawns its spoke. The
// claim is the concurrency choke point: only one caller wins ready->running, so
// the phase is dispatched exactly once even under duplicate webhooks or racing
// replicas. On spawn failure the claim is rolled back to 'ready' for a later
// pass.
func (e *orchestrationEngine) dispatchReadyPhase(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) {
	claimed, ok, err := e.store.ClaimPhaseForSpawn(ctx, phase.PhaseID)
	if err != nil {
		e.log.Warn("orchestration dispatch: claim failed", "phase_id", phase.PhaseID, "error", err)
		return
	}
	if !ok {
		recordOrchestrationSpawn("claim_lost")
		return
	}
	if e.spawn == nil {
		recordOrchestrationSpawn("spawn_unavailable")
		_, _ = e.store.RequeuePhaseForRespawn(ctx, phase.PhaseID)
		return
	}
	sessionID, err := e.spawn(ctx, orch, claimed)
	if err != nil || strings.TrimSpace(sessionID) == "" {
		recordOrchestrationSpawn("spawn_failed")
		e.log.Warn("orchestration dispatch: spawn failed",
			"phase_id", phase.PhaseID, "phase_key", phase.Key, "error", err)
		if _, rerr := e.store.RequeuePhaseForRespawn(ctx, phase.PhaseID); rerr != nil {
			e.log.Warn("orchestration dispatch: requeue after spawn failure failed",
				"phase_id", phase.PhaseID, "error", rerr)
		}
		return
	}
	if _, err := e.store.AttachPhaseSpoke(ctx, phase.PhaseID, sessionID); err != nil {
		// The spoke session exists but we failed to record it. Leave the phase
		// running-with-empty-spoke for the recovery path rather than spawning a
		// second session here.
		recordOrchestrationSpawn("attach_failed")
		e.log.Warn("orchestration dispatch: attach spoke failed",
			"phase_id", phase.PhaseID, "session_id", sessionID, "error", err)
		return
	}
	recordOrchestrationSpawn("spawned")
	e.log.Info("orchestration phase dispatched",
		"orchestration_id", orch.OrchestrationID, "phase_id", phase.PhaseID,
		"phase_key", phase.Key, "session_id", sessionID)
}

// settleRunState transitions the run after a drive pass. approved -> running
// once a phase is in flight. Every phase a terminal success: a run with an
// integration branch parks on the human review gate (awaiting_review), a
// main-only run is done. A run that can no longer progress because the only
// remaining work is downstream of a blocked phase also parks on the human gate,
// so it never sits silently in 'running' with nothing in flight.
func (e *orchestrationEngine) settleRunState(ctx context.Context, orchestrationID string) error {
	orch, phases, err := e.store.GetWithPhases(ctx, orchestrationID)
	if err != nil {
		return err
	}
	if orch.State != pgstore.OrchestrationApproved && orch.State != pgstore.OrchestrationRunning {
		return nil
	}
	if len(phases) == 0 {
		return nil
	}

	allTerminalSuccess := true
	// inFlight counts only phases that can still make progress on their own —
	// ready/running/pr_open. A 'pending' phase is intentionally NOT in flight:
	// it is waiting on a dependency, and if that dependency is blocked it will
	// wait forever, so counting it as active is exactly what would let a run sit
	// silently in 'running' behind a blocked branch.
	inFlight := false
	anyBlocked := false
	for _, p := range phases {
		switch p.Status {
		case pgstore.PhaseMerged, pgstore.PhaseSkipped:
			// terminal success
		default:
			allTerminalSuccess = false
		}
		switch p.Status {
		case pgstore.PhaseReady, pgstore.PhaseRunning, pgstore.PhasePROpen:
			inFlight = true
		case pgstore.PhaseBlocked:
			anyBlocked = true
		}
	}

	switch {
	case allTerminalSuccess:
		return e.settleAllMerged(ctx, orch, phases)
	case !inFlight && anyBlocked:
		// Nothing is in flight and a phase is blocked: every remaining phase is
		// downstream of the block and cannot progress. Park on the human gate
		// (already notified at block time) instead of sitting in 'running'.
		return e.parkAwaitingReview(ctx, orch, phases,
			"run paused: blocked phase(s) need attention before it can finish")
	case orch.State == pgstore.OrchestrationApproved && inFlight:
		if _, err := e.store.UpdateState(ctx, orchestrationID, pgstore.OrchestrationRunning); err != nil {
			return err
		}
	}
	return nil
}

// settleAllMerged resolves a run whose every phase reached a terminal success.
// A run with an integration branch holds at the human review gate (its work is
// staged on integration, not yet promoted to main); a main-only run is done.
func (e *orchestrationEngine) settleAllMerged(ctx context.Context, orch pgstore.Orchestration, phases []pgstore.OrchestrationPhase) error {
	if strings.TrimSpace(orch.IntegrationBranch) != "" {
		return e.parkAwaitingReview(ctx, orch, phases,
			"all phases merged; integration branch ready for your review")
	}
	if _, err := e.store.UpdateState(ctx, orch.OrchestrationID, pgstore.OrchestrationDone); err != nil {
		return err
	}
	recordOrchestrationRunDone()
	e.log.Info("orchestration run done", "orchestration_id", orch.OrchestrationID)
	e.emit(ctx, orch, representativeSpokePhase(phases), orchestrationEventRunDone, "all phases merged to main")
	return nil
}

// parkAwaitingReview transitions a run to the awaiting_review human gate, emits
// the display-plane record + notification, and best-effort brings up a review
// environment from the integration branch. awaiting_review is not a reconcile-
// driven state, so settleRunState returns early for it afterward and the record
// is emitted exactly once on the transition.
func (e *orchestrationEngine) parkAwaitingReview(ctx context.Context, orch pgstore.Orchestration, phases []pgstore.OrchestrationPhase, detail string) error {
	updated, err := e.store.UpdateState(ctx, orch.OrchestrationID, pgstore.OrchestrationAwaitingReview)
	if err != nil {
		return err
	}
	recordOrchestrationAwaitingReview()
	e.log.Info("orchestration run awaiting review",
		"orchestration_id", orch.OrchestrationID, "detail", detail)
	if e.reviewEnv != nil && strings.TrimSpace(orch.IntegrationBranch) != "" {
		if locator, eerr := e.reviewEnv(ctx, updated); eerr != nil {
			recordOrchestrationReviewEnv("error")
			e.log.Warn("orchestration review env bring-up failed",
				"orchestration_id", orch.OrchestrationID, "error", eerr)
		} else if strings.TrimSpace(locator) != "" {
			recordOrchestrationReviewEnv("up")
			detail = detail + " — test environment: " + locator
		}
	}
	e.emit(ctx, updated, representativeSpokePhase(phases), orchestrationEventAwaitingReview, detail)
	return nil
}

// representativeSpokePhase picks the phase a run-level display record attaches
// to: the highest-ordinal phase that actually has a spoke session (the run's
// "final" worked node is where a human looks for the run's outcome). Falls back
// to the last phase, or the zero value when there are none.
func representativeSpokePhase(phases []pgstore.OrchestrationPhase) pgstore.OrchestrationPhase {
	var chosen pgstore.OrchestrationPhase
	found := false
	for _, p := range phases {
		if strings.TrimSpace(p.SpokeSessionID) != "" {
			chosen = p
			found = true
		}
	}
	if found {
		return chosen
	}
	if len(phases) > 0 {
		return phases[len(phases)-1]
	}
	return pgstore.OrchestrationPhase{}
}

// maybeSyncForward brings the integration branch current with main while
// integration-target work is still pending and at least one main phase has
// landed. It is the merge-forward that keeps integration phases building on the
// latest main. Best-effort and idempotent (a no-diff forward is a no-op); a
// failure is logged and retried next pass, never blocking dispatch.
func (e *orchestrationEngine) maybeSyncForward(ctx context.Context, orch pgstore.Orchestration, phases []pgstore.OrchestrationPhase) {
	if e.syncForward == nil || strings.TrimSpace(orch.IntegrationBranch) == "" {
		return
	}
	if !integrationNeedsForward(phases) {
		return
	}
	if err := e.syncForward(ctx, orch); err != nil {
		recordOrchestrationSyncForward("error")
		e.log.Warn("orchestration merge-forward failed",
			"orchestration_id", orch.OrchestrationID,
			"integration_branch", orch.IntegrationBranch, "error", err)
		return
	}
	recordOrchestrationSyncForward("ok")
}

// integrationNeedsForward reports whether bringing integration current with main
// is worthwhile right now: at least one main-target phase has landed (there is
// new main to forward) and at least one integration-target phase is still
// non-terminal (work that should build on it remains). Outside that window the
// merge-forward is skipped, so it stays bounded.
func integrationNeedsForward(phases []pgstore.OrchestrationPhase) bool {
	landedMain := false
	pendingIntegration := false
	for _, p := range phases {
		switch p.Target {
		case pgstore.PhaseTargetMain:
			if p.Status == pgstore.PhaseMerged {
				landedMain = true
			}
		case pgstore.PhaseTargetIntegration:
			switch p.Status {
			case pgstore.PhaseMerged, pgstore.PhaseSkipped, pgstore.PhaseBlocked:
				// terminal — no longer needs a fresher base
			default:
				pendingIntegration = true
			}
		}
	}
	return landedMain && pendingIntegration
}

// phaseDepsSatisfied reports whether every depends_on edge of a phase points at
// a phase that reached a terminal success (merged or skipped). A dangling edge
// (no sibling for the key) is treated as unsatisfied — the plan validator
// rejects those at freeze time, so this is a defensive floor, never the steady
// state.
func phaseDepsSatisfied(phase pgstore.OrchestrationPhase, byKey map[string]pgstore.OrchestrationPhase) bool {
	for _, dep := range phase.DependsOn {
		d, ok := byKey[dep]
		if !ok {
			return false
		}
		if d.Status != pgstore.PhaseMerged && d.Status != pgstore.PhaseSkipped {
			return false
		}
	}
	return true
}
