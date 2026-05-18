package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
)

// fakeLifecycleStore is the test stand-in for lifecycleevents.Store on
// the handler side. Returns canned pages from in-memory storage; cursor
// behavior matches the Postgres semantics enough to exercise the
// HasOrderKey -> resync_required path.
//
// Storage is keyed by (owner, scope) so the scope-isolation tests can
// seed two scopes for the same owner and assert ListByOwner /
// HasOrderKey never cross the boundary.
type fakeLifecycleStore struct {
	mu     sync.Mutex
	rows   map[string][]lifecycleevents.Event // keyed by owner|scope
	ledger map[string]struct{}                // set of "owner|scope|order_key" present in the ledger
}

func newFakeLifecycleStore() *fakeLifecycleStore {
	return &fakeLifecycleStore{
		rows:   map[string][]lifecycleevents.Event{},
		ledger: map[string]struct{}{},
	}
}

func (f *fakeLifecycleStore) seed(owner string, events ...lifecycleevents.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range events {
		key := owner + "|" + e.SessionScope
		f.rows[key] = append(f.rows[key], e)
		f.ledger[owner+"|"+e.SessionScope+"|"+e.OrderKey] = struct{}{}
	}
}

func (f *fakeLifecycleStore) Append(_ context.Context, _ lifecycleevents.Event) (lifecycleevents.Event, bool, error) {
	panic("unused in handler tests")
}

