package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Orchestration advance-loop observability. Per CLAUDE.md, observability is a
// completion requirement, not a follow-up: these counters make the deterministic
// engine's behavior — and the dropped-webhook backstop catching what the hot
// path missed — visible without reading logs.
var (
	orchestrationAdvanceTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_advance_total",
		Help: "Merged-PR advance attempts, by result (merged|already_merged|not_phase|error).",
	}, []string{"result"})

	orchestrationSpawnTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_phase_spawn_total",
		Help: "Phase spoke dispatch outcomes (spawned|claim_lost|spawn_failed|attach_failed|spawn_unavailable).",
	}, []string{"result"})

	orchestrationPRLinkTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_phase_pr_link_total",
		Help: "Phase->PR link attempts when a spoke registers a PR (linked|not_phase|skipped|error).",
	}, []string{"result"})

	orchestrationReconcileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_reconcile_total",
		Help: "Reconcile-backstop runs driven, by result (ok|error).",
	}, []string{"result"})

	orchestrationRunDoneTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_orchestration_run_done_total",
		Help: "Orchestration runs transitioned to done (all phases merged/skipped).",
	})

	orchestrationMergeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_phase_merge_total",
		Help: "Autonomous green→merge attempts on phase PRs (merged|not_ready|mark_failed|error).",
	}, []string{"result"})

	orchestrationSyncForwardTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_sync_forward_total",
		Help: "Merge-forward of main into the run's integration branch, by result (ok|error).",
	}, []string{"result"})

	orchestrationBlockedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_orchestration_phase_blocked_total",
		Help: "Phases moved to blocked because their spoke signalled it is stuck.",
	})

	orchestrationAwaitingReviewTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_orchestration_awaiting_review_total",
		Help: "Runs parked on the human review/escalation gate (awaiting_review).",
	})

	orchestrationReviewEnvTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_review_env_total",
		Help: "Terminal-gate review-environment bring-ups from the integration branch (up|error).",
	}, []string{"result"})

	orchestrationKickoffTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_kickoff_total",
		Help: "Create-and-start kickoff attempts from a plan doc (started|rejected).",
	}, []string{"result"})

	orchestrationPromoteTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_orchestration_promote_total",
		Help: "Integration→main promotions at the human go gate (merged|error).",
	}, []string{"result"})
)

func recordOrchestrationMerge(result string) {
	orchestrationMergeTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationSyncForward(result string) {
	orchestrationSyncForwardTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationBlocked() {
	orchestrationBlockedTotal.Inc()
}

func recordOrchestrationAwaitingReview() {
	orchestrationAwaitingReviewTotal.Inc()
}

func recordOrchestrationReviewEnv(result string) {
	orchestrationReviewEnvTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationKickoff(result string) {
	orchestrationKickoffTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationPromote(result string) {
	orchestrationPromoteTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationAdvance(result string) {
	orchestrationAdvanceTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationSpawn(result string) {
	orchestrationSpawnTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationPRLink(result string) {
	orchestrationPRLinkTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationReconcile(result string) {
	orchestrationReconcileTotal.WithLabelValues(result).Inc()
}

func recordOrchestrationRunDone() {
	orchestrationRunDoneTotal.Inc()
}
