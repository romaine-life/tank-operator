package store

import "testing"

func TestTranscriptRowCursorRoundTrip(t *testing.T) {
	raw := "0000000000001\x1fturn-1:item:msg-1"
	encoded := EncodeTranscriptRowCursor(raw)
	if encoded == "" || encoded == raw {
		t.Fatalf("encoded cursor = %q", encoded)
	}
	decoded, err := DecodeTranscriptRowCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeTranscriptRowCursor: %v", err)
	}
	if decoded != raw {
		t.Fatalf("decoded = %q, want %q", decoded, raw)
	}
	if _, err := DecodeTranscriptRowCursor("not-a-row-cursor"); err == nil {
		t.Fatalf("invalid cursor decoded successfully")
	}
}

func TestTranscriptRowFromTurnActivityEntryUsesVisibleRowCursor(t *testing.T) {
	row, ok := transcriptRowFromEntry(map[string]any{
		"id":       "turn-1:activity",
		"kind":     "turn_activity",
		"turnId":   "turn-1",
		"orderKey": "003",
		"activity": map[string]any{
			"startOrderKey": "001",
			"endOrderKey":   "004",
		},
	})
	if !ok {
		t.Fatalf("turn_activity entry was not accepted")
	}
	if row.Cursor != "001\x1fturn-1:activity" {
		t.Fatalf("cursor = %q, want start-order cursor", row.Cursor)
	}
	if row.StartOrderKey != "001" || row.EndOrderKey != "004" || row.TurnID != "turn-1" {
		t.Fatalf("row = %#v", row)
	}
}

func TestTranscriptRowFromEntryDropsStartupSessionStatusMessages(t *testing.T) {
	_, ok := transcriptRowFromEntry(map[string]any{
		"id":            "session:63:status:loading",
		"kind":          "message",
		"role":          "system",
		"text":          "Session is loading.",
		"orderKey":      "001",
		"sourceEventId": "session:63:status:loading",
	})
	if ok {
		t.Fatalf("startup loading row was accepted")
	}

	_, ok = transcriptRowFromEntry(map[string]any{
		"id":            "session:63:provider:codex:status",
		"kind":          "message",
		"role":          "system",
		"text":          "Codex sign-in is back online.",
		"sessionStatus": "ready",
		"orderKey":      "002",
		"sourceEventId": "session:63:provider:codex:status",
	})
	if !ok {
		t.Fatalf("provider recovery status row should remain visible")
	}
}

// TestTranscriptRowFromEntryLiftsContentOrderKey pins issue #1077 item 4's
// delivery mechanism: an in-place payload mutation (answered/dismissed flips
// on awaiting cards) advances end_order_key past open SSE cursors while row
// identity and transcript position stay fixed.
func TestTranscriptRowFromEntryLiftsContentOrderKey(t *testing.T) {
	row, ok := transcriptRowFromEntry(map[string]any{
		"id":              "msg-1",
		"kind":            "message",
		"orderKey":        "005",
		"contentOrderKey": "009",
	})
	if !ok {
		t.Fatal("entry rejected")
	}
	if row.EndOrderKey != "009" {
		t.Fatalf("EndOrderKey = %q, want 009 (lifted by contentOrderKey)", row.EndOrderKey)
	}
	if row.StartOrderKey != "005" || row.Cursor != "005\x1fmsg-1" {
		t.Fatalf("row position must not move: start=%q cursor=%q", row.StartOrderKey, row.Cursor)
	}

	// A stale contentOrderKey (≤ the entry's own key) never lowers the bound.
	row, ok = transcriptRowFromEntry(map[string]any{
		"id":              "msg-2",
		"kind":            "message",
		"orderKey":        "010",
		"contentOrderKey": "007",
	})
	if !ok {
		t.Fatal("entry rejected")
	}
	if row.EndOrderKey != "010" {
		t.Fatalf("EndOrderKey = %q, want 010", row.EndOrderKey)
	}
}
