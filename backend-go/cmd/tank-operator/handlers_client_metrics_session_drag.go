package main

import (
	"encoding/json"
	"net/http"
)

const sessionDragStepMaxBodyBytes = 1 << 12 // 4 KiB; one small step beacon.

type sessionDragStepRequest struct {
	Step   string `json:"step"`
	Detail string `json:"detail"`
}

// handleSessionDragStepMetric ingests one browser-side sidebar-drag lifecycle
// step beacon (mousedown → dragstart → dragover → drop → persist) and records it
// on tank_session_drag_step_total. This exists so a "the drag does nothing"
// report is diagnosable from the metrics stack instead of the user's DevTools:
// the last step that increments localizes where the gesture dies. Pure
// observability — recordSessionDragStep bounds the label cardinality, so an
// unexpected step/detail collapses to "other" rather than failing the request.
func (s *appServer) handleSessionDragStepMetric(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	var body sessionDragStepRequest
	limited := http.MaxBytesReader(w, r.Body, sessionDragStepMaxBodyBytes)
	if err := json.NewDecoder(limited).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid drag-step payload")
		return
	}
	recordSessionDragStep(body.Step, body.Detail)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}
