package pgstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// PendingLaunchStatus tracks a deferred attachment launch from create to a
// terminal. The browser stages bytes (awaiting_bytes -> ready); the backend
// reconciler claims and dispatches (ready -> claiming -> dispatched) or fails
// it (-> failed). See issue #865 / docs/tank-conversation-protocol.md.
type PendingLaunchStatus string

const (
	PendingLaunchAwaitingBytes PendingLaunchStatus = "awaiting_bytes"
	PendingLaunchReady         PendingLaunchStatus = "ready"
	PendingLaunchClaiming      PendingLaunchStatus = "claiming"
	PendingLaunchDispatched    PendingLaunchStatus = "dispatched"
	PendingLaunchFailed        PendingLaunchStatus = "failed"
)

// PendingLaunchTurn is the durable dispatch record for one deferred launch.
// base_prompt/skill/model/effort are the parameters the reconciler composes
// the runnable turn from; the final workspace attachment paths are stamped in
// at materialization, not stored here.
type PendingLaunchTurn struct {
	TankSessionID    string
	TurnID           string
	SessionScope     string
	SessionID        string
	ClientNonce      string
	OwnerEmail       string
	Runtime          string
	SkillName        string
	BasePrompt       string
	DisplayText      string
	Model            string
	Effort           string
	AttachmentCount  int
	Status           PendingLaunchStatus
	AttemptCount     int
	LastError        string
	DispatchedTurnID string
	CreatedAt        time.Time
	// SessionStatus / SessionTerminated are populated only by ClaimReady (the
	// join to sessions); zero on plain reads.
	SessionStatus     string
	SessionTerminated bool
}

// RegisterPendingLaunchRequest is the create-time insert. AttachmentCount is
// how many staged blobs must arrive before the launch flips to ready.
type RegisterPendingLaunchRequest struct {
	SessionScope    string
	SessionID       string
	TurnID          string
	ClientNonce     string
	OwnerEmail      string
	Runtime         string
	SkillName       string
	BasePrompt      string
	DisplayText     string
	Model           string
	Effort          string
	AttachmentCount int
}

// LaunchAttachmentBlob is one staged attachment: durable bytes that the
// reconciler writes into the live pod workspace, then deletes.
type LaunchAttachmentBlob struct {
	Ordinal     int
	Name        string
	ContentType string
	Size        int64
	Bytes       []byte
}

type PendingLaunchStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewPendingLaunchStore(pool *pgxpool.Pool, scope string) *PendingLaunchStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &PendingLaunchStore{pool: pool, scope: scope}
}

// Register inserts the pending launch in awaiting_bytes. Idempotent on
// (tank_session_id, turn_id): a retried create is a no-op that returns the
// existing row, so a double-submit can't fork the launch.
func (s *PendingLaunchStore) Register(ctx context.Context, req RegisterPendingLaunchRequest) (PendingLaunchTurn, error) {
	if s == nil || s.pool == nil {
		return PendingLaunchTurn{}, errors.New("pending launch store unavailable")
	}
	scope := strings.TrimSpace(req.SessionScope)
	if scope == "" {
		scope = s.scope
	}
	sessionID := strings.TrimSpace(req.SessionID)
	turnID := strings.TrimSpace(req.TurnID)
	if sessionID == "" || turnID == "" {
		return PendingLaunchTurn{}, errors.New("pending launch requires session_id and turn_id")
	}
	count := req.AttachmentCount
	if count < 0 {
		count = 0
	}
	status := PendingLaunchAwaitingBytes
	if count == 0 {
		// No attachments to wait on — ready to dispatch immediately. (The
		// frontend only defers when attachments exist, but keep the model
		// total: a zero-attachment pending launch is trivially complete.)
		status = PendingLaunchReady
	}
	tankSessionID := sessionmodel.SessionStorageKey(scope, sessionID)
	const q = `
		INSERT INTO session_pending_launch_turns (
			tank_session_id, turn_id, session_scope, session_id, client_nonce,
			owner_email, runtime, skill_name, base_prompt, display_text,
			model, effort, attachment_count, status, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, now()
		)
		ON CONFLICT (tank_session_id, turn_id) DO UPDATE
		SET updated_at = session_pending_launch_turns.updated_at
		RETURNING tank_session_id, turn_id, session_scope, session_id, client_nonce,
			owner_email, runtime, skill_name, base_prompt, display_text,
			model, effort, attachment_count, status, attempt_count, last_error,
			dispatched_turn_id, created_at,
			NULL::text AS session_status, NULL::boolean AS session_terminated
	`
	return scanPendingLaunch(s.pool.QueryRow(ctx, q,
		tankSessionID, turnID, scope, sessionID, strings.TrimSpace(req.ClientNonce),
		strings.ToLower(strings.TrimSpace(req.OwnerEmail)), strings.TrimSpace(req.Runtime),
		strings.TrimSpace(req.SkillName), req.BasePrompt, req.DisplayText,
		strings.TrimSpace(req.Model), strings.TrimSpace(req.Effort), count, string(status),
	))
}

