package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// TestEmitSessionRowPayloadAdvancesCursor confirms the live-stream
// receiver decodes the row-update payload, validates the scope,
// advances the cursor, and forwards the payload verbatim to the SSE
// client. The wire shape is deliberately a pre-marshaled
// RowUpdatePayload so the catch-up and live paths share one wire
// shape and one SessionStore replace-by-id semantic.
func TestEmitSessionRowPayloadAdvancesCursor(t *testing.T) {
	srv := newTestAppServer(t)

	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:         "42",
		Email:      "u@example.com",
		Mode:       sessionmodel.ClaudeGUIMode,
		Scope:      "default",
		Visible:    true,
		Status:     "Active",
		RowVersion: 17,
	})
	if err != nil {
		t.Fatal(err)
	}

	cursor := int64(10)
	resp := httptest.NewRecorder()
	srv.emitSessionRowPayload(resp, &cursor, srv.sessionScope, payload)
	if cursor != 17 {
		t.Fatalf("cursor = %d, want 17 (the row's row_version)", cursor)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "event: session-row") {
		t.Fatalf("SSE body missing session-row event: %q", body)
	}
	if !strings.Contains(body, "id: 17") {
		t.Fatalf("SSE body missing id: 17 line: %q", body)
	}
}

// TestEmitSessionRowPayloadDropsCrossScope locks in the defensive
// subscriber-side guard: even if a producer regression lands a wrong-
// scope payload on a same-(email) subject, the SSE handler drops it
// before emitting. The (email, scope) NATS subject shape makes this
// unreachable in steady state — the test guards against future
// producer bugs that would re-introduce silent state mutation.
func TestEmitSessionRowPayloadDropsCrossScope(t *testing.T) {
	srv := newTestAppServer(t)

	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:         "42",
		Email:      "u@example.com",
		Scope:      "tank-operator-slot-0", // different from srv.sessionScope
		Visible:    true,
		Status:     "Active",
		RowVersion: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	cursor := int64(0)
	resp := httptest.NewRecorder()
	srv.emitSessionRowPayload(resp, &cursor, srv.sessionScope, payload)
	if cursor != 0 {
		t.Fatalf("cursor advanced to %d, want 0 (cross-scope payload must not move the cursor)", cursor)
	}
	if resp.Body.Len() != 0 {
		t.Fatalf("emit wrote %d bytes, want 0 (cross-scope payload must drop): %q", resp.Body.Len(), resp.Body.String())
	}
}

// TestEmitSessionRowPayloadSkipsStaleCursor confirms the deduplication
// invariant: a NATS payload whose cursor is ≤ the SSE handler's
// current cursor was already emitted during catch-up. Re-emitting
// would make the SPA's SessionStore replace the row with an older
// snapshot.
func TestEmitSessionRowPayloadSkipsStaleCursor(t *testing.T) {
	srv := newTestAppServer(t)

	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:         "42",
		Email:      "u@example.com",
		Scope:      "default",
		Visible:    true,
		RowVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	cursor := int64(10) // ahead of the payload's row_version
	resp := httptest.NewRecorder()
	srv.emitSessionRowPayload(resp, &cursor, srv.sessionScope, payload)
	if cursor != 10 {
		t.Fatalf("cursor moved to %d, want 10 (stale payload must not rewind)", cursor)
	}
	if resp.Body.Len() != 0 {
		t.Fatalf("emit wrote %d bytes, want 0: %q", resp.Body.Len(), resp.Body.String())
	}
}

// TestMarshalRowUpdateIncludesDeletedRow asserts the wire shape for a
// row marked visible=false. The SPA's SessionStore reads Row.Visible
// directly — there is no separate `deleted: true` discriminator on
// the wire, just the row.
func TestMarshalRowUpdateIncludesDeletedRow(t *testing.T) {
	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:         "8",
		Email:      "u@example.com",
		Scope:      "default",
		Visible:    false,
		Status:     "Failed",
		RowVersion: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Cursor string `json:"cursor"`
		Row    struct {
			ID              string `json:"id"`
			Visible         bool   `json:"visible"`
			SidebarPosition int64  `json:"sidebar_position"`
			RowVersion      int64  `json:"row_version"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Row.ID != "8" {
		t.Fatalf("row.id = %q, want 8", probe.Row.ID)
	}
	if probe.Row.Visible {
		t.Fatalf("row.visible = true, want false (the deleted row signal)")
	}
	if probe.Row.RowVersion != 99 || probe.Cursor != "99" {
		t.Fatalf("row_version/cursor mismatch: %#v", probe)
	}
}

func TestMarshalRowUpdateIncludesSidebarPosition(t *testing.T) {
	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:              "8",
		Email:           "u@example.com",
		Scope:           "default",
		Visible:         true,
		Status:          "Active",
		SidebarPosition: 42,
		RowVersion:      99,
	})
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Row struct {
			SidebarPosition int64 `json:"sidebar_position"`
			RowVersion      int64 `json:"row_version"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Row.SidebarPosition != 42 {
		t.Fatalf("sidebar_position = %d, want 42", probe.Row.SidebarPosition)
	}
	if probe.Row.RowVersion != 99 {
		t.Fatalf("row_version = %d, want 99", probe.Row.RowVersion)
	}
}

func TestMarshalRowUpdateIncludesSessionRunConfig(t *testing.T) {
	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:                  "8",
		Email:               "u@example.com",
		Scope:               "default",
		Visible:             true,
		Status:              "Active",
		Model:               "gpt-5.5",
		Effort:              "xhigh",
		RuntimeModel:        "gpt-5.5",
		RuntimeEffort:       "xhigh",
		RuntimeConfiguredAt: "2026-05-21T00:00:00Z",
		RowVersion:          99,
	})
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Row struct {
			Model               string `json:"model"`
			Effort              string `json:"effort"`
			RuntimeModel        string `json:"runtime_model"`
			RuntimeEffort       string `json:"runtime_effort"`
			RuntimeConfiguredAt string `json:"runtime_configured_at"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Row.Model != "gpt-5.5" || probe.Row.Effort != "xhigh" || probe.Row.RuntimeModel != "gpt-5.5" || probe.Row.RuntimeEffort != "xhigh" || probe.Row.RuntimeConfiguredAt == "" {
		t.Fatalf("run config row = %#v", probe.Row)
	}
}

func TestMarshalRowUpdateIncludesAvatarAssignments(t *testing.T) {
	payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
		ID:             "8",
		Email:          "u@example.com",
		Scope:          "default",
		Visible:        true,
		Status:         "Active",
		AgentAvatarID:  "agent-a",
		SystemAvatarID: "system-b",
		RowVersion:     99,
	})
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Row struct {
			AgentAvatarID  string `json:"agent_avatar_id"`
			SystemAvatarID string `json:"system_avatar_id"`
		} `json:"row"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Row.AgentAvatarID != "agent-a" || probe.Row.SystemAvatarID != "system-b" {
		t.Fatalf("avatar assignment row = %#v", probe.Row)
	}
}

