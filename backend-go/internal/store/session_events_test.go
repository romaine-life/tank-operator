package store

import "testing"

func TestSessionEventPageFromAscendingScanUsesCanonicalOrderKey(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "order_key": "001", "type": "first"},
		{"id": "b", "order_key": "002", "type": "second"},
		{"id": "c", "order_key": "003", "type": "third"},
	}

	page := sessionEventPageFromAscendingScan(events, 2, SessionEventCursor{})
	if !page.HasMore {
		t.Fatal("HasMore = false, want true")
	}
	if page.NextOrderKey != "002" {
		t.Fatalf("NextOrderKey = %q, want 002", page.NextOrderKey)
	}
	if page.PrevOrderKey != "001" {
		t.Fatalf("PrevOrderKey = %q, want 001", page.PrevOrderKey)
	}
	if !page.FoundOldest {
		t.Fatal("FoundOldest = false, want true (no AfterOrderKey cursor)")
	}
	if page.FoundNewest {
		t.Fatal("FoundNewest = true, want false (HasMore true means more to fetch)")
	}
	if got := eventTypes(page.Events); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("page types = %#v, want first, second", got)
	}
}

func TestSessionEventPageFromAscendingScanReachesNewest(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "order_key": "001", "type": "first"},
		{"id": "b", "order_key": "002", "type": "second"},
	}

	page := sessionEventPageFromAscendingScan(events, 10, SessionEventCursor{AfterOrderKey: "000"})
	if page.HasMore {
		t.Fatal("HasMore = true, want false")
	}
	if !page.FoundNewest {
		t.Fatal("FoundNewest = false, want true (fewer rows than limit)")
	}
	if page.FoundOldest {
		t.Fatal("FoundOldest = true, want false (AfterOrderKey cursor was set)")
	}
}

func TestSessionEventPageFromDescendingScanReversesAndTrims(t *testing.T) {
	// Caller already reversed the DESC scan into ASC order. The (limit+1)th
	// row from DESC sits at index 0 after reversal — must be dropped.
	events := []map[string]any{
		{"id": "extra", "order_key": "001", "type": "extra"},
		{"id": "a", "order_key": "002", "type": "first"},
		{"id": "b", "order_key": "003", "type": "second"},
	}

	page := sessionEventPageFromDescendingScan(events, 2, SessionEventCursor{})
	if !page.HasMore {
		t.Fatal("HasMore = false, want true")
	}
	if !page.FoundNewest {
		t.Fatal("FoundNewest = false, want true (no BeforeOrderKey cursor; tail read)")
	}
	if page.FoundOldest {
		t.Fatal("FoundOldest = true, want false (HasMore true means more to fetch)")
	}
	if got := eventTypes(page.Events); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("page types = %#v, want first, second", got)
	}
	if page.PrevOrderKey != "002" {
		t.Fatalf("PrevOrderKey = %q, want 002", page.PrevOrderKey)
	}
	if page.NextOrderKey != "003" {
		t.Fatalf("NextOrderKey = %q, want 003", page.NextOrderKey)
	}
}

func TestSessionEventPageFromDescendingScanReachesOldest(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "order_key": "001", "type": "first"},
		{"id": "b", "order_key": "002", "type": "second"},
	}

	page := sessionEventPageFromDescendingScan(events, 10, SessionEventCursor{BeforeOrderKey: "099"})
	if page.HasMore {
		t.Fatal("HasMore = true, want false")
	}
	if !page.FoundOldest {
		t.Fatal("FoundOldest = false, want true (fewer rows than limit)")
	}
	if page.FoundNewest {
		t.Fatal("FoundNewest = true, want false (BeforeOrderKey cursor was set)")
	}
}

func TestSessionEventPageEmpty(t *testing.T) {
	page := sessionEventPageFromAscendingScan(nil, 10, SessionEventCursor{})
	if page.NextOrderKey != "" || page.PrevOrderKey != "" {
		t.Fatalf("empty page cursors = (%q, %q), want empty", page.PrevOrderKey, page.NextOrderKey)
	}
	if page.HasMore {
		t.Fatal("empty page HasMore = true, want false")
	}
}

func eventTypes(events []map[string]any) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event["type"].(string))
	}
	return types
}
