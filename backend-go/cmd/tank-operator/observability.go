package main

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/nelsong6/tank-operator/backend-go/internal/pgstats"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessioncontroller"
)

// Observability is a real Prometheus surface scraped by the
// kube-prometheus-stack via the ServiceMonitor in k8s/templates/.
// docs/observability.md is the contract: counter names, label budgets,
// and the cardinality rules ("no pod_name, no session_id, no email as
// metric labels"). Anything added here must respect the same budget or
// the cluster's Prometheus instance will pay the bill in RAM.
//
// The previous ad-hoc counter surface was deleted in the observability
// cutover — scripts/check-removed-chat-runtime.mjs blocks reintroduction.

// --- HTTP request-path metrics ---

var (
	// httpRequestsTotal counts every request that reaches the orchestrator
	// mux. status_class is bucketed (2xx/3xx/4xx/5xx) instead of full
	// status code to keep cardinality bounded at routes * methods * 4.
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_http_requests_total",
			Help: "Total HTTP requests handled by tank-operator, labeled by route, method, and status class.",
		},
		[]string{"method", "route", "status_class"},
	)

	// httpRequestDurationSeconds is the latency histogram. It deliberately
	// omits status_class — that label would multiply series by 4 with no
	// operational gain (4xx/5xx latency rarely tells a different story than
	// 2xx latency for this app's surface).
	httpRequestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tank_http_request_duration_seconds",
			Help:    "Latency of HTTP requests handled by tank-operator.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)
)

// --- Session-event stream metrics (the names match what the prior
// counter surface exposed, so dashboards reading the old series keep
// rendering against the new collectors). ---