// StageAttachment durably stores one attachment's bytes and, when the staged
// count reaches attachment_count, flips the launch awaiting_bytes -> ready in
// the same transaction. Idempotent on (tank_session_id, turn_id, ordinal): a
// retried upload overwrites the same slot rather than double-counting. Returns
// the launch's status after staging so the caller knows when it became ready.
func (s *PendingLaunchStore) StageAttachment(ctx context.Context, tankSessionID, turnID string, blob LaunchAttachmentBlob) (PendingLaunchStatus, error) {
	if s == nil || s.pool == nil {
		return "", errors.New("pending launch store unavailable")
	}
	tankSessionID = strings.TrimSpace(tankSessionID)
	turnID = strings.TrimSpace(turnID)
	if tankSessionID == "" || turnID == "" {
		return "", errors.New("stage attachment requires tank_session_id and turn_id")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The launch row must exist and still be awaiting bytes. Lock it so the
	// count-then-flip below is race-free against a concurrent upload of a
	// sibling attachment.
	var attachmentCount int
	var status string
	err = tx.QueryRow(ctx, `
		SELECT attachment_count, status
		FROM session_pending_launch_turns
		WHERE tank_session_id = $1 AND turn_id = $2
		FOR UPDATE
	`, tankSessionID, turnID).Scan(&attachmentCount, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrPendingLaunchNotFound
	}
	if err != nil {
		return "", err
	}
	if PendingLaunchStatus(status) != PendingLaunchAwaitingBytes && PendingLaunchStatus(status) != PendingLaunchReady {
		// Already claiming/dispatched/failed — refuse late byte writes so a
		// stray retry can't mutate an in-flight or terminal launch.
		return PendingLaunchStatus(status), ErrPendingLaunchNotAcceptingBytes
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO session_launch_attachment_blobs (
			tank_session_id, turn_id, ordinal, name, content_type, size_bytes, bytes
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tank_session_id, turn_id, ordinal) DO UPDATE
		SET name = EXCLUDED.name,
			content_type = EXCLUDED.content_type,
			size_bytes = EXCLUDED.size_bytes,
			bytes = EXCLUDED.bytes
	`, tankSessionID, turnID, blob.Ordinal, strings.TrimSpace(blob.Name),
		strings.TrimSpace(blob.ContentType), blob.Size, blob.Bytes); err != nil {
		return "", err
	}

	var staged int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM session_launch_attachment_blobs
		WHERE tank_session_id = $1 AND turn_id = $2
	`, tankSessionID, turnID).Scan(&staged); err != nil {
		return "", err
	}

	newStatus := PendingLaunchStatus(status)
	if staged >= attachmentCount {
		newStatus = PendingLaunchReady
		if _, err := tx.Exec(ctx, `
			UPDATE session_pending_launch_turns
			SET status = 'ready', updated_at = now()
			WHERE tank_session_id = $1 AND turn_id = $2 AND status = 'awaiting_bytes'
		`, tankSessionID, turnID); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return newStatus, nil
}

// ClaimReady leases dispatchable launches: status=ready (all bytes staged) for
// a session whose pod is Active and not terminating, plus stale claiming rows
// whose lock has expired (a reconciler that crashed mid-dispatch). Mirrors the
// scheduled-wakeup ClaimDue lease so two orchestrator replicas never dispatch
// the same launch. Rows are returned with the joined session status.
func (s *PendingLaunchStore) ClaimReady(ctx context.Context, now time.Time, limit int, staleAfter time.Duration) ([]PendingLaunchTurn, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("pending launch store unavailable")
	}
	if limit <= 0 {
		limit = 25
	}
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	const q = `
		WITH claimable AS (
			SELECT pl.tank_session_id, pl.turn_id
			FROM session_pending_launch_turns pl
			JOIN sessions sess
			  ON sess.email = pl.owner_email
			 AND sess.session_scope = pl.session_scope
			 AND sess.session_id = pl.session_id
			WHERE pl.session_scope = $1
			  AND sess.status = 'Active'
			  AND sess.terminating_at IS NULL
			  AND (
			    pl.status = 'ready'
			    OR (pl.status = 'claiming' AND pl.locked_at < $2 - make_interval(secs => $4::double precision))
			  )
			ORDER BY pl.created_at ASC
			LIMIT $3
			FOR UPDATE OF pl SKIP LOCKED
		)
		UPDATE session_pending_launch_turns pl
		SET status = 'claiming',
			attempt_count = pl.attempt_count + 1,
			locked_at = $2,
			updated_at = now()
		FROM claimable
		WHERE pl.tank_session_id = claimable.tank_session_id
		  AND pl.turn_id = claimable.turn_id
		RETURNING pl.tank_session_id, pl.turn_id, pl.session_scope, pl.session_id, pl.client_nonce,
			pl.owner_email, pl.runtime, pl.skill_name, pl.base_prompt, pl.display_text,
			pl.model, pl.effort, pl.attachment_count, pl.status, pl.attempt_count, pl.last_error,
			pl.dispatched_turn_id, pl.created_at,
			COALESCE((SELECT status FROM sessions sess
				WHERE sess.email = pl.owner_email AND sess.session_scope = pl.session_scope AND sess.session_id = pl.session_id), '') AS session_status,
			COALESCE((SELECT terminating_at IS NOT NULL FROM sessions sess
				WHERE sess.email = pl.owner_email AND sess.session_scope = pl.session_scope AND sess.session_id = pl.session_id), true) AS session_terminated
	`
	rows, err := s.pool.Query(ctx, q, s.scope, now.UTC(), limit, staleAfter.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingLaunchTurn
	for rows.Next() {
		row, err := scanPendingLaunch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// LoadAttachments returns the staged blobs for a launch, ordered by ordinal,
// for the reconciler to materialize into the pod workspace.
func (s *PendingLaunchStore) LoadAttachments(ctx context.Context, tankSessionID, turnID string) ([]LaunchAttachmentBlob, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("pending launch store unavailable")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ordinal, name, content_type, size_bytes, bytes
		FROM session_launch_attachment_blobs
		WHERE tank_session_id = $1 AND turn_id = $2
		ORDER BY ordinal ASC
	`, strings.TrimSpace(tankSessionID), strings.TrimSpace(turnID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LaunchAttachmentBlob
	for rows.Next() {
		var b LaunchAttachmentBlob
		if err := rows.Scan(&b.Ordinal, &b.Name, &b.ContentType, &b.Size, &b.Bytes); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// MarkDispatched flips the launch to dispatched and deletes the staged bytes —
// they now live in the pod workspace, so the durable staging copy is no longer
// needed. The two writes share a transaction so a crash can't leave a
// dispatched launch with orphaned blobs.
func (s *PendingLaunchStore) MarkDispatched(ctx context.Context, tankSessionID, turnID, dispatchedTurnID string) error {
	if s == nil || s.pool == nil {
		return errors.New("pending launch store unavailable")
	}
	tankSessionID = strings.TrimSpace(tankSessionID)
	turnID = strings.TrimSpace(turnID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE session_pending_launch_turns
		SET status = 'dispatched',
			dispatched_turn_id = $3,
			last_error = '',
			locked_at = NULL,
			updated_at = now(),
			dispatched_at = now()
		WHERE tank_session_id = $1 AND turn_id = $2
	`, tankSessionID, turnID, strings.TrimSpace(dispatchedTurnID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM session_launch_attachment_blobs
		WHERE tank_session_id = $1 AND turn_id = $2
	`, tankSessionID, turnID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkFailed records a terminal failure and drops the staged bytes. The caller
// emits the durable turn.command_failed event separately so the SPA renders
// the launch as failed.
func (s *PendingLaunchStore) MarkFailed(ctx context.Context, tankSessionID, turnID, reason string) error {
	if s == nil || s.pool == nil {
		return errors.New("pending launch store unavailable")
	}
	tankSessionID = strings.TrimSpace(tankSessionID)
	turnID = strings.TrimSpace(turnID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE session_pending_launch_turns
		SET status = 'failed',
			last_error = left($3, 2000),
			locked_at = NULL,
			updated_at = now()
		WHERE tank_session_id = $1 AND turn_id = $2
	`, tankSessionID, turnID, strings.TrimSpace(reason)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM session_launch_attachment_blobs
		WHERE tank_session_id = $1 AND turn_id = $2
	`, tankSessionID, turnID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Get returns one launch row by key (no session join). Used by handlers and
// tests; returns ErrPendingLaunchNotFound when absent.
func (s *PendingLaunchStore) Get(ctx context.Context, tankSessionID, turnID string) (PendingLaunchTurn, error) {
	if s == nil || s.pool == nil {
		return PendingLaunchTurn{}, errors.New("pending launch store unavailable")
	}
	const q = `
		SELECT tank_session_id, turn_id, session_scope, session_id, client_nonce,
			owner_email, runtime, skill_name, base_prompt, display_text,
			model, effort, attachment_count, status, attempt_count, last_error,
			dispatched_turn_id, created_at,
			NULL::text AS session_status, NULL::boolean AS session_terminated
		FROM session_pending_launch_turns
		WHERE tank_session_id = $1 AND turn_id = $2
	`
	row, err := scanPendingLaunch(s.pool.QueryRow(ctx, q, strings.TrimSpace(tankSessionID), strings.TrimSpace(turnID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingLaunchTurn{}, ErrPendingLaunchNotFound
	}
	return row, err
}

var (
	// ErrPendingLaunchNotFound is returned when no launch row matches the key.
	ErrPendingLaunchNotFound = errors.New("pending launch not found")
	// ErrPendingLaunchNotAcceptingBytes is returned when bytes arrive for a
	// launch that has already been claimed, dispatched, or failed.
	ErrPendingLaunchNotAcceptingBytes = errors.New("pending launch is no longer accepting attachment bytes")
)

type pendingLaunchScanner interface {
	Scan(dest ...any) error
}

func scanPendingLaunch(row pendingLaunchScanner) (PendingLaunchTurn, error) {
	var out PendingLaunchTurn
	var status string
	var sessionStatus *string
	var sessionTerminated *bool
	err := row.Scan(
		&out.TankSessionID,
		&out.TurnID,
		&out.SessionScope,
		&out.SessionID,
		&out.ClientNonce,
		&out.OwnerEmail,
		&out.Runtime,
		&out.SkillName,
		&out.BasePrompt,
		&out.DisplayText,
		&out.Model,
		&out.Effort,
		&out.AttachmentCount,
		&status,
		&out.AttemptCount,
		&out.LastError,
		&out.DispatchedTurnID,
		&out.CreatedAt,
		&sessionStatus,
		&sessionTerminated,
	)
	if err != nil {
		return PendingLaunchTurn{}, err
	}
	out.Status = PendingLaunchStatus(status)
	if sessionStatus != nil {
		out.SessionStatus = *sessionStatus
	}
	if sessionTerminated != nil {
		out.SessionTerminated = *sessionTerminated
	}
	return out, nil
}
