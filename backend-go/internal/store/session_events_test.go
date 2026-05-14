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

func TestSortSessionEventsRecognizesCanonicalOrderKey(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "order_key": "0002", "written_at": "2026-05-12T01:00:01Z", "type": "second"},
		{"id": "b", "order_key": "0001", "written_at": "2026-05-12T01:00:03Z", "type": "first"},
	}

	sortSessionEvents(events)

	if got := events[0]["type"]; got != "first" {
		t.Fatalf("first event = %v, want first", got)
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

func TestPaginateSessionEventsUsesRenderOrderCursor(t *testing.T) {
	events := []map[string]any{
		{"id": "random-c", "tank_order_key": "0003", "type": "third"},
		{"id": "random-a", "tank_order_key": "0001", "type": "first"},
		{"id": "random-b", "tank_order_key": "0002", "type": "second"},
	}
	sortSessionEvents(events)

	firstPage := paginateSessionEvents(events, SessionEventCursor{}, 2)
	if !firstPage.HasMore {
		t.Fatal("first page HasMore = false, want true")
	}
	if got := eventTypes(firstPage.Events); got[0] != "first" || got[1] != "second" {
		t.Fatalf("first page types = %#v, want first, second", got)
	}

	secondPage := paginateSessionEvents(events, SessionEventCursor{AfterOrderKey: firstPage.NextOrderKey}, 2)
	if secondPage.HasMore {
		t.Fatal("second page HasMore = true, want false")
	}
	if got := eventTypes(secondPage.Events); len(got) != 1 || got[0] != "third" {
		t.Fatalf("second page types = %#v, want third", got)
	}
}

func TestPaginateSessionEventsAcceptsDocumentIDCursor(t *testing.T) {
	events := []map[string]any{
		{"id": "b", "written_at": "2026-05-12T01:00:01Z", "type": "first"},
		{"id": "a", "written_at": "2026-05-12T01:00:02Z", "type": "second"},
	}
	sortSessionEvents(events)

	page := paginateSessionEvents(events, SessionEventCursor{AfterID: "b"}, 10)
	if got := eventTypes(page.Events); len(got) != 1 || got[0] != "second" {
		t.Fatalf("page types = %#v, want second", got)
	}
}

func TestPaginateSessionEventsUsesFullCursorWhenOrderKeysCollide(t *testing.T) {
	events := []map[string]any{
		{
			"id":         "same-order-a",
			"order_key":  "0001",
			"sequence":   float64(1),
			"created_at": "2026-05-12T01:00:01Z",
			"type":       "first",
		},
		{
			"id":         "same-order-b",
			"order_key":  "0001",
			"sequence":   float64(2),
			"created_at": "2026-05-12T01:00:01Z",
			"type":       "second",
		},
		{
			"id":         "same-order-c",
			"order_key":  "0001",
			"sequence":   float64(3),
			"created_at": "2026-05-12T01:00:01Z",
			"type":       "third",
		},
	}
	sortSessionEvents(events)

	firstPage := paginateSessionEvents(events, SessionEventCursor{}, 2)
	if got := eventTypes(firstPage.Events); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("first page types = %#v, want first, second", got)
	}

	secondPage := paginateSessionEvents(events, SessionEventCursor{AfterOrderKey: firstPage.NextOrderKey}, 2)
	if got := eventTypes(secondPage.Events); len(got) != 1 || got[0] != "third" {
		t.Fatalf("second page types = %#v, want third", got)
	}
}

func TestPaginateSessionEventsUnknownCursorRestartsFromBeginning(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "tank_order_key": "0001", "type": "first"},
		{"id": "b", "tank_order_key": "0002", "type": "second"},
	}
	sortSessionEvents(events)

	page := paginateSessionEvents(events, SessionEventCursor{AfterOrderKey: "missing"}, 10)
	if got := eventTypes(page.Events); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("page types = %#v, want first, second", got)
	}
}

func eventTypes(events []map[string]any) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event["type"].(string))
	}
	return types
}
