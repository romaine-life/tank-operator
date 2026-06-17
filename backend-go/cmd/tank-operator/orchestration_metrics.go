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
)

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