var (
	sessionEventStreamOpenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_stream_open_total",
		Help: "Times the per-session SSE event stream opened (durable transcript replay).",
	})
	sessionEventStreamReconnectTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_stream_reconnect_total",
		Help: "Times a session SSE event stream reconnected with a resumable cursor.",
	})
	sessionEventStreamResyncTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_stream_resync_required_total",
		Help: "Times a session SSE event stream required full resync (cursor not found in the durable ledger).",
	})
	sessionEventStreamErrorTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_stream_error_total",
		Help: "Errors emitted while serving the per-session SSE event stream.",
	})
	sessionEventTimelineFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_timeline_failure_total",
		Help: "Failures of the GET /timeline snapshot used by the SPA to bootstrap a session.",
	})
	// sessionEventTimelineRequestTotal labels each /timeline read by the
	// anchor shape the SPA chose. The tail-first navigation cutover makes
	// `newest` the default bootstrap, with `timeline_id` used for explicit
	// transcript links and `before_cursor` used for row back-pagination.
	sessionEventTimelineRequestTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_event_timeline_request_total",
		Help: "GET /timeline requests labeled by anchor shape the SPA chose.",
	}, []string{"anchor"})
	sessionEventStreamEmittedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_stream_emitted_total",
		Help: "Events emitted to a connected SSE consumer (post-filter).",
	})
	sessionEventStreamHeartbeatTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_stream_heartbeat_total",
		Help: "Heartbeat frames written on idle SSE streams.",
	})
	sessionEventWakeSubscribeFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_wake_subscribe_failure_total",
		Help: "Failures setting up the per-session NATS wake subscription that drives SSE streams.",
	})

	// sessionEventStreamLagSeconds is a histogram of producer→browser lag.
	// Replaces the prior last/max gauges, which couldn't be aggregated
	// usefully across replicas. Buckets target the live-render path's
	// expected latency band (10ms–10s).
	sessionEventStreamLagSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tank_session_event_stream_lag_seconds",
		Help:    "End-to-end lag from durable event creation to SSE emission.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})

	// Persister: schema rejections are permanent — anything malformed will
	// fail the same way on retry. The persister terminates these and
	// increments this counter. Steady-state expectation is zero; any
	// non-zero value means a producer is emitting non-Tank events to the
	// bus and the cutover guard in conversation-contract.yml missed it.
	sessionEventPersistSchemaRejectedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_persist_schema_rejected_total",
		Help: "Bus events the persister terminated because they failed Tank-event schema validation (producer-side regression).",
	})
	// Transient persister failures (Postgres connection blips, network
	// hiccups, etc.). These are retried up to the JetStream MaxDeliver
	// bound; the counter helps distinguish them from schema rejections
	// so alerts on the rejection counter aren't masked by infrastructure
	// noise.
	sessionEventPersistTransientFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_persist_transient_failure_total",
		Help: "Persister failures the persister chose to NAK for retry (infrastructure-side, retryable).",
	})

	// Durable per-turn failure surface (turn.failed / turn.command_failed).
	// Replaces the SPA run-status pill as the "every session is failing"
	// observability surface: with the pill removed, this counter is how
	// Grafana / alert rules detect provider-credential storms and other
	// session-affecting failures. Labels: source (claude/codex/tank) and
	// reason from payload.reason (e.g. provider_failure, command_failed).
	transcriptTurnFailureTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_transcript_turn_failure_total",
		Help: "Durable turn.failed / turn.command_failed events persisted to session_events, partitioned by producer source and payload reason.",
	}, []string{"source", "reason"})

	// Provider-credential health: the durable Layer 1 surface for
	// "Codex / Claude sign-in expired" banners. Replaces the SPA pill
	// as the user-trust observability surface for sustained
	// provider-auth failures. Steady-state expectation: poll counters
	// climb at one tick per provider per interval; status gauge stays
	// 1 on healthy and flips to 0 only during an actual outage;
	// transition counter and failure-duration histogram are quiet.
	providerHealthPollTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_provider_credential_poll_total",
		Help: "Provider-health poll attempts, partitioned by provider and outcome.",
	}, []string{"provider", "outcome"})
	providerHealthStatusGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tank_provider_credential_health_status",
		Help: "Current provider credential health (1 when status == label, else 0).",
	}, []string{"provider", "scope", "status"})
	providerHealthTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_provider_credential_transitions_total",
		Help: "Provider credential health transitions (healthy↔failed etc), partitioned by provider, scope, from, to, reason.",
	}, []string{"provider", "scope", "from", "to", "reason"})
	providerHealthFailureDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tank_provider_credential_failure_duration_seconds",
		Help:    "How long a provider's credential was in the failed state, observed on recovery.",
		Buckets: []float64{30, 60, 300, 900, 3600, 14400, 86400},
	}, []string{"provider", "scope"})
	providerHealthFanoutSessionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_provider_credential_fanout_sessions_total",
		Help: "Sessions that received a session.status banner event on a provider-credential transition.",
	}, []string{"provider", "scope"})

	// Wake-publish failures. The bus records here before returning the
	// error to the caller. Per-session wakes drive the chat-window SSE;
	// per-owner session-list events drive the sidebar SSE (replaced
	// the prior opaque wake subject in tank-operator#83). Steady-state
	// expectation is zero on both — a non-zero value means NATS is
	// unreachable, and SSE clients catch up from the durable Postgres
	// ledger on reconnect.
	sessionEventWakePublishFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_wake_publish_failure_total",
		Help: "Per-session SSE wake publishes that failed against NATS.",
	})
	sessionListEventPublishFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_event_publish_failure_total",
		Help: "Per-owner typed session-list event publishes that failed against NATS.",
	})
	// sessionBusCommandPublishFailureTotal covers the JetStream Publish
	// path for runner commands (submit_turn / interrupt_turn /
	// input_reply / stop_background_task). The two counters above
	// cover the raw nc.Publish wake fabric; this counter covers the
	// js.Publish command fabric. Both are needed: the 2026-05-25
	// incident produced a sustained js.Publish failure (JetStream
	// quorum loss → `nats: no response from stream`) while the wake
	// counters above stayed quiet, because raw nc.Publish does not
	// wait for a stream ack. Labels: kind from the closed Command.Type
	// set (4 series), reason from classifyPublishError (5 series) =
	// 20 series total. The TankSessionBusPublishFailing alert in
	// k8s/templates/observability.yaml pages on any non-zero rate.
	sessionBusCommandPublishFailureTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_bus_command_publish_failure_total",
		Help: "Session-bus JetStream command publishes (submit_turn/interrupt_turn/input_reply/stop_background_task) that failed, labeled by kind and classified reason.",
	}, []string{"kind", "reason"})
	// Wake success counters + persist→wake latency. The published vs
	// received delta is the candidate-A stethoscope (see
	// docs/quality-timeframes.md observability requirement and the
	// /api/debug/session-event-streams admin endpoint for per-session
	// resolution). Unlabeled aggregates per docs/observability.md
	// cardinality rules; per-storage-key resolution lives in the slog
	// line and the admin endpoint's stream snapshot, not in labels.
	sessionEventWakePublishedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_wake_published_total",
		Help: "Per-session SSE wakes successfully published to NATS by the persister or direct-writer call sites.",
	})
	sessionEventWakeReceivedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_event_wake_received_total",
		Help: "Per-session SSE wakes received by orchestrator-side NATS subscribers (the SSE handler's notify path).",
	})
	sessionEventPersistToWakeSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tank_session_event_persist_to_wake_seconds",
		Help:    "Duration from session_events row Upsert returning to the NATS wake publish completing.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	})
	// Per-event-type emit counter — the candidate-C stethoscope. When
	// the server's emit-by-type vs the client's receive-by-type
	// (tank_session_event_client_received_total, ingested through
	// POST /api/client-metrics/session-events-stream) diverge for a
	// specific event_type, the SPA reducer is dropping events
	// silently. Event types are a closed enum at
	// internal/conversation/types.go — bounded cardinality.
	sessionEventStreamEmittedByTypeTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_session_event_stream_emitted_by_type_total",
			Help: "Events emitted to a connected SSE consumer, labeled by Tank event type.",
		},
		[]string{"event_type"},
	)

	// turnLifecycleTotal counts durable turn lifecycle events committed
	// to session_events, partitioned by event type. The five types covered
	// — turn.submitted plus the four terminal types (turn.completed,
	// turn.failed, turn.command_failed, turn.interrupted) — bound a
	// turn's open/close lifecycle. The TankTurnSilentStranding alert in
	// k8s/templates/observability.yaml fires when sum(submitted) exceeds
	// sum(terminal) over a window long enough to rule out a single long
	// Codex turn.
	//
	// This is the silent-stranding observability surface named in the
	// Agent Runners contract (docs/features/agent-runners/contract.md
	// → Observability: "Silent strandings, where a requested action has
	// no terminal event, are a counted bug class"). ea70777
	// (nelsong6/tank-operator#652) — orchestrator deploy left every
	// pre-existing runner subscribed to the OLD wire format, every
	// submit_turn published to the new format was dropped, no terminal
	// event ever fired — would have triggered this alert within minutes
	// of deploy. Both the backend-direct path (handlers_turns.go
	// persistBackendEvent) and the runner-side persister
	// (sessionbus.RunEventPersister) increment this counter, so the
	// signal works regardless of which path wrote the row.
	//
	// Label cardinality is bounded to 5 series; non-lifecycle event
	// types are dropped at the recordTurnLifecyclePersisted call site.
	turnLifecycleTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_turn_lifecycle_total",
			Help: "Durable turn lifecycle events committed to session_events, labeled by event type (turn.submitted + four terminal types).",
		},
		[]string{"event_type"},
	)

	// turnTerminalMissingClientNonceTotal catches a producer contract
	// violation that the lifecycle counter cannot: terminal rows are
	// present, so the server-side turn is not silently stranded, but an
	// already-open browser tab cannot correlate the terminal back to its
	// local currentRunRef by client_nonce. That leaves follow-up submits
	// queued until refresh. Labels are bounded by the closed source enum
	// and the four terminal event types.
	turnTerminalMissingClientNonceTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_turn_terminal_missing_client_nonce_total",
			Help: "Durable turn terminal events persisted without client_nonce, partitioned by bounded producer source and terminal event type.",
		},
		[]string{"source", "event_type"},
	)

	// turnInterruptRequestTotal counts stop requests posted to /interrupt,
	// labeled by outcome at each exit point. Steady-state expectation:
	// persisted dominates; persist_failed and publish_failed near zero.
	// Cardinality bounded at 3, respects docs/observability.md budget.
	// The "stopping" durable-state migration is observable through this
	// counter — a regression that lets requests slip silently shows up as
	// a divergence between persisted and the durable session_events ledger
	// row count.
	turnInterruptRequestTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_turn_interrupt_request_total",
			Help: "Stop requests posted to /interrupt, labeled by outcome.",
		},
		[]string{"outcome"},
	)
)

// --- Hermes bridge metrics (hermes_gui session mode) ---
//
// Counters for the external-backend bridge driving Hermes Agent's
// /v1/runs API. Cardinality is bounded: outcome is a closed string set
// (created / failed_to_create), terminal is closed (completed / failed /
// interrupted / command_failed / lost), reason is closed (provider /
// stream / decode / unhandled_type). No per-session / per-user labels.
// See nelsong6/tank-operator#540's observability section.

var (
	hermesRunTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_hermes_run_total",
			Help: "POST /v1/runs attempts from the hermes bridge, by outcome.",
		},
		[]string{"outcome"},
	)

	hermesRunTerminalTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_hermes_run_terminal_total",
			Help: "Terminal outcomes observed on a hermes run's SSE stream.",
		},
		[]string{"terminal"},
	)

	hermesRunEventTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_hermes_run_event_total",
			Help: "Hermes /v1/runs event-stream events observed by the bridge, by bounded upstream event type.",
		},
		[]string{"event_type"},
	)

	hermesRunDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tank_hermes_run_duration_seconds",
			Help:    "Wall-clock duration from Hermes run creation or recovery pointer timestamp to terminal handling.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600},
		},
		[]string{"terminal"},
	)

	hermesCapabilityCheckTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_hermes_capability_check_total",
			Help: "Hermes /v1/capabilities startup checks by bounded result.",
		},
		[]string{"result"},
	)

	hermesTranslatorErrorTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_hermes_translator_error_total",
			Help: "Hermes event shapes the translator could not map. Non-zero is a Hermes-upstream schema-drift signal.",
		},
		[]string{"reason"},
	)
)

