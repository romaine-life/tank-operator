package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

type fakeSessionTurnStore struct {
	byNumber map[int64]store.TurnNumberResolution
	byTurnID map[string]int64
	err      error
}

func (f fakeSessionTurnStore) ResolveTurnNumber(_ context.Context, _ string, number int64) (store.TurnNumberResolution, bool, error) {
	if f.err != nil {
		return store.TurnNumberResolution{}, false, f.err
	}
	res, ok := f.byNumber[number]
	return res, ok, nil
}

func (f fakeSessionTurnStore) TurnNumberForTurnID(_ context.Context, _ string, turnID string) (int64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	n, ok := f.byTurnID[turnID]
	return n, ok, nil
}

func (f fakeSessionTurnStore) TurnNumbersForSession(_ context.Context, _ string) (map[string]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byTurnID, nil
}

// TestRegisterRoutesNoConflict guards against a ServeMux pattern collision when
// the durable turn-number route was added. http.ServeMux panics at
// registration on overlapping patterns, so a clean registration proves
// GET /turns/{number} does not collide with /turns/{turn_id}/activity etc.
func TestRegisterRoutesNoConflict(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registerRoutes panicked (route conflict?): %v", r)
		}
	}()
	(&appServer{}).registerRoutes(http.NewServeMux())
}

func TestHandleResolveSessionTurnNumberResolvesDurableNumber(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	app.transcriptRows = &fakeSessionTranscriptRowStore{needsBackfill: false}
	app.turns = fakeSessionTurnStore{
		byNumber: map[int64]store.TurnNumberResolution{
			3: {TurnID: "turn_abc", TurnNumber: 3, RowCursor: "Y3Vyc29y"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/3", nil)
	req.SetPathValue("session_id", "63")
	req.SetPathValue("number", "3")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleResolveSessionTurnNumber(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", res.Code, res.Body.String())
	}
	var body struct {
		SessionID  string `json:"session_id"`
		TurnID     string `json:"turn_id"`
		TurnNumber int64  `json:"turn_number"`
		RowCursor  string `json:"row_cursor"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TurnID != "turn_abc" || body.TurnNumber != 3 || body.SessionID != "63" || body.RowCursor != "Y3Vyc29y" {
		t.Fatalf("resolution body = %#v", body)
	}
}

func TestHandleResolveSessionTurnNumberUnknownReturns404(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	app.transcriptRows = &fakeSessionTranscriptRowStore{needsBackfill: false}
	app.turns = fakeSessionTurnStore{byNumber: map[int64]store.TurnNumberResolution{}}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/99", nil)
	req.SetPathValue("session_id", "63")
	req.SetPathValue("number", "99")
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	res := httptest.NewRecorder()

	app.handleResolveSessionTurnNumber(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404", res.Code, res.Body.String())
	}
}

func TestHandleResolveSessionTurnNumberRejectsNonNumeric(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"

	for _, bad := range []string{"turn_abc", "0", "-1", "1.5"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/63/turns/"+bad, nil)
		req.SetPathValue("session_id", "63")
		req.SetPathValue("number", bad)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		res := httptest.NewRecorder()

		app.handleResolveSessionTurnNumber(res, req)

		if res.Code != http.StatusBadRequest {
			t.Fatalf("number %q: status = %d body = %s, want 400", bad, res.Code, res.Body.String())
		}
	}
}
