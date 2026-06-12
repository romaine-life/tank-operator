package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
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
	WakeID                string
	SessionScope          string
	SessionID             string
	TankSessionID         string
	OwnerEmail            string
	Provider              string
	TaskID                string
	TaskStatus            string
	TaskDescription       string
	TaskSummary           string
	TaskLastTool          string
	TaskError             string
	ObservedEventID       string
	Generation            int
	ClientNonce           string
	RegisteredAt          time.Time
	DueAt                 time.Time
	Status                BackgroundTaskWakeStatus
	AttemptCount          int
	FiredTurnID           string
	LastError             string
	SessionStatus         string
	SessionTerminated     bool
	SessionNeedsInput     bool
	SessionActivityStatus string
}

type RegisterBackgroundTaskWakeRequest struct {
	SessionScope string
	SessionID    string
	OwnerEmail   string
	Provider     string
	TaskID       string
	TaskStatus   string
	Description  string
	Summary      string
	LastToolName string
	Error        string
	// ObservedEventID is the durable shell_task.exited event id whose
	// observation registered this wake. It is the re-arm discriminator: a
	// re-registration carrying the SAME observation is a duplicate (runner
	// retry, restart re-adoption); a DIFFERENT observation of an
	// already-fired task arms the next wake generation (the real completion
	// arriving after a premature fire).
	ObservedEventID string
	RegisteredAt    time.Time
}

// BackgroundTaskWakeRegisterOutcome names what Register decided, for metrics
// and the runner-facing response.
type BackgroundTaskWakeRegisterOutcome string

const (
	BackgroundTaskWakeRegisterScheduled        BackgroundTaskWakeRegisterOutcome = "scheduled"
	BackgroundTaskWakeRegisterPendingUpdated   BackgroundTaskWakeRegisterOutcome = "pending_updated"
	BackgroundTaskWakeRegisterDuplicate        BackgroundTaskWakeRegisterOutcome = "duplicate_observation"
	BackgroundTaskWakeRegisterRearmed          BackgroundTaskWakeRegisterOutcome = "rearmed"
	BackgroundTaskWakeRegisterGenerationCapped BackgroundTaskWakeRegisterOutcome = "generation_capped"
	BackgroundTaskWakeRegisterTerminalNoop     BackgroundTaskWakeRegisterOutcome = "terminal_noop"
)

// maxBackgroundTaskWakeGenerations bounds re-arming: a task whose observer
// flaps (fires, re-observes, fires again) gets at most this many wake
// generations before further observations are ignored. The cap exists so a
// pathological liveness source cannot turn one task into an unbounded wake
// stream; reaching it is counted, never silent.
const maxBackgroundTaskWakeGenerations = 3

// MaxBackgroundTaskWakeAttempts bounds futile fire retries of ONE wake
// generation, on the same scale as the launch dispatcher's
// maxLaunchDispatchAttempts and MaxScheduledWakeupAttempts. ClaimDue refuses
// rows at or over the cap, so a wake whose fire keeps half-finishing (claim
// landed but MarkFired/MarkFailed never did) is not reclaimed every stale tick
// forever — an eventual re-fire past the session bus's 24h msg-id dedupe
// window would run the same wake turn twice. Soft-defers don't burn attempts:
// Release undoes the claim's bump. Capped rows are not left in limbo:
// FailExceeded terminals them.
const MaxBackgroundTaskWakeAttempts = 5

// backgroundTaskWakeAttemptCapError is the durable last_error stamped by
// FailExceeded. Prefix-stable: cmd/tank-operator folds it into the
// attempt_cap_exceeded fire-failure metric label.
const backgroundTaskWakeAttemptCapError = "attempt_cap_exceeded"

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

// BackgroundTaskWakeID is the idempotency key of the FIRST wake generation:
// one finished background task yields at most one wake row per (session,
// provider, task, generation).
func BackgroundTaskWakeID(tankSessionID, provider, taskID string) string {
	return BackgroundTaskWakeIDForGeneration(tankSessionID, provider, taskID, 1)
}

