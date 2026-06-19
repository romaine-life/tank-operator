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

// PendingTestProvisionKind labels which deterministic test-slot provisioning
// path owns a pending record, so the durable reconcile backstop re-drives it
// through the right entry point. The value set is bounded (CHECK constraint +
// the reconcile-redrive metric label).
type PendingTestProvisionKind string

const (
	// PendingTestProvisionInteractive is the UI-button trigger
	// (handlers_test_workflow.go -> runInteractiveTestWorkflow).
	PendingTestProvisionInteractive PendingTestProvisionKind = "interactive"
	// PendingTestProvisionOrchestrationReview is the server-driven
	// orchestration-review trigger (orchestration_branch_pr.go ->
	// provisionOrchestrationReviewSlot).
	PendingTestProvisionOrchestrationReview PendingTestProvisionKind = "orchestration-review"
)

// PendingTestProvisionStatus is the durable lifecycle of a pending provision.
// 'pending' is the only non-terminal state: a record sits there from the moment
// a provision is kicked off until its goroutine reaches a verdict (provisioned
// or a refusal) or an infra error. A record stranded in 'pending' is the
// signature of an orchestrator restart mid-settle-wait -- the gap the reconcile
// backstop closes.
type PendingTestProvisionStatus string

const (
	PendingTestProvisionPending PendingTestProvisionStatus = "pending"
	// PendingTestProvisionDone marks a record whose run reached a verdict:
	// provisioned, or a legitimate gate refusal (failed/conflict/merged/timeout/
	// head-moved). No work is owed; the backstop must not re-drive it.
	PendingTestProvisionDone PendingTestProvisionStatus = "done"
	// PendingTestProvisionFailed marks a record whose run hit an infra error it
	// could not reach a verdict through. Terminal: it mirrors the entry points'
	// existing non-retrying behavior on infra failure, so the backstop only
	// recovers restart-stranded 'pending' records, never a glimmung-down loop.
	PendingTestProvisionFailed PendingTestProvisionStatus = "failed"
)

// PendingTestProvision is one durable pending-provision record: enough state to
// re-drive the gate idempotently after an orchestrator restart and to surface
// the outcome through the same entry point that kicked it off.
type PendingTestProvision struct {
	ProvisionID     string
	SessionScope    string
	SessionID       string
	TankSessionID   string
	OwnerEmail      string
	RepoOwner       string
	RepoName        string
	Branch          string
	Project         string
	Workflow        string
	Kind            PendingTestProvisionKind
	PRNumber        int
	ExpectedSHA     string
	HeadSHA         string
	OrchestrationID string
	Status          PendingTestProvisionStatus
	Detail          string
	AttemptCount    int
	StartedAt       time.Time
	LastEventAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// RegisterPendingTestProvisionRequest carries the coordinates the gate validates
// and provisions, captured at kickoff so the backstop can rebuild the request.
type RegisterPendingTestProvisionRequest struct {
	SessionScope    string
	SessionID       string
	OwnerEmail      string
	RepoOwner       string
	RepoName        string
	Branch          string
	Project         string
	Workflow        string
	Kind            PendingTestProvisionKind
	PRNumber        int
	ExpectedSHA     string
	HeadSHA         string
	OrchestrationID string
}

// ErrPendingTestProvisionStale signals that a conditional write (ClaimForRedrive
// or MarkTerminal) matched no row: the record was already terminalized or
// claimed by a concurrent reconcile. Callers must treat it as "another writer
// owns this transition" and not re-drive or double-mark. Mirrors
// ErrCIWatchObservationStale.
var ErrPendingTestProvisionStale = errors.New("pgstore: pending test provision did not match its pending row")

type PendingTestProvisionStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewPendingTestProvisionStore(pool *pgxpool.Pool, scope string) *PendingTestProvisionStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &PendingTestProvisionStore{pool: pool, scope: scope}
}

// PendingTestProvisionID is the stable identity for an in-flight provision: one
// row per (session, repo, branch, kind), so a re-kickoff of the same target is
// the same row (the double-trigger guard rides this) and a re-drive addresses
// the existing record rather than spawning a duplicate.
func PendingTestProvisionID(tankSessionID, repoOwner, repoName, branch string, kind PendingTestProvisionKind) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tankSessionID),
		strings.ToLower(strings.TrimSpace(repoOwner)),
		strings.ToLower(strings.TrimSpace(repoName)),
		strings.TrimSpace(branch),
		strings.TrimSpace(string(kind)),
	}, "\x1f")))
	return "pendprov_" + hex.EncodeToString(h[:])[:32]
}

// pendingTestProvisionColumns is the canonical column order shared by every
// RETURNING/SELECT in this store and by scanPendingTestProvision.
const pendingTestProvisionColumns = `provision_id, session_scope, session_id, tank_session_id, owner_email,
	repo_owner, repo_name, branch, project, workflow,
	kind, pr_number, expected_sha, head_sha, orchestration_id,
	status, detail, attempt_count, started_at, last_event_at,
	created_at, updated_at`

