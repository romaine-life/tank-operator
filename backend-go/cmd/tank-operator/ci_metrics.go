package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CI-watch observability. See docs/event-driven-rollout.md "Observability".
var (
	ciWebhookTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_ci_webhooks_total",
		Help: "GitHub webhook deliveries seen by the CI-watch receiver, by event and result.",
	}, []string{"event", "result"})

	ciTerminalTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_ci_terminal_total",
		Help: "CI-watch terminal transitions applied from webhooks, by state.",
	}, []string{"state"})

	ciWakeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_ci_wake_total",
		Help: "Agent wakes fired by the CI-watch receiver, by source and result.",
	}, []string{"source", "result"})

	ciWatchAgeSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tank_ci_watch_age_seconds",
		Help:    "Age from CI-watch registration to a terminal transition (ready/failed/conflict/merged).",
		Buckets: []float64{15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200},
	})

	ciWatchOldestStaleAge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tank_ci_watch_oldest_stale_age_seconds",
		Help: "Age of the oldest 'watching' CI watch with no recent event; the durable stall backstop's signal (0 when none).",
	})

	// Deterministic test-slot provisioning gate (provision_test_slot.go). The
	// gate validates PR-readiness with the same classifyCIWatchState reducer the
	// CI-watch path uses, then provisions only on a green/mergeable verdict.
	testSlotValidateTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_test_slot_validate_total",
		Help: "Deterministic test-slot provisioning gate validate verdicts, by bounded outcome.",
	}, []string{"outcome"})

	testSlotProvisionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_test_slot_provision_total",
		Help: "Deterministic test-slot provisioning attempts after a ready verdict, by bounded outcome.",
	}, []string{"outcome"})

	// Interactive (UI-button-triggered) deterministic test-workflow gate
	// (handlers_test_workflow.go). One increment per completed background run,
	// labeled by the terminal outcome: "provisioned" on a ready verdict that
	// deployed, "error" when no verdict could be reached, else the bounded
	// refusal verdict (failed/conflict/merged/watching_timeout/head_moved). The
	// underlying validate/provision steps still bump the shared gate counters
	// above; this one isolates the interactive trigger's end-to-end outcome.
	testSlotInteractiveTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_test_slot_interactive_total",
		Help: "Interactive test-workflow trigger outcomes, by bounded terminal outcome.",
	}, []string{"outcome"})

	// Durable pending-provision reconcile backstop (pending_test_provisions +
	// pending_provision_reconcile_loop.go). The two background provisioning entry
	// points can wait ~23 min for CI to settle; an orchestrator restart mid-wait
	// strands the provision in a 'pending' record the loop re-drives idempotently.
	testSlotPendingProvisionOldestAge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tank_test_slot_pending_provision_oldest_age_seconds",
		Help: "Age of the oldest still-'pending' test-slot provision record; the durable backstop's stuck-provision signal (0 when none).",
	})

	testSlotProvisionRedriveTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_test_slot_provision_redrive_total",
		Help: "Stranded pending test-slot provisions re-driven by the reconcile backstop, by kind.",
	}, []string{"kind"})

	// Interactive-endpoint double-trigger guard outcomes. "launched" is the
	// normal first trigger; "in_flight" is a refused second trigger while a
	// provision is already running; "test_state_active" is a refused trigger
	// while a test environment is already up for the session.
	testSlotProvisionGuardTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_test_slot_provision_guard_total",
		Help: "Interactive test-workflow double-trigger guard outcomes, by bounded result.",
	}, []string{"result"})

	// Read-only test-slot status surface (handlers_test_slot_status.go). The
	// dedicated test-slot page reads this on navigation/refresh. "result" is the
	// bounded fetch outcome: "durable" (snapshot from the session_ci_watches +
	// pending_test_provisions rows), "live" (a ?refresh=1 read that also ran the
	// zero-side-effect preflight), "no_repo"/"ambiguous_repo" (coordinates could
	// not be resolved), "not_found", or "error".
	testSlotStatusRequestTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_test_slot_status_requests_total",
		Help: "Read-only test-slot status-surface requests, by bounded result.",
	}, []string{"result"})
)

func recordTestSlotStatus(result string) {
	if result == "" {
		result = "error"
	}
	testSlotStatusRequestTotal.WithLabelValues(result).Inc()
}

func setTestSlotPendingProvisionOldestAge(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	testSlotPendingProvisionOldestAge.Set(seconds)
}

func recordTestSlotProvisionRedrive(kind string) {
	if kind == "" {
		kind = "unknown"
	}
	testSlotProvisionRedriveTotal.WithLabelValues(kind).Inc()
}

func recordTestSlotProvisionGuard(result string) {
	if result == "" {
		result = "unknown"
	}
	testSlotProvisionGuardTotal.WithLabelValues(result).Inc()
}

func recordTestSlotValidate(outcome string) {
	if outcome == "" {
		outcome = "error"
	}
	testSlotValidateTotal.WithLabelValues(outcome).Inc()
}

func recordTestSlotProvision(outcome string) {
	if outcome == "" {
		outcome = "error"
	}
	testSlotProvisionTotal.WithLabelValues(outcome).Inc()
}

func recordTestSlotInteractive(outcome string) {
	if outcome == "" {
		outcome = "error"
	}
	testSlotInteractiveTotal.WithLabelValues(outcome).Inc()
}

func recordCIWebhook(event, result string) {
	if event == "" {
		event = "unknown"
	}
	ciWebhookTotal.WithLabelValues(event, result).Inc()
}

func recordCITerminal(state string) {
	ciTerminalTotal.WithLabelValues(state).Inc()
}

func recordCIWake(source, result string) {
	ciWakeTotal.WithLabelValues(source, result).Inc()
}

func recordCIWatchAge(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	ciWatchAgeSeconds.Observe(seconds)
}

func setCIWatchOldestStaleAge(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	ciWatchOldestStaleAge.Set(seconds)
}