// --- Service-principal (role=service) request metrics ---

// serviceRoleRequestsTotal counts every call to a service-principal-gated
// internal handler, labeled by route and outcome. Tracks both denial
// breakdown (helps catch upstream regressions in auth.romaine.life's
// /api/auth/exchange/k8s) and success volume (drives Stage 6 quota
// decisions). Labels are bounded: routes are static path patterns,
// results are a closed string set defined alongside the handler
// (denied_token | denied_role | denied_actor_missing |
// error_verifier_unconfigured | error_create_failed | ok). See
// nelsong6/tank-operator#486 stage 5.
var serviceRoleRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_service_role_requests_total",
		Help: "Calls to service-principal internal handlers, by route and outcome.",
	},
	[]string{"route", "result"},
)

func recordServiceRoleRequest(route, result string) {
	serviceRoleRequestsTotal.WithLabelValues(route, result).Inc()
}

// sessionReposSelectedTotal counts every session-create call by the
// coarse repo-count bucket (none | one | many). Bounded cardinality
// (3 series) keeps Prometheus happy while still surfacing the
// operational shape for the repo-cloner path:
//
//   - Is the splash picker being used at all? (none vs. one+many ratio)
//   - Is the many-repo path getting real exercise? (predicts
//     init-container parallelism / latency budget)
//
// The exact slug list is durable on sessions.repos, so any deeper
// analysis (which repos, which users) is recoverable from the DB on
// demand. Per docs/observability.md: emit the counter that answers
// the user-trust question; don't pre-mint dimensions we won't query.
var sessionReposSelectedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_session_repos_selected_total",
		Help: "Sessions created bucketed by how many repos the user picked at create time.",
	},
	[]string{"count_bucket"},
)

var sessionRuntimeConfigUpdateTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_session_runtime_config_update_total",
		Help: "Session runtime config reports from pod-side runners, labeled by provider and bounded result.",
	},
	[]string{"provider", "result"},
)

func recordSessionRuntimeConfigUpdate(provider, result string) {
	sessionRuntimeConfigUpdateTotal.WithLabelValues(
		sessionRuntimeConfigProviderLabel(provider),
		sessionRuntimeConfigResultLabel(result),
	).Inc()
}

var avatarAssetRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_avatar_asset_requests_total",
		Help: "Avatar asset API requests labeled by bounded operation, kind, and result.",
	},
	[]string{"operation", "kind", "result"},
)

var avatarUploadAttemptsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_avatar_upload_attempts_total",
		Help: "Avatar upload attempts labeled by bounded server-side stage and result.",
	},
	[]string{"stage", "result"},
)

func recordAvatarAssetRequest(operation, kind, result string) {
	avatarAssetRequestsTotal.WithLabelValues(
		avatarAssetOperationLabel(operation),
		avatarAssetKindLabel(kind),
		avatarAssetResultLabel(result),
	).Inc()
}

func recordAvatarUploadAttempt(stage, result string) {
	avatarUploadAttemptsTotal.WithLabelValues(
		avatarUploadStageLabel(stage),
		avatarUploadResultLabel(result),
	).Inc()
}

func avatarAssetOperationLabel(operation string) string {
	switch operation {
	case "list", "read_image", "create", "delete", "update_kind":
		return operation
	default:
		return "unknown"
	}
}

func avatarAssetKindLabel(kind string) string {
	switch kind {
	case "agent", "system":
		return kind
	default:
		return "unknown"
	}
}

func avatarAssetResultLabel(result string) string {
	switch result {
	case "ok", "bad_request", "forbidden", "not_found", "store_unavailable", "store_error":
		return result
	default:
		return "other"
	}
}

func avatarUploadStageLabel(stage string) string {
	switch stage {
	case "received", "parse_multipart", "validate_kind", "validate_name", "validate_crop", "read_avatar", "read_backing", "store_avatar", "store_backing", "create_metadata", "complete":
		return stage
	default:
		return "other"
	}
}

func avatarUploadResultLabel(result string) string {
	switch result {
	case "started", "ok", "store_unavailable", "wrong_media_type", "missing_boundary", "parse_error", "body_too_large", "invalid_kind", "invalid_name", "invalid_crop", "missing_field", "empty_file", "field_too_large", "invalid_mime", "read_error", "store_error":
		return result
	default:
		return "other"
	}
}

func sessionRuntimeConfigProviderLabel(provider string) string {
	switch provider {
	case "claude", "codex":
		return provider
	default:
		return "unknown"
	}
}

func sessionRuntimeConfigResultLabel(result string) string {
	switch result {
	case "ok", "bad_request", "forbidden", "not_found", "manager_unavailable", "update_failed":
		return result
	default:
		return "other"
	}
}

// --- Browser stream auth metrics ---
//
// Native EventSource cannot attach Authorization headers, so the SPA mints
// short-lived opaque stream tickets through POST /api/auth/stream-ticket and
// presents those to the SSE handlers. This counter tells on the auth boundary
// that broke live delivery in the auth.romaine.life JWT cutover: if create or
// validate store failures rise, EventSource connections will never become open
// streams, even though ordinary REST timeline reads keep working.
var streamAuthTicketTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "tank_stream_auth_ticket_total",
		Help: "Stream auth ticket create/validate attempts, labeled by bounded operation, stream, and result.",
	},
	[]string{"operation", "stream", "result"},
)

func recordStreamAuthTicket(operation, stream, result string) {
	streamAuthTicketTotal.WithLabelValues(
		streamAuthTicketOperationLabel(operation),
		streamAuthTicketStreamLabel(stream),
		streamAuthTicketResultLabel(result),
	).Inc()
}

func streamAuthTicketOperationLabel(operation string) string {
	switch operation {
	case "create", "validate":
		return operation
	default:
		return "unknown"
	}
}

func streamAuthTicketStreamLabel(stream string) string {
	switch stream {
	case streamKindSessionList, streamKindSessionEvents:
		return stream
	default:
		return "unknown"
	}
}

func streamAuthTicketResultLabel(result string) string {
	switch result {
	case "ok", "invalid", "denied", "store_unavailable", "store_error":
		return result
	default:
		return "other"
	}
}

// --- Browser session-event stream telemetry ---
//
// The candidate-B and candidate-C stethoscope on the browser side. The
// SPA's sessionEventStreamTelemetry.ts emits one event per opened /
// received / silent / closed / error transition; the orchestrator
// buckets the labels server-side. Cardinality budget: 10 events ×
// 13 modes = 130 base + 15 event_types × 13 modes = 195 receive +
// 13 mode × 10 silent histogram buckets = 130 → ~455 series.