// Register lands (or re-arms) a pending record at provision kickoff. It is the
// atomic double-trigger guard: the conditional ON CONFLICT only re-arms a
// terminal row, so a second concurrent kickoff for a target already in flight
// matches no row and returns created=false. A returned row (fresh insert OR a
// re-armed terminal row) means the caller owns this run (created=true).
func (s *PendingTestProvisionStore) Register(ctx context.Context, req RegisterPendingTestProvisionRequest) (PendingTestProvision, bool, error) {
	if s == nil || s.pool == nil {
		return PendingTestProvision{}, false, errors.New("pending test provision store unavailable")
	}
	scope := strings.TrimSpace(req.SessionScope)
	if scope == "" {
		scope = s.scope
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return PendingTestProvision{}, false, errors.New("missing session_id")
	}
	repoOwner := strings.TrimSpace(req.RepoOwner)
	repoName := strings.TrimSpace(req.RepoName)
	if repoOwner == "" || repoName == "" {
		return PendingTestProvision{}, false, errors.New("missing repo owner/name")
	}
	kind := req.Kind
	if kind != PendingTestProvisionInteractive && kind != PendingTestProvisionOrchestrationReview {
		return PendingTestProvision{}, false, errors.New("invalid pending test provision kind")
	}
	branch := strings.TrimSpace(req.Branch)
	tankSessionID := sessionmodel.SessionStorageKey(scope, sessionID)
	provisionID := PendingTestProvisionID(tankSessionID, repoOwner, repoName, branch, kind)

	const q = `
		INSERT INTO pending_test_provisions (
			provision_id, session_scope, session_id, tank_session_id, owner_email,
			repo_owner, repo_name, branch, project, workflow,
			kind, pr_number, expected_sha, head_sha, orchestration_id,
			status, detail, attempt_count, started_at, last_event_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			'pending', '', 0, now(), now(), now()
		)
		ON CONFLICT (provision_id) DO UPDATE
		SET session_scope = EXCLUDED.session_scope,
			session_id = EXCLUDED.session_id,
			owner_email = EXCLUDED.owner_email,
			repo_owner = EXCLUDED.repo_owner,
			repo_name = EXCLUDED.repo_name,
			branch = EXCLUDED.branch,
			project = EXCLUDED.project,
			workflow = EXCLUDED.workflow,
			kind = EXCLUDED.kind,
			pr_number = EXCLUDED.pr_number,
			expected_sha = EXCLUDED.expected_sha,
			head_sha = EXCLUDED.head_sha,
			orchestration_id = EXCLUDED.orchestration_id,
			status = 'pending',
			detail = '',
			attempt_count = 0,
			started_at = now(),
			last_event_at = now(),
			updated_at = now()
		WHERE pending_test_provisions.status <> 'pending'
		RETURNING ` + pendingTestProvisionColumns
	p, err := scanPendingTestProvision(s.pool.QueryRow(ctx, q,
		provisionID, scope, sessionID, tankSessionID, strings.ToLower(strings.TrimSpace(req.OwnerEmail)),
		repoOwner, repoName, branch, strings.TrimSpace(req.Project), strings.TrimSpace(req.Workflow),
		string(kind), req.PRNumber, strings.TrimSpace(req.ExpectedSHA), strings.TrimSpace(req.HeadSHA), strings.TrimSpace(req.OrchestrationID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		// Conflict + the row is still 'pending': a provision for this target is
		// already in flight. Not an error -- the signal the double-trigger guard
		// turns into a 409.
		return PendingTestProvision{}, false, nil
	}
	if err != nil {
		return PendingTestProvision{}, false, err
	}
	return p, true, nil
}

// MarkTerminal terminals a pending record when its run reaches a verdict or an
// infra error. Gated on status='pending' so a double-mark (or a mark racing the
// backstop) matches no row and returns ErrPendingTestProvisionStale instead of
// resurrecting a record. headSHA is recorded only when non-empty.
func (s *PendingTestProvisionStore) MarkTerminal(ctx context.Context, provisionID string, status PendingTestProvisionStatus, detail, headSHA string) (PendingTestProvision, error) {
	if s == nil || s.pool == nil {
		return PendingTestProvision{}, errors.New("pending test provision store unavailable")
	}
	if status != PendingTestProvisionDone && status != PendingTestProvisionFailed {
		return PendingTestProvision{}, errors.New("MarkTerminal requires a terminal status")
	}
	const q = `
		UPDATE pending_test_provisions
		SET status = $2,
			detail = $3,
			head_sha = COALESCE(NULLIF($4, ''), head_sha),
			last_event_at = now(),
			updated_at = now()
		WHERE provision_id = $1 AND status = 'pending'
		RETURNING ` + pendingTestProvisionColumns
	p, err := scanPendingTestProvision(s.pool.QueryRow(ctx, q,
		strings.TrimSpace(provisionID), string(status), strings.TrimSpace(detail), strings.TrimSpace(headSHA),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingTestProvision{}, ErrPendingTestProvisionStale
	}
	return p, err
}

// ClaimForRedrive is the conditional write the reconcile backstop takes before
// re-driving a stale record, so two concurrent reconciles (two replicas, or a
// double pass) cannot both fire the expensive gate+provision for the same row.
// It bumps attempt_count (gated on the attempt the caller read) and moves
// last_event_at forward, so the winner owns the re-drive and the loser -- and
// the next reconcile pass within the staleness window -- matches no row and
// backs off. Returns ErrPendingTestProvisionStale when the claim is lost.
func (s *PendingTestProvisionStore) ClaimForRedrive(ctx context.Context, provisionID string, knownAttempt int) (PendingTestProvision, error) {
	if s == nil || s.pool == nil {
		return PendingTestProvision{}, errors.New("pending test provision store unavailable")
	}
	const q = `
		UPDATE pending_test_provisions
		SET attempt_count = attempt_count + 1,
			last_event_at = now(),
			updated_at = now()
		WHERE provision_id = $1 AND status = 'pending' AND attempt_count = $2
		RETURNING ` + pendingTestProvisionColumns
	p, err := scanPendingTestProvision(s.pool.QueryRow(ctx, q, strings.TrimSpace(provisionID), knownAttempt))
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingTestProvision{}, ErrPendingTestProvisionStale
	}
	return p, err
}

// ListStale returns 'pending' records whose last activity (last_event_at, else
// started_at) is older than the cutoff: the signature of a provision stranded by
// an orchestrator restart mid-settle-wait. Mirrors CIWatchStore.ListStaleWatching.
func (s *PendingTestProvisionStore) ListStale(ctx context.Context, olderThan time.Duration, limit int) ([]PendingTestProvision, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("pending test provision store unavailable")
	}
	if limit <= 0 {
		limit = 100
	}
	cutoff := olderThan.Seconds()
	if cutoff < 0 {
		cutoff = 0
	}
	const q = `SELECT ` + pendingTestProvisionColumns + `
		FROM pending_test_provisions
		WHERE status = 'pending'
			AND COALESCE(last_event_at, started_at) < now() - make_interval(secs => $1::double precision)
		ORDER BY COALESCE(last_event_at, started_at) ASC
		LIMIT $2`
	rows, err := s.pool.Query(ctx, q, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingTestProvision{}
	for rows.Next() {
		p, err := scanPendingTestProvision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// OldestPendingAgeSeconds is the age (by started_at, which never moves once a
// record is created) of the oldest still-'pending' provision. Backs the
// oldest-pending-age gauge the stuck-provision alert fires on. 0 when none.
func (s *PendingTestProvisionStore) OldestPendingAgeSeconds(ctx context.Context) (float64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("pending test provision store unavailable")
	}
	var age float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(started_at))), 0)
		FROM pending_test_provisions
		WHERE status = 'pending'
	`).Scan(&age)
	if err != nil {
		return 0, err
	}
	if age < 0 {
		age = 0
	}
	return age, nil
}

// Get returns a single record by id (test/debug surface).
func (s *PendingTestProvisionStore) Get(ctx context.Context, provisionID string) (PendingTestProvision, error) {
	if s == nil || s.pool == nil {
		return PendingTestProvision{}, errors.New("pending test provision store unavailable")
	}
	const q = `SELECT ` + pendingTestProvisionColumns + ` FROM pending_test_provisions WHERE provision_id = $1`
	return scanPendingTestProvision(s.pool.QueryRow(ctx, q, strings.TrimSpace(provisionID)))
}

type pendingTestProvisionScanner interface {
	Scan(dest ...any) error
}

func scanPendingTestProvision(row pendingTestProvisionScanner) (PendingTestProvision, error) {
	var out PendingTestProvision
	var kind, status string
	var lastEventAt *time.Time
	err := row.Scan(
		&out.ProvisionID,
		&out.SessionScope,
		&out.SessionID,
		&out.TankSessionID,
		&out.OwnerEmail,
		&out.RepoOwner,
		&out.RepoName,
		&out.Branch,
		&out.Project,
		&out.Workflow,
		&kind,
		&out.PRNumber,
		&out.ExpectedSHA,
		&out.HeadSHA,
		&out.OrchestrationID,
		&status,
		&out.Detail,
		&out.AttemptCount,
		&out.StartedAt,
		&lastEventAt,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		return PendingTestProvision{}, err
	}
	out.Kind = PendingTestProvisionKind(kind)
	out.Status = PendingTestProvisionStatus(status)
	out.LastEventAt = lastEventAt
	return out, nil
}
