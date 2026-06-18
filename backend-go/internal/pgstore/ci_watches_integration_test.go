package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestCIWatchStoreRegisterAndLifecycle exercises the session_ci_watches table
// (migrations 0165-0168) and the store's upsert + reaper-gate semantics against
// a real Postgres schema. Skips locally unless TANK_TEST_POSTGRES_DSN is set;
// runs in CI against the postgres:16 service (see .github/workflows/go-backend.yml).
func TestCIWatchStoreRegisterAndLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "ci_watches")
	store := NewCIWatchStore(pool, "default")

	w, err := store.Register(ctx, RegisterCIWatchRequest{
		SessionID:      "u1",
		OwnerEmail:     "User@Example.test",
		PROwner:        "romaine-life",
		PRName:         "tank-operator",
		PRNumber:       7,
		HeadSHA:        "sha1",
		MergeableState: "clean",
		CheckState:     "pending",
		PRURL:          "https://github.com/romaine-life/tank-operator/pull/7",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if w.Status != CIWatchWatching {
		t.Fatalf("status = %q, want watching", w.Status)
	}
	if w.HeadSHA != "sha1" {
		t.Fatalf("head_sha = %q, want sha1", w.HeadSHA)
	}
	if w.OwnerEmail != "user@example.test" {
		t.Fatalf("owner_email = %q, want lowercased user@example.test", w.OwnerEmail)
	}

	active, err := store.HasActiveForSession(ctx, "default", "u1")
	if err != nil {
		t.Fatalf("HasActiveForSession: %v", err)
	}
	if !active {
		t.Fatalf("a watching watch is not reaper-active, want true")
	}

	// Re-publishing the same PR with a new head SHA upserts the same row: head
	// refreshed, status reset to watching (a resolved-then-changed PR is watched
	// again on its new SHA).
	w2, err := store.Register(ctx, RegisterCIWatchRequest{
		SessionID:  "u1",
		OwnerEmail: "user@example.test",
		PROwner:    "romaine-life",
		PRName:     "tank-operator",
		PRNumber:   7,
		HeadSHA:    "sha2",
	})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if w2.WatchID != w.WatchID {
		t.Fatalf("watch_id changed on re-register: %q vs %q", w2.WatchID, w.WatchID)
	}
	if w2.HeadSHA != "sha2" {
		t.Fatalf("head_sha not refreshed on re-register: %q", w2.HeadSHA)
	}

	// Once green-and-awaiting-human-merge ('ready'), the watch is intentionally
	// no longer reaper-protective - the originating session may reap before the
	// human merges.
	if _, err := store.UpdateStatus(ctx, w.WatchID, CIWatchReady, "green"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	active, err = store.HasActiveForSession(ctx, "default", "u1")
	if err != nil {
		t.Fatalf("HasActiveForSession after ready: %v", err)
	}
	if active {
		t.Fatalf("a 'ready' watch is still reaper-active, want false")
	}
}

// TestCIWatchStoreRejectsBadStatus proves the CHECK constraint on the status
// column rejects an out-of-set value.
func TestCIWatchStoreRejectsBadStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "ci_watches_check")

	_, err := pool.Exec(ctx, `
		INSERT INTO session_ci_watches
			(watch_id, session_scope, session_id, tank_session_id, owner_email,
			 pr_owner, pr_name, pr_number, status, registered_at)
		VALUES ('w1', 'default', 'u1', 'default/u1', 'e@x.test',
			'romaine-life', 'tank-operator', 1, 'bogus', now())
	`)
	if err == nil {
		t.Fatalf("insert with status='bogus' succeeded, want CHECK violation")
	}
}

// TestCIWatchStoreGetByPRAndMerge covers the webhook reverse lookup (matched
// case-insensitively on the GitHub slug) and the merge transition.
func TestCIWatchStoreGetByPRAndMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "ci_watches_pr")
	store := NewCIWatchStore(pool, "default")

	if _, err := store.Register(ctx, RegisterCIWatchRequest{
		SessionID:  "u1",
		OwnerEmail: "u@x.test",
		PROwner:    "Romaine-Life",
		PRName:     "Tank-Operator",
		PRNumber:   9,
		HeadSHA:    "sha9",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	w, err := store.GetByPR(ctx, "romaine-life", "tank-operator", 9)
	if err != nil {
		t.Fatalf("GetByPR: %v", err)
	}
	if w.SessionID != "u1" || w.HeadSHA != "sha9" {
		t.Fatalf("GetByPR returned %+v", w)
	}

	merged, err := store.MarkMerged(ctx, w.WatchID, "mergesha")
	if err != nil {
		t.Fatalf("MarkMerged: %v", err)
	}
	if merged.Status != CIWatchMerged || merged.MergeCommit != "mergesha" {
		t.Fatalf("after merge: status=%q merge_commit=%q", merged.Status, merged.MergeCommit)
	}
}
