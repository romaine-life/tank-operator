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
	UpdateState(ctx context.Context, orchestrationID string, state pgstore.OrchestrationState) (pgstore.Orchestration, error)
	ListActiveOrchestrationIDs(ctx context.Context) ([]string, error)
}

// phaseSpokeSpawnFunc creates the spoke session that works a phase and returns
// its session id. The production implementation is appServer.spawnPhaseSpoke
// (mgr.Create + enqueueSDKTurn — the same machinery spawn_run_session and
// POST /api/internal/sessions/{id}/turns use); tests pass a fake.
type phaseSpokeSpawnFunc func(ctx context.Context, orch pgstore.Orchestration, phase pgstore.OrchestrationPhase) (string, error)

// orchestrationEngine owns the advance loop. It holds no mutable state of its
// own — every decision reads the durable DAG and every effect is a guarded
// store write — so the webhook hot path and the reconcile loop can both call it
// concurrently.
type orchestrationEngine struct {
	store orchestrationStore
	spawn phaseSpokeSpawnFunc
	log   *slog.Logger
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

// settleRunState transitions the run after a drive pass: approved -> running
// once a phase is in flight, and running -> done once every phase reached a
// terminal success (merged or skipped). The terminal human-review gate is a
// later slice; for now all-phases-merged is done.
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
	anyActive := false
	for _, p := range phases {
		switch p.Status {
		case pgstore.PhaseMerged, pgstore.PhaseSkipped:
			// terminal success
		default:
			allTerminalSuccess = false
		}
		switch p.Status {
		case pgstore.PhasePending, pgstore.PhaseReady, pgstore.PhaseRunning, pgstore.PhasePROpen:
			anyActive = true
		}
	}

	if allTerminalSuccess {
		if _, err := e.store.UpdateState(ctx, orchestrationID, pgstore.OrchestrationDone); err != nil {
			return err
		}
		recordOrchestrationRunDone()
		e.log.Info("orchestration run done", "orchestration_id", orchestrationID)
		return nil
	}
	if orch.State == pgstore.OrchestrationApproved && anyActive {
		if _, err := e.store.UpdateState(ctx, orchestrationID, pgstore.OrchestrationRunning); err != nil {
			return err
		}
	}
	return nil
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
