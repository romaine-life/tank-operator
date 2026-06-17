package pgstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// CIWatchStatus is the lifecycle of a session's GitHub PR CI/mergeability
// watch. See docs/event-driven-rollout.md.
type CIWatchStatus string

const (
	// CIWatchWatching: CI is running or mergeability is still unresolved. The
	// agent has handed off and ended its turn; a red/conflict transition will
	// wake it. Reaper-protective: a session with a 'watching' row must stay
	// alive so the wake can land.
	CIWatchWatching CIWatchStatus = "watching"
	// CIWatchReady: all required checks are green and the PR is mergeable,
	// awaiting a human merge through Tank. Not reaper-protective - no agent work
	// remains, so the originating session may reap before the human merges.
	CIWatchReady CIWatchStatus = "ready"
	// CIWatchFailed: a required check failed; the agent was (or will be) woken to
	// fix its own code.
	CIWatchFailed CIWatchStatus = "failed"
	// CIWatchConflict: the PR is dirty/behind; the agent was (or will be) woken
	// to rebase.
	CIWatchConflict CIWatchStatus = "conflict"
	// CIWatchMerged: a human merged the PR through Tank. Terminal.
	CIWatchMerged CIWatchStatus = "merged"
	// CIWatchSuperseded: a newer head SHA replaced this watch.
	CIWatchSuperseded CIWatchStatus = "superseded"
	// CIWatchCancelled: the watch was explicitly cancelled.
	CIWatchCancelled CIWatchStatus = "cancelled"
)

// CIWatch is one durable PR watch row.
type CIWatch struct {
	WatchID        string
	SessionScope   string
	SessionID      string
	TankSessionID  string
	OwnerEmail     string
	PROwner        string
	PRName         string
	PRNumber       int
	HeadSHA        string
	Status         CIWatchStatus
	MergeableState string
	CheckState     string
	Detail         string
	PRURL          string
	MergeCommit    string
	RegisteredAt   time.Time
	LastEventAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// RegisterCIWatchRequest carries the authoritative-read result the watch tool
// gathered from GitHub at hand-off time.
type RegisterCIWatchRequest struct {
	SessionScope   string
	SessionID      string
	OwnerEmail     string
	PROwner        string
	PRName         string
	PRNumber       int
	HeadSHA        string
	MergeableState string
	CheckState     string
	Detail         string
	PRURL          string
}

// UpdateCIWatchObservationRequest carries the latest live GitHub state read by
// the backend reducer. It refreshes the durable watch without changing watch
// identity, so every event source re-enters the same state machine.
type UpdateCIWatchObservationRequest struct {
	WatchID string
	// ExpectedHeadSHA is the head_sha the reducer read this observation against.
	// The write is gated on it (plus status='watching'), so a stale or losing
	// concurrent reconcile cannot clobber a watch that was already terminalized
	// or re-headed by a re-publish. Empty skips the head gate.
	ExpectedHeadSHA string
	Status          CIWatchStatus
	HeadSHA         string
	MergeableState  string
	CheckState      string
	Detail          string
	PRURL           string
}

// ErrCIWatchObservationStale signals that UpdateObservation matched no row: the
// watch had already moved out of the 'watching' state the reducer read (a
// concurrent winning transition) or was re-headed by a re-publish. Callers must
// treat it as "another writer owns this transition" and not re-fire side effects.
var ErrCIWatchObservationStale = errors.New("pgstore: ci watch observation did not match its watching row")

type CIWatchStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewCIWatchStore(pool *pgxpool.Pool, scope string) *CIWatchStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &CIWatchStore{pool: pool, scope: scope}
}

// CIWatchID is the stable identity for a (session, PR) watch: one row per PR per
// session, so a re-publish of the same PR upserts rather than duplicates.
func CIWatchID(tankSessionID, prOwner, prName string, prNumber int) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tankSessionID),
		strings.ToLower(strings.TrimSpace(prOwner)),
		strings.ToLower(strings.TrimSpace(prName)),
		strconv.Itoa(prNumber),
	}, "\x1f")))
	return "ciwatch_" + hex.EncodeToString(h[:])[:32]
}

