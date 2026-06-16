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
)

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
