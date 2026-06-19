// Package transcriptstore persists session transcript JSONL snapshots to
// durable object storage so a session's conversation can be resurrected onto
// a fresh pod after pod death. See docs/session-transcript-capture.md.
//
// The artifact is opaque, append-mostly, and read whole exactly once (on
// resurrection) — the object-storage profile, not a relational one. This
// package deliberately mirrors internal/avatarassets (same azblob client
// shape) rather than introducing a new storage abstraction.
package transcriptstore

import (
	"context"
	"sync"
)

// Snapshot is one whole-file transcript upload plus the restore metadata that
// rides as blob metadata (so a Stage-2 restore can materialize the file at its
// exact SDK-expected path and gate resume on SDK-format compatibility).
type Snapshot struct {
	Bytes       []byte
	ContentType string
	Metadata    map[string]string
}

// Store is the durable sink. Put overwrites the blob at key; the latest
// snapshot is the resume source, so last-write-wins is the intended semantics.
type Store interface {
	Put(ctx context.Context, key string, snap Snapshot) error
}

// MemoryStore is an in-process stub used in tests and as a non-fatal fallback.
type MemoryStore struct {
	mu   sync.Mutex
	objs map[string]Snapshot
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objs: make(map[string]Snapshot)}
}

func (m *MemoryStore) Put(_ context.Context, key string, snap Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(snap.Bytes))
	copy(cp, snap.Bytes)
	stored := snap
	stored.Bytes = cp
	m.objs[key] = stored
	return nil
}

// Get returns a stored snapshot; second result is false when absent. Test-only
// helper (the production read path is Stage-2 restore).
func (m *MemoryStore) Get(key string) (Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.objs[key]
	return snap, ok
}
