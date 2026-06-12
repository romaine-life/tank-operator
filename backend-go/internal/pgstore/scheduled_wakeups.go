package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

type ScheduledWakeupStatus string

const (
	ScheduledWakeupScheduled ScheduledWakeupStatus = "scheduled"
	ScheduledWakeupClaiming  ScheduledWakeupStatus = "claiming"
	ScheduledWakeupFired     ScheduledWakeupStatus = "fired"
	ScheduledWakeupFailed    ScheduledWakeupStatus = "failed"
	ScheduledWakeupCancelled ScheduledWakeupStatus = "cancelled"
)

// MaxScheduledWakeupAttempts bounds futile fire retries, on the same scale as
// the launch dispatcher's maxLaunchDispatchAttempts. ClaimDue refuses rows at
// or over the cap, so a wake whose fire keeps half-finishing (claim landed but
// MarkFired/MarkFailed never did — a Postgres partial outage, a crash loop) is
// not reclaimed every stale tick forever. Without the cap an eventual re-fire
// can outlive the session bus's 24h msg-id dedupe window and run the same wake
// turn twice. Capped rows are not left in limbo: FailExceeded terminals them.
const MaxScheduledWakeupAttempts = 5

// scheduledWakeupAttemptCapError is the durable last_error stamped by
// FailExceeded. Prefix-stable: cmd/tank-operator folds it into the
// attempt_cap_exceeded fire-failure metric label.
const scheduledWakeupAttemptCapError = "attempt_cap_exceeded"

type ScheduledWakeup struct {
	WakeupID          string
	SessionScope      string
	SessionID         string
	TankSessionID     string
	OwnerEmail        string
	Provider          string
	Prompt            string
	ClientNonce       string
	ScheduledTurnID   string
	ProviderItemID    string
	ScheduledAt       time.Time
	DueAt             time.Time
	Status            ScheduledWakeupStatus
	AttemptCount      int
	FiredTurnID       string
	LastError         string
	SessionStatus     string
	SessionTerminated bool
}

type RegisterScheduledWakeupRequest struct {
	SessionScope    string
	SessionID       string
	OwnerEmail      string
	Provider        string
	Prompt          string
	ScheduledTurnID string
	ProviderItemID  string
	ScheduledAt     time.Time
	DueAt           time.Time
}

type ScheduledWakeupStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewScheduledWakeupStore(pool *pgxpool.Pool, scope string) *ScheduledWakeupStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &ScheduledWakeupStore{pool: pool, scope: scope}
}

func ScheduledWakeupID(tankSessionID, provider, providerItemID string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tankSessionID),
		strings.TrimSpace(provider),
		strings.TrimSpace(providerItemID),
	}, "\x1f")))
	return "wakeup_" + hex.EncodeToString(h[:])[:32]
}

func ScheduledWakeupClientNonce(wakeupID string) string {
	clean := strings.TrimSpace(wakeupID)
	if clean == "" {
		return ""
	}
	return "schedule_wakeup-" + clean
}

func (s *ScheduledWakeupStore) Register(ctx context.Context, req RegisterScheduledWakeupRequest) (ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return ScheduledWakeup{}, errors.New("scheduled wakeup store unavailable")
	}
	req.SessionScope = strings.TrimSpace(req.SessionScope)
	if req.SessionScope == "" {
		req.SessionScope = s.scope
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	req.Provider = strings.TrimSpace(req.Provider)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ScheduledTurnID = strings.TrimSpace(req.ScheduledTurnID)
	req.ProviderItemID = strings.TrimSpace(req.ProviderItemID)
	req.ScheduledAt = req.ScheduledAt.UTC()
	req.DueAt = req.DueAt.UTC()
	tankSessionID := sessionmodel.SessionStorageKey(req.SessionScope, req.SessionID)
	wakeupID := ScheduledWakeupID(tankSessionID, req.Provider, req.ProviderItemID)
	clientNonce := ScheduledWakeupClientNonce(wakeupID)

	const q = `
		INSERT INTO session_scheduled_wakeups (
			wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, scheduled_turn_id, provider_item_id,
			scheduled_at, due_at, status, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, 'scheduled', now()
		)
		ON CONFLICT (wakeup_id) DO UPDATE
		SET updated_at = session_scheduled_wakeups.updated_at
		RETURNING wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, scheduled_turn_id, provider_item_id,
			scheduled_at, due_at, status, attempt_count, fired_turn_id, last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated
	`
	return scanScheduledWakeup(s.pool.QueryRow(ctx, q,
		wakeupID, req.SessionScope, req.SessionID, tankSessionID, req.OwnerEmail,
		req.Provider, req.Prompt, clientNonce, req.ScheduledTurnID, req.ProviderItemID,
		req.ScheduledAt, req.DueAt,
	))
}

