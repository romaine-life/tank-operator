package conversation

import (
	"testing"
	"time"
)

// TestNoticeTurnLifecycleValidates proves the backend can author a COMPLETE,
// turn-anchored turn — open (user_message.created + turn.submitted) + body
// (assistant_message.created) + close (turn.completed) — that passes the same
// ValidateEventMap the runner-produced events pass, with every event sharing
// one turn_id. This is the contract the old turn-less test_provision.updated
// records failed: they had no turn_id, so the UI had no turn to render or land
// on.
func TestNoticeTurnLifecycleValidates(t *testing.T) {
	now := time.Date(2026, 6, 19, 4, 0, 0, 0, time.UTC)
	nonce := "test-provision-abc123def456"

	turnID, openEvents, err := NoticeTurnOpenEventMaps(NoticeTurnOpenArgs{
		SessionID:         "1068",
		SessionStorageKey: "1068",
		Email:             "owner@example.com",
		ClientNonce:       nonce,
		OpenerText:        "Creating test slot.",
		Now:               now,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	bodyValidating := AssistantNoticeEventMap(AssistantNoticeArgs{
		SessionID:         "1068",
		SessionStorageKey: "1068",
		Email:             "owner@example.com",
		TurnID:            turnID,
		TimelineID:        turnID + ":notice:validating",
		Text:              "Validating PR readiness…",
		Now:               now,
	})
	bodyReady := AssistantNoticeEventMap(AssistantNoticeArgs{
		SessionID:         "1068",
		SessionStorageKey: "1068",
		Email:             "owner@example.com",
		TurnID:            turnID,
		TimelineID:        turnID + ":notice:ready",
		Text:              "Test environment ready at https://tank-operator-slot-1.tank.dev.romaine.life/",
		Now:               now,
	})
	done := NoticeTurnCompletedEventMap(NoticeTurnCompletedArgs{
		SessionID:         "1068",
		SessionStorageKey: "1068",
		Email:             "owner@example.com",
		TurnID:            turnID,
		FinalTimelineIDs:  []string{turnID + ":notice:ready"},
		Now:               now,
	})

	all := append([]map[string]any{}, openEvents...)
	all = append(all, bodyValidating, bodyReady, done)

	for _, ev := range all {
		if err := ValidateEventMap(ev); err != nil {
			t.Fatalf("validate %v: %v", ev["type"], err)
		}
		if ev["turn_id"] != turnID {
			t.Fatalf("event %v has turn_id %v, want %v", ev["type"], ev["turn_id"], turnID)
		}
		if ev["tank_session_id"] != "1068" {
			t.Fatalf("event %v missing tank_session_id partition key", ev["type"])
		}
	}

	// The whole point: the body lines are turn-anchored (have a turn_id), unlike
	// the orphan role:system test_provision.updated records they replace.
	if bodyValidating["turn_id"] == "" || bodyReady["turn_id"] == "" {
		t.Fatal("notice body lines must be turn-anchored")
	}
}
