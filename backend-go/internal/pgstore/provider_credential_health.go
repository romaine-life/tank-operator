package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderCredentialHealth is the durable Layer 1 model for whether a
// provider's OAuth blob (host-wide today, per-user when that lands) is
// currently usable. It's the source of truth the orchestrator reads
// when deciding whether to emit a session.status:failed banner into a
// session's durable transcript ledger; the SPA renders that banner
// through the existing session.status rendering path. See
// docs/features/transcript/contract.md ("session.status events own
// startup notices") for the surface and docs/quality-timeframes.md
// ("durable state over process memory") for why the row drives both
// detection and rendering rather than an in-process flag.
type ProviderCredentialHealth struct {
	Provider        string
	OwnerScope      string
	Status          string
	Reason          string
	Text            string
	ActionLabel     string
	ActionHref      string
	DetectedAt      time.Time
	LastAttemptedAt time.Time
	LastSucceededAt *time.Time
	RowVersion      int64
}

const (
	ProviderHealthStatusHealthy  = "healthy"
	ProviderHealthStatusDegraded = "degraded"
	ProviderHealthStatusFailed   = "failed"

	// OwnerScopeHost is the per-deployment OAuth blob scope. The
	// schema is multi-scope so a future per-user OAuth path can use
	// "user:<email>" without a migration; today every row is "host".
	OwnerScopeHost = "host"
)

var (
	ErrProviderCredentialHealthNotFound = errors.New("provider credential health row not found")
	ErrProviderCredentialHealthStale    = errors.New("provider credential health row_version stale")
)

type ProviderCredentialHealthStore struct {
	pool *pgxpool.Pool
}

func NewProviderCredentialHealthStore(pool *pgxpool.Pool) *ProviderCredentialHealthStore {
	return &ProviderCredentialHealthStore{pool: pool}
}

// Get returns the row for (provider, scope) or ErrProviderCredentialHealthNotFound
// when nothing has been written yet. A missing row is treated by callers
// as "healthy unknown" — the orchestrator does not fan out a banner
// from absence; only an explicit "failed" row triggers events.
func (s *ProviderCredentialHealthStore) Get(ctx context.Context, provider, scope string) (ProviderCredentialHealth, error) {
	if s == nil || s.pool == nil {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health store not configured")
	}
	provider = normalizeProviderHealthKey(provider)
	scope = normalizeProviderHealthKey(scope)
	if provider == "" || scope == "" {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: provider and scope are required")
	}
	row := ProviderCredentialHealth{Provider: provider, OwnerScope: scope}
	err := s.pool.QueryRow(ctx, `
		SELECT status, reason, text, action_label, action_href,
		       detected_at, last_attempted_at, last_succeeded_at, row_version
		FROM provider_credential_health
		WHERE provider = $1 AND owner_scope = $2
	`, provider, scope).Scan(
		&row.Status, &row.Reason, &row.Text, &row.ActionLabel, &row.ActionHref,
		&row.DetectedAt, &row.LastAttemptedAt, &row.LastSucceededAt, &row.RowVersion,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProviderCredentialHealth{}, ErrProviderCredentialHealthNotFound
		}
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: get: %w", err)
	}
	return row, nil
}

// UpsertTransition writes the row for (provider, scope) and returns the
// post-write row (with row_version incremented when the persisted row
// changed). The expectedVersion parameter is the row_version the caller
// believed it was overwriting; pass -1 to skip the optimistic-concurrency
// check (first-write path). When expectedVersion >= 0 and disagrees with
// the persisted row, returns ErrProviderCredentialHealthStale — the
// caller should re-read and decide whether to retry.
//
// The orchestrator's debouncer uses the optimistic check so two replicas
// observing the same proxy transition only fan out session.status events
// once: whichever wins the version bump publishes, the loser sees Stale
// and drops the fan-out.
func (s *ProviderCredentialHealthStore) UpsertTransition(ctx context.Context, row ProviderCredentialHealth, expectedVersion int64) (ProviderCredentialHealth, error) {
	if s == nil || s.pool == nil {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health store not configured")
	}
	row.Provider = normalizeProviderHealthKey(row.Provider)
	row.OwnerScope = normalizeProviderHealthKey(row.OwnerScope)
	if row.Provider == "" || row.OwnerScope == "" {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: provider and scope are required")
	}
	if !isProviderHealthStatus(row.Status) {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: status %q must be one of healthy/degraded/failed", row.Status)
	}
	if row.DetectedAt.IsZero() {
		row.DetectedAt = time.Now().UTC()
	}
	if row.LastAttemptedAt.IsZero() {
		row.LastAttemptedAt = row.DetectedAt
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: begin upsert: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentVersion int64 = -1
	err = tx.QueryRow(ctx, `
		SELECT row_version FROM provider_credential_health
		WHERE provider = $1 AND owner_scope = $2
		FOR UPDATE
	`, row.Provider, row.OwnerScope).Scan(&currentVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: select for update: %w", err)
	}
	if expectedVersion >= 0 && currentVersion >= 0 && currentVersion != expectedVersion {
		return ProviderCredentialHealth{}, ErrProviderCredentialHealthStale
	}
	nextVersion := currentVersion + 1
	if currentVersion < 0 {
		nextVersion = 1
	}
	row.RowVersion = nextVersion

	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_credential_health (
			provider, owner_scope, status, reason, text,
			action_label, action_href,
			detected_at, last_attempted_at, last_succeeded_at, row_version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (provider, owner_scope) DO UPDATE SET
			status            = EXCLUDED.status,
			reason            = EXCLUDED.reason,
			text              = EXCLUDED.text,
			action_label      = EXCLUDED.action_label,
			action_href       = EXCLUDED.action_href,
			detected_at       = EXCLUDED.detected_at,
			last_attempted_at = EXCLUDED.last_attempted_at,
			last_succeeded_at = EXCLUDED.last_succeeded_at,
			row_version       = EXCLUDED.row_version
	`,
		row.Provider, row.OwnerScope, row.Status, row.Reason, row.Text,
		row.ActionLabel, row.ActionHref,
		row.DetectedAt, row.LastAttemptedAt, row.LastSucceededAt, row.RowVersion,
	); err != nil {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: upsert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ProviderCredentialHealth{}, fmt.Errorf("provider credential health: commit: %w", err)
	}
	return row, nil
}

func normalizeProviderHealthKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isProviderHealthStatus(value string) bool {
	switch value {
	case ProviderHealthStatusHealthy,
		ProviderHealthStatusDegraded,
		ProviderHealthStatusFailed:
		return true
	}
	return false
}
