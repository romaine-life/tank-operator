package sessioncontroller

// DSN-gated integration coverage for the activity_summary stale-write
// guard. Activity refreshes are concurrent read-fold-write cycles with no
// transaction spanning the read and the write (per-event persister workers
// on two replicas, the read-state HTTP path, wake/cancel paths); unguarded,
// a refresh that folded an older ledger tail could land last and durably
// overwrite a terminal status — the "stuck working forever" sidebar class,
// and a background-wake fire gate that defers forever on the stale status.
// The guard: only a summary derived from an equal-or-newer last_order_key
// may replace the stored one.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type nopRowEmitter struct{}

func (nopRowEmitter) PublishCurrentRow(context.Context, string, string) {}

type countingRowWriterMetrics struct {
	updates    int
	failures   int
	superseded int
}

func (m *countingRowWriterMetrics) RecordRowUpdate(string)            { m.updates++ }
func (m *countingRowWriterMetrics) RecordRowUpdateFailure(string)     { m.failures++ }
func (m *countingRowWriterMetrics) RecordRowActivityWriteSuperseded() { m.superseded++ }

func TestRowWriterActivityStaleWriteGuard(t *testing.T) {
	dsn := os.Getenv("TANK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TANK_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("tank_activity_cas_%d", time.Now().UnixNano())
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
		t.Fatalf("parse dsn: %v", err)
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
	if err := pgstore.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	owner := "user@example.com"
	scope := "default"
	sessionID := "77"
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (
			email, session_scope, session_id, mode, pod_name, name, visible,
			requested_at, created_at, updated_at, status
		) VALUES ($1, $2, $3, 'claude_gui', 'session-77', $3, true, $4, $4, $4, 'Active')
	`, owner, scope, sessionID, now); err != nil {
		t.Fatalf("insert session row: %v", err)
	}

	metrics := &countingRowWriterMetrics{}
	writer := &RowWriter{Emitter: nopRowEmitter{}, Pool: pool, Metrics: metrics}

	writeSummary := func(status, lastOrderKey string) {
		t.Helper()
		payload := map[string]any{"status": status}
		if lastOrderKey != "" {
			payload["last_order_key"] = lastOrderKey
		}
		if _, err := writer.RecordTransition(ctx, Event{
			Email:        owner,
			SessionScope: scope,
			SessionID:    sessionID,
			Type:         EventTypeActivityChanged,
			OccurredAt:   now.Format(time.RFC3339Nano),
			Payload:      payload,
		}); err != nil {
			t.Fatalf("record %s/%s: %v", status, lastOrderKey, err)
		}
	}
	storedStatus := func() string {
		t.Helper()
		var status string
		if err := pool.QueryRow(ctx, `
			SELECT activity_summary ->> 'status' FROM sessions
			WHERE email = $1 AND session_scope = $2 AND session_id = $3
		`, owner, scope, sessionID).Scan(&status); err != nil {
			t.Fatalf("read stored summary: %v", err)
		}
		return status
	}

	// k2 lands first (the terminal-bearing refresh).
	writeSummary("ready", "0000000000200-00000001-b")
	if got := storedStatus(); got != "ready" {
		t.Fatalf("stored status = %q, want ready", got)
	}

	// A stale concurrent refresh that folded only up to k1 must be dropped
	// — this is the exact lost-update that durably stranded "streaming".
	writeSummary("streaming", "0000000000100-00000001-a")
	if got := storedStatus(); got != "ready" {
		t.Fatalf("stale write overwrote terminal: stored = %q, want ready", got)
	}
	if metrics.superseded != 1 {
		t.Fatalf("superseded metric = %d, want 1", metrics.superseded)
	}

	// Equal key must pass: read-state refreshes recompute unread against
	// the same ledger tail and must still be able to update the summary.
	writeSummary("needs_input", "0000000000200-00000001-b")
	if got := storedStatus(); got != "needs_input" {
		t.Fatalf("equal-key write rejected: stored = %q, want needs_input", got)
	}

	// Newer key advances normally.
	writeSummary("working", "0000000000300-00000001-c")
	if got := storedStatus(); got != "working" {
		t.Fatalf("newer write rejected: stored = %q, want working", got)
	}

	// A keyless summary (fold saw no events) may never replace a keyed one.
	writeSummary("ready", "")
	if got := storedStatus(); got != "working" {
		t.Fatalf("keyless write replaced keyed summary: stored = %q, want working", got)
	}
	if metrics.superseded != 2 {
		t.Fatalf("superseded metric = %d, want 2", metrics.superseded)
	}
}
