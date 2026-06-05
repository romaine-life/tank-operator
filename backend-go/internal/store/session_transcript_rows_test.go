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