var (
	sessionEventStreamClientReportsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_session_event_client_reports_total",
			Help: "Browser session-event stream metric report requests, labeled by bounded result.",
		},
		[]string{"result"},
	)
	sessionEventStreamClientEventsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_session_event_client_events_total",
			Help: "Semantic browser-side SSE stream events reported by the SPA, labeled by event + session_mode.",
		},
		[]string{"event", "session_mode"},
	)
	// sessionEventStreamClientReceivedTotal is the candidate-C
	// stethoscope: divergence between this counter and the
	// server-side tank_session_event_stream_emitted_by_type_total
	// for the same event_type means the SPA reducer is dropping
	// events the SSE handler emitted.
	sessionEventStreamClientReceivedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_session_event_client_received_total",
			Help: "Tank conversation events the SPA observed via the per-session SSE stream, labeled by event type + session_mode.",
		},
		[]string{"event_type", "session_mode"},
	)
	// sessionEventStreamClientSilentSeconds is the candidate-B
	// histogram. When the SPA sits with a connected SSE but receives
	// no tank events for N seconds while a turn is running, it
	// observes the idle duration here. p95 climbing into the
	// minutes is the zombie-connection signature.
	sessionEventStreamClientSilentSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tank_session_event_client_stream_silent_seconds",
			Help:    "Browser-observed duration the per-session SSE remained silent while a turn was in flight.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800},
		},
		[]string{"session_mode"},
	)
)

func recordSessionEventStreamClientReport(result string) {
	sessionEventStreamClientReportsTotal.WithLabelValues(sessionEventStreamClientResultLabel(result)).Inc()
}

func recordSessionEventStreamClientEvent(event sessionEventStreamMetricEvent) {
	mode := chatScrollSessionModeLabel(event.SessionMode)
	eventLabel := sessionEventStreamClientEventLabel(event.Event)
	sessionEventStreamClientEventsTotal.WithLabelValues(eventLabel, mode).Inc()
	if eventLabel == "tank_event_received" {
		sessionEventStreamClientReceivedTotal.WithLabelValues(
			sessionEventTypeLabel(event.EventType),
			mode,
		).Inc()
	}
	if eventLabel == "stream_silent_while_running" && event.IdleSeconds != nil {
		sessionEventStreamClientSilentSeconds.WithLabelValues(mode).Observe(*event.IdleSeconds)
	}
}

func sessionEventStreamClientResultLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "ok", "invalid_json", "invalid_value", "too_many_events", "denied_role":
		return raw
	default:
		return "other"
	}
}

// --- Browser session-list debug captures ---
//
// The SPA posts bounded client-side debug rings only when an operator or user
// explicitly captures the current browser state or records a diagnostic window.
// Metrics stay bounded: no email, session_id, path, or raw reason labels.

var (
	sessionListDebugCaptureReportsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_session_list_debug_capture_reports_total",
			Help: "Browser session-list debug capture report requests, labeled by bounded result and reason.",
		},
		[]string{"result", "reason"},
	)
	debugSessionListCaptureReadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_admin_debug_session_list_capture_reads_total",
			Help: "Admin reads of /api/debug/session-list-captures, labeled by bounded result.",
		},
		[]string{"result"},
	)
)

func recordSessionListDebugCapture(result, reason string) {
	sessionListDebugCaptureReportsTotal.WithLabelValues(
		sessionListDebugCaptureResultLabel(result),
		sessionListDebugCaptureReasonLabel(reason),
	).Inc()
}

func recordDebugSessionListCaptureRead(result string) {
	debugSessionListCaptureReadsTotal.WithLabelValues(
		sessionListDebugCaptureReadResultLabel(result),
	).Inc()
}

func sessionListDebugCaptureResultLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "ok", "invalid_json", "invalid_value", "denied_role", "not_configured", "store_error":
		return raw
	default:
		return "other"
	}
}

func sessionListDebugCaptureReadResultLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "ok", "empty", "bad_request", "forbidden", "not_configured", "store_error":
		return raw
	default:
		return "other"
	}
}

func sessionListDebugCaptureReasonLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "manual-capture",
		"manual-record-start",
		"manual-record-sample",
		"manual-record-stop":
		return raw
	default:
		return "other"
	}
}

// --- Browser chat-scroll metrics ---
//
// The SPA reports semantic scroll decisions here; the orchestrator owns
// label bucketing so browser details never become high-cardinality series.
// No user, session_id, URL, or raw path labels are exposed.

var (
	chatScrollClientReportsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_chat_scroll_client_reports_total",
			Help: "Browser chat-scroll metric report requests, labeled by bounded result.",
		},
		[]string{"result"},
	)
	chatScrollClientEventsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_chat_scroll_client_events_total",
			Help: "Semantic chat transcript scroll events reported by browsers.",
		},
		[]string{"event", "surface", "session_mode", "at_bottom", "has_scroll_parent"},
	)
	chatScrollClientBottomDistancePixels = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "tank_chat_scroll_client_bottom_distance_pixels",
			Help: "Distance in pixels from the transcript viewport bottom when browser scroll events were reported.",
			Buckets: []float64{
				0, 1, 4, 24, 100, 500, 1000, 5000, 10000, 50000,
			},
		},
		[]string{"event", "surface", "session_mode"},
	)
	chatScrollClientEntries = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tank_chat_scroll_client_entries",
			Help:    "Transcript entry counts attached to browser scroll events.",
			Buckets: []float64{0, 10, 50, 100, 200, 500, 1000, 2000, 5000},
		},
		[]string{"event", "surface", "session_mode"},
	)
)

func recordChatScrollClientReport(result string) {
	chatScrollClientReportsTotal.WithLabelValues(result).Inc()
}

func recordChatScrollClientEvent(event chatScrollMetricEvent) {
	eventLabel := chatScrollEventLabel(event.Event)
	surface := chatScrollSurfaceLabel(event.Surface)
	mode := chatScrollSessionModeLabel(event.SessionMode)
	atBottom := boolMetricLabel(event.AtBottom)
	hasScrollParent := boolMetricLabel(event.HasScrollParent)
	chatScrollClientEventsTotal.WithLabelValues(
		eventLabel,
		surface,
		mode,
		atBottom,
		hasScrollParent,
	).Inc()
	if event.BottomDistance != nil && *event.BottomDistance >= 0 {
		chatScrollClientBottomDistancePixels.WithLabelValues(
			eventLabel,
			surface,
			mode,
		).Observe(*event.BottomDistance)
	}
	if event.Entries != nil && *event.Entries >= 0 {
		chatScrollClientEntries.WithLabelValues(
			eventLabel,
			surface,
			mode,
		).Observe(*event.Entries)
	}
}

