package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// BackgroundTaskWakeStatus mirrors the scheduled-wakeup lifecycle. A wake is
// 'scheduled' the moment the runner registers a finished background task,
// briefly 'claiming' while a fire loop tick holds it, then 'fired' once the
// orchestrator has enqueued the durable turn, or 'failed' when the session is
// gone before the wake could fire.
type BackgroundTaskWakeStatus string

const (
	BackgroundTaskWakeScheduled BackgroundTaskWakeStatus = "scheduled"
	BackgroundTaskWakeClaiming  BackgroundTaskWakeStatus = "claiming"
	BackgroundTaskWakeFired     BackgroundTaskWakeStatus = "fired"
	BackgroundTaskWakeFailed    BackgroundTaskWakeStatus = "failed"
	BackgroundTaskWakeCancelled BackgroundTaskWakeStatus = "cancelled"
)

type BackgroundTaskWake struct {
	WakeID            string
	SessionScope      string
	SessionID         string
	TankSessionID     string
	OwnerEmail        string
	Provider          string
	TaskID            string
	TaskStatus        string
	Prompt            string
	ClientNonce       string
	RegisteredAt      time.Time
	DueAt             time.Time
	Status            BackgroundTaskWakeStatus
	AttemptCount      int
	FiredTurnID       string
	LastError         string
	SessionStatus     string
	SessionTerminated bool
	SessionNeedsInput bool
}

type RegisterBackgroundTaskWakeRequest struct {
	SessionScope string
	SessionID    string
	OwnerEmail   string
	Provider     string
	TaskID       string
	TaskStatus   string
	Prompt       string
	RegisteredAt time.Time
}

type BackgroundTaskWakeStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewBackgroundTaskWakeStore(pool *pgxpool.Pool, scope string) *BackgroundTaskWakeStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &BackgroundTaskWakeStore{pool: pool, scope: scope}
}

// BackgroundTaskWakeID is the idempotency key: one finished background task
// yields at most one wake row per (session, provider, task).
func BackgroundTaskWakeID(tankSessionID, provider, taskID string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tankSessionID),
		strings.TrimSpace(provider),
		strings.TrimSpace(taskID),
	}, "\x1f")))
	return "bgwake_" + hex.EncodeToString(h[:])[:32]
}

var backgroundTaskWakeNonceSafe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// BackgroundTaskWakeClientNonce derives a deterministic turn client_nonce from
// the task id. The nonce becomes the turn id (turnIDForClientNonce), so it must
// satisfy the backend turnIDPattern (^[A-Za-z0-9._-]{1,80}$). Claude background
// task ids elsewhere are validated by the wider backgroundTaskIDPattern (allows
// ':' and up to 160 chars), so when the raw id is not turn-id-safe or would
// overflow the length cap we fall back to a sha256 prefix. Determinism in
// task_id is what makes the nonce a real idempotency key alongside wake_id.
func BackgroundTaskWakeClientNonce(taskID string) string {
	clean := strings.TrimSpace(taskID)
	if clean == "" {
		return ""
	}
	if backgroundTaskWakeNonceSafe.MatchString(clean) && len("bgtask-"+clean) <= 80 {
		return "bgtask-" + clean
	}
	h := sha256.Sum256([]byte(clean))
	return "bgtask-" + hex.EncodeToString(h[:])[:32]
}

