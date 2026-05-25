package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionstream"
)

func TestDebugSessionEventStreamsAdminGate(t *testing.T) {
	app := adminTestServer(t)
	app.streamRegistry = sessionstream.NewRegistry()
	app.sessionScope = "default"

	t.Run("non-admin role 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-streams", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, otherUser, auth.RoleUser))
		resp := httptest.NewRecorder()
		app.handleDebugSessionEventStreams(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("unauthenticated 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-streams", nil)
		resp := httptest.NewRecorder()
		app.handleDebugSessionEventStreams(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.Code)
		}
	})

	t.Run("admin gets empty registry", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-streams", nil)
		req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
		resp := httptest.NewRecorder()
		app.handleDebugSessionEventStreams(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["open_count"].(float64) != 0 {
			t.Fatalf("open_count = %v, want 0 on empty registry", body["open_count"])
		}
		if streams, _ := body["streams"].([]any); len(streams) != 0 {
			t.Fatalf("streams len = %d, want 0", len(streams))
		}
		if body["scope"] != "default" {
			t.Fatalf("scope = %v, want default", body["scope"])
		}
	})

	t.Run("service admin actor gets empty registry", func(t *testing.T) {
		t.Setenv("SUPER_ADMIN_EMAILS", adminEmail)
		req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-streams", nil)
		req.Header.Set("Authorization", "Bearer "+signedServiceToken(t, "pod-200@service.tank.romaine.life", adminEmail))
		resp := httptest.NewRecorder()
		app.handleDebugSessionEventStreams(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
		}
	})
}

func TestDebugSessionEventStreamsReturnsRegisteredState(t *testing.T) {
	app := adminTestServer(t)
	app.streamRegistry = sessionstream.NewRegistry()
	app.sessionScope = "default"

	opened := time.Now().Add(-2 * time.Second)
	stateOne := sessionstream.NewStreamState("stream-1", "63", "63", adminEmail, opened, "")
	stateTwo := sessionstream.NewStreamState("stream-2", "64", "64", adminEmail, opened.Add(time.Second), "abc100")
	app.streamRegistry.Register(stateOne)
	app.streamRegistry.Register(stateTwo)

	stateOne.RecordWake(opened.Add(500*time.Millisecond), "tank.live.63.wake")
	stateOne.RecordPageRead(opened.Add(501*time.Millisecond), 3)
	stateOne.RecordEmit(opened.Add(502*time.Millisecond), "abc20", "user_message.created", "abc20")

	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-streams", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()
	app.handleDebugSessionEventStreams(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	streams, _ := body["streams"].([]any)
	if len(streams) != 2 {
		t.Fatalf("streams len = %d, want 2", len(streams))
	}
	first, _ := streams[0].(map[string]any)
	if first["stream_id"] != "stream-1" {
		t.Fatalf("expected stream-1 first (sorted by opened_at), got %v", first["stream_id"])
	}
	if first["wakes_received"].(float64) != 1 {
		t.Fatalf("wakes_received = %v, want 1", first["wakes_received"])
	}
	if first["last_wake_subject"] != "tank.live.63.wake" {
		t.Fatalf("last_wake_subject = %v", first["last_wake_subject"])
	}
	if first["last_emit_event_type"] != "user_message.created" {
		t.Fatalf("last_emit_event_type = %v", first["last_emit_event_type"])
	}
	if first["cursor_after_order_key"] != "abc20" {
		t.Fatalf("cursor_after_order_key = %v", first["cursor_after_order_key"])
	}
	if first["pages_read_non_empty"].(float64) != 1 {
		t.Fatalf("pages_read_non_empty = %v", first["pages_read_non_empty"])
	}
}

func TestDebugSessionEventStreamsSessionFilter(t *testing.T) {
	app := adminTestServer(t)
	app.streamRegistry = sessionstream.NewRegistry()
	app.sessionScope = "default"

	opened := time.Now()
	app.streamRegistry.Register(sessionstream.NewStreamState("a", "10", "10", adminEmail, opened, ""))
	app.streamRegistry.Register(sessionstream.NewStreamState("b", "20", "20", adminEmail, opened, ""))
	app.streamRegistry.Register(sessionstream.NewStreamState("c", "20", "20", adminEmail, opened.Add(time.Second), ""))

	req := httptest.NewRequest(http.MethodGet, "/api/debug/session-event-streams?session_id=20", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokenWithRole(t, adminEmail, auth.RoleAdmin))
	resp := httptest.NewRecorder()
	app.handleDebugSessionEventStreams(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d", resp.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(resp.Body.Bytes(), &body)
	if body["matched"].(float64) != 2 {
		t.Fatalf("matched = %v, want 2 streams on session_id=20", body["matched"])
	}
	if body["open_count"].(float64) != 3 {
		t.Fatalf("open_count = %v, want 3 (all open streams across all sessions)", body["open_count"])
	}
	if body["session_id"] != "20" {
		t.Fatalf("session_id = %v", body["session_id"])
	}
}
