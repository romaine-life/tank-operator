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
type fakeLifecycleStore struct {
	mu     sync.Mutex
	rows   map[string][]lifecycleevents.Event // keyed by owner
	ledger map[string]struct{}                // set of "owner|order_key" present in the ledger
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
	f.rows[owner] = append(f.rows[owner], events...)
	for _, e := range events {
		f.ledger[owner+"|"+e.OrderKey] = struct{}{}
	}
}

func (f *fakeLifecycleStore) Append(_ context.Context, _ lifecycleevents.Event) (lifecycleevents.Event, bool, error) {
	panic("unused in handler tests")
}

func (f *fakeLifecycleStore) ListByOwner(_ context.Context, owner string, cursor lifecycleevents.Cursor, limit int) (lifecycleevents.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.rows[owner]
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

func (f *fakeLifecycleStore) HasOrderKey(_ context.Context, owner, orderKey string) (bool, error) {
	if strings.TrimSpace(orderKey) == "" {
		return true, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.ledger[owner+"|"+orderKey]
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
