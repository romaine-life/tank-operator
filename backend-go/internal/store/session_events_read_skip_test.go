package store

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestDecodeStoredSessionEventSkipsRetiredShapes pins the read-side contract:
// a stored ledger row the current schema rejects (a retired event type from
// an old session) is skipped and counted instead of poisoning the whole
// session's reads — before this, one such row made a session permanently
// un-projectable (the session-288 resync failures during the
// tank-operator#1051 recovery). The write path still hard-rejects invalid
// docs; this is read-side tolerance for history only.
func TestDecodeStoredSessionEventSkipsRetiredShapes(t *testing.T) {
	valid := []byte(`{
		"id": "turn-1:item.completed:x",
		"event_id": "turn-1:item.completed:x",
		"type": "item.completed",
		"actor": "tool",
		"source": "claude",
		"session_id": "63",
		"turn_id": "turn-1",
		"timeline_id": "turn-1:item:x",
		"order_key": "001",
		"created_at": "2026-06-11T00:00:00Z",
		"written_at": "2026-06-11T00:00:00Z",
		"producer": {"name": "claude-runner"},
		"visibility": "durable",
		"payload": {"kind": "command_execution"}
	}`)
	doc, ok := decodeStoredSessionEvent(valid, "63")
	if !ok {
		t.Fatalf("valid doc must decode")
	}
	if doc["tank_session_id"] != "63" {
		t.Fatalf("decoded doc missing tank_session_id stamp")
	}

	before := testutil.ToFloat64(sessionEventReadRejectedTotal.WithLabelValues("schema_rejected"))
	retired := []byte(`{
		"id": "turn-1:legacy.retired_event:x",
		"event_id": "turn-1:legacy.retired_event:x",
		"type": "legacy.retired_event",
		"actor": "tool",
		"source": "claude",
		"session_id": "288",
		"turn_id": "turn-1",
		"order_key": "001",
		"created_at": "2026-05-28T00:00:00Z"
	}`)
	if _, ok := decodeStoredSessionEvent(retired, "288"); ok {
		t.Fatalf("retired event type must not decode")
	}
	if after := testutil.ToFloat64(sessionEventReadRejectedTotal.WithLabelValues("schema_rejected")); after-before != 1 {
		t.Fatalf("schema_rejected counter delta = %v, want 1", after-before)
	}

	jsonBefore := testutil.ToFloat64(sessionEventReadRejectedTotal.WithLabelValues("invalid_json"))
	if _, ok := decodeStoredSessionEvent([]byte("{not json"), "288"); ok {
		t.Fatalf("invalid json must not decode")
	}
	if after := testutil.ToFloat64(sessionEventReadRejectedTotal.WithLabelValues("invalid_json")); after-jsonBefore != 1 {
		t.Fatalf("invalid_json counter delta = %v, want 1", after-jsonBefore)
	}
}