// BackgroundTaskWakeIDForGeneration derives the wake row id for a given
// generation. Generation 1 keeps the historical id shape so pre-generation
// rows remain the gen-1 rows they always were.
func BackgroundTaskWakeIDForGeneration(tankSessionID, provider, taskID string, generation int) string {
	parts := []string{
		strings.TrimSpace(tankSessionID),
		strings.TrimSpace(provider),
		strings.TrimSpace(taskID),
	}
	if generation > 1 {
		parts = append(parts, "g"+strconv.Itoa(generation))
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
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

// BackgroundTaskWakeClientNonceForGeneration derives the wake turn nonce for a
// generation. Generation 1 keeps the historical nonce (turn_bgtask-<task>);
// later generations append -g<N> so each re-armed wake opens its own
// deterministic turn while the projection still folds it into the originating
// turn through the turn.submitted payload task_id edge.
func BackgroundTaskWakeClientNonceForGeneration(taskID string, generation int) string {
	base := BackgroundTaskWakeClientNonce(taskID)
	if base == "" || generation <= 1 {
		return base
	}
	suffix := "-g" + strconv.Itoa(generation)
	if len(base+suffix) <= 80 {
		return base + suffix
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(taskID)))
	return "bgtask-" + hex.EncodeToString(h[:])[:32] + suffix
}

// backgroundTaskWakeRowColumns is the canonical SELECT/RETURNING column list
// for wake rows that are not joined against session state.
const backgroundTaskWakeRowColumns = `wake_id, session_scope, session_id, tank_session_id, owner_email,
	provider, task_id, task_status, task_description, task_summary, task_last_tool, task_error,
	observed_event_id, generation, client_nonce,
	registered_at, due_at, status, attempt_count, fired_turn_id, last_error,
	NULL::text AS session_status, NULL::boolean AS session_terminated,
	NULL::boolean AS session_needs_input, NULL::text AS session_activity_status`

// Register records a finished-task observation and decides what it means:
// schedule the first wake, refresh a still-pending wake's task facts,
// ignore a duplicate of an already-acted-on observation, or — when a NEW
// observation (different durable shell_task.exited event id) arrives for a
// task whose wake already fired — arm the next wake generation, so a
// premature fire does not permanently burn the task's only report. Latest
// terminal rows in failed/cancelled are never resurrected.
func (s *BackgroundTaskWakeStore) Register(ctx context.Context, req RegisterBackgroundTaskWakeRequest) (BackgroundTaskWake, BackgroundTaskWakeRegisterOutcome, error) {
	if s == nil || s.pool == nil {
		return BackgroundTaskWake{}, "", errors.New("background task wake store unavailable")
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
	req.Description = strings.TrimSpace(req.Description)
	req.Summary = strings.TrimSpace(req.Summary)
	req.LastToolName = strings.TrimSpace(req.LastToolName)
	req.Error = strings.TrimSpace(req.Error)
	req.ObservedEventID = strings.TrimSpace(req.ObservedEventID)
	if req.RegisteredAt.IsZero() {
		req.RegisteredAt = time.Now()
	}
	req.RegisteredAt = req.RegisteredAt.UTC()
	tankSessionID := sessionmodel.SessionStorageKey(req.SessionScope, req.SessionID)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BackgroundTaskWake{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	latest, err := scanBackgroundTaskWake(tx.QueryRow(ctx, `
		SELECT `+backgroundTaskWakeRowColumns+`
		FROM session_background_task_wakes
		WHERE tank_session_id = $1 AND task_id = $2
		ORDER BY generation DESC
		LIMIT 1
		FOR UPDATE
	`, tankSessionID, req.TaskID))
	haveLatest := true
	if errors.Is(err, pgx.ErrNoRows) {
		haveLatest = false
	} else if err != nil {
		return BackgroundTaskWake{}, "", err
	}

	insertGeneration := func(generation int) (BackgroundTaskWake, error) {
		wakeID := BackgroundTaskWakeIDForGeneration(tankSessionID, req.Provider, req.TaskID, generation)
		clientNonce := BackgroundTaskWakeClientNonceForGeneration(req.TaskID, generation)
		return scanBackgroundTaskWake(tx.QueryRow(ctx, `
			INSERT INTO session_background_task_wakes (
				wake_id, session_scope, session_id, tank_session_id, owner_email,
				provider, task_id, task_status, task_description, task_summary,
				task_last_tool, task_error, observed_event_id, generation, client_nonce,
				registered_at, due_at, status, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15,
				$16, $16, 'scheduled', now()
			)
			ON CONFLICT (wake_id) DO UPDATE
			SET updated_at = session_background_task_wakes.updated_at
			RETURNING `+backgroundTaskWakeRowColumns,
			wakeID, req.SessionScope, req.SessionID, tankSessionID, req.OwnerEmail,
			req.Provider, req.TaskID, req.TaskStatus, req.Description, req.Summary,
			req.LastToolName, req.Error, req.ObservedEventID, generation, clientNonce,
			req.RegisteredAt,
		))
	}

	var row BackgroundTaskWake
	var outcome BackgroundTaskWakeRegisterOutcome
	switch {
	case !haveLatest:
		row, err = insertGeneration(1)
		outcome = BackgroundTaskWakeRegisterScheduled
	case latest.Status == BackgroundTaskWakeScheduled || latest.Status == BackgroundTaskWakeClaiming:
		// Still pending: refresh the task facts so the fire-time prompt
		// reflects the freshest observation, without touching the lifecycle.
		row, err = scanBackgroundTaskWake(tx.QueryRow(ctx, `
			UPDATE session_background_task_wakes
			SET task_status = $2, task_description = $3, task_summary = $4,
				task_last_tool = $5, task_error = $6,
				observed_event_id = CASE WHEN $7 <> '' THEN $7 ELSE observed_event_id END,
				updated_at = now()
			WHERE wake_id = $1
			RETURNING `+backgroundTaskWakeRowColumns,
			latest.WakeID, req.TaskStatus, req.Description, req.Summary,
			req.LastToolName, req.Error, req.ObservedEventID,
		))
		outcome = BackgroundTaskWakeRegisterPendingUpdated
	case latest.Status == BackgroundTaskWakeFired:
		switch {
		case req.ObservedEventID == "" || latest.ObservedEventID == "" || req.ObservedEventID == latest.ObservedEventID:
			// Same observation (or an observation identity is missing on
			// either side, where re-arming would be guesswork): duplicate.
			row, outcome = latest, BackgroundTaskWakeRegisterDuplicate
		case latest.Generation >= maxBackgroundTaskWakeGenerations:
			row, outcome = latest, BackgroundTaskWakeRegisterGenerationCapped
		default:
			row, err = insertGeneration(latest.Generation + 1)
			outcome = BackgroundTaskWakeRegisterRearmed
		}
	default:
		// failed/cancelled: terminal by decision; never resurrected.
		row, outcome = latest, BackgroundTaskWakeRegisterTerminalNoop
	}
	if err != nil {
		return BackgroundTaskWake{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return BackgroundTaskWake{}, "", err
	}
	return row, outcome, nil
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
			  AND attempt_count < $5
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
			bw.provider, bw.task_id, bw.task_status, bw.task_description, bw.task_summary,
			bw.task_last_tool, bw.task_error, bw.observed_event_id, bw.generation, bw.client_nonce,
			bw.registered_at, bw.due_at, bw.status, bw.attempt_count, bw.fired_turn_id, bw.last_error,
			COALESCE((SELECT status FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), '') AS session_status,
			COALESCE((SELECT terminating_at IS NOT NULL FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), true) AS session_terminated,
			COALESCE((SELECT (activity_summary->>'needs_input')::boolean FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), false) AS session_needs_input,
			COALESCE((SELECT activity_summary->>'status' FROM sessions sess
				WHERE sess.email = bw.owner_email AND sess.session_scope = bw.session_scope AND sess.session_id = bw.session_id), '') AS session_activity_status
	`
	rows, err := s.pool.Query(ctx, q, s.scope, now.UTC(), limit, staleAfter.Seconds(), MaxBackgroundTaskWakeAttempts)
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

// FailExceeded gives a durable 'failed' terminal to wakes whose attempt count
// reached MaxBackgroundTaskWakeAttempts without MarkFired/MarkFailed ever
// landing. ClaimDue stops claiming such rows, so without this pass they would
// sit in 'claiming' limbo forever — still "pending" to HasPending and the
// activity fold, invisible to the user. Only rows that are demonstrably not
// in-flight are touched: a 'claiming' row must also be stale (locked_at older
// than staleAfter), so a capped final attempt still mid-fire is left to
// finish; 'scheduled' rows at the cap (the pre-cap data shape a claim-bumped
// row Released back to 'scheduled' carries) terminal immediately. Returns the
// terminaled snapshots so the fire loop can run the same post-failure
// bookkeeping as MarkFailed (away-error ring + activity refresh).
func (s *BackgroundTaskWakeStore) FailExceeded(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]BackgroundTaskWake, error) {
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
		WITH exceeded AS (
			SELECT wake_id
			FROM session_background_task_wakes
			WHERE session_scope = $1
			  AND attempt_count >= $5
			  AND (
			    status = 'scheduled'
			    OR (status = 'claiming' AND locked_at < $2 - make_interval(secs => $4::double precision))
			  )
			ORDER BY due_at ASC, created_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE session_background_task_wakes bw
		SET status = 'failed',
			last_error = left($6 || ': gave up after ' || bw.attempt_count || ' attempts', 2000),
			locked_at = NULL,
			updated_at = now()
		FROM exceeded
		WHERE bw.wake_id = exceeded.wake_id
		RETURNING bw.wake_id, bw.session_scope, bw.session_id, bw.tank_session_id, bw.owner_email,
			bw.provider, bw.task_id, bw.task_status, bw.task_description, bw.task_summary,
			bw.task_last_tool, bw.task_error, bw.observed_event_id, bw.generation, bw.client_nonce,
			bw.registered_at, bw.due_at, bw.status, bw.attempt_count, bw.fired_turn_id, bw.last_error,
			NULL::text AS session_status, NULL::boolean AS session_terminated,
			NULL::boolean AS session_needs_input, NULL::text AS session_activity_status
	`
	rows, err := s.pool.Query(ctx, q, s.scope, now.UTC(), limit, staleAfter.Seconds(),
		MaxBackgroundTaskWakeAttempts, backgroundTaskWakeAttemptCapError)
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

// CancelPendingForTask cancels the still-pending wake generations of ONE task.
// It is the delivered-mid-turn path: when the runner observes that the task's
// completion was already delivered into an active turn (the model has seen it
// and can act on it), any pending wake would be a duplicate notification —
// the session-788 "the same completion arrived as both a mid-turn notification
// and a new turn" defect. The reason lands in last_error for audit (cancelled
// is a decision, not an error).
func (s *BackgroundTaskWakeStore) CancelPendingForTask(ctx context.Context, sessionScope, sessionID, taskID, reason string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("background task wake store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	taskID = strings.TrimSpace(taskID)
	if sessionID == "" || taskID == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE session_background_task_wakes
		SET status = 'cancelled', locked_at = NULL, last_error = left($4, 2000), updated_at = now()
		WHERE session_scope = $1 AND session_id = $2 AND task_id = $3 AND status IN ('scheduled', 'claiming')
	`, sessionScope, sessionID, taskID, strings.TrimSpace(reason))
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
	var sessionActivityStatus *string
	err := row.Scan(
		&out.WakeID,
		&out.SessionScope,
		&out.SessionID,
		&out.TankSessionID,
		&out.OwnerEmail,
		&out.Provider,
		&out.TaskID,
		&out.TaskStatus,
		&out.TaskDescription,
		&out.TaskSummary,
		&out.TaskLastTool,
		&out.TaskError,
		&out.ObservedEventID,
		&out.Generation,
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
		&sessionActivityStatus,
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
	if sessionActivityStatus != nil {
		out.SessionActivityStatus = *sessionActivityStatus
	}
	return out, nil
}