func boolMetricLabel(value *bool) string {
	if value == nil {
		return "unknown"
	}
	if *value {
		return "true"
	}
	return "false"
}

// --- Browser long-task metrics ---
//
// Surfaces main-thread blocks ≥50 ms reported by the SPA via
// PerformanceObserver({type: "longtask"}). The SPA passes raw
// durations and correlation deltas; the server picks a single bounded
// `correlation` label so cardinality stays predictable. The SPA user
// can't open devtools' Performance panel, so this counter and its
// duration histogram are the operational replacement.
var (
	longTaskClientReportsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_client_long_task_reports_total",
			Help: "Browser long-task metric report requests, labeled by bounded result.",
		},
		[]string{"result"},
	)
	longTaskClientTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_client_long_task_total",
			Help: "Browser main-thread long tasks (≥50 ms) reported by the SPA, labeled by session mode, attribution, and correlation.",
		},
		[]string{"session_mode", "attribution", "correlation"},
	)
	longTaskClientDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "tank_client_long_task_duration_seconds",
			Help: "Duration of browser main-thread long tasks (≥50 ms) reported by the SPA.",
			// Buckets target the input-responsiveness range. 0.05 is the
			// spec floor for a longtask; >2 s is the "page feels frozen"
			// threshold past which finer resolution doesn't change the
			// diagnosis ("it's stuck").
			Buckets: []float64{0.05, 0.075, 0.1, 0.15, 0.25, 0.5, 1, 2, 5},
		},
		[]string{"session_mode", "correlation"},
	)
)

func recordLongTaskClientReport(result string) {
	longTaskClientReportsTotal.WithLabelValues(result).Inc()
}

func recordLongTaskClientEvent(event longTaskMetricEvent) {
	if event.DurationMs == nil || *event.DurationMs < longTaskMinDurationMs {
		return
	}
	mode := longTaskSessionModeLabel(event.SessionMode)
	attribution := longTaskAttributionLabel(event.Attribution)
	correlation := longTaskCorrelationLabel(event)
	longTaskClientTotal.WithLabelValues(mode, attribution, correlation).Inc()
	longTaskClientDurationSeconds.WithLabelValues(mode, correlation).Observe(*event.DurationMs / 1000.0)
}

// GET /api/github/repos counters. The endpoint proxies through to
// mcp-github via an on-behalf-of token mint; both legs can fail
// independently, so we surface a simple ok|error outcome label plus
// the end-to-end latency histogram. The picker's "All repos" section
// uses both: rate(error) > 0 → red banner on the dashboard; p95
// > 2s → the SPA's spinner is starting to feel laggy.
var (
	githubRepoListRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_github_repo_list_requests_total",
			Help: "Calls to /api/github/repos, labeled by outcome.",
		},
		[]string{"result"},
	)
	githubRepoListDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "tank_github_repo_list_duration_seconds",
			Help:    "End-to-end /api/github/repos latency: orchestrator → auth.romaine.life exchange → mcp-github → orchestrator response.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10},
		},
	)
)

// --- Admin cross-user read metrics ---
//
// Counts every time a Tank admin-power caller reads a session whose Owner !=
// their owner email, or lists sessions for a different owner via
// `?owner=<email>`. Single-counter shape (no labels) keeps cardinality
// at 2 series total — the operational signal is "is this happening at
// all" plus per-rate alerting if it ever spikes outside expected admin
// activity windows. The reads themselves are intentionally not
// per-target-email so audit-trail concerns belong in the request log,
// not the metrics surface.

var (
	adminCrossUserReadsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_admin_cross_user_session_reads_total",
		Help: "Times a Tank admin-power caller read a session belonging to another user (per-session read endpoints).",
	})
	adminCrossUserListsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_admin_cross_user_session_lists_total",
		Help: "Times a Tank admin-power caller passed `?owner=<email>` to list sessions or session-list events for another user.",
	})

	// debugSessionEventLedgerReadsTotal is the volume signal for the
	// admin-only `GET /api/debug/session-event-ledger` surface.
	// `result` labels: ok, empty, bad_request, forbidden, store_error,
	// not_configured. Pair with the audit slog line per call; the
	// counter answers "is this being used at scale", the slog line
	// answers "who read what, and when". `result=empty` is its own
	// label so a wave of misdirected lookups (wrong scope, wrong id)
	// is visible without grepping logs.
	debugSessionEventLedgerReadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_admin_debug_session_event_ledger_reads_total",
			Help: "Admin reads of /api/debug/session-event-ledger, labeled by bounded result.",
		},
		[]string{"result"},
	)
)

func recordDebugSessionEventLedgerRead(result string) {
	debugSessionEventLedgerReadsTotal.WithLabelValues(
		debugSessionEventLedgerResultLabel(result),
	).Inc()
}

func debugSessionEventLedgerResultLabel(result string) string {
	switch result {
	case "ok", "empty", "bad_request", "forbidden", "store_error", "not_configured":
		return result
	default:
		return "other"
	}
}

func recordAdminCrossUserRead() {
	adminCrossUserReadsTotal.Inc()
}

func recordAdminCrossUserList() {
	adminCrossUserListsTotal.Inc()
}

// --- Session-list (sidebar) stream metrics --- the matching names for
// the chat-side counters above so dashboards can render both ledgers
// side-by-side. Same shape: open/reconnect/resync/error/emitted/heartbeat.

var (
	sessionListStreamOpenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_open_total",
		Help: "Times the per-owner sidebar SSE stream opened (durable session-list replay).",
	})
	sessionListStreamReconnectTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_reconnect_total",
		Help: "Times a sidebar SSE stream reconnected with a resumable cursor.",
	})
	sessionListStreamResyncTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_resync_required_total",
		Help: "Times a sidebar SSE stream required full resync (cursor not found in the lifecycle ledger).",
	})
	sessionListStreamErrorTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_error_total",
		Help: "Errors emitted while serving the per-owner sidebar SSE stream.",
	})
	sessionListStreamEmittedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_emitted_total",
		Help: "Typed session-events emitted to a connected sidebar SSE consumer (post-filter).",
	})
	sessionListStreamHeartbeatTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_heartbeat_total",
		Help: "Heartbeat frames written on idle sidebar SSE streams.",
	})
	// sessionListStreamColdOpenFastForwardTotal fires when an SSE
	// connection opens with no cursor and the server jumps the cursor
	// to the current row-version tip instead of replaying every
	// historical row. New clients pass the snapshot's
	// Tank-Sessions-Snapshot-Cursor as their initial cursor; a high
	// rate here indicates legacy clients (or a client whose snapshot
	// pre-dated the sessions table having any rows) —
	// informational, not an alert signal.
	sessionListStreamColdOpenFastForwardTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_cold_open_fast_forward_total",
		Help: "Sidebar SSE opens that jumped an empty cursor to the current tip instead of replaying history.",
	})
	// sessionListCrossScopeEventsDroppedTotal fires when a payload arrives
	// on the per-(owner, scope) NATS subject whose embedded session_scope
	// does not match this orchestrator's configured scope. The subject
	// shape itself prevents cross-scope delivery in steady state, so any
	// non-zero rate here is a producer-side regression: someone published
	// to the right subject with the wrong scope inside the payload, which
	// would silently mutate sidebar state on the client before this guard
	// was added. PrometheusRule alerts on rate > 0 over 10 minutes.
	sessionListCrossScopeEventsDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_cross_scope_events_dropped_total",
		Help: "Session-list NATS payloads dropped because the embedded session_scope did not match the local orchestrator scope.",
	})
)

