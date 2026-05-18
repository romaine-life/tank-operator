package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

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
	// anchor shape the SPA chose. The `legacy_forward` label exists so a
	// Prometheus alert can fire if the pre-anchor forward-walk path
	// reappears after the Stage 2 cutover documented in
	// docs/quality-timeframes.md ("migration guards prevent old paths
	// from returning"). Expected steady-state values once Stage 2 lands:
	// `newest`, `first_unread`, `around`, `before` non-zero;
	// `legacy_forward` always zero.
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

// --- Admin cross-user read metrics ---
//
// Counts every time a role=admin caller reads a session whose Owner !=
// their email, or lists sessions for a different owner via
// `?owner=<email>`. Single-counter shape (no labels) keeps cardinality
// at 2 series total — the operational signal is "is this happening at
// all" plus per-rate alerting if it ever spikes outside expected admin
// activity windows. The reads themselves are intentionally not
// per-target-email so audit-trail concerns belong in the request log,
// not the metrics surface.

var (
	adminCrossUserReadsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_admin_cross_user_session_reads_total",
		Help: "Times an admin-role caller read a session belonging to another user (per-session read endpoints).",
	})
	adminCrossUserListsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_admin_cross_user_session_lists_total",
		Help: "Times an admin-role caller passed `?owner=<email>` to list sessions or session-list events for another user.",
	})
)

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
	// to the current lifecycle-ledger tip instead of replaying from
	// order_key=0. New clients pass the snapshot tip via
	// Tank-Lifecycle-Tip-Order-Key, so a high rate here indicates
	// legacy clients (or a client whose snapshot pre-dated the
	// lifecycle ledger having any rows) — informational, not an alert
	// signal.
	sessionListStreamColdOpenFastForwardTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_stream_cold_open_fast_forward_total",
		Help: "Sidebar SSE opens that jumped an empty cursor to the current tip instead of replaying history.",
	})
	// sessionListClientPlaceholderSynthesizedTotal is pushed by the
	// SPA via POST /api/debug/client-metric whenever the reducer's
	// applyPodStatusEvent branch synthesizes a Session row for an
	// unknown session id. Pre-tank-operator#525 that branch fired on
	// every cold-open replay for deleted sessions because the SSE
	// catch-up walked history from order_key=0; post-fix the cold-
	// open fast-forward + the Reader.List visible-filter mean the
	// branch should only fire in the narrow live-event race window
	// (pod_ready arriving before session.created in NATS delivery).
	// A non-zero rate in steady state is the regression-detection
	// signal that a resurrection path snuck back in.
	sessionListClientPlaceholderSynthesizedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_list_client_placeholder_synthesized_total",
		Help: "SPA-reported placeholder synthesis in the session-list reducer; expected ~0 in steady state post-#525.",
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

// --- Pod-informer producer metrics. ---

var (
	// sessionLifecycleEventWritesTotal counts every durable ledger write
	// (whether by Manager, the persister-driven ChatActivityEmitter, or the
	// pod-informer leader). The type label is bounded by the small fixed
	// set of EventType constants — cardinality budget is fine.
	sessionLifecycleEventWritesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_lifecycle_event_writes_total",
		Help: "Durable session_lifecycle_events rows written, labeled by event type.",
	}, []string{"type"})
	sessionPodInformerLagSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "tank_session_pod_informer_lag_seconds",
		Help:    "End-to-end lag between an observed pod transition and the lifecycle ledger row insert.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	})
	// sessionPodInformerLeaderHeld is the single-writer health signal.
	// 1 on the replica currently holding the lease, 0 elsewhere. If the
	// SUM across replicas is 0 for an extended window the alert fires.
	sessionPodInformerLeaderHeld = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tank_session_pod_informer_leader_held",
		Help: "1 when this replica currently holds the pod-informer Lease; 0 otherwise.",
	})
	// sessionLifecycleActivityEmitTotal: emitted-or-skipped breakdown of
	// the persister-driven session.activity_changed deltas. The "skipped"
	// counter dominates in steady state (most chat events are turn-status
	// indicator no-ops); a sudden surge in "emitted" without a matching
	// pattern in user activity is a regression signal.
	sessionLifecycleActivityEmitTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_lifecycle_activity_emit_total",
		Help: "Persister activity-summary delta decisions, labeled by outcome (emitted/skipped).",
	}, []string{"outcome"})
	sessionLifecycleActivityFailureTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tank_session_lifecycle_activity_failure_total",
		Help: "Persister activity-summary delta derivation failures (store/publish errors).",
	})
	// sessionRowUpdatesTotal counts sessioncontroller.RowWriter's
	// per-event dual-write outcomes on the sessions row. Outcome=ok
	// dominates in steady state; outcome=failed > 0 means the row has
	// drifted from the lifecycle ledger and Phase 2's snapshot would
	// return stale state. Alerts in
	// k8s/templates/observability.yaml.
	sessionRowUpdatesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tank_session_row_updates_total",
		Help: "sessions row column updates from RowWriter, labeled by lifecycle event type and outcome.",
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

