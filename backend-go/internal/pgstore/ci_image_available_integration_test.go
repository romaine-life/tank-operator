package pgstore

import (
	"testing"
)

// TestCIImageAvailableUpsertIdempotency proves the durable image-readiness
// signal is idempotent under ACR's at-least-once redelivery: two upserts of the
// same (registry, repo, commit) leave exactly one row, with the tag/digest/
// observed_at refreshed from the latest delivery. It also exercises
// ImageAvailableForCommit (the currently-unused stage-2 existence check) so it
// does not rot before the provisioning-gate cutover reads it. Skips when
// TANK_TEST_POSTGRES_DSN is unset (local runs); CI's postgres service sets it.
func TestCIImageAvailableUpsertIdempotency(t *testing.T) {
	ctx, pool := newTurnNumberTestPool(t)
	store := NewCIImageAvailableStore(pool)

	const (
		registry = "romainecr.azurecr.io"
		repo     = "chess-tactics"
		commit   = "abc123"
	)

	// Existence check is false before any push is recorded.
	if ok, err := store.ImageAvailableForCommit(ctx, registry, repo, commit); err != nil {
		t.Fatalf("ImageAvailableForCommit (pre): %v", err)
	} else if ok {
		t.Fatal("ImageAvailableForCommit reported true before any upsert")
	}

	// First push.
	if err := store.UpsertCIImageAvailable(ctx, CIImageAvailable{
		Registry: registry, RepoName: repo, CommitSHA: commit,
		ImageTag: "sha-abc123", ImageDigest: "sha256:first",
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first, err := store.Get(ctx, registry, repo, commit)
	if err != nil {
		t.Fatalf("get after first upsert: %v", err)
	}

	// Re-delivery of the same commit with a refreshed digest. Same PK → one row,
	// refreshed.
	if err := store.UpsertCIImageAvailable(ctx, CIImageAvailable{
		Registry: registry, RepoName: repo, CommitSHA: commit,
		ImageTag: "sha-abc123", ImageDigest: "sha256:second",
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM ci_image_available
		WHERE registry = $1 AND repo_name = $2 AND commit_sha = $3
	`, registry, repo, commit).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want exactly 1 after two upserts of the same key", count)
	}

	second, err := store.Get(ctx, registry, repo, commit)
	if err != nil {
		t.Fatalf("get after second upsert: %v", err)
	}
	if second.ImageDigest != "sha256:second" {
		t.Fatalf("digest = %q, want refreshed sha256:second", second.ImageDigest)
	}
	if !second.ObservedAt.After(first.ObservedAt) && !second.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("observed_at went backwards: first=%s second=%s", first.ObservedAt, second.ObservedAt)
	}

	// Existence check now reports true; a different commit stays false.
	if ok, err := store.ImageAvailableForCommit(ctx, registry, repo, commit); err != nil {
		t.Fatalf("ImageAvailableForCommit (post): %v", err)
	} else if !ok {
		t.Fatal("ImageAvailableForCommit reported false after upsert")
	}
	if ok, err := store.ImageAvailableForCommit(ctx, registry, repo, "different"); err != nil {
		t.Fatalf("ImageAvailableForCommit (other): %v", err)
	} else if ok {
		t.Fatal("ImageAvailableForCommit reported true for an unrecorded commit")
	}
}
