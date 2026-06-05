package pgstore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// newPendingLaunchTestPool provisions an isolated schema, runs all migrations,
// and returns a pool bound to it. Skips when TANK_TEST_POSTGRES_DSN is unset
// (local runs); CI's postgres:16 service sets it.
func newPendingLaunchTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_pending_launch_%d", time.Now().UnixNano())
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdent); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schemaIdent+" CASCADE")
		adminPool.Close()
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test dsn: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return ctx, pool
}

func insertSessionRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, scope, sessionID, status string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (
			email, session_scope, session_id, mode, pod_name, name, visible,
			requested_at, created_at, updated_at, status
		) VALUES ($1, $2, $3, $4, $5, $3, true, $6, $6, $6, 'Pending')
	`, owner, scope, sessionID, sessionmodel.ClaudeGUIMode, "session-"+sessionID, now); err != nil {
		t.Fatalf("insert session row: %v", err)
	}
	if status == "Active" {
		if _, err := pool.Exec(ctx, `
			UPDATE sessions SET status = 'Active', ready_at = $4, updated_at = $4
			WHERE email = $1 AND session_scope = $2 AND session_id = $3
		`, owner, scope, sessionID, now.Add(time.Second)); err != nil {
			t.Fatalf("mark session active: %v", err)
		}
	}
}

func TestPendingLaunchStoreDispatchRoundTrip(t *testing.T) {
	ctx, pool := newPendingLaunchTestPool(t)
	const (
		owner     = "nelson@romaine.life"
		scope     = "default"
		sessionID = "523"
		turnID    = "turn_launch_a"
	)
	insertSessionRow(t, ctx, pool, owner, scope, sessionID, "Active")
	st := NewPendingLaunchStore(pool, scope)
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)

	launch, err := st.Register(ctx, RegisterPendingLaunchRequest{
		SessionScope:    scope,
		SessionID:       sessionID,
		TurnID:          turnID,
		ClientNonce:     "nonce_a",
		OwnerEmail:      owner,
		Runtime:         "claude",
		SkillName:       "test",
		BasePrompt:      "do the thing",
		DisplayText:     "do the thing",
		Model:           "claude-opus-4-8",
		Effort:          "max",
		AttachmentCount: 2,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if launch.Status != PendingLaunchAwaitingBytes {
		t.Fatalf("status after register = %q, want awaiting_bytes", launch.Status)
	}

	// Not claimable before all bytes are staged.
	claimed, err := st.ClaimReady(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady (awaiting): %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d launches while awaiting bytes, want 0", len(claimed))
	}

	// Stage first of two — still awaiting.
	gotStatus, err := st.StageAttachment(ctx, storageKey, turnID, LaunchAttachmentBlob{
		Ordinal: 0, Name: "a.zip", ContentType: "application/zip", Size: 3, Bytes: []byte("aaa"),
	})
	if err != nil {
		t.Fatalf("StageAttachment 0: %v", err)
	}
	if gotStatus != PendingLaunchAwaitingBytes {
		t.Fatalf("status after 1/2 = %q, want awaiting_bytes", gotStatus)
	}

	// Stage second — flips to ready.
	gotStatus, err = st.StageAttachment(ctx, storageKey, turnID, LaunchAttachmentBlob{
		Ordinal: 1, Name: "b.png", ContentType: "image/png", Size: 4, Bytes: []byte("bbbb"),
	})
	if err != nil {
		t.Fatalf("StageAttachment 1: %v", err)
	}
	if gotStatus != PendingLaunchReady {
		t.Fatalf("status after 2/2 = %q, want ready", gotStatus)
	}

	// Now claimable; the join surfaces the Active session status.
	claimed, err = st.ClaimReady(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady (ready): %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d launches, want 1", len(claimed))
	}
	got := claimed[0]
	if got.TurnID != turnID || got.Status != PendingLaunchClaiming {
		t.Fatalf("claimed = {turn:%q status:%q}, want {%q claiming}", got.TurnID, got.Status, turnID)
	}
	if got.SessionStatus != "Active" || got.SessionTerminated {
		t.Fatalf("claimed session join = {status:%q terminated:%v}, want {Active false}", got.SessionStatus, got.SessionTerminated)
	}
	if got.BasePrompt != "do the thing" || got.SkillName != "test" || got.Model != "claude-opus-4-8" || got.Effort != "max" {
		t.Fatalf("dispatch params not preserved: %+v", got)
	}

	// A claimed launch refuses late byte writes.
	if _, err := st.StageAttachment(ctx, storageKey, turnID, LaunchAttachmentBlob{Ordinal: 2, Name: "c", Bytes: []byte("c")}); err != ErrPendingLaunchNotAcceptingBytes {
		t.Fatalf("StageAttachment after claim err = %v, want ErrPendingLaunchNotAcceptingBytes", err)
	}

	// Bytes round-trip in ordinal order.
	blobs, err := st.LoadAttachments(ctx, storageKey, turnID)
	if err != nil {
		t.Fatalf("LoadAttachments: %v", err)
	}
	if len(blobs) != 2 || blobs[0].Ordinal != 0 || !bytes.Equal(blobs[0].Bytes, []byte("aaa")) || !bytes.Equal(blobs[1].Bytes, []byte("bbbb")) {
		t.Fatalf("LoadAttachments mismatch: %+v", blobs)
	}

	// Dispatch deletes the staged bytes and records the dispatched turn id.
	if err := st.MarkDispatched(ctx, storageKey, turnID, turnID); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	after, err := st.Get(ctx, storageKey, turnID)
	if err != nil {
		t.Fatalf("Get after dispatch: %v", err)
	}
	if after.Status != PendingLaunchDispatched || after.DispatchedTurnID != turnID {
		t.Fatalf("after dispatch = {status:%q dispatched:%q}, want {dispatched %q}", after.Status, after.DispatchedTurnID, turnID)
	}
	blobs, err = st.LoadAttachments(ctx, storageKey, turnID)
	if err != nil {
		t.Fatalf("LoadAttachments after dispatch: %v", err)
	}
	if len(blobs) != 0 {
		t.Fatalf("staged bytes not cleared after dispatch: %d remain", len(blobs))
	}

	// A dispatched launch is no longer claimable.
	claimed, err = st.ClaimReady(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady (dispatched): %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d dispatched launches, want 0", len(claimed))
	}
}

func TestPendingLaunchStoreSkipsInactiveSession(t *testing.T) {
	ctx, pool := newPendingLaunchTestPool(t)
	const (
		owner     = "nelson@romaine.life"
		scope     = "default"
		sessionID = "600"
		turnID    = "turn_launch_pending_pod"
	)
	// Session exists but pod is not ready (status Pending).
	insertSessionRow(t, ctx, pool, owner, scope, sessionID, "Pending")
	st := NewPendingLaunchStore(pool, scope)
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)

	if _, err := st.Register(ctx, RegisterPendingLaunchRequest{
		SessionScope: scope, SessionID: sessionID, TurnID: turnID,
		OwnerEmail: owner, Runtime: "claude", BasePrompt: "x", AttachmentCount: 1,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := st.StageAttachment(ctx, storageKey, turnID, LaunchAttachmentBlob{Ordinal: 0, Name: "a", Bytes: []byte("a")}); err != nil {
		t.Fatalf("StageAttachment: %v", err)
	}
	// Ready, but the pod is not Active — the reconciler must not dispatch yet.
	claimed, err := st.ClaimReady(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d launches for a non-Active session, want 0", len(claimed))
	}
}

func TestPendingLaunchStoreFindStale(t *testing.T) {
	ctx, pool := newPendingLaunchTestPool(t)
	const (
		owner = "nelson@romaine.life"
		scope = "default"
	)
	insertSessionRow(t, ctx, pool, owner, scope, "700", "Active")
	insertSessionRow(t, ctx, pool, owner, scope, "701", "Active")
	st := NewPendingLaunchStore(pool, scope)

	// Two launches: one old + still awaiting_bytes (stuck), one fresh.
	for _, tc := range []struct{ session, turn string }{{"700", "turn_old"}, {"701", "turn_fresh"}} {
		if _, err := st.Register(ctx, RegisterPendingLaunchRequest{
			SessionScope: scope, SessionID: tc.session, TurnID: tc.turn,
			OwnerEmail: owner, Runtime: "claude", BasePrompt: "x", AttachmentCount: 1,
		}); err != nil {
			t.Fatalf("Register %s: %v", tc.turn, err)
		}
	}
	// Backdate the "old" one well past any deadline.
	if _, err := pool.Exec(ctx, `
		UPDATE session_pending_launch_turns SET created_at = now() - interval '1 hour'
		WHERE tank_session_id = $1 AND turn_id = 'turn_old'
	`, sessionmodel.SessionStorageKey(scope, "700")); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	stale, err := st.FindStale(ctx, time.Now().UTC().Add(-20*time.Minute), 100)
	if err != nil {
		t.Fatalf("FindStale: %v", err)
	}
	if len(stale) != 1 || stale[0].TurnID != "turn_old" {
		t.Fatalf("FindStale = %+v, want exactly turn_old", stale)
	}

	// A dispatched launch is never stale.
	if err := st.MarkDispatched(ctx, sessionmodel.SessionStorageKey(scope, "700"), "turn_old", "turn_old"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	stale, err = st.FindStale(ctx, time.Now().UTC().Add(-20*time.Minute), 100)
	if err != nil {
		t.Fatalf("FindStale after dispatch: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("FindStale after dispatch = %+v, want none", stale)
	}
}

func TestPendingLaunchStoreMarkFailedClearsBytes(t *testing.T) {
	ctx, pool := newPendingLaunchTestPool(t)
	const (
		owner     = "nelson@romaine.life"
		scope     = "default"
		sessionID = "601"
		turnID    = "turn_launch_fail"
	)
	insertSessionRow(t, ctx, pool, owner, scope, sessionID, "Active")
	st := NewPendingLaunchStore(pool, scope)
	storageKey := sessionmodel.SessionStorageKey(scope, sessionID)

	if _, err := st.Register(ctx, RegisterPendingLaunchRequest{
		SessionScope: scope, SessionID: sessionID, TurnID: turnID,
		OwnerEmail: owner, Runtime: "claude", BasePrompt: "x", AttachmentCount: 1,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := st.StageAttachment(ctx, storageKey, turnID, LaunchAttachmentBlob{Ordinal: 0, Name: "a", Bytes: []byte("a")}); err != nil {
		t.Fatalf("StageAttachment: %v", err)
	}
	if err := st.MarkFailed(ctx, storageKey, turnID, "materialization failed: pod gone"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	got, err := st.Get(ctx, storageKey, turnID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != PendingLaunchFailed || got.LastError == "" {
		t.Fatalf("after fail = {status:%q err:%q}, want {failed, non-empty}", got.Status, got.LastError)
	}
	blobs, err := st.LoadAttachments(ctx, storageKey, turnID)
	if err != nil {
		t.Fatalf("LoadAttachments: %v", err)
	}
	if len(blobs) != 0 {
		t.Fatalf("staged bytes not cleared after fail: %d remain", len(blobs))
	}
}
