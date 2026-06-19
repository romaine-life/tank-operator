package transcriptstore

import (
	"context"
	"testing"
)

func TestMemoryStorePutGetRoundTrip(t *testing.T) {
	m := NewMemoryStore()
	want := Snapshot{
		Bytes:       []byte(`{"type":"system"}` + "\n"),
		ContentType: "application/x-ndjson",
		Metadata:    map[string]string{"sdk_session_id": "abc"},
	}
	if err := m.Put(context.Background(), "owner/8/abc.jsonl", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := m.Get(context.Background(), "owner/8/abc.jsonl")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: snapshot not found after Put")
	}
	if string(got.Bytes) != string(want.Bytes) {
		t.Fatalf("bytes mismatch: got %q want %q", got.Bytes, want.Bytes)
	}
	if got.ContentType != want.ContentType {
		t.Fatalf("content type mismatch: got %q want %q", got.ContentType, want.ContentType)
	}
	if got.Metadata["sdk_session_id"] != "abc" {
		t.Fatalf("metadata mismatch: %v", got.Metadata)
	}
}

func TestMemoryStoreCopiesBytes(t *testing.T) {
	m := NewMemoryStore()
	src := []byte("one\n")
	if err := m.Put(context.Background(), "k", Snapshot{Bytes: src}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Mutating the caller's buffer must not corrupt the stored snapshot.
	src[0] = 'X'
	got, _, _ := m.Get(context.Background(), "k")
	if string(got.Bytes) != "one\n" {
		t.Fatalf("stored bytes aliased caller buffer: %q", got.Bytes)
	}
}

func TestMemoryStoreLastWriteWins(t *testing.T) {
	m := NewMemoryStore()
	_ = m.Put(context.Background(), "k", Snapshot{Bytes: []byte("v1")})
	_ = m.Put(context.Background(), "k", Snapshot{Bytes: []byte("v2")})
	got, _, _ := m.Get(context.Background(), "k")
	if string(got.Bytes) != "v2" {
		t.Fatalf("expected last write to win, got %q", got.Bytes)
	}
}
