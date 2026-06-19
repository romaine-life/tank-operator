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
	"errors"
	"strings"
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
// Get reads a snapshot back for restore (Stage 2); ok is false when absent.
type Store interface {
	Put(ctx context.Context, key string, snap Snapshot) error
	Get(ctx context.Context, key string) (snap Snapshot, ok bool, err error)
	// Latest returns the most-recently-written snapshot whose key starts with
	// prefix, or ok=false when none exist. Restore uses it to find a dead
	// session's transcript without a separate durable pointer: one session
	// normally has a single transcript blob, and last-write-wins picks the
	// freshest if there are several.
	Latest(ctx context.Context, prefix string) (snap Snapshot, ok bool, err error)
}

// ErrNotFound is returned by stores when a key is absent. Get reports absence
// via its ok result; this sentinel is for callers that prefer error matching.
var ErrNotFound = errors.New("transcriptstore: not found")

// MemoryStore is an in-process stub used in tests and as a non-fatal fallback.
type MemoryStore struct {
	mu      sync.Mutex
	objs    map[string]Snapshot
	seq     map[string]int64
	counter int64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objs: make(map[string]Snapshot), seq: make(map[string]int64)}
}

func (m *MemoryStore) Put(_ context.Context, key string, snap Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(snap.Bytes))
	copy(cp, snap.Bytes)
	stored := snap
	stored.Bytes = cp
	m.objs[key] = stored
	m.counter++
	m.seq[key] = m.counter
	return nil
}

func (m *MemoryStore) Get(_ context.Context, key string) (Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.objs[key]
	if !ok {
		return Snapshot{}, false, nil
	}
	return copyOf(snap), true, nil
}

func (m *MemoryStore) Latest(_ context.Context, prefix string) (Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bestKey := ""
	var bestSeq int64
	for key := range m.objs {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if bestKey == "" || m.seq[key] > bestSeq {
			bestKey = key
			bestSeq = m.seq[key]
		}
	}
	if bestKey == "" {
		return Snapshot{}, false, nil
	}
	return copyOf(m.objs[bestKey]), true, nil
}

func copyOf(snap Snapshot) Snapshot {
	cp := make([]byte, len(snap.Bytes))
	copy(cp, snap.Bytes)
	out := snap
	out.Bytes = cp
	return out
}
