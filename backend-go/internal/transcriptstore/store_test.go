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

func TestMemoryStoreLatestByPrefix(t *testing.T) {
	m := NewMemoryStore()
	ctx := context.Background()
	_ = m.Put(ctx, "owner/8/aaa.jsonl", Snapshot{Bytes: []byte("a"), Metadata: map[string]string{"sdk_session_id": "aaa"}})
	_ = m.Put(ctx, "owner/8/bbb.jsonl", Snapshot{Bytes: []byte("b"), Metadata: map[string]string{"sdk_session_id": "bbb"}})
	_ = m.Put(ctx, "owner/9/ccc.jsonl", Snapshot{Bytes: []byte("c")})

	// Newest under the 8 prefix is bbb (written after aaa).
	snap, ok, err := m.Latest(ctx, "owner/8/")
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if snap.Metadata["sdk_session_id"] != "bbb" {
		t.Fatalf("expected newest=bbb, got %v", snap.Metadata)
	}

	// A prefix with no blobs returns ok=false.
	if _, ok, _ := m.Latest(ctx, "owner/404/"); ok {
		t.Fatal("expected no match for empty prefix")
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
