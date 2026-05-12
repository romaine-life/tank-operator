package store

import (
	"encoding/json"
	"testing"
)

// TestTurnDocShape pins the wire JSON the orchestrator writes to Cosmos.
// The pod-side SDK runners read these exact fields; any drift here is a
// wire-shape regression that the runners will silently misparse.
func TestTurnDocShape(t *testing.T) {
	claimed := "2026-05-11T17:00:00Z"
	rec := TurnRecord{
		RunID:          "abc123",
		SessionID:      "61",
		Email:          "nelson@romaine.life",
		Provider:       "claude",
		Source:         "sdk",
		ClientNonce:    "client-abc123",
		Prompt:         "hello",
		Model:          "claude-sonnet-4-6",
		PermissionMode: "bypassPermissions",
		SkillName:      "init",
		FollowUp:       false,
		Status:         TurnPending,
		CreatedAt:      "2026-05-11T16:59:59Z",
		ClaimedAt:      &claimed,
	}
	doc := turnDoc(rec)

	if got, want := doc["id"], "turn:abc123"; got != want {
		t.Fatalf("id = %q, want %q (runner reads by 'turn:<run_id>')", got, want)
	}
	if got, want := doc["session_id"], "61"; got != want {
		t.Fatalf("session_id = %q (must match container partition key /session_id)", got)
	}
	if got, want := doc["status"], "pending"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := doc["source"], "sdk"; got != want {
		t.Fatalf("source = %q, want %q", got, want)
	}
	if got, want := doc["client_nonce"], "client-abc123"; got != want {
		t.Fatalf("client_nonce = %q, want %q", got, want)
	}
	if got, want := doc["prompt"], "hello"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if got, want := doc["model"], "claude-sonnet-4-6"; got != want {
		t.Fatalf("model = %q, want %q", got, want)
	}
	if got, want := doc["follow_up"], false; got != want {
		t.Fatalf("follow_up = %v, want %v", got, want)
	}
	if got, want := doc["claimed_at"], &claimed; got != want {
		t.Fatalf("claimed_at = %v, want pointer", got)
	}
	if doc["completed_at"] != (*string)(nil) {
		t.Fatalf("completed_at = %v, want nil", doc["completed_at"])
	}
}

// TestTurnDocRoundtrip ensures the SDK runners can decode what the producer
// wrote without information loss on any field.
func TestTurnDocRoundtrip(t *testing.T) {
	orig := TurnRecord{
		RunID:          "abc",
		SessionID:      "61",
		Email:          "nelson@romaine.life",
		Provider:       "claude",
		Source:         "sdk",
		ClientNonce:    "client-abc",
		Prompt:         "say hi",
		Model:          "claude-sonnet-4-6",
		PermissionMode: "bypassPermissions",
		SkillName:      "",
		FollowUp:       true,
		Status:         TurnClaimed,
		CreatedAt:      "2026-05-11T16:00:00Z",
		ClaimedAt:      strptr("2026-05-11T16:00:01Z"),
		CompletedAt:    nil,
	}
	raw, err := json.Marshal(turnDoc(orig))
	if err != nil {
		t.Fatal(err)
	}
	got, err := turnFromDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != orig.RunID || got.SessionID != orig.SessionID ||
		got.Provider != orig.Provider || got.Prompt != orig.Prompt ||
		got.Source != orig.Source || got.ClientNonce != orig.ClientNonce ||
		got.Model != orig.Model || got.FollowUp != orig.FollowUp {
		t.Fatalf("roundtrip mismatch:\ngot  = %#v\nwant = %#v", got, orig)
	}
	if got.Status != TurnClaimed {
		t.Fatalf("status = %q, want %q", got.Status, TurnClaimed)
	}
	if got.ClaimedAt == nil || *got.ClaimedAt != "2026-05-11T16:00:01Z" {
		t.Fatalf("claimed_at = %v, want pointer to set value", got.ClaimedAt)
	}
	if got.CompletedAt != nil {
		t.Fatalf("completed_at = %v, want nil", got.CompletedAt)
	}
}

func strptr(s string) *string { return &s }