// TestMarshalRowUpdateRepos pins the SSE wire contract for the
// repo-selection field: always an array, never absent, even when
// the row has no repos picked. The SPA reads this directly into the
// SessionStore so existing sessions stay in lockstep with the durable
// column — local optimism never overrides the
// server's view of "which repos belong to session N."
func TestMarshalRowUpdateRepos(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantOnWire []string
	}{
		{
			name:       "nil round-trips as empty array",
			in:         nil,
			wantOnWire: []string{},
		},
		{
			name:       "empty round-trips as empty array",
			in:         []string{},
			wantOnWire: []string{},
		},
		{
			name:       "non-empty preserves order",
			in:         []string{"nelsong6/tank-operator", "nelsong6/mcp-github"},
			wantOnWire: []string{"nelsong6/tank-operator", "nelsong6/mcp-github"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
				ID:         "55",
				Email:      "u@example.com",
				Scope:      "default",
				Visible:    true,
				Status:     "Active",
				RowVersion: 1,
				Repos:      tc.in,
			})
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Row struct {
					Repos []string `json:"repos"`
				} `json:"row"`
			}
			if err := json.Unmarshal(payload, &probe); err != nil {
				t.Fatal(err)
			}
			if probe.Row.Repos == nil {
				t.Fatalf("row.repos was nil on the wire; want explicit array")
			}
			if !stringSliceEqual(probe.Row.Repos, tc.wantOnWire) {
				t.Fatalf("row.repos = %v, want %v", probe.Row.Repos, tc.wantOnWire)
			}
		})
	}
}

func TestMarshalRowUpdateCapabilities(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantOnWire []string
	}{
		{
			name:       "nil round-trips as empty array",
			in:         nil,
			wantOnWire: []string{},
		},
		{
			name:       "empty round-trips as empty array",
			in:         []string{},
			wantOnWire: []string{},
		},
		{
			name:       "non-empty preserves order",
			in:         []string{sessionmodel.SessionCapabilitySpireLensMCP},
			wantOnWire: []string{sessionmodel.SessionCapabilitySpireLensMCP},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := sessioncontroller.MarshalRowUpdate(sessionmodel.SessionRecord{
				ID:           "55",
				Email:        "u@example.com",
				Scope:        "default",
				Visible:      true,
				Status:       "Active",
				RowVersion:   1,
				Capabilities: tc.in,
			})
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Row struct {
					Capabilities []string `json:"capabilities"`
				} `json:"row"`
			}
			if err := json.Unmarshal(payload, &probe); err != nil {
				t.Fatal(err)
			}
			if probe.Row.Capabilities == nil {
				t.Fatalf("row.capabilities was nil on the wire; want explicit array")
			}
			if !stringSliceEqual(probe.Row.Capabilities, tc.wantOnWire) {
				t.Fatalf("row.capabilities = %v, want %v", probe.Row.Capabilities, tc.wantOnWire)
			}
		})
	}
}

// --- helpers ---

func newTestAppServer(t *testing.T) *appServer {
	t.Helper()
	return &appServer{
		verifier:     auth.NewVerifier(testJWT(t)),
		sessionScope: "default",
	}
}

// Compile-time guard that the recordingSessionBus stub fully satisfies
// the row-update wire interface, so a future refactor doesn't drop a
// method.
var _ sessionCommandBus = (*recordingSessionBus)(nil)

// authedListTimelineRequest is referenced by no tests anymore (the
// timeline endpoint was retired in Phase 3); kept to satisfy other
// test files that might still reference it. Remove in Phase 4 if
// nothing else picks it up by then.
func authedListTimelineRequest(t *testing.T, _ string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/timeline", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "u@example.com"))
	return req
}

// ignore unused
var _ = context.Background