func (promK8sWatchMetrics) RecordTransition(eventType string) {
	sessionLifecycleEventWritesTotal.WithLabelValues(eventType).Inc()
}

func (promK8sWatchMetrics) RecordLag(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	sessionPodInformerLagSeconds.Observe(seconds)
}

func (promK8sWatchMetrics) RecordLeaderStatus(isLeader bool) {
	if isLeader {
		sessionPodInformerLeaderHeld.Set(1)
		return
	}
	sessionPodInformerLeaderHeld.Set(0)
}

// promRowWriterMetrics satisfies sessioncontroller.RowWriterMetrics —
// the dual-write contract's per-event observability surface. Phase 2's
// snapshot cutover depends on row-update failure rate being zero;
// non-zero here means the row drifted from the lifecycle ledger.
type promRowWriterMetrics struct{}

func (promRowWriterMetrics) RecordRowUpdate(eventType string) {
	sessionRowUpdatesTotal.WithLabelValues(eventType, "ok").Inc()
}

func (promRowWriterMetrics) RecordRowUpdateFailure(eventType string) {
	sessionRowUpdatesTotal.WithLabelValues(eventType, "failed").Inc()
}

// promLifecycleEmitterMetrics satisfies sessioncontroller.LifecycleEmitterMetrics for the
// chat→activity_changed bridge. Emitted=true increments
// {outcome="emitted"}, false → {outcome="skipped"}.
type promLifecycleEmitterMetrics struct{}

func (promLifecycleEmitterMetrics) RecordActivityDelta(emitted bool) {
	outcome := "skipped"
	if emitted {
		outcome = "emitted"
		sessionLifecycleEventWritesTotal.WithLabelValues("session.activity_changed").Inc()
	}
	sessionLifecycleActivityEmitTotal.WithLabelValues(outcome).Inc()
}

func (promLifecycleEmitterMetrics) RecordActivityFailure() {
	sessionLifecycleActivityFailureTotal.Inc()
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
)

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

// promWakeMetrics satisfies sessionbus.WakeMetrics so the bus can increment
// wake/event-publish failure counters without importing prometheus directly.
type promWakeMetrics struct{}

func (promWakeMetrics) RecordSessionEventWakePublishFailed() {
	sessionEventWakePublishFailureTotal.Inc()
}

func (promWakeMetrics) RecordSessionListEventPublishFailed() {
	sessionListEventPublishFailureTotal.Inc()
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
	_ sessionbus.PersisterMetrics              = promPersisterMetrics{}
	_ sessionbus.WakeMetrics                   = promWakeMetrics{}
	_ sessionbus.ConnectionMetrics             = promNATSConnectionMetrics{}
	_ pgstore.SQLMetrics                       = promPGMetrics{}
	_ sessioncontroller.K8sWatchMetrics        = promK8sWatchMetrics{}
	_ sessioncontroller.RowWriterMetrics       = promRowWriterMetrics{}
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