func (f *fakeLifecycleStore) ListByOwner(_ context.Context, owner, scope string, cursor lifecycleevents.Cursor, limit int) (lifecycleevents.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.rows[owner+"|"+scope]
	out := make([]lifecycleevents.Event, 0, len(src))
	for _, e := range src {
		if cursor.AfterOrderKey != "" && e.OrderKey <= cursor.AfterOrderKey {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	hasMore := false
	// HasMore is whether there's a further event past the page; the
	// caller capped at `limit`, so peek into src for a tail row.
	if len(src) > len(out)+cursorPosition(cursor, src) {
		hasMore = true
	}
	next := ""
	if len(out) > 0 {
		next = out[len(out)-1].OrderKey
	}
	return lifecycleevents.Page{Events: out, NextOrderKey: next, HasMore: hasMore}, nil
}

func cursorPosition(cursor lifecycleevents.Cursor, src []lifecycleevents.Event) int {
	if cursor.AfterOrderKey == "" {
		return 0
	}
	for i, e := range src {
		if e.OrderKey == cursor.AfterOrderKey {
			return i + 1
		}
	}
	return 0
}

func (f *fakeLifecycleStore) HasOrderKey(_ context.Context, owner, scope, orderKey string) (bool, error) {
	if strings.TrimSpace(orderKey) == "" {
		return true, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ledger[owner+"|"+scope+"|"+orderKey]
	return ok, nil
}

func (f *fakeLifecycleStore) LatestActivity(_ context.Context, _, _ string) (*lifecycleevents.ActivitySummary, error) {
	return nil, nil
}

func (f *fakeLifecycleStore) LatestPodStatus(_ context.Context, _, _ string) (*lifecycleevents.PodStatusSummary, error) {
	return nil, nil
}

// TestSessionListTimelineReturnsCursorPaginatedRows confirms the REST
// snapshot endpoint emits the same wire shape the SSE catch-up loop
// emits, with order_key + next_order_key + has_more set correctly.
// This is the SPA's resync recovery surface.
func TestSessionListTimelineReturnsCursorPaginatedRows(t *testing.T) {
	store := newFakeLifecycleStore()
	store.seed("u@example.com",
		lifecycleevents.Event{
			OrderKey: "1", Email: "u@example.com", SessionScope: "default",
			SessionID: "21", Type: lifecycleevents.EventTypeCreated, EventID: "created",
			Payload: map[string]any{"mode": "claude_gui"},
		},
		lifecycleevents.Event{
			OrderKey: "2", Email: "u@example.com", SessionScope: "default",
			SessionID: "21", Type: lifecycleevents.EventTypePodReady, EventID: "pod_ready:uid:0",
			Payload: map[string]any{"status": "Active"},
		},
	)
	srv := newTestAppServer(t, store)
	req := authedListTimelineRequest(t, "")
	resp := httptest.NewRecorder()

	srv.handleSessionListTimeline(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Events       []map[string]any `json:"events"`
		NextOrderKey string           `json:"next_order_key"`
		HasMore      bool             `json:"has_more"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(body.Events))
	}
	if body.Events[0]["type"] != lifecycleevents.EventTypeCreated {
		t.Fatalf("first event type = %v, want session.created", body.Events[0]["type"])
	}
	if body.NextOrderKey != "2" {
		t.Fatalf("next_order_key = %q, want 2", body.NextOrderKey)
	}
	if body.HasMore {
		t.Fatalf("has_more = true, want false (only 2 rows total)")
	}
}

// TestSessionListTimelineRejectsUnknownCursor mirrors the SSE
// resync_required path on the REST side: the SPA's recovery fetch must
// 409 on an out-of-range cursor instead of silently returning an empty
// page from the wrong starting point.
func TestSessionListTimelineRejectsUnknownCursor(t *testing.T) {
	store := newFakeLifecycleStore()
	srv := newTestAppServer(t, store)

	req := authedListTimelineRequest(t, "999")
	resp := httptest.NewRecorder()
	srv.handleSessionListTimeline(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (unknown cursor must surface as conflict)", resp.Code)
	}
}

// TestSessionListTimelineDoesNotCrossScopes is the regression gate for
// the bug class that drove tank-operator#83's follow-up: prod and slot
// orchestrators share one Postgres + NATS broker, the writes were
// correctly scoped on session_scope, but the read paths joined only on
// email. Symptom: a slot's session.created event rendered in prod's
// sidebar; a slot's session.deleted purged a prod row of the same id.
//
// Seed two scopes with overlapping session_ids, fetch the timeline as
// the prod orchestrator (sessionScope="default"), and assert the slot's
// rows are unreachable on the read path.
func TestSessionListTimelineDoesNotCrossScopes(t *testing.T) {
	store := newFakeLifecycleStore()
	store.seed("u@example.com",
		lifecycleevents.Event{
			OrderKey: "1", Email: "u@example.com", SessionScope: "default",
			SessionID: "8", Type: lifecycleevents.EventTypeCreated, EventID: "default-created",
			Payload: map[string]any{"mode": "claude_gui"},
		},
		lifecycleevents.Event{
			OrderKey: "2", Email: "u@example.com", SessionScope: "tank-operator-slot-0",
			SessionID: "8", Type: lifecycleevents.EventTypeCreated, EventID: "slot-created",
			Payload: map[string]any{"mode": "claude_gui"},
		},
	)
	srv := newTestAppServer(t, store)
	req := authedListTimelineRequest(t, "")
	resp := httptest.NewRecorder()
	srv.handleSessionListTimeline(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events = %d, want 1 (only the default-scope row should be visible to a default-scope orchestrator)", len(body.Events))
	}
	if got := body.Events[0]["event_id"]; got != "default-created" {
		t.Fatalf("event_id = %v, want default-created (slot-created leaked through)", got)
	}
	if got := body.Events[0]["session_scope"]; got != "default" {
		t.Fatalf("session_scope = %v, want default", got)
	}
}

// TestSessionListTimelineRejectsCursorFromOtherScope locks in the
// cursor-resync gate. A cursor from a different scope must be treated
// as unknown — same shape as "cursor predates the ledger" — so the SPA
// resyncs from scratch instead of silently returning an empty page or
// crossing the partition. The browser's auto-reconnect path uses this
// to recover after the orchestrator scope changed under a long-lived
// session.
func TestSessionListTimelineRejectsCursorFromOtherScope(t *testing.T) {
	store := newFakeLifecycleStore()
	store.seed("u@example.com",
		lifecycleevents.Event{
			OrderKey: "42", Email: "u@example.com", SessionScope: "tank-operator-slot-0",
			SessionID: "8", Type: lifecycleevents.EventTypeCreated, EventID: "slot-created",
		},
	)
	srv := newTestAppServer(t, store)

	// Cursor exists in the ledger but only under "tank-operator-slot-0";
	// the default-scope orchestrator must treat it as unknown.
	req := authedListTimelineRequest(t, "42")
	resp := httptest.NewRecorder()
	srv.handleSessionListTimeline(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (cursor in another scope must trigger resync)", resp.Code)
	}
}

// TestSessionListPayloadDropsCrossScope confirms the subscriber-side
// defensive guard: even if a publisher regression lands a wrong-scope
// payload on a same-email subject, the SSE handler must drop it before
// emitting to the client. The (email, scope) subject shape makes this
// physically unreachable in steady state — this test guards against
// future producer bugs that would re-introduce the silent state
// mutation.
func TestSessionListPayloadDropsCrossScope(t *testing.T) {
	srv := newTestAppServer(t, newFakeLifecycleStore())
	cursor := &lifecycleevents.Cursor{}

	// Payload from a different scope arriving on the prod subscriber.
	wrongScope := lifecycleevents.Event{
		OrderKey: "9", Email: "u@example.com", SessionScope: "tank-operator-slot-0",
		SessionID: "8", Type: lifecycleevents.EventTypeDeleted, EventID: "wrong",
	}
	payload, err := json.Marshal(wrongScope)
	if err != nil {
		t.Fatal(err)
	}

	resp := httptest.NewRecorder()
	srv.emitSessionListPayload(resp, cursor, payload)

	if resp.Body.Len() != 0 {
		t.Fatalf("emit wrote %d bytes, want 0 (cross-scope payload must drop): %q", resp.Body.Len(), resp.Body.String())
	}
	if cursor.AfterOrderKey != "" {
		t.Fatalf("cursor advanced to %q, want \"\" (dropped payload must not move the cursor)", cursor.AfterOrderKey)
	}
}

// --- helpers --------------------------------------------------------------

func newTestAppServer(t *testing.T, lifecycle lifecycleevents.Store) *appServer {
	t.Helper()
	return &appServer{
		verifier:        auth.NewVerifier(testJWT(t)),
		lifecycleEvents: lifecycle,
		sessionScope:    "default",
	}
}

func authedListTimelineRequest(t *testing.T, afterOrderKey string) *http.Request {
	t.Helper()
	url := "/api/sessions/timeline"
	if afterOrderKey != "" {
		url += "?after_order_key=" + afterOrderKey
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "u@example.com"))
	return req
}
