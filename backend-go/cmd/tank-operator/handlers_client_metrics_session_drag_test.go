package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// The drag-step beacon records the lifecycle on tank_session_drag_step_total and
// bounds label cardinality: a known step/detail records as-is; anything off the
// allowlist collapses to "other" rather than failing the request or exploding
// the series. This is the observability behind diagnosing "the drag does
// nothing" from metrics instead of the user's DevTools.
func TestHandleSessionDragStepMetricRecordsAndBounds(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	for _, body := range []string{
		`{"step":"drop","detail":"nest"}`,
		`{"step":"whatever","detail":"surprise"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-drag-step", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		res := httptest.NewRecorder()
		app.handleSessionDragStepMetric(res, req)
		if res.Code != http.StatusAccepted {
			t.Fatalf("status = %d body=%s, want 202", res.Code, res.Body.String())
		}
	}
	metrics := scrapePrometheus(t)
	for _, want := range []string{
		`tank_session_drag_step_total{detail="nest",step="drop"} 1`,
		`tank_session_drag_step_total{detail="other",step="other"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("scrape missing %s\n%s", want, metrics)
		}
	}
}

func TestHandleSessionDragStepMetricRejectsBadJSON(t *testing.T) {
	app := &appServer{verifier: auth.NewVerifier(testJWT(t))}
	req := httptest.NewRequest(http.MethodPost, "/api/client-metrics/session-drag-step", strings.NewReader(`not json`))
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()
	app.handleSessionDragStepMetric(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
}
