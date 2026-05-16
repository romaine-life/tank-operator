package main

import (
	"expvar"
	"time"
)

var (
	sessionEventStreamOpenTotal       = expvar.NewInt("tank_session_event_stream_open_total")
	sessionEventStreamReconnectTotal  = expvar.NewInt("tank_session_event_stream_reconnect_total")
	sessionEventStreamResyncTotal     = expvar.NewInt("tank_session_event_stream_resync_required_total")
	sessionEventStreamErrorTotal      = expvar.NewInt("tank_session_event_stream_error_total")
	sessionEventTimelineFailureTotal  = expvar.NewInt("tank_session_event_timeline_failure_total")
	sessionEventStreamLastLagMillis   = expvar.NewInt("tank_session_event_stream_last_lag_ms")
	sessionEventStreamMaxLagMillis    = expvar.NewInt("tank_session_event_stream_max_lag_ms")
	sessionEventStreamEmittedTotal    = expvar.NewInt("tank_session_event_stream_emitted_total")
	sessionEventStreamHeartbeatTotal  = expvar.NewInt("tank_session_event_stream_heartbeat_total")
	sessionEventWakeSubscribeFailures = expvar.NewInt("tank_session_event_wake_subscribe_failure_total")

	// Persister: schema rejections are permanent — anything malformed will
	// fail the same way on retry. The persister terminates these and
	// increments this counter. Steady-state expectation is zero; any
	// non-zero value means a producer is emitting non-Tank events to the
	// bus and the cutover guard in conversation-contract.yml missed it.
	sessionEventPersistSchemaRejectedTotal = expvar.NewInt("tank_session_event_persist_schema_rejected_total")
	// Transient persister failures (Postgres connection blips, network
	// hiccups, etc.). These are retried up to the JetStream MaxDeliver
	// bound; the counter helps distinguish them from schema rejections
	// so alerts on the rejection counter aren't masked by infrastructure
	// noise.
	sessionEventPersistTransientFailureTotal = expvar.NewInt("tank_session_event_persist_transient_failure_total")

	// Wake-publish failures. The bus records here before returning the
	// error to the caller, which keeps the silent
	// `_ = m.waker.PublishSessionListWake(...)` pattern in Manager
	// mutations visible. Steady-state expectation: zero on both. A non-
	// zero value means NATS is unreachable / overloaded, in which case
	// SSE clients won't notice changes until the next heartbeat (15s for
	// per-session events, 30s for the per-owner session list).
	sessionEventWakePublishFailureTotal = expvar.NewInt("tank_session_event_wake_publish_failure_total")
	sessionListWakePublishFailureTotal  = expvar.NewInt("tank_session_list_wake_publish_failure_total")
)

func recordSessionEventStreamError() {
	sessionEventStreamErrorTotal.Add(1)
}

func recordSessionEventTimelineFailure() {
	sessionEventTimelineFailureTotal.Add(1)
}

func recordSessionEventPersistSchemaRejected() {
	sessionEventPersistSchemaRejectedTotal.Add(1)
}

func recordSessionEventPersistTransientFailure() {
	sessionEventPersistTransientFailureTotal.Add(1)
}

// expvarPersisterMetrics wires the persister's PersisterMetrics interface
// (defined in internal/sessionbus) to the package-level expvar counters
// above. The interface stays in the internal package so the persister
// doesn't need to import expvar directly.
type expvarPersisterMetrics struct{}

func (expvarPersisterMetrics) RecordSchemaRejected() {
	recordSessionEventPersistSchemaRejected()
}

func (expvarPersisterMetrics) RecordTransientFailure() {
	recordSessionEventPersistTransientFailure()
}

// expvarWakeMetrics wires sessionbus.WakeMetrics into the package-level
// expvar counters above so the bus can record wake-publish failures
// without importing expvar directly.
type expvarWakeMetrics struct{}

func (expvarWakeMetrics) RecordSessionEventWakePublishFailed() {
	sessionEventWakePublishFailureTotal.Add(1)
}

func (expvarWakeMetrics) RecordSessionListWakePublishFailed() {
	sessionListWakePublishFailureTotal.Add(1)
}

func recordSessionEventLag(event map[string]any) {
	eventTime := eventTimestamp(event)
	if eventTime.IsZero() {
		return
	}
	lag := time.Since(eventTime)
	if lag < 0 {
		lag = 0
	}
	lagMillis := lag.Milliseconds()
	sessionEventStreamLastLagMillis.Set(lagMillis)
	if lagMillis > sessionEventStreamMaxLagMillis.Value() {
		sessionEventStreamMaxLagMillis.Set(lagMillis)
	}
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
