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

func TestReadStateRecordFromDocNormalizesFields(t *testing.T) {
	rec, err := readStateRecordFromDoc([]byte(`{
		"email": "User@Example.COM",
		"session_id": "63",
		"last_read_order_key": "002",
		"updated_at": "2026-05-12T00:00:00Z"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Email != "user@example.com" || rec.SessionID != "63" || rec.LastReadOrderKey != "002" {
		t.Fatalf("record = %#v", rec)
	}
}

func TestReadStateDocIDUsesScopedStorageKey(t *testing.T) {
	if got, want := readStateDocID("default", "63"), "read-state:63"; got != want {
		t.Fatalf("default read-state doc id = %q, want %q", got, want)
	}
	if got, want := readStateDocID("slot-a", "63"), "read-state:slot-a:63"; got != want {
		t.Fatalf("slot read-state doc id = %q, want %q", got, want)
	}
	doc := readStateDoc("slot-a", ConversationReadStateRecord{
		Email:            "user@example.com",
		SessionID:        "63",
		LastReadOrderKey: "002",
		UpdatedAt:        "2026-05-12T00:00:00Z",
	})
	if got, want := doc["session_id"], "63"; got != want {
		t.Fatalf("session_id = %q, want public id %q", got, want)
	}
	if got, want := doc["session_storage_key"], "slot-a:63"; got != want {
		t.Fatalf("session_storage_key = %q, want %q", got, want)
	}
}
