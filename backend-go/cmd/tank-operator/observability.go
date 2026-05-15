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
)

func recordSessionEventStreamError() {
	sessionEventStreamErrorTotal.Add(1)
}

func recordSessionEventTimelineFailure() {
	sessionEventTimelineFailureTotal.Add(1)
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
