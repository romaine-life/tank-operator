package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type turnDirectoryResponse struct {
	SessionID        string           `json:"session_id"`
	Turns            []map[string]any `json:"turns"`
	Count            int              `json:"count"`
	LatestTurnNumber int64            `json:"latest_turn_number"`
	Truncated        bool             `json:"truncated"`
	Projection       string           `json:"projection"`
}

func TestHandleSessionTurnDirectoryReturnsCompleteOrderedSet(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	rowStore := &fakeSessionTranscriptRowStore{
		needsBackfill: false,
		directory: store.TurnDirectoryPage{
			Shells: []map[string]any{
				{"kind": "turn_activity", "turnId": "turn_a", "turnNumber": float64(1)},
				{"kind": "turn_activity", "turnId": "turn_b", "turnNumber": float64(2)},
				{"kind": "turn_activity", "turnId": "turn_c", "turnNumber": float64(3)},
			},
			LatestTurnNumber: 3,
		},
	}
	app.transcriptRows = rowStore

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/directory", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleSessionTurnDirectory(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var body turnDirectoryResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != "63" {
		t.Fatalf("session_id = %q", body.SessionID)
	}
	if body.Count != 3 || len(body.Turns) != 3 {
		t.Fatalf("count = %d turns = %d, want 3", body.Count, len(body.Turns))
	}
	if body.LatestTurnNumber != 3 {
		t.Fatalf("latest_turn_number = %d, want 3", body.LatestTurnNumber)
	}
	if body.Truncated {
		t.Fatalf("truncated = true, want false")
	}
	if body.Turns[0]["turnId"] != "turn_a" || body.Turns[2]["turnId"] != "turn_c" {
		t.Fatalf("turns not in submission order: %#v", body.Turns)
	}
	if body.Projection != "server_turn_directory_v1" {
		t.Fatalf("projection = %q", body.Projection)
	}
	// The directory must read the durable ledger, not a transcript window: the
	// handler asks the store for the full set (the high cap), never a tail page.
	if rowStore.directoryMax != store.TurnDirectoryMaxRows {
		t.Fatalf("directory cap = %d, want %d", rowStore.directoryMax, store.TurnDirectoryMaxRows)
	}
}

func TestHandleSessionTurnDirectoryEmptySession(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	app.transcriptRows = &fakeSessionTranscriptRowStore{
		needsBackfill: false,
		directory:     store.TurnDirectoryPage{Shells: []map[string]any{}},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/directory", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleSessionTurnDirectory(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var body turnDirectoryResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Turns) != 0 || body.LatestTurnNumber != 0 {
		t.Fatalf("empty session body = %#v", body)
	}
}

func TestHandleSessionTurnDirectorySurfacesTruncation(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	app.transcriptRows = &fakeSessionTranscriptRowStore{
		needsBackfill: false,
		directory: store.TurnDirectoryPage{
			Shells:           []map[string]any{{"kind": "turn_activity", "turnId": "turn_z", "turnNumber": float64(9001)}},
			LatestTurnNumber: 9001,
			Truncated:        true,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/directory", nil)
	req.SetPathValue("session_id", "63")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleSessionTurnDirectory(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var body turnDirectoryResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Truncated {
		t.Fatalf("truncated = false, want true (caller must know the oldest turns were elided)")
	}
}

func TestHandleSessionTurnDirectoryRequiresAuth(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/directory", nil)
	req.SetPathValue("session_id", "63")
	res := httptest.NewRecorder()

	app.handleSessionTurnDirectory(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated directory read", res.Code)
	}
}

// TestTurnDirectoryRouteBeatsNumberResolver proves the literal /turns/directory
// route is matched in preference to /turns/{number}: Go's ServeMux routes the
// strictly-more-specific literal segment, so "directory" never reaches the
// numeric resolver (which would 400 it).
func TestTurnDirectoryRouteBeatsNumberResolver(t *testing.T) {
	mux := http.NewServeMux()
	(&appServer{}).registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/directory", nil)
	_, pattern := mux.Handler(req)
	if pattern != "GET /api/sessions/{session_id}/turns/directory" {
		t.Fatalf("matched pattern = %q, want the literal directory route", pattern)
	}
}
