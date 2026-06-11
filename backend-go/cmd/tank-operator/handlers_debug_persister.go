// Admin-only debug surface for the session-bus event persister: the
// per-session in-process queue state behind the aggregate
// tank_session_event_persister_* gauges.
//
// This is the per-entity localizer the TankSessionEventPersisterBacklog
// runbook names: once the alert fires, this endpoint answers "which
// session's events are queued, how many, and how stale" — per-session
// detail the metric cardinality rules keep out of labels, served without
// kubectl per the observability contract. During the 2026-06-11 incident
// (tank-operator#1051) this view would have named session 815 as the
// flood source in one request.
//
// Auth: Tank admin power required. Emits a structured slog audit line per
// call.
package main

import (
	"log/slog"
	"net/http"
)

func (s *appServer) handleDebugPersister(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	if s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "session bus not wired")
		return
	}
	queues := s.sessionBus.PersisterDebugSnapshot()
	slog.Info("debug persister read",
		"email", user.Email,
		"queued_sessions", len(queues),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"description": "Session-event persister in-process queue state: messages " +
			"consumed from the bus and routed to per-session serial queues, not yet " +
			"persisted. Aggregate gauges: tank_session_event_persister_pending " +
			"(undelivered on the durable), tank_session_event_persister_queue_depth " +
			"(sum of the queues below), " +
			"tank_session_event_persister_processed_event_age_seconds (staleness of " +
			"the newest persisted event). An empty list with a high pending gauge " +
			"means delivery itself is stalled; a deep queue for one session names " +
			"the flood source.",
		"queues": queues,
	})
}
