// Admin-only debug surface for the per-session SSE event stream
// handlers. Returns the in-memory registry's snapshot — every open
// stream's wake/page/emit state — so an operator (or the AI support
// agent reading via gh + kubectl) can answer "did a wake arrive on
// the subject I expected" / "did the page read return anything" /
// "is the cursor advancing" without browser devtools.
//
// Per memory/feedback_no_devtools_build_surfaces_instead.md the
// user-trust constraint on this repo is that observability lives
// behind curl-able endpoints. This is the per-replica counterpart
// of the Prometheus counters in observability.go: the counters
// answer "is this happening at scale", this endpoint answers "what
// is happening to THIS specific stream right now."
package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionstream"
)

func (s *appServer) handleDebugSessionEventStreams(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if !hasAdminPower(user) {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}

	// Optional session_id filter — when an operator already knows
	// which session is misbehaving, filtering at the endpoint cuts
	// noise on busy replicas. Empty filter returns every open stream.
	sessionFilter := strings.TrimSpace(r.URL.Query().Get("session_id"))

	now := time.Now()
	all := s.streamRegistry.Snapshot(now)
	filtered := all
	if sessionFilter != "" {
		filtered = make([]sessionstream.Snapshot, 0, len(all))
		for _, snap := range all {
			if snap.SessionID == sessionFilter {
				filtered = append(filtered, snap)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scope":       s.sessionScope,
		"replica_at":  now.UTC().Format(time.RFC3339Nano),
		"open_count":  len(all),
		"matched":     len(filtered),
		"streams":     filtered,
		"session_id":  sessionFilter,
		"description": debugSessionEventStreamsDescription,
	})
}

// debugSessionEventStreamsDescription is rendered into the JSON
// response so an operator running `curl | jq` sees the meaning of
// each field without leaving the terminal. Per docs/quality-
// timeframes.md "Observability exists for the bugs a user would
// otherwise have to guess about."
const debugSessionEventStreamsDescription = `Per-open-SSE-handler diagnostic surface for /api/sessions/{id}/events.

How to read the heartbeat-catchup signature: Prometheus
tank_session_event_stream_heartbeat_catchup_total increments and the
server logs "session event stream caught up from heartbeat" with
session_id, stream_id, storage_key, cursor_before, and cursor_after.
On this endpoint, inspect the matching stream's wakes_received,
last_wake_subject, last_emit_at, and cursor_after_order_key. If the
cursor advanced while wakes_received stayed flat, inspect the wake
subject wiring. If wakes_received advanced but heartbeat catch-up still
fired, inspect the handler notify loop and NATS callback on that
replica.

How to read the candidate-B zombie-SSE signature: last_emit_at is
seconds-fresh, then stops advancing while heartbeats_sent keeps
climbing. The client-side tank_session_event_client_*_total counters
(POST /api/client-metrics/session-events-stream) tell you whether the
browser is still receiving anything.

How to read the candidate-C reducer-drop signature: emits_total keeps
climbing on the server, but the matching client-side
tank_session_event_client_received_total{event_type} stays flat for the
same event_type. The server is emitting, the browser is filtering.`
