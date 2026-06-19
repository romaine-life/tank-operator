package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

const (
	// pendingProvisionReconcileInterval matches the CI-watch backstop cadence:
	// this is a restart-recovery backstop, not a poll of CI, so a few minutes of
	// latency on a rare stranded provision is fine.
	pendingProvisionReconcileInterval = 5 * time.Minute
	// pendingProvisionStaleAfter is the settle cap + deploy grace + slack. A
	// healthy provision terminalizes its record well within
	// provisionBackgroundTimeout() (settle cap 18m + deploy grace 5m); only a
	// record still 'pending' past that window is a genuine strand (an
	// orchestrator restart mid-wait), so re-driving before then would race a
	// run that is still legitimately settling.
	pendingProvisionStaleAfter     = 25 * time.Minute
	pendingProvisionReconcileBatch = 100
)

// runPendingTestProvisionReconcileLoop is the durable backstop for the
// fire-and-forget test-slot provisioning goroutines (provisionOrchestrationReviewSlot
// and runInteractiveTestWorkflow). Those goroutines can wait ~23 min for CI to
// settle; if the orchestrator pod restarts mid-wait the provision was previously
// lost with no retry. Each pass re-drives only records stranded in 'pending'
// past the staleness window, through the same gate the entry points use, with an
// idempotency check (already-provisioned -> mark done) and a conditional claim so
// two replicas (or a double pass) cannot double-provision. Mirrors
// runCIWatchReconcileLoop.
func runPendingTestProvisionReconcileLoop(ctx context.Context, app *appServer, interval, staleAfter time.Duration) error {
	if app == nil || app.pendingTestProvisions == nil {
		return nil
	}
	if interval <= 0 {
		interval = pendingProvisionReconcileInterval
	}
	if staleAfter <= 0 {
		staleAfter = pendingProvisionStaleAfter
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := app.reconcilePendingTestProvisions(ctx, staleAfter); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("pending test provision reconcile loop pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *appServer) reconcilePendingTestProvisions(ctx context.Context, staleAfter time.Duration) error {
	if s == nil || s.pendingTestProvisions == nil {
		return nil
	}
	// Publish the oldest-pending-age gauge the stuck-provision alert fires on.
	// Independent of the stale scan so a single long-stuck record is visible even
	// when it is younger than the re-drive window.
	if age, err := s.pendingTestProvisions.OldestPendingAgeSeconds(ctx); err == nil {
		setTestSlotPendingProvisionOldestAge(age)
	} else if !errors.Is(err, context.Canceled) {
		slog.Warn("pending test provision oldest-age gauge failed", "error", err)
	}

	stale, err := s.pendingTestProvisions.ListStale(ctx, staleAfter, pendingProvisionReconcileBatch)
	if err != nil {
		return err
	}
	for _, rec := range stale {
		s.redrivePendingTestProvision(ctx, rec)
	}
	return nil
}

// redrivePendingTestProvision re-drives one stranded provision idempotently. It
// claims the record first (a conditional attempt bump, so a concurrent reconcile
// loses the race safely and cannot double-fire the gate), short-circuits when the
// slot is already provisioned (marks done, never re-provisions), then re-invokes
// the same entry point that originally kicked it off -- which runs the gate,
// surfaces the outcome durably, and marks the record terminal.
func (s *appServer) redrivePendingTestProvision(ctx context.Context, rec pgstore.PendingTestProvision) {
	claimed, err := s.pendingTestProvisions.ClaimForRedrive(ctx, rec.ProvisionID, rec.AttemptCount)
	if errors.Is(err, pgstore.ErrPendingTestProvisionStale) {
		// Another reconcile already claimed (or terminalized) this row; that
		// winner owns the re-drive.
		return
	}
	if err != nil {
		slog.Warn("pending test provision claim failed", "provision_id", rec.ProvisionID, "error", err)
		return
	}

	// Idempotency: if a test environment is already active for this session the
	// original provision (or a prior re-drive) succeeded and only the terminal
	// mark was lost. Mark done rather than provision a second slot.
	if s.testStateActiveForProvision(ctx, claimed) {
		if _, err := s.pendingTestProvisions.MarkTerminal(ctx, claimed.ProvisionID, pgstore.PendingTestProvisionDone,
			"already provisioned (test environment active); not re-driving", ""); err != nil && !errors.Is(err, pgstore.ErrPendingTestProvisionStale) {
			slog.Warn("pending test provision idempotent mark failed", "provision_id", claimed.ProvisionID, "error", err)
		}
		return
	}

	recordTestSlotProvisionRedrive(string(claimed.Kind))
	slog.Info("re-driving stranded test-slot provision",
		"provision_id", claimed.ProvisionID, "kind", claimed.Kind,
		"session_id", claimed.SessionID, "repo", claimed.RepoOwner+"/"+claimed.RepoName,
		"branch", claimed.Branch, "attempt", claimed.AttemptCount)

	switch claimed.Kind {
	case pgstore.PendingTestProvisionInteractive:
		// The entry point owns its own budgeted background context and marks the
		// record terminal at its end; run it off the reconcile goroutine so a
		// settle-wait does not stall the loop.
		go s.runInteractiveTestWorkflow(provisionReqFromPending(claimed))
	case pgstore.PendingTestProvisionOrchestrationReview:
		s.redriveOrchestrationReviewProvision(ctx, claimed)
	default:
		if _, err := s.pendingTestProvisions.MarkTerminal(ctx, claimed.ProvisionID, pgstore.PendingTestProvisionFailed,
			"unknown provision kind "+string(claimed.Kind), ""); err != nil && !errors.Is(err, pgstore.ErrPendingTestProvisionStale) {
			slog.Warn("pending test provision unknown-kind mark failed", "provision_id", claimed.ProvisionID, "error", err)
		}
	}
}

// redriveOrchestrationReviewProvision re-drives a stranded orchestration-review
// provision by reloading its run + spoke phase and re-invoking the same entry
// point (which re-registers the now-claimed row as a no-op and marks it terminal
// at its end). If the run is no longer resolvable the record is terminalized so
// it does not strand forever.
func (s *appServer) redriveOrchestrationReviewProvision(ctx context.Context, rec pgstore.PendingTestProvision) {
	if s.orchestrationRuns == nil || rec.OrchestrationID == "" {
		s.failPendingProvision(ctx, rec.ProvisionID, "orchestration run store unavailable for re-drive")
		return
	}
	orch, phases, err := s.orchestrationRuns.GetWithPhases(ctx, rec.OrchestrationID)
	if err != nil {
		s.failPendingProvision(ctx, rec.ProvisionID, "orchestration not found for re-drive: "+err.Error())
		return
	}
	target, ok := phaseBySpoke(phases, rec.SessionID)
	if !ok {
		target, ok = latestPhaseWithSpoke(phases)
	}
	if !ok {
		s.failPendingProvision(ctx, rec.ProvisionID, "no orchestration phase spoke to re-drive")
		return
	}
	go s.provisionOrchestrationReviewSlot(orch, target)
}

func (s *appServer) failPendingProvision(ctx context.Context, provisionID, detail string) {
	if s.pendingTestProvisions == nil {
		return
	}
	if _, err := s.pendingTestProvisions.MarkTerminal(ctx, provisionID, pgstore.PendingTestProvisionFailed, detail, ""); err != nil &&
		!errors.Is(err, pgstore.ErrPendingTestProvisionStale) {
		slog.Warn("pending test provision fail-mark failed", "provision_id", provisionID, "error", err)
	}
}

// testStateActiveForProvision reports whether the provision's session already has
// an active test environment -- the idempotency signal that a slot was already
// provisioned for this target.
func (s *appServer) testStateActiveForProvision(ctx context.Context, rec pgstore.PendingTestProvision) bool {
	if s.mgr == nil {
		return false
	}
	info, err := s.mgr.GetRegisteredByOwner(ctx, rec.OwnerEmail, rec.SessionID)
	if err != nil {
		return false
	}
	active, _ := info.TestState["active"].(bool)
	return active
}

// markPendingTestProvisionTerminal closes the durable pending record for a
// provision target at the end of its run. The provision_id is deterministic from
// the target coordinates, so the entry points need not thread the record id
// through -- they recompute it. No-op when the store is unset (stub mode); a
// stale sentinel is benign (the record was already terminalized or claimed).
func (s *appServer) markPendingTestProvisionTerminal(ctx context.Context, sessionID, repoOwner, repoName, branch string, kind pgstore.PendingTestProvisionKind, status pgstore.PendingTestProvisionStatus, detail, headSHA string) {
	if s.pendingTestProvisions == nil {
		return
	}
	tankSessionID := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	id := pgstore.PendingTestProvisionID(tankSessionID, repoOwner, repoName, branch, kind)
	if _, err := s.pendingTestProvisions.MarkTerminal(ctx, id, status, detail, headSHA); err != nil &&
		!errors.Is(err, pgstore.ErrPendingTestProvisionStale) {
		slog.Warn("pending test provision terminal mark failed", "provision_id", id, "status", status, "error", err)
	}
}

func provisionReqFromPending(rec pgstore.PendingTestProvision) provisionTestSlotRequest {
	return provisionTestSlotRequest{
		OwnerEmail:  rec.OwnerEmail,
		SessionID:   rec.SessionID,
		Project:     rec.Project,
		Workflow:    rec.Workflow,
		RepoOwner:   rec.RepoOwner,
		RepoName:    rec.RepoName,
		Branch:      rec.Branch,
		PRNumber:    rec.PRNumber,
		ExpectedSHA: rec.ExpectedSHA,
	}
}

func phaseBySpoke(phases []pgstore.OrchestrationPhase, sessionID string) (pgstore.OrchestrationPhase, bool) {
	for i := len(phases) - 1; i >= 0; i-- {
		if phases[i].SpokeSessionID == sessionID && sessionID != "" {
			return phases[i], true
		}
	}
	return pgstore.OrchestrationPhase{}, false
}
