// Client-side ingest for browser long-task entries. Pairs with
// frontend/src/longTaskTelemetry.ts; the SPA ships raw durations and
// correlation deltas (since last tank-event / session-switch / scroll)
// and this handler buckets each entry into a single bounded
// `correlation` label plus the duration histogram, so the client
// cannot blow the active-series budget regardless of what it sends.
//
// The probe exists because the SPA user can't reach for devtools'
// Performance panel (memory/feedback_no_devtools_build_surfaces_instead),
// and click-unresponsiveness at low memory + low CPU is the classic
// shape of bursty main-thread blocking that this counter is built to
// surface.
package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
)

const (
	longTaskMetricsMaxBodyBytes = 64 * 1024
	longTaskMetricsMaxEvents    = 40
	// Lower bound mirrors the client-side filter. A value below 50 ms
	// is not a longtask per the spec and indicates either a future
	// PerformanceObserver entry type slipping through or a misbehaving
	// caller; bucket it to keep the metric honest.
	longTaskMinDurationMs = 50
	// Above this, attribution is "stale" — the long task happened long
	// enough after the correlation signal that calling it caused-by is
	// noise. Mirrors a reasonable upper bound on "render fallout from
	// the user action that triggered it." Server-side check; the client
	// passes the raw delta and lets the server decide.
	longTaskCorrelationWindowMs = 1500
)

type longTaskMetricsRequest struct {
	Events []longTaskMetricEvent `json:"events"`
}

type longTaskMetricEvent struct {
	DurationMs           *float64 `json:"durationMs"`
	StartMs              *float64 `json:"startMs,omitempty"`
	SessionMode          string   `json:"sessionMode"`
	SinceTankEventMs     *float64 `json:"sinceTankEventMs,omitempty"`
	SinceSessionSwitchMs *float64 `json:"sinceSessionSwitchMs,omitempty"`
	SinceScrollMs        *float64 `json:"sinceScrollMs,omitempty"`
	Attribution          string   `json:"attribution,omitempty"`
	PagePath             string   `json:"pagePath,omitempty"`
}

func (s *appServer) handleLongTaskMetrics(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !user.IsHuman() {
		recordLongTaskClientReport("denied_role")
		writeError(w, http.StatusForbidden, "human user required")
		return
	}

	var body longTaskMetricsRequest
	limited := http.MaxBytesReader(w, r.Body, longTaskMetricsMaxBodyBytes)
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&body); err != nil {
		recordLongTaskClientReport("invalid_json")
		writeError(w, http.StatusBadRequest, "invalid long-task metrics payload")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		recordLongTaskClientReport("invalid_json")
		writeError(w, http.StatusBadRequest, "invalid long-task metrics payload")
		return
	}
	if len(body.Events) > longTaskMetricsMaxEvents {
		recordLongTaskClientReport("too_many_events")
		writeError(w, http.StatusBadRequest, "too many long-task metric events")
		return
	}
	for _, event := range body.Events {
		if !validLongTaskMetricNumbers(event) {
			recordLongTaskClientReport("invalid_value")
			writeError(w, http.StatusBadRequest, "invalid long-task metric value")
			return
		}
	}
	for _, event := range body.Events {
		recordLongTaskClientEvent(event)
	}
	recordLongTaskClientReport("ok")
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": len(body.Events)})
}

func validLongTaskMetricNumbers(event longTaskMetricEvent) bool {
	for _, value := range []*float64{
		event.DurationMs,
		event.StartMs,
		event.SinceTankEventMs,
		event.SinceSessionSwitchMs,
		event.SinceScrollMs,
	} {
		if value == nil {
			continue
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			return false
		}
		if *value < 0 {
			return false
		}
	}
	if event.DurationMs == nil {
		return false
	}
	return true
}

// longTaskCorrelationLabel buckets each entry into a single attribution
// by picking the most-recent correlation signal that fired inside the
// 1.5s window. The order — tank-event > session-switch > scroll — matches
// the expected diagnostic priority: an event burst that lands during a
// fresh session switch is most usefully labeled as the burst, since the
// switch is a known one-time cost.
func longTaskCorrelationLabel(event longTaskMetricEvent) string {
	type candidate struct {
		label string
		since *float64
	}
	candidates := []candidate{
		{"event_burst", event.SinceTankEventMs},
		{"session_switch", event.SinceSessionSwitchMs},
		{"scroll", event.SinceScrollMs},
	}
	bestLabel := "idle"
	bestSince := math.Inf(1)
	for _, c := range candidates {
		if c.since == nil {
			continue
		}
		if *c.since < 0 || *c.since > longTaskCorrelationWindowMs {
			continue
		}
		if *c.since < bestSince {
			bestSince = *c.since
			bestLabel = c.label
		}
	}
	return bestLabel
}

func longTaskAttributionLabel(raw string) string {
	switch strings.TrimSpace(raw) {
	case "self":
		return "self"
	case "":
		return "unknown"
	default:
		return "other"
	}
}

// longTaskSessionModeLabel reuses the chat-scroll mode allowlist so all
// browser-ingested metrics share a single bounded mode label set. Drift
// between the two would fragment dashboards; keeping one source of
// truth means any new mode added to chat scroll automatically flows
// here too.
func longTaskSessionModeLabel(raw string) string {
	return chatScrollSessionModeLabel(raw)
}