// --- Session controller producer metrics. ---
//
// Post-Phase-4 the sessions row is the only durable state on the
// sidebar path; these metrics track the producers that write it.

var (
	sessionRowUpdateLagSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tank_session_row_update_lag_seconds",
		Help:    "End-to-end lag between an observed K8s pod transition and the sessions row column update.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})
	// sessionPodInformerLeaderHeld is the single-writer health
	// signal. 1 on the replica currently holding the lease, 0
	// elsewhere. If the SUM across replicas is 0 for an extended
	// window the alert fires. Name retained for grafana / alert
	// continuity; the producer is the K8s watch leader.
	sessionPodInformerLeaderHeld = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tank_session_pod_informer_leader_held",
		Help: "1 when this replica currently holds the session-controller Lease; 0 otherwise.",
	})
	// sessionActivityDeltaTotal: emitted-or-skipped breakdown of the
	// persister-driven session.activity_changed deltas. The "skipped"
	// counter dominates in steady state (most chat events are turn-
	// status indicator no-ops); a sudden surge in "emitted" without a
	// matching pattern in user activity is a regression signal.
	sessionActivityDeltaTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_activity_delta_total",
		Help: "Activity-summary delta decisions from the chat-activity emitter, labeled by outcome (emitted/skipped).",
	}, []string{"outcome"})
	sessionActivityDeltaFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_activity_delta_failure_total",
		Help: "Activity-summary delta derivation failures (store/publish errors).",
	})
	// sessionActivityErrorTransitionsTotal: how often the per-session
	// activity pill flips from a non-error state into "error", labeled
	// by cause. The previous behavior — folding item.failed (a single
	// failed tool call) into session-level error — left healthy
	// mid-turn sessions pinned red; the fix narrows session-level
	// error to durable turn-terminal events and pod state. This
	// counter is the user-trust signal that the narrowing held: a
	// surge in reason="unknown" (or a reappearance of item-level
	// inference if anyone rewires it) is the regression alarm.
	// See docs/tank-conversation-protocol.md "State Machine".
	sessionActivityErrorTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_activity_error_transitions_total",
		Help: "Session activity pill transitions into the \"error\" state, labeled by cause (pod_failed, turn_failed, turn_command_failed, unknown).",
	}, []string{"reason"})
	sessionActivityLateInterruptIgnoredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_activity_late_interrupt_ignored_total",
		Help: "Late turn.interrupt_requested lifecycle events ignored because the activity fold had already reached a non-active state.",
	}, []string{"status"})
	// sessionRowUpdatesTotal counts sessioncontroller.RowWriter's
	// per-event outcomes on the sessions row. Outcome=ok dominates in
	// steady state; outcome=failed > 0 means the sidebar's column
	// values are drifting from observed pod state. Alerts in
	// k8s/templates/observability.yaml.
	sessionRowUpdatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_row_updates_total",
		Help: "sessions row column updates from RowWriter, labeled by event type and outcome.",
	}, []string{"type", "outcome"})
)

// recordSessionListStreamError centralizes the sidebar SSE error counter
// bump so handler call sites don't import the metric symbol directly.
func recordSessionListStreamError() {
	sessionListStreamErrorTotal.Inc()
}

// promK8sWatchMetrics satisfies sessioncontroller.K8sWatchMetrics so
// the watch can record transitions, leadership state, and producer
// lag without importing prometheus. Renamed from
// promPodInformerMetrics in docs/session-list-redesign.md Phase 1
// when the K8s watch loop was consolidated into the controller
// package.
type promK8sWatchMetrics struct{}

func (promK8sWatchMetrics) RecordTransition(_ string) {
	// Per-transition counts are now owned by RowWriter via
	// tank_session_row_updates_total{type,outcome} — keeping a
	// separate K8s-watch counter would double-count the same emit.
}

func (promK8sWatchMetrics) RecordLag(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	sessionRowUpdateLagSeconds.Observe(seconds)
}

func (promK8sWatchMetrics) RecordLeaderStatus(isLeader bool) {
	if isLeader {
		sessionPodInformerLeaderHeld.Set(1)
		return
	}
	sessionPodInformerLeaderHeld.Set(0)
}

// promRowWriterMetrics satisfies sessioncontroller.RowWriterMetrics —
// the per-event observability surface on the sessions row write path.
// Post-Phase-4 the sidebar renders directly from the row, so a
// non-zero failure rate here is user-visible as stale columns.
type promRowWriterMetrics struct{}

func (promRowWriterMetrics) RecordRowUpdate(eventType string) {
	sessionRowUpdatesTotal.WithLabelValues(eventType, "ok").Inc()
}

func (promRowWriterMetrics) RecordRowUpdateFailure(eventType string) {
	sessionRowUpdatesTotal.WithLabelValues(eventType, "failed").Inc()
}

// promLifecycleEmitterMetrics satisfies
// sessioncontroller.LifecycleEmitterMetrics for the chat→activity-
// summary bridge. Emitted=true increments {outcome="emitted"}, false
// → {outcome="skipped"}. The RowWriter's tank_session_row_updates_total
// counter covers the actual sessions row write that follows an
// emitted=true call; this metric covers the emitter's own dedup
// decision (most chat events resolve to no-op).
type promLifecycleEmitterMetrics struct{}

func (promLifecycleEmitterMetrics) RecordActivityDelta(emitted bool) {
	outcome := "skipped"
	if emitted {
		outcome = "emitted"
	}
	sessionActivityDeltaTotal.WithLabelValues(outcome).Inc()
}

func (promLifecycleEmitterMetrics) RecordActivityFailure() {
	sessionActivityDeltaFailureTotal.Inc()
}

func (promLifecycleEmitterMetrics) RecordActivityErrorTransition(reason string) {
	sessionActivityErrorTransitionsTotal.WithLabelValues(reason).Inc()
}

func (promLifecycleEmitterMetrics) RecordActivityLateInterruptIgnored(status string) {
	sessionActivityLateInterruptIgnoredTotal.WithLabelValues(sessionActivityStatusLabel(status)).Inc()
}

func sessionActivityStatusLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "ready", "submitted", "streaming", "needs_input", "stopping", "stopped", "error":
		return strings.TrimSpace(raw)
	default:
		return "other"
	}
}

// --- Postgres query tracer metrics ---