func (s *ScheduledWakeupStore) ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("scheduled wakeup store unavailable")
	}
	if limit <= 0 {
		limit = 25
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	const q = `
		WITH due AS (
			SELECT wakeup_id
			FROM session_scheduled_wakeups
			WHERE session_scope = $1
			  AND due_at <= $2
			  AND attempt_count < $5
			  AND (
			    status = 'scheduled'
			    OR (status = 'claiming' AND locked_at < $2 - make_interval(secs => $4::double precision))
			  )
			ORDER BY due_at ASC, created_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE session_scheduled_wakeups sw
		SET status = 'claiming',
			attempt_count = sw.attempt_count + 1,
			locked_at = $2,
			updated_at = now()
		FROM due
		WHERE sw.wakeup_id = due.wakeup_id
		RETURNING sw.wakeup_id, sw.session_scope, sw.session_id, sw.tank_session_id, sw.owner_email,
			sw.provider, sw.prompt, sw.client_nonce, sw.scheduled_turn_id, sw.provider_item_id,
			sw.scheduled_at, sw.due_at, sw.status, sw.attempt_count, sw.fired_turn_id, sw.last_error,
			COALESCE((SELECT status FROM sessions sess
				WHERE sess.email = sw.owner_email AND sess.session_scope = sw.session_scope AND sess.session_id = sw.session_id), '') AS session_status,
			COALESCE((SELECT terminating_at IS NOT NULL FROM sessions sess
				WHERE sess.email = sw.owner_email AND sess.session_scope = sw.session_scope AND sess.session_id = sw.session_id), true) AS session_terminated
	`
	rows, err := s.pool.Query(ctx, q, s.scope, now.UTC(), limit, staleAfter.Seconds(), MaxScheduledWakeupAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledWakeup
	for rows.Next() {
		row, err := scanScheduledWakeup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// FailExceeded gives a durable 'failed' terminal to wakes whose attempt count
// reached MaxScheduledWakeupAttempts without MarkFired/MarkFailed ever
// landing. ClaimDue stops claiming such rows, so without this pass they would
// sit in 'claiming' limbo forever — still "pending" to HasPending and the
// activity fold, invisible to the user. Only rows that are demonstrably not
// in-flight are touched: a 'claiming' row must also be stale (locked_at older
// than staleAfter), so a capped final attempt still mid-fire is left to finish;
// 'scheduled' rows at the cap (no live claim at all — a shape pre-cap data or
// a future release path could produce) terminal immediately. Returns the
// terminaled snapshots so the fire loop can run the same post-failure
// bookkeeping as MarkFailed (durable wake event + away-error ring + activity
// refresh).
func (s *ScheduledWakeupStore) FailExceeded(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("scheduled wakeup store unavailable")
	}
	if limit <= 0 {
		limit = 25
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	const q = `
		WITH exceeded AS (
			SELECT wakeup_id
			FROM session_scheduled_wakeups
			WHERE session_scope = $1
			  AND attempt_count >= $5
			  AND (
			    status = 'scheduled'
			    OR (status = 'claiming' AND locked_at < $2::timestamptz - make_interval(secs => $4::double precision))
			  )
			ORDER BY due_at ASC, created_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE session_scheduled_wakeups sw
		SET status = 'failed',
			last_error = left($6 || ': gave up after ' || sw.attempt_count || ' attempts', 2000),
			locked_at = NULL,
			updated_at = now()
		FROM exceeded
		WHERE sw.wakeup_id = exceeded.wakeup_id
		RETURNING sw.wakeup_id, sw.session_scope, sw.session_id, sw.tank_session_id, sw.owner_email,
			sw.provider, sw.prompt, sw.client_nonce, sw.scheduled_turn_id, sw.provider_item_id,
			sw.scheduled_at, sw.due_at, sw.status, sw.attempt_count, sw.fired_turn_id, sw.last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated
	`
	rows, err := s.pool.Query(ctx, q, s.scope, now.UTC(), limit, staleAfter.Seconds(),
		MaxScheduledWakeupAttempts, scheduledWakeupAttemptCapError)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledWakeup
	for rows.Next() {
		row, err := scanScheduledWakeup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *ScheduledWakeupStore) ListBySession(ctx context.Context, sessionScope, sessionID string) ([]ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("scheduled wakeup store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("missing session_id")
	}
	const q = `
		SELECT wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, scheduled_turn_id, provider_item_id,
			scheduled_at, due_at, status, attempt_count, fired_turn_id, last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated
		FROM session_scheduled_wakeups
		WHERE session_scope = $1
		  AND session_id = $2
		ORDER BY
		  CASE WHEN status IN ('scheduled', 'claiming') THEN 0 ELSE 1 END,
		  due_at ASC,
		  scheduled_at ASC
	`
	rows, err := s.pool.Query(ctx, q, sessionScope, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledWakeup
	for rows.Next() {
		row, err := scanScheduledWakeup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *ScheduledWakeupStore) MarkFired(ctx context.Context, wakeupID, turnID string) (ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return ScheduledWakeup{}, errors.New("scheduled wakeup store unavailable")
	}
	return scanScheduledWakeup(s.pool.QueryRow(ctx, `
		UPDATE session_scheduled_wakeups
		SET status = 'fired',
			fired_turn_id = $2,
			last_error = '',
			locked_at = NULL,
			updated_at = now(),
			fired_at = now()
		WHERE wakeup_id = $1
		RETURNING wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, scheduled_turn_id, provider_item_id,
			scheduled_at, due_at, status, attempt_count, fired_turn_id, last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated
	`, strings.TrimSpace(wakeupID), strings.TrimSpace(turnID)))
}

func (s *ScheduledWakeupStore) MarkFailed(ctx context.Context, wakeupID, reason string) (ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return ScheduledWakeup{}, errors.New("scheduled wakeup store unavailable")
	}
	return scanScheduledWakeup(s.pool.QueryRow(ctx, `
		UPDATE session_scheduled_wakeups
		SET status = 'failed',
			last_error = left($2, 2000),
			locked_at = NULL,
			updated_at = now()
		WHERE wakeup_id = $1
		RETURNING wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, scheduled_turn_id, provider_item_id,
			scheduled_at, due_at, status, attempt_count, fired_turn_id, last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated
	`, strings.TrimSpace(wakeupID), strings.TrimSpace(reason)))
}

// ReleaseRetainingAttempt returns a claimed wakeup to 'scheduled' without
// firing or failing it, KEEPING the claim's attempt_count bump. It is the
// bounded-defer path for a session that exists but is transiently not Active —
// any kubelet probe blip flips the durable session row Active → Pending
// (sessioncontroller writer), and terminal-failing the wake on that blip
// permanently killed the agent's continuation. The wake must instead survive
// the blip; but a session that never recovers must not defer invisibly
// forever, so each deferred claim still burns a fire attempt and
// MaxScheduledWakeupAttempts bounds the deferral — FailExceeded then terminals
// the wake WITH the away-error ring. Contrast BackgroundTaskWakeStore.Release,
// which refunds the attempt for turn-coupled defers whose resolution is owned
// by the turn lifecycle itself.
func (s *ScheduledWakeupStore) ReleaseRetainingAttempt(ctx context.Context, wakeupID string) error {
	if s == nil || s.pool == nil {
		return errors.New("scheduled wakeup store unavailable")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE session_scheduled_wakeups
		SET status = 'scheduled',
			locked_at = NULL,
			updated_at = now()
		WHERE wakeup_id = $1
	`, strings.TrimSpace(wakeupID))
	return err
}

func (s *ScheduledWakeupStore) ScheduledDueCount(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("scheduled wakeup store unavailable")
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM session_scheduled_wakeups
		WHERE session_scope = $1
		  AND status IN ('scheduled', 'claiming')
		  AND due_at <= $2
	`, s.scope, now.UTC()).Scan(&count)
	return count, err
}

// HasPending reports whether the session has a registered wakeup not yet fired,
// failed, or cancelled — self-scheduled work the agent is parked waiting on. It
// is the durable authority for the non-summoning "scheduled" activity status
// (docs/scheduled-turn-continuity.md): a session with a pending wake is
// mid-(simulated)-turn, not idle, so a turn terminal folds to "scheduled"
// instead of the summoning "ready".
func (s *ScheduledWakeupStore) HasPending(ctx context.Context, sessionScope, sessionID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("scheduled wakeup store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	var pending bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM session_scheduled_wakeups
			WHERE session_scope = $1
			  AND session_id = $2
			  AND status IN ('scheduled', 'claiming')
		)
	`, sessionScope, sessionID).Scan(&pending)
	return pending, err
}

// CancelPendingForSession marks every still-pending wakeup for the session
// 'cancelled' — a terminal that leaves the wake non-pending without 'failed”s
// error semantics (a cancel must not ring or read as an error). Used by the
// explicit cancel control and the prompt-mid-sleep take-over (a user turn to a
// parked session). Returns the cancelled row snapshots.
func (s *ScheduledWakeupStore) CancelPendingForSession(ctx context.Context, sessionScope, sessionID string) ([]ScheduledWakeup, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("scheduled wakeup store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE session_scheduled_wakeups
		SET status = 'cancelled', locked_at = NULL, updated_at = now()
		WHERE session_scope = $1 AND session_id = $2 AND status IN ('scheduled', 'claiming')
		RETURNING wakeup_id, session_scope, session_id, tank_session_id, owner_email,
			provider, prompt, client_nonce, scheduled_turn_id, provider_item_id,
			scheduled_at, due_at, status, attempt_count, fired_turn_id, last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated
	`, sessionScope, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledWakeup
	for rows.Next() {
		row, err := scanScheduledWakeup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type scheduledWakeupScanner interface {
	Scan(dest ...any) error
}

func scanScheduledWakeup(row scheduledWakeupScanner) (ScheduledWakeup, error) {
	var out ScheduledWakeup
	var status string
	var sessionStatus *string
	var sessionTerminated *bool
	err := row.Scan(
		&out.WakeupID,
		&out.SessionScope,
		&out.SessionID,
		&out.TankSessionID,
		&out.OwnerEmail,
		&out.Provider,
		&out.Prompt,
		&out.ClientNonce,
		&out.ScheduledTurnID,
		&out.ProviderItemID,
		&out.ScheduledAt,
		&out.DueAt,
		&status,
		&out.AttemptCount,
		&out.FiredTurnID,
		&out.LastError,
		&sessionStatus,
		&sessionTerminated,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduledWakeup{}, err
	}
	if err != nil {
		return ScheduledWakeup{}, err
	}
	out.Status = ScheduledWakeupStatus(status)
	if sessionStatus != nil {
		out.SessionStatus = *sessionStatus
	}
	if sessionTerminated != nil {
		out.SessionTerminated = *sessionTerminated
	}
	return out, nil
}
