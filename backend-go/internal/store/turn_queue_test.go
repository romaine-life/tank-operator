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
	claimID := "claim-1"
	claimedBy := "claude-runner:61:runner-1"
	claimExpiresAt := "2026-05-11T17:02:00Z"
	availableAt := "2026-05-11T16:59:59Z"
	rec := TurnRecord{
		TurnID:         "abc123",
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
		ClaimID:        &claimID,
		ClaimedBy:      &claimedBy,
		ClaimExpiresAt: &claimExpiresAt,
		AttemptCount:   2,
		AvailableAt:    &availableAt,
	}
	doc := turnDoc(rec)

	if got, want := doc["id"], "turn:abc123"; got != want {
		t.Fatalf("id = %q, want %q (runner reads by 'turn:<turn_id>')", got, want)
	}
	if got, want := doc["session_id"], "61"; got != want {
		t.Fatalf("session_id = %q (must match container partition key /session_id)", got)
	}
	if got, want := doc["tank_public_session_id"], "61"; got != want {
		t.Fatalf("tank_public_session_id = %q, want %q", got, want)
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
	if got, want := doc["claim_id"], &claimID; got != want {
		t.Fatalf("claim_id = %v, want pointer", got)
	}
	if got, want := doc["claimed_by"], &claimedBy; got != want {
		t.Fatalf("claimed_by = %v, want pointer", got)
	}
	if got, want := doc["claim_expires_at"], &claimExpiresAt; got != want {
		t.Fatalf("claim_expires_at = %v, want pointer", got)
	}
	if got, want := doc["attempt_count"], 2; got != want {
		t.Fatalf("attempt_count = %v, want %v", got, want)
	}
	if got, want := doc["available_at"], &availableAt; got != want {
		t.Fatalf("available_at = %v, want pointer", got)
	}
	if doc["completed_at"] != (*string)(nil) {
		t.Fatalf("completed_at = %v, want nil", doc["completed_at"])
	}
}

func TestTurnDocUsesScopedStorageKeyForPartition(t *testing.T) {
	rec := TurnRecord{
		TurnID:    "abc123",
		SessionID: "61",
		Email:     "nelson@romaine.life",
		Provider:  "claude",
		Prompt:    "hello",
		Status:    TurnPending,
	}
	doc := turnDocForStorageKey(rec, "slot-a:61")
	if got, want := doc["session_id"], "slot-a:61"; got != want {
		t.Fatalf("session_id = %q, want scoped storage key %q", got, want)
	}
	if got, want := doc["tank_public_session_id"], "61"; got != want {
		t.Fatalf("tank_public_session_id = %q, want public id %q", got, want)
	}
}

// TestTurnDocRoundtrip ensures the SDK runners can decode what the producer
// wrote without information loss on any field.
func TestTurnDocRoundtrip(t *testing.T) {
	orig := TurnRecord{
		TurnID:         "abc",
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
		ClaimID:        strptr("claim-abc"),
		ClaimedBy:      strptr("claude-runner:61:runner-1"),
		ClaimExpiresAt: strptr("2026-05-11T16:02:01Z"),
		AttemptCount:   1,
		AvailableAt:    strptr("2026-05-11T16:00:00Z"),
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
	if got.TurnID != orig.TurnID || got.SessionID != orig.SessionID ||
		got.Provider != orig.Provider || got.Prompt != orig.Prompt ||
		got.Source != orig.Source || got.ClientNonce != orig.ClientNonce ||
		got.Model != orig.Model || got.FollowUp != orig.FollowUp ||
		got.AttemptCount != orig.AttemptCount {
		t.Fatalf("roundtrip mismatch:\ngot  = %#v\nwant = %#v", got, orig)
	}
	if got.Status != TurnClaimed {
		t.Fatalf("status = %q, want %q", got.Status, TurnClaimed)
	}
	if got.ClaimedAt == nil || *got.ClaimedAt != "2026-05-11T16:00:01Z" {
		t.Fatalf("claimed_at = %v, want pointer to set value", got.ClaimedAt)
	}
	if got.ClaimID == nil || *got.ClaimID != "claim-abc" {
		t.Fatalf("claim_id = %v, want pointer to set value", got.ClaimID)
	}
	if got.ClaimedBy == nil || *got.ClaimedBy != "claude-runner:61:runner-1" {
		t.Fatalf("claimed_by = %v, want pointer to set value", got.ClaimedBy)
	}
	if got.ClaimExpiresAt == nil || *got.ClaimExpiresAt != "2026-05-11T16:02:01Z" {
		t.Fatalf("claim_expires_at = %v, want pointer to set value", got.ClaimExpiresAt)
	}
	if got.AvailableAt == nil || *got.AvailableAt != "2026-05-11T16:00:00Z" {
		t.Fatalf("available_at = %v, want pointer to set value", got.AvailableAt)
	}
	if got.CompletedAt != nil {
		t.Fatalf("completed_at = %v, want nil", got.CompletedAt)
	}
}

func strptr(s string) *string { return &s }