func (s *BackgroundTaskWakeStore) Register(ctx context.Context, req RegisterBackgroundTaskWakeRequest) (BackgroundTaskWake, error) {
	if s == nil || s.pool == nil {
		return BackgroundTaskWake{}, errors.New("background task wake store unavailable")
	}
	req.SessionScope = strings.TrimSpace(req.SessionScope)
	if req.SessionScope == "" {
		req.SessionScope = s.scope
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	req.Provider = strings.TrimSpace(req.Provider)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.TaskStatus = strings.TrimSpace(req.TaskStatus)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.RegisteredAt.IsZero() {
		req.RegisteredAt = time.Now()
	}
	req.RegisteredAt = req.RegisteredAt.UTC()
	tankSessionID := sessionmodel.SessionStorageKey(req.SessionScope, req.SessionID)
	wakeID := BackgroundTaskWakeID(tankSessionID, req.Provider, req.TaskID)
	clientNonce := BackgroundTaskWakeClientNonce(req.TaskID)

	// ON CONFLICT is a pure no-op (re-registering the same finished task is
	// ignored, never resurrecting a fired/failed row). This is the durable
	// idempotency the in-process Set cannot provide across runner restart.
	const q = `
		INSERT INTO session_background_task_wakes (
			wake_id, session_scope, session_id, tank_session_id, owner_email,
			provider, task_id, task_status, prompt, client_nonce,
			registered_at, due_at, status, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $11, 'scheduled', now()
		)
		ON CONFLICT (wake_id) DO UPDATE
		SET updated_at = session_background_task_wakes.updated_at
		RETURNING wake_id, session_scope, session_id, tank_session_id, owner_email,
			provider, task_id, task_status, prompt, client_nonce,
			registered_at, due_at, status, attempt_count, fired_turn_id, last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated,
			NULL::boolean AS session_needs_input
	`
	return scanBackgroundTaskWake(s.pool.QueryRow(ctx, q,
		wakeID, req.SessionScope, req.SessionID, tankSessionID, req.OwnerEmail,
		req.Provider, req.TaskID, req.TaskStatus, req.Prompt, clientNonce,
		req.RegisteredAt,
	))
}

func (s *BackgroundTaskWakeStore) ClaimDue(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]BackgroundTaskWake, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("background task wake store unavailable")
	}
	if limit <= 0 {
		limit = 25
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	const q = `
		WITH due AS (
			SELECT wake_id
			FROM session_background_task_wakes
			WHERE session_scope = $1
			  AND due_at <= $2
			  AND (
			    status = 'scheduled'
			    OR (status = 'claiming' AND locked_at < $2 - make_interval(secs => $4::double precision))
			  )
			ORDER BY due_at ASC, created_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE session_background_task_wakes bw
		SET status = 'claiming',
			attempt_count = bw.attempt_count + 1,
			locked_at = $2,
			updated_at = now()
		FROM due
		WHERE bw.wake_id = due.wake_id
		RETURNING bw.wake_id, bw.session_scope, bw.session_id, bw.tank_session_id, bw.owner_email,
			bw.provider, bw.task_id, bw.task_status, bw.prompt, bw.client_nonce,
			bw.registered_at, bw.due_at, bw.status, bw.attempt_count, bw.fired_turn_id, bw.last_error,
			COALESCE((SELECT status FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), '') AS session_status,
			COALESCE((SELECT terminating_at IS NOT NULL FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), true) AS session_terminated,
			COALESCE((SELECT (activity_summary->>'needs_input')::boolean FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), false) AS session_needs_input
	`
	rows, err := s.pool.Query(ctx, q, s.scope, now.UTC(), limit, staleAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackgroundTaskWake
	for rows.Next() {
		row, err := scanBackgroundTaskWake(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *BackgroundTaskWakeStore) MarkFired(ctx context.Context, wakeID, turnID string) error {
	if s == nil || s.pool == nil {
		return errors.New("background task wake store unavailable")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE session_background_task_wakes
		SET status = 'fired',
			fired_turn_id = $2,
			last_error = '',
			locked_at = NULL,
			updated_at = now(),
			fired_at = now()
		WHERE wake_id = $1
	`, strings.TrimSpace(wakeID), strings.TrimSpace(turnID))
	return err
}

func (s *BackgroundTaskWakeStore) MarkFailed(ctx context.Context, wakeID, reason string) error {
	if s == nil || s.pool == nil {
		return errors.New("background task wake store unavailable")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE session_background_task_wakes
		SET status = 'failed',
			last_error = left($2, 2000),
			locked_at = NULL,
			updated_at = now()
		WHERE wake_id = $1
	`, strings.TrimSpace(wakeID), strings.TrimSpace(reason))
	return err
}

// Release returns a claimed wake to 'scheduled' without firing or failing it,
// undoing the claim's attempt_count bump. It is the soft-defer path: when a
// due wake's session is mid-turn or awaiting an AskUserQuestion answer, firing
// would clobber that turn, so the loop releases the claim and retries on a
// later tick once the session is idle again.
func (s *BackgroundTaskWakeStore) Release(ctx context.Context, wakeID string) error {
	if s == nil || s.pool == nil {
		return errors.New("background task wake store unavailable")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE session_background_task_wakes
		SET status = 'scheduled',
			locked_at = NULL,
			attempt_count = GREATEST(attempt_count - 1, 0),
			updated_at = now()
		WHERE wake_id = $1
	`, strings.TrimSpace(wakeID))
	return err
}

func (s *BackgroundTaskWakeStore) DueCount(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("background task wake store unavailable")
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM session_background_task_wakes
		WHERE session_scope = $1
		  AND status IN ('scheduled', 'claiming')
		  AND due_at <= $2
	`, s.scope, now.UTC()).Scan(&count)
	return count, err
}

// HasPending reports whether the session has a registered background-task wake
// not yet fired or failed — the agent ended a turn but a run_in_background task
// it spawned has not re-invoked it. Like ScheduledWakeupStore.HasPending, this
// is durable authority for the non-summoning "scheduled" activity status
// (docs/scheduled-turn-continuity.md): the agent is parked waiting on its own
// background work, so the session is mid-(simulated)-turn, not idle.
func (s *BackgroundTaskWakeStore) HasPending(ctx context.Context, sessionScope, sessionID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("background task wake store unavailable")
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
			SELECT 1 FROM session_background_task_wakes
			WHERE session_scope = $1
			  AND session_id = $2
			  AND status IN ('scheduled', 'claiming')
		)
	`, sessionScope, sessionID).Scan(&pending)
	return pending, err
}

// CancelPendingForSession marks every still-pending background-task wake for the
// session 'cancelled', mirroring ScheduledWakeupStore.CancelPendingForSession:
// it leaves the wake non-pending without the error semantics of 'failed'. Used
// by the explicit cancel control and the prompt-mid-sleep take-over. Returns the
// number cancelled.
func (s *BackgroundTaskWakeStore) CancelPendingForSession(ctx context.Context, sessionScope, sessionID string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("background task wake store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE session_background_task_wakes
		SET status = 'cancelled', locked_at = NULL, updated_at = now()
		WHERE session_scope = $1 AND session_id = $2 AND status IN ('scheduled', 'claiming')
	`, sessionScope, sessionID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

type backgroundTaskWakeScanner interface {
	Scan(dest ...any) error
}

func scanBackgroundTaskWake(row backgroundTaskWakeScanner) (BackgroundTaskWake, error) {
	var out BackgroundTaskWake
	var status string
	var sessionStatus *string
	var sessionTerminated *bool
	var sessionNeedsInput *bool
	err := row.Scan(
		&out.WakeID,
		&out.SessionScope,
		&out.SessionID,
		&out.TankSessionID,
		&out.OwnerEmail,
		&out.Provider,
		&out.TaskID,
		&out.TaskStatus,
		&out.Prompt,
		&out.ClientNonce,
		&out.RegisteredAt,
		&out.DueAt,
		&status,
		&out.AttemptCount,
		&out.FiredTurnID,
		&out.LastError,
		&sessionStatus,
		&sessionTerminated,
		&sessionNeedsInput,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackgroundTaskWake{}, err
	}
	if err != nil {
		return BackgroundTaskWake{}, err
	}
	out.Status = BackgroundTaskWakeStatus(status)
	if sessionStatus != nil {
		out.SessionStatus = *sessionStatus
	}
	if sessionTerminated != nil {
		out.SessionTerminated = *sessionTerminated
	}
	if sessionNeedsInput != nil {
		out.SessionNeedsInput = *sessionNeedsInput
	}
	return out, nil
}