// ciWatchColumns is the canonical column order shared by every RETURNING/SELECT
// in this store and by scanCIWatch.
const ciWatchColumns = `watch_id, session_scope, session_id, tank_session_id, owner_email,
	pr_owner, pr_name, pr_number, head_sha, status,
	mergeable_state, check_state, detail, pr_url, merge_commit,
	registered_at, last_event_at, created_at, updated_at`

// Register upserts a watch in the 'watching' state. A re-publish of the same PR
// (new head SHA) refreshes head/state and resets the row to 'watching', so a
// resolved-then-changed PR is watched again on its new SHA.
func (s *CIWatchStore) Register(ctx context.Context, req RegisterCIWatchRequest) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	req.SessionScope = strings.TrimSpace(req.SessionScope)
	if req.SessionScope == "" {
		req.SessionScope = s.scope
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return CIWatch{}, errors.New("missing session_id")
	}
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	req.PROwner = strings.ToLower(strings.TrimSpace(req.PROwner))
	req.PRName = strings.ToLower(strings.TrimSpace(req.PRName))
	if req.PROwner == "" || req.PRName == "" || req.PRNumber <= 0 {
		return CIWatch{}, errors.New("missing pr owner/name/number")
	}
	req.HeadSHA = strings.TrimSpace(req.HeadSHA)
	tankSessionID := sessionmodel.SessionStorageKey(req.SessionScope, req.SessionID)
	watchID := CIWatchID(tankSessionID, req.PROwner, req.PRName, req.PRNumber)

	const q = `
		INSERT INTO session_ci_watches (
			watch_id, session_scope, session_id, tank_session_id, owner_email,
			pr_owner, pr_name, pr_number, head_sha, status,
			mergeable_state, check_state, detail, pr_url, registered_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, 'watching',
			$10, $11, $12, $13, now(), now()
		)
		ON CONFLICT (watch_id) DO UPDATE
		SET head_sha = EXCLUDED.head_sha,
			status = 'watching',
			mergeable_state = EXCLUDED.mergeable_state,
			check_state = EXCLUDED.check_state,
			detail = EXCLUDED.detail,
			pr_url = EXCLUDED.pr_url,
			updated_at = now()
		RETURNING ` + ciWatchColumns
	return scanCIWatch(s.pool.QueryRow(ctx, q,
		watchID, req.SessionScope, req.SessionID, tankSessionID, req.OwnerEmail,
		req.PROwner, req.PRName, req.PRNumber, req.HeadSHA,
		req.MergeableState, req.CheckState, req.Detail, req.PRURL,
	))
}

// UpdateStatus terminals or transitions a watch and stamps last_event_at. Used
// by the webhook receiver (failed/conflict/superseded) and the human merge
// surface (merged).
func (s *CIWatchStore) UpdateStatus(ctx context.Context, watchID string, status CIWatchStatus, detail string) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	const q = `
		UPDATE session_ci_watches
		SET status = $2, detail = $3, last_event_at = now(), updated_at = now()
		WHERE watch_id = $1
		RETURNING ` + ciWatchColumns
	return scanCIWatch(s.pool.QueryRow(ctx, q, strings.TrimSpace(watchID), string(status), strings.TrimSpace(detail)))
}

// UpdateObservation refreshes the watch with the latest live PR/CI evidence and
// the reducer's current state. last_event_at moves because this represents a
// webhook/timer/handoff observation, not just metadata churn.
func (s *CIWatchStore) UpdateObservation(ctx context.Context, req UpdateCIWatchObservationRequest) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	status := req.Status
	if status == "" {
		status = CIWatchWatching
	}
	// Single atomic conditional UPDATE: it only applies while the row is still in
	// the 'watching' state (and on the head) the reducer read. The first
	// transition out of 'watching' wins; a stale or losing concurrent reconcile
	// matches no row and cannot clobber a terminalized or re-headed watch.
	// expected_head_sha ($8) is enforced only when supplied.
	const q = `
		UPDATE session_ci_watches
		SET status = $2,
			head_sha = $3,
			mergeable_state = $4,
			check_state = $5,
			detail = $6,
			pr_url = $7,
			last_event_at = now(),
			updated_at = now()
		WHERE watch_id = $1
			AND status = 'watching'
			AND ($8 = '' OR head_sha = $8)
		RETURNING ` + ciWatchColumns
	w, err := scanCIWatch(s.pool.QueryRow(ctx, q,
		strings.TrimSpace(req.WatchID),
		string(status),
		strings.TrimSpace(req.HeadSHA),
		strings.TrimSpace(req.MergeableState),
		strings.TrimSpace(req.CheckState),
		strings.TrimSpace(req.Detail),
		strings.TrimSpace(req.PRURL),
		strings.TrimSpace(req.ExpectedHeadSHA),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return CIWatch{}, ErrCIWatchObservationStale
	}
	return w, err
}

