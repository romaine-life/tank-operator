package sessionregistry

// DSN-gated integration coverage for Store.SetParentSession — the explicit
// drag-to-nest / un-nest mutation of sessions.parent_session_id. Distinct from
// the write-once create stamp in parent_session_id_integration_test.go: this
// proves the UPDATE ... SET parent_session_id = NULLIF($4,'') round trips a set
// and a clear against the real migrations, so the sidebar nesting edge can be
// changed (and removed) after create.

import (
	"context"
	"testing"
	"time"
)

func TestSetParentSessionRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := newDSNStore(t, ctx)
	owner := "user@example.com"
	seedVisibleSession(t, ctx, store, owner, "child")
	seedVisibleSession(t, ctx, store, owner, "origin")

	// Nest: stamp child's parent pointer at origin.
	if err := store.SetParentSession(ctx, owner, "child", "origin"); err != nil {
		t.Fatalf("SetParentSession nest: %v", err)
	}
	got, ok, err := store.Get(ctx, owner, "child")
	if err != nil || !ok {
		t.Fatalf("get child: ok=%v err=%v", ok, err)
	}
	if got.ParentSessionID != "origin" {
		t.Fatalf("parent_session_id = %q, want origin", got.ParentSessionID)
	}

	// Un-nest: an empty parent writes NULL, which reads back as the empty string.
	if err := store.SetParentSession(ctx, owner, "child", ""); err != nil {
		t.Fatalf("SetParentSession unnest: %v", err)
	}
	got2, ok, err := store.Get(ctx, owner, "child")
	if err != nil || !ok {
		t.Fatalf("get child after unnest: ok=%v err=%v", ok, err)
	}
	if got2.ParentSessionID != "" {
		t.Fatalf("parent_session_id = %q, want empty after un-nest", got2.ParentSessionID)
	}
}
