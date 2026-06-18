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
