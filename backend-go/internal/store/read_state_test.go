package store

import (
	"context"
	"testing"
)

func TestStubConversationReadStateStoreIsMonotonic(t *testing.T) {
	s := NewStubConversationReadStateStore()
	if _, err := s.Set(context.Background(), "User@Example.COM", "63", "002"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(context.Background(), "user@example.com", "63", "001"); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Get(context.Background(), "user@example.com", "63")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.LastReadOrderKey != "002" {
		t.Fatalf("read state = %#v, want cursor 002", rec)
	}

	if _, err := s.Set(context.Background(), "user@example.com", "63", "003"); err != nil {
		t.Fatal(err)
	}
	rec, err = s.Get(context.Background(), "user@example.com", "63")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.LastReadOrderKey != "003" {
		t.Fatalf("read state = %#v, want cursor 003", rec)
	}
}

func TestNormalizeReadStateEmailLowercasesAndTrims(t *testing.T) {
	if got := normalizeReadStateEmail("  User@Example.COM "); got != "user@example.com" {
		t.Fatalf("normalize = %q", got)
	}
	if got := normalizeReadStateEmail(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestReadStateMemoryKeyEmpty(t *testing.T) {
	if key, _, _ := readStateMemoryKey("", "63"); key != "" {
		t.Fatalf("empty email -> key %q", key)
	}
	if key, _, _ := readStateMemoryKey("a@b", "  "); key != "" {
		t.Fatalf("empty sessionID -> key %q", key)
	}
}
