package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// fakeDebugSessionEventLedgerStore is a tiny in-test implementation of
// store.SessionEventStore that the ledger handler tests can drive
// directly. The real Postgres-backed store is exercised by
// session_events_test.go; here we just need to assert handler shape
// (auth, query parsing, response envelope, metric labels).
type fakeDebugSessionEventLedgerStore struct {
	lastSessionID string
	lastCursor    store.SessionEventCursor
	lastLimit     int
	page          store.SessionEventPage
	err           error
	calls         int
}

func (f *fakeDebugSessionEventLedgerStore) Upsert(context.Context, map[string]any) error {
	return nil
}

func (f *fakeDebugSessionEventLedgerStore) FindStrandedLaunchTurns(context.Context, time.Time, time.Time, int) ([]store.StrandedLaunchTurn, error) {
	return nil, nil
}

func (f *fakeDebugSessionEventLedgerStore) ListBySession(_ context.Context, sessionID string, cursor store.SessionEventCursor, limit int) (store.SessionEventPage, error) {
	f.calls++
	f.lastSessionID = sessionID
	f.lastCursor = cursor
	f.lastLimit = limit
	if f.err != nil {
		return store.SessionEventPage{}, f.err
	}
	return f.page, nil
}

func (f *fakeDebugSessionEventLedgerStore) HasOrderKey(context.Context, string, string) (bool, error) {
	return true, nil
}

func (f *fakeDebugSessionEventLedgerStore) OrderKeyForTimelineID(context.Context, string, string) (string, error) {
	return "", nil
}

func (f *fakeDebugSessionEventLedgerStore) LatestEvents(context.Context, string, int) (store.SessionEventPage, error) {
	return store.SessionEventPage{}, nil
}

func (f *fakeDebugSessionEventLedgerStore) EventsForTurnAfter(context.Context, string, string, string, int) (store.SessionEventPage, error) {
	return store.SessionEventPage{FoundNewest: true}, nil
}

func (f *fakeDebugSessionEventLedgerStore) FindTurnTerminal(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}

func (f *fakeDebugSessionEventLedgerStore) LatestLifecycleEvents(context.Context, string, int) ([]map[string]any, error) {
	return nil, nil
}

func (f *fakeDebugSessionEventLedgerStore) UnreadOutputCount(context.Context, string, string) (int, error) {
	return 0, nil
}

func newLedgerTestServer(t *testing.T) (*appServer, *fakeDebugSessionEventLedgerStore) {
	t.Helper()
	app := adminTestServer(t)
	app.sessionScope = "default"
	fake := &fakeDebugSessionEventLedgerStore{}
	app.sessionEvents = fake
	return app, fake
}

func TestDebugSessionEventLedgerNonAdmin403(t *testing.T) {
	app, _ := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
	}
}

func TestDebugSessionEventLedgerMissingSessionID400(t *testing.T) {
	app, _ := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "session_id is required") {
		t.Fatalf("error body should explain missing session_id; got %s", resp.Body.String())
	}
}

func TestDebugSessionEventLedgerInvalidLimit400(t *testing.T) {
	app, _ := newLedgerTestServer(t)
	for _, raw := range []string{"abc", "0", "-1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&limit="+raw, nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		resp := httptest.NewRecorder()

		app.handleDebugSessionEventLedger(resp, req)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("limit=%q expected 400, got %d; body=%s", raw, resp.Code, resp.Body.String())
		}
	}
}

func TestDebugSessionEventLedgerInvalidDirection400(t *testing.T) {
	app, _ := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&direction=sideways", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
}

func TestDebugSessionEventLedgerBothCursors400(t *testing.T) {
	app, _ := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&after_order_key=a&before_order_key=b", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.Code, resp.Body.String())
	}
}

func TestDebugSessionEventLedgerStubMode503(t *testing.T) {
	app := adminTestServer(t)
	app.sessionScope = "default"
	// Keep the stub sessionEvents that adminTestServer wires up; the
	// handler should refuse to pretend a stub-mode ledger is real data.
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "stub mode") {
		t.Fatalf("stub-mode response should name stub mode; got %s", resp.Body.String())
	}
}

func TestDebugSessionEventLedgerStoreError500(t *testing.T) {
	app, fake := newLedgerTestServer(t)
	fake.err = errors.New("boom")
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", resp.Code, resp.Body.String())
	}
}

func TestDebugSessionEventLedgerHappyPath200(t *testing.T) {
	app, fake := newLedgerTestServer(t)
	fake.page = store.SessionEventPage{
		Events: []map[string]any{
			{"id": "evt-1", "order_key": "01", "type": "user_message.created"},
			{"id": "evt-2", "order_key": "02", "type": "turn.submitted"},
		},
		NextOrderKey: "02",
		PrevOrderKey: "01",
		HasMore:      true,
		FoundOldest:  true,
		FoundNewest:  false,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&limit=2", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.lastSessionID != "203" {
		t.Fatalf("store called with session_id=%q, want 203", fake.lastSessionID)
	}
	if fake.lastLimit != 2 {
		t.Fatalf("store called with limit=%d, want 2", fake.lastLimit)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	for _, field := range []string{"description", "session_id", "session_scope", "storage_key", "count", "events", "has_more", "next_order_key", "prev_order_key", "found_oldest", "found_newest", "fetched_at"} {
		if _, ok := body[field]; !ok {
			t.Errorf("response missing field %q; body=%s", field, resp.Body.String())
		}
	}
	if count, ok := body["count"].(float64); !ok || int(count) != 2 {
		t.Errorf("count=%v, want 2", body["count"])
	}
	if storageKey, _ := body["storage_key"].(string); storageKey == "" {
		t.Errorf("storage_key should be set; body=%s", resp.Body.String())
	}
}

func TestDebugSessionEventLedgerEmptyResultIs200(t *testing.T) {
	app, fake := newLedgerTestServer(t)
	fake.page = store.SessionEventPage{Events: []map[string]any{}, FoundOldest: true, FoundNewest: true}

	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=999999", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if count, _ := body["count"].(float64); int(count) != 0 {
		t.Errorf("count=%v, want 0", body["count"])
	}
}

func TestDebugSessionEventLedgerClampsLimitAtMax(t *testing.T) {
	app, fake := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&limit=9999", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.lastLimit != debugSessionEventLedgerMaxLimit {
		t.Errorf("limit=%d, want clamp to %d", fake.lastLimit, debugSessionEventLedgerMaxLimit)
	}
}

func TestDebugSessionEventLedgerForwardCursor(t *testing.T) {
	app, fake := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&after_order_key=05", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.lastCursor.AfterOrderKey != "05" {
		t.Errorf("cursor.AfterOrderKey=%q, want 05", fake.lastCursor.AfterOrderKey)
	}
	if fake.lastCursor.BeforeOrderKey != "" {
		t.Errorf("cursor.BeforeOrderKey=%q, want empty", fake.lastCursor.BeforeOrderKey)
	}
}

func TestDebugSessionEventLedgerBackwardCursor(t *testing.T) {
	app, fake := newLedgerTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-ledger?session_id=203&before_order_key=20&direction=desc", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()

	app.handleDebugSessionEventLedger(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if fake.lastCursor.BeforeOrderKey != "20" {
		t.Errorf("cursor.BeforeOrderKey=%q, want 20", fake.lastCursor.BeforeOrderKey)
	}
	if fake.lastCursor.Direction != "desc" {
		t.Errorf("cursor.Direction=%q, want desc", fake.lastCursor.Direction)
	}
}
