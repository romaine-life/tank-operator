package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPendingTestProvisionRegisterAndGuard exercises the durable
// pending-provision backstop table (migrations 0175-0177): the atomic
// double-trigger guard, the terminalize + re-arm lifecycle, and the conditional
// claim/terminal writes against a real Postgres schema. Skips locally unless
// TANK_TEST_POSTGRES_DSN is set; runs in CI against the postgres service.
func TestPendingTestProvisionRegisterAndGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "pending_provisions")
	store := NewPendingTestProvisionStore(pool, "default")

	req := RegisterPendingTestProvisionRequest{
		SessionID: "77", OwnerEmail: "User@Example.test",
		RepoOwner: "romaine-life", RepoName: "tank-operator",
		Branch: "tank/session/77/tank-operator", Project: "tank-operator",
		Workflow: "interactive-test", Kind: PendingTestProvisionInteractive,
	}

	first, created, err := store.Register(ctx, req)
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if !created {
		t.Fatal("first register created=false, want true")
	}
	if first.Status != PendingTestProvisionPending {
		t.Fatalf("status=%q, want pending", first.Status)
	}
	if first.OwnerEmail != "user@example.test" {
		t.Fatalf("owner not lowercased: %q", first.OwnerEmail)
	}

	// The atomic double-trigger guard: a second register for the same target
	// while the first is still 'pending' is refused (created=false, no row).
	second, created, err := store.Register(ctx, req)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if created {
		t.Fatalf("second register created=true; in-flight target should be guarded, got %+v", second)
	}

	// Terminalizing then re-registering re-arms the same row (created=true).
	if _, err := store.MarkTerminal(ctx, first.ProvisionID, PendingTestProvisionDone, "done", "sha-final"); err != nil {
		t.Fatalf("mark terminal: %v", err)
	}
	rearmed, created, err := store.Register(ctx, req)
	if err != nil {
		t.Fatalf("re-register after terminal: %v", err)
	}
	if !created {
		t.Fatal("re-register after terminal created=false, want true (re-armed)")
	}
	if rearmed.ProvisionID != first.ProvisionID {
		t.Fatalf("re-arm changed provision_id: %q vs %q", rearmed.ProvisionID, first.ProvisionID)
	}
	if rearmed.Status != PendingTestProvisionPending || rearmed.AttemptCount != 0 {
		t.Fatalf("re-armed row not reset: status=%q attempt=%d", rearmed.Status, rearmed.AttemptCount)
	}

	// A double-mark on the now-re-armed-then-terminalized row is stale, not a
	// resurrection.
	if _, err := store.MarkTerminal(ctx, first.ProvisionID, PendingTestProvisionFailed, "x", ""); err != nil {
		t.Fatalf("terminalize re-armed row: %v", err)
	}
	if _, err := store.MarkTerminal(ctx, first.ProvisionID, PendingTestProvisionDone, "y", ""); !errors.Is(err, ErrPendingTestProvisionStale) {
		t.Fatalf("double mark err=%v, want ErrPendingTestProvisionStale", err)
	}
}

// TestPendingTestProvisionClaimAndStale covers the reconcile backstop's store
// surface: ListStale finds only 'pending' rows past the cutoff, ClaimForRedrive
// is a conditional write that a concurrent reconcile loses, and
// OldestPendingAgeSeconds reads the oldest in-flight record.
func TestPendingTestProvisionClaimAndStale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "pending_provisions_claim")
	store := NewPendingTestProvisionStore(pool, "default")

	rec, _, err := store.Register(ctx, RegisterPendingTestProvisionRequest{
		SessionID: "88", OwnerEmail: "u@x.test",
		RepoOwner: "romaine-life", RepoName: "tank-operator",
		Branch: "b", Kind: PendingTestProvisionOrchestrationReview,
		OrchestrationID: "orch1",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Fresh record is not yet stale.
	if stale, err := store.ListStale(ctx, time.Hour, 100); err != nil {
		t.Fatalf("list stale (fresh): %v", err)
	} else if len(stale) != 0 {
		t.Fatalf("fresh record returned as stale: %d", len(stale))
	}

	// Backdate its activity so it falls into the stale window.
	if _, err := pool.Exec(ctx, `UPDATE pending_test_provisions
		SET started_at = now() - interval '40 minutes', last_event_at = now() - interval '40 minutes'
		WHERE provision_id = $1`, rec.ProvisionID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	stale, err := store.ListStale(ctx, 25*time.Minute, 100)
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	if len(stale) != 1 || stale[0].ProvisionID != rec.ProvisionID {
		t.Fatalf("stale scan = %+v, want the backdated record", stale)
	}

	age, err := store.OldestPendingAgeSeconds(ctx)
	if err != nil {
		t.Fatalf("oldest age: %v", err)
	}
	if age < 25*60 {
		t.Fatalf("oldest pending age = %.0fs, want > 1500s", age)
	}

	// Conditional claim: the first claim (on the known attempt) wins; a
	// concurrent claim on the same stale attempt loses the race.
	claimed, err := store.ClaimForRedrive(ctx, rec.ProvisionID, rec.AttemptCount)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.AttemptCount != rec.AttemptCount+1 {
		t.Fatalf("claim did not bump attempt: %d", claimed.AttemptCount)
	}
	if _, err := store.ClaimForRedrive(ctx, rec.ProvisionID, rec.AttemptCount); !errors.Is(err, ErrPendingTestProvisionStale) {
		t.Fatalf("lost-race claim err=%v, want ErrPendingTestProvisionStale", err)
	}

	// Once terminal, it drops out of the stale scan and the oldest-age gauge.
	if _, err := store.MarkTerminal(ctx, rec.ProvisionID, PendingTestProvisionDone, "done", ""); err != nil {
		t.Fatalf("mark terminal: %v", err)
	}
	if stale, err := store.ListStale(ctx, 25*time.Minute, 100); err != nil {
		t.Fatalf("list stale after terminal: %v", err)
	} else if len(stale) != 0 {
		t.Fatalf("terminal record still stale-scanned: %d", len(stale))
	}
	if age, err := store.OldestPendingAgeSeconds(ctx); err != nil {
		t.Fatalf("oldest age after terminal: %v", err)
	} else if age != 0 {
		t.Fatalf("oldest age after terminal = %.0f, want 0", age)
	}
}

// TestPendingTestProvisionRejectsBadEnum proves the CHECK constraints reject
// out-of-set kind and status values (the bounded-label contract the metrics and
// reconcile switch rely on).
func TestPendingTestProvisionRejectsBadEnum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newStrandedTurnTestPool(t, ctx, "pending_provisions_check")

	if _, err := pool.Exec(ctx, `INSERT INTO pending_test_provisions
		(provision_id, session_scope, session_id, tank_session_id, repo_owner, repo_name, kind, status, started_at)
		VALUES ('p1','default','1','default/1','o','r','bogus','pending', now())`); err == nil {
		t.Fatal("insert with kind='bogus' succeeded, want CHECK violation")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pending_test_provisions
		(provision_id, session_scope, session_id, tank_session_id, repo_owner, repo_name, kind, status, started_at)
		VALUES ('p2','default','1','default/1','o','r','interactive','bogus', now())`); err == nil {
		t.Fatal("insert with status='bogus' succeeded, want CHECK violation")
	}
}
