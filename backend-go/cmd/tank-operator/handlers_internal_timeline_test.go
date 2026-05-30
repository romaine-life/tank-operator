package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
)

// The internal timeline endpoint is the service-principal read path that
// backs the mcp-tank-operator read_transcript tool. It must reuse the same
// ownership gate the browser /timeline uses (a role=service caller reads
// only sessions whose owner == actor_email) and reject human callers, since
// it is a service-only route. adminTestServer seeds session "63" owned by
// otherUser with stub event/transcript/read-state stores, so the projection
// returns an empty-but-well-formed body.

func TestHandleInternalSessionTimeline_ServiceActorOwnSessionOK(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/63/timeline", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-94@service.tank.romaine.life",
		otherUser,
	))
	rec := httptest.NewRecorder()

	app.handleInternalSessionTimeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got, want := body["session_id"], "63"; got != want {
		t.Fatalf("session_id = %v, want %q", got, want)
	}
	if _, ok := body["rows"]; !ok {
		t.Fatalf("response missing rows key; body=%v", body)
	}
	if got, want := body["projection"], "server_transcript_rows_v1"; got != want {
		t.Fatalf("projection = %v, want %q", got, want)
	}
}

func TestHandleInternalSessionTimeline_RejectsHumanRole(t *testing.T) {
	app := adminTestServer(t)
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin} {
		req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/63/timeline", nil)
		req.SetPathValue("session_id", "63")
		// otherUser owns session 63, so this is denied on role, not ownership.
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, role))
		rec := httptest.NewRecorder()

		app.handleInternalSessionTimeline(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("role=%s: status=%d, want 403; body=%s", role, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleInternalSessionTimeline_ServiceActorCrossUserReturns404(t *testing.T) {
	app := adminTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/sessions/63/timeline", nil)
	req.SetPathValue("session_id", "63")
	// actor_email is an intruder, not the owner (otherUser) of session 63.
	req.Header.Set("Authorization", "Bearer "+signedServiceToken(
		t,
		"pod-94@service.tank.romaine.life",
		"intruder@example.com",
	))
	rec := httptest.NewRecorder()

	app.handleInternalSessionTimeline(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user: status=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	// Must not leak the real owner email — same 404 a missing session yields.
	if body := rec.Body.String(); strings.Contains(body, otherUser) {
		t.Fatalf("404 body leaked owner email: %s", body)
	}
}