var (
	pgQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tank_pg_queries_total",
			Help: "Postgres queries executed by tank-operator, labeled by operation type and outcome.",
		},
		[]string{"operation", "outcome"},
	)
	pgQueryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tank_pg_query_duration_seconds",
			Help:    "Latency of Postgres queries executed by tank-operator.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		},
		[]string{"operation"},
	)

	// Postgres server-side connection-saturation surface. The
	// pgQueriesTotal counter above sees this orchestrator's own
	// pool traffic; these gauges see the SERVER's total backend
	// count and max_connections ceiling. The 2026-05-25 incident
	// crash-looped the orchestrator on SQLSTATE 53300 with no
	// advance signal at all — these gauges + the
	// TankPgConnectionSaturation alert make the next saturation
	// approach visible 13+ conns before the cap. Polled every 30s
	// by internal/pgstats.Poller using the orchestrator's existing
	// AAD-aware pgxpool; see that package for the auth/scale
	// rationale.
	//
	// Both are unlabeled gauges — the server is a single shared
	// instance; per-pod resolution would just multiply N identical
	// series since every orchestrator pod sees the same server
	// state. Alert PromQL uses max() across pods to dedupe.
	pgBackendConnectionsGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tank_pg_backend_connections",
		Help: "Total Postgres backend connections currently established on the shared Flex Server (sum across all databases). Read every 30s from pg_stat_database. Same value across orchestrator pods — aggregate with max() in PromQL.",
	})
	pgMaxConnectionsGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tank_pg_max_connections",
		Help: "Postgres server's max_connections cap. Read every 30s from current_setting('max_connections') so an in-place SKU change updates the alert headroom without an orchestrator restart.",
	})
	pgStatsPollTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_pg_stats_poll_total",
		Help: "Outcomes of the pgstats connection-saturation poller. Steady-state expectation: every increment lands on outcome=ok.",
	}, []string{"outcome"})
)

// promPGStatsMetrics satisfies pgstats.Metrics so the poller can
// emit gauges/counters without importing prometheus. Outcome label
// is validated against the closed set the poller emits — anything
// else collapses to "other" so a future poller change can't quietly
// inflate the cardinality.
type promPGStatsMetrics struct{}

func (promPGStatsMetrics) RecordBackendConnections(count float64) {
	pgBackendConnectionsGauge.Set(count)
}

func (promPGStatsMetrics) RecordMaxConnections(count float64) {
	pgMaxConnectionsGauge.Set(count)
}

func (promPGStatsMetrics) RecordPollOutcome(outcome string) {
	switch outcome {
	case "ok", "query_failed":
		pgStatsPollTotal.WithLabelValues(outcome).Inc()
	default:
		pgStatsPollTotal.WithLabelValues("other").Inc()
	}
}

// --- NATS connection metrics ---

var (
	natsDisconnectTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_nats_disconnect_total",
		Help: "Times the NATS client transitioned from connected to disconnected.",
	})
	natsReconnectTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_nats_reconnect_total",
		Help: "Times the NATS client reconnected to a server.",
	})
	natsAsyncErrorTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_nats_async_error_total",
		Help: "Async errors raised on the NATS connection (slow consumer, permission, etc.).",
	})
)

// recordSessionEventStreamError is kept as a function so callers don't
// import this package's symbols directly — same pattern as before the
// prometheus cutover.
func recordSessionEventStreamError() {
	sessionEventStreamErrorTotal.Inc()
}

func recordSessionEventTimelineFailure() {
	sessionEventTimelineFailureTotal.Inc()
}

// recordSessionEventTimelineRequest tags one /timeline read with the anchor
// shape the SPA chose. Centralized so the label set stays disciplined.
func recordSessionEventTimelineRequest(anchor string) {
	sessionEventTimelineRequestTotal.WithLabelValues(anchor).Inc()
}

func recordSessionEventPersistSchemaRejected() {
	sessionEventPersistSchemaRejectedTotal.Inc()
}

func recordSessionEventPersistTransientFailure() {
	sessionEventPersistTransientFailureTotal.Inc()
}

// promPersisterMetrics satisfies sessionbus.PersisterMetrics so the bus
// can increment the schema/transient-failure counters without importing
// prometheus directly.
type promPersisterMetrics struct{}

func (promPersisterMetrics) RecordSchemaRejected() {
	recordSessionEventPersistSchemaRejected()
}

func (promPersisterMetrics) RecordTransientFailure() {
	recordSessionEventPersistTransientFailure()
}

func (promPersisterMetrics) RecordTurnFailurePersisted(source string, reason string) {
	transcriptTurnFailureTotal.WithLabelValues(source, reason).Inc()
}

func (promPersisterMetrics) RecordTurnLifecyclePersisted(eventType string) {
	recordTurnLifecyclePersisted(eventType)
}

func (promPersisterMetrics) RecordTurnTerminalMissingClientNonce(source string, eventType string) {
	recordTurnTerminalMissingClientNonce(source, eventType)
}

// recordTurnLifecyclePersisted bumps tank_turn_lifecycle_total for the
// five lifecycle event types that bound a turn. Callers MUST filter
// via conversation.IsTurnLifecycleEvent before invoking this helper —
// the label set is bounded at 5 series, and the impl trusts the call
// site rather than re-filtering. Both persistOneEvent (sessionbus) and
// persistBackendEvent (handlers_turns.go) follow this pattern. Adding
// a third writer is a regression unless that writer also lands rows in
// session_events and applies the same call-site filter.
func recordTurnLifecyclePersisted(eventType string) {
	turnLifecycleTotal.WithLabelValues(eventType).Inc()
}

func recordTurnTerminalMissingClientNonce(source string, eventType string) {
	turnTerminalMissingClientNonceTotal.WithLabelValues(
		sessionEventSourceLabel(source),
		sessionEventTypeLabel(eventType),
	).Inc()
}

// promProviderHealthMetrics satisfies providerhealth.Metrics so the
// transcript-banner poller can increment counters without importing
// prometheus directly. The gauge flips between status labels rather
// than zero/one to match the multi-status enum (healthy/degraded/failed)
// without making PromQL queries guess at the "off" state.
type promProviderHealthMetrics struct{}

func (promProviderHealthMetrics) RecordPoll(provider string, ok bool) {
	outcome := "ok"
	if !ok {
		outcome = "error"
	}
	providerHealthPollTotal.WithLabelValues(provider, outcome).Inc()
}

func (promProviderHealthMetrics) RecordHealthStatus(provider, scope, status string) {
	for _, candidate := range []string{"healthy", "degraded", "failed"} {
		value := 0.0
		if candidate == status {
			value = 1.0
		}
		providerHealthStatusGauge.WithLabelValues(provider, scope, candidate).Set(value)
	}
}

func (promProviderHealthMetrics) RecordTransition(provider, scope, from, to, reason string) {
	if reason == "" {
		reason = "unknown"
	}
	providerHealthTransitionsTotal.WithLabelValues(provider, scope, from, to, reason).Inc()
}

