// Client-side telemetry beacon. The SPA POSTs here when its
// session-list reducer hits a code path that should be cold in
// production (currently: placeholder synthesis on an unknown session
// id). The endpoint translates the allowlisted name into a Prometheus
// counter increment so an operator can spot a regression of the
// resurrection paths without needing the user to share devtools
// captures.
//
// Design choices:
//   - Auth required (existing JWT/cookie path). No anonymous push.
//   - Strict name allowlist: every accepted value maps to a fixed
//     pre-registered counter. Unknown names get 400 — never a dynamic
//     counter, since unbounded names would explode Prometheus cardinality.
//   - 256-byte body cap. The wire shape is intentionally tiny.
//   - No per-caller label on the counter. The slog line below carries
//     the email for forensic correlation if the rate ever goes
//     non-zero; the metric itself stays low-cardinality.
//   - Response is 204; the SPA does not consume the body and the
//     beacon is best-effort.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const clientMetricMaxBody = 256

func (s *appServer) handleClientMetric(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, clientMetricMaxBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid client metric payload")
		return
	}
	name := strings.TrimSpace(body.Name)
	switch name {
	case "session_list.placeholder_synthesized":
		sessionListClientPlaceholderSynthesizedTotal.Inc()
		slog.Info("client metric",
			"name", name,
			"caller", user.Email,
		)
	default:
		// Strict allowlist — unknown names would let the SPA register
		// arbitrary metric families and is the cardinality risk this
		// endpoint exists to prevent. The 400 surfaces the typo to the
		// client developer.
		writeError(w, http.StatusBadRequest, "unknown client metric name")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
