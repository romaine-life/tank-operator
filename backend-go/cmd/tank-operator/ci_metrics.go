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
)

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