func (promProviderHealthMetrics) RecordFailureDuration(provider, scope string, seconds float64) {
	providerHealthFailureDurationSeconds.WithLabelValues(provider, scope).Observe(seconds)
}

func (promProviderHealthMetrics) RecordFanout(provider, scope string, sessions int) {
	if sessions <= 0 {
		return
	}
	providerHealthFanoutSessionsTotal.WithLabelValues(provider, scope).Add(float64(sessions))
}

// promWakeMetrics satisfies sessionbus.WakeMetrics so the bus can increment
// wake/event-publish failure counters without importing prometheus directly.
// The published/received pair powers the candidate-A wake-key-mismatch
// stethoscope; the persist→wake duration histogram catches a slow-publish
// tail the simple failure counter cannot.
type promWakeMetrics struct{}

func (promWakeMetrics) RecordSessionEventWakePublishFailed() {
	sessionEventWakePublishFailureTotal.Inc()
}

func (promWakeMetrics) RecordSessionListEventPublishFailed() {
	sessionListEventPublishFailureTotal.Inc()
}

func (promWakeMetrics) RecordSessionEventWakePublished() {
	sessionEventWakePublishedTotal.Inc()
}

func (promWakeMetrics) RecordSessionEventWakeReceived() {
	sessionEventWakeReceivedTotal.Inc()
}

func (promWakeMetrics) RecordSessionEventPersistToWakeDuration(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	sessionEventPersistToWakeSeconds.Observe(seconds)
}

func (promWakeMetrics) RecordCommandPublishFailed(kind string, reason string) {
	sessionBusCommandPublishFailureTotal.WithLabelValues(
		sessionBusCommandKindLabel(kind),
		sessionBusCommandReasonLabel(reason),
	).Inc()
}

// sessionBusCommandKindLabel is the prom-side validator for the kind
// label. The bus already buckets via commandKindLabel; this guard
// catches a future schema drift where a new Command.Type slips through
// without a matching bucket update, so cardinality stays bounded.
func sessionBusCommandKindLabel(kind string) string {
	switch kind {
	case "submit_turn", "interrupt_turn", "input_reply", "stop_background_task", "other":
		return kind
	default:
		return "other"
	}
}

// sessionBusCommandReasonLabel mirrors classifyPublishError's bounded
// output. Validation is intentionally redundant with the bus's own
// classifier so a future bus change can't silently widen this label
// set without touching the prom layer too.
func sessionBusCommandReasonLabel(reason string) string {
	switch reason {
	case "no_response_from_stream", "connection", "timeout", "canceled", "other":
		return reason
	default:
		return "other"
	}
}

// recordSessionEventStreamEmittedByType is the per-event-type bump that
// pairs with the existing unlabeled tank_session_event_stream_emitted_total.
// Event type is bucketed against the closed enum in
// internal/conversation/types.go; unknown shapes collapse to "other" so
// label cardinality stays bounded if a producer regresses.
func recordSessionEventStreamEmittedByType(eventType string) {
	sessionEventStreamEmittedByTypeTotal.WithLabelValues(sessionEventTypeLabel(eventType)).Inc()
}

func sessionEventTypeLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "user_message.created",
		"turn.submitted",
		"turn.started",
		"turn.completed",
		"turn.failed",
		"turn.command_failed",
		"turn.interrupt_requested",
		"turn.interrupted",
		"session.status",
		"item.started",
		"item.completed",
		"item.failed",
		"tool.approval_requested",
		"tool.approval_resolved":
		return raw
	default:
		return "other"
	}
}

func sessionEventSourceLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "tank", "claude", "codex", "hermes":
		return raw
	default:
		return "unknown"
	}
}

func hermesTerminalLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "completed", "failed", "interrupted", "command_failed", "lost":
		return strings.TrimSpace(raw)
	default:
		return "other"
	}
}

func hermesRunEventTypeLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "response.created",
		"run.created",
		"run.started",
		"response.output_text.delta",
		"message.delta",
		"response.output_item.added",
		"response.output_item.done",
		"response.completed",
		"run.completed",
		"response.failed",
		"response.error",
		"run.failed",
		"response.cancelled",
		"run.cancelled",
		"hermes.tool.progress":
		return strings.TrimSpace(raw)
	default:
		return "other"
	}
}

func hermesCapabilityResultLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "ok", "error", "missing_required":
		return strings.TrimSpace(raw)
	default:
		return "other"
	}
}

func hermesTranslatorErrorLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "decode", "unhandled_type":
		return strings.TrimSpace(raw)
	default:
		return "other"
	}
}

// promNATSConnectionMetrics satisfies sessionbus.ConnectionMetrics so the
// bus can record connection lifecycle events without importing prometheus.
type promNATSConnectionMetrics struct{}

func (promNATSConnectionMetrics) RecordDisconnect() { natsDisconnectTotal.Inc() }
func (promNATSConnectionMetrics) RecordReconnect()  { natsReconnectTotal.Inc() }
func (promNATSConnectionMetrics) RecordAsyncError() { natsAsyncErrorTotal.Inc() }

// promPGMetrics satisfies pgstore.SQLMetrics so the QueryTracer can
// record per-operation counters and duration without importing
// prometheus from the pgstore package.
type promPGMetrics struct{}

func (promPGMetrics) RecordQuery(operation, outcome string, duration time.Duration) {
	pgQueriesTotal.WithLabelValues(operation, outcome).Inc()
	pgQueryDurationSeconds.WithLabelValues(operation).Observe(duration.Seconds())
}

// Compile-time interface conformance checks. If a future refactor
// renames a method on the sessionbus / pgstore / sessioncontroller
// interfaces, this won't silently fall back to "no metrics emitted" —
// it will fail to build.
var (
	_ sessionbus.PersisterMetrics               = promPersisterMetrics{}
	_ sessionbus.WakeMetrics                    = promWakeMetrics{}
	_ sessionbus.ConnectionMetrics              = promNATSConnectionMetrics{}
	_ pgstore.SQLMetrics                        = promPGMetrics{}
	_ pgstats.Metrics                           = promPGStatsMetrics{}
	_ sessioncontroller.K8sWatchMetrics         = promK8sWatchMetrics{}
	_ sessioncontroller.RowWriterMetrics        = promRowWriterMetrics{}
	_ sessioncontroller.LifecycleEmitterMetrics = promLifecycleEmitterMetrics{}
)

func recordSessionEventLag(event map[string]any) {
	eventTime := eventTimestamp(event)
	if eventTime.IsZero() {
		return
	}
	lag := time.Since(eventTime)
	if lag < 0 {
		lag = 0
	}
	sessionEventStreamLagSeconds.Observe(lag.Seconds())
}

func eventTimestamp(event map[string]any) time.Time {
	for _, field := range []string{"written_at", "created_at"} {
		raw, _ := event[field].(string)
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}