// Get returns a single watch by id.
func (s *CIWatchStore) Get(ctx context.Context, watchID string) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	const q = `SELECT ` + ciWatchColumns + ` FROM session_ci_watches WHERE watch_id = $1`
	return scanCIWatch(s.pool.QueryRow(ctx, q, strings.TrimSpace(watchID)))
}

// GetByPR returns the most-recently-updated watch for a PR. The webhook
// receiver uses it as the reverse lookup (repo + PR number -> owning session).
// Owner/name are matched case-insensitively (they are stored lowercased).
func (s *CIWatchStore) GetByPR(ctx context.Context, prOwner, prName string, prNumber int) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	const q = `SELECT ` + ciWatchColumns + `
		FROM session_ci_watches
		WHERE pr_owner = $1 AND pr_name = $2 AND pr_number = $3
		ORDER BY updated_at DESC
		LIMIT 1`
	return scanCIWatch(s.pool.QueryRow(ctx, q,
		strings.ToLower(strings.TrimSpace(prOwner)), strings.ToLower(strings.TrimSpace(prName)), prNumber))
}

// MarkMerged terminals a watch as merged and records the merge commit. Called
// by the human merge surface after a successful governed merge.
func (s *CIWatchStore) MarkMerged(ctx context.Context, watchID, mergeCommit string) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	const q = `
		UPDATE session_ci_watches
		SET status = 'merged', merge_commit = $2, last_event_at = now(), updated_at = now()
		WHERE watch_id = $1
		RETURNING ` + ciWatchColumns
	return scanCIWatch(s.pool.QueryRow(ctx, q, strings.TrimSpace(watchID), strings.TrimSpace(mergeCommit)))
}

// GetLatestForSession returns the most-recently-updated watch for a session
// (any status). The human merge surface uses it to resolve PR coordinates.
func (s *CIWatchStore) GetLatestForSession(ctx context.Context, sessionScope, sessionID string) (CIWatch, error) {
	if s == nil || s.pool == nil {
		return CIWatch{}, errors.New("ci watch store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	const q = `SELECT ` + ciWatchColumns + `
		FROM session_ci_watches
		WHERE session_scope = $1 AND session_id = $2
		ORDER BY updated_at DESC
		LIMIT 1`
	return scanCIWatch(s.pool.QueryRow(ctx, q, sessionScope, strings.TrimSpace(sessionID)))
}

// HasActiveForSession reports whether the session owns a watch still in the
// reaper-protective 'watching' state. 'ready' (awaiting human merge) is
// intentionally excluded: once CI is green there is no pending agent work, so
// the originating session is allowed to reap before the human merges.
func (s *CIWatchStore) HasActiveForSession(ctx context.Context, sessionScope, sessionID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("ci watch store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false, nil
	}
	var active bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM session_ci_watches
			WHERE session_scope = $1 AND session_id = $2 AND status = 'watching'
		)
	`, sessionScope, sessionID).Scan(&active)
	return active, err
}

type ciWatchScanner interface {
	Scan(dest ...any) error
}

func scanCIWatch(row ciWatchScanner) (CIWatch, error) {
	var out CIWatch
	var status string
	var lastEventAt *time.Time
	err := row.Scan(
		&out.WatchID,
		&out.SessionScope,
		&out.SessionID,
		&out.TankSessionID,
		&out.OwnerEmail,
		&out.PROwner,
		&out.PRName,
		&out.PRNumber,
		&out.HeadSHA,
		&status,
		&out.MergeableState,
		&out.CheckState,
		&out.Detail,
		&out.PRURL,
		&out.MergeCommit,
		&out.RegisteredAt,
		&lastEventAt,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CIWatch{}, err
	}
	if err != nil {
		return CIWatch{}, err
	}
	out.Status = CIWatchStatus(status)
	out.LastEventAt = lastEventAt
	return out, nil
}
