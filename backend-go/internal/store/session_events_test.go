package store

import "testing"

func TestSessionEventPageFromOrderedUsesCanonicalOrderKey(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "order_key": "001", "type": "first"},
		{"id": "b", "order_key": "002", "type": "second"},
		{"id": "c", "order_key": "003", "type": "third"},
	}

	page := sessionEventPageFromOrdered(events, 2)
	if !page.HasMore {
		t.Fatal("HasMore = false, want true")
	}
	if page.NextOrderKey != "002" {
		t.Fatalf("NextOrderKey = %q, want 002", page.NextOrderKey)
	}
	if got := eventTypes(page.Events); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("page types = %#v, want first, second", got)
	}
}

func TestSessionEventPageFromOrderedDoesNotUseOldCursorFields(t *testing.T) {
	events := []map[string]any{
		{"id": "a", "written_at": "2026-05-12T01:00:01Z", "type": "old"},
	}

	page := sessionEventPageFromOrdered(events, 10)
	if page.NextOrderKey != "" {
		t.Fatalf("NextOrderKey = %q, want empty without canonical order_key", page.NextOrderKey)
	}
}

func eventTypes(events []map[string]any) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event["type"].(string))
	}
	return types
}
