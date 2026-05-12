package store

import "testing"

func TestSortSessionEventsPrefersWrittenAt(t *testing.T) {
	events := []map[string]any{
		{"id": "0002-random-v4", "written_at": "2026-05-12T01:00:02Z", "type": "item.completed"},
		{"id": "0001-random-v4", "written_at": "2026-05-12T01:00:03Z", "type": "turn.completed"},
		{"id": "9999-random-v4", "written_at": "2026-05-12T01:00:01Z", "type": "tank.user_message"},
	}

	sortSessionEvents(events)

	got := []string{
		events[0]["type"].(string),
		events[1]["type"].(string),
		events[2]["type"].(string),
	}
	want := []string{"tank.user_message", "item.completed", "turn.completed"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted event %d = %q, want %q (all events: %#v)", i, got[i], want[i], events)
		}
	}
}

func TestSortSessionEventsPrefersTankOrderKey(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "tank_order_key": "0003", "written_at": "2026-05-12T01:00:01Z", "type": "third"},
		{"id": "b", "tank_order_key": "0001", "written_at": "2026-05-12T01:00:03Z", "type": "first"},
		{"id": "c", "tank_order_key": "0002", "written_at": "2026-05-12T01:00:02Z", "type": "second"},
	}

	sortSessionEvents(events)

	got := []string{
		events[0]["type"].(string),
		events[1]["type"].(string),
		events[2]["type"].(string),
	}
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted event %d = %q, want %q (all events: %#v)", i, got[i], want[i], events)
		}
	}
}

func TestSortSessionEventsFallsBackToID(t *testing.T) {
	events := []map[string]any{
		{"id": "b", "type": "second"},
		{"id": "a", "type": "first"},
	}

	sortSessionEvents(events)

	if got := events[0]["type"]; got != "first" {
		t.Fatalf("first event = %v, want first", got)
	}
}
