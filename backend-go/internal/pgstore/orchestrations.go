package pgstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OrchestrationState is the run-level state machine for a deterministic,
// multi-phase orchestration executing a human-approved plan. The transition
// logic that drives a run between these states lives in a later slice; this
// store only owns the durable column and its CHECK-enforced value set.
type OrchestrationState string

const (
	// OrchestrationDraft: the plan is being assembled and has not been approved.
	OrchestrationDraft OrchestrationState = "draft"
	// OrchestrationApproved: a human approved the frozen plan; the run is ready
	// to start but no phase has been dispatched yet.
	OrchestrationApproved OrchestrationState = "approved"
	// OrchestrationRunning: at least one phase is in flight.
	OrchestrationRunning OrchestrationState = "running"
	// OrchestrationAwaitingReview: the run is paused on a human review gate.
	OrchestrationAwaitingReview OrchestrationState = "awaiting_review"
	// OrchestrationDone: every phase reached a terminal success. Terminal.
	OrchestrationDone OrchestrationState = "done"
	// OrchestrationFailed: the run terminated without completing. Terminal.
	OrchestrationFailed OrchestrationState = "failed"
)

// PhaseTarget is the branch a phase's PR targets: the repo's main line, or the
// run's shared integration branch.
type PhaseTarget string

const (
	PhaseTargetMain        PhaseTarget = "main"
	PhaseTargetIntegration PhaseTarget = "integration"
)

// PhaseStatus is the lifecycle of a single DAG node. Like OrchestrationState,
// the advance logic (which phase becomes ready when its deps merge) is a later
// slice; the store owns the durable column and its CHECK set only.
type PhaseStatus string

const (
	// PhasePending: dependencies not yet satisfied; the node is waiting.
	PhasePending PhaseStatus = "pending"
	// PhaseReady: dependencies satisfied; the node may be dispatched.
	PhaseReady PhaseStatus = "ready"
	// PhaseRunning: a spoke session is working the node.
	PhaseRunning PhaseStatus = "running"
	// PhasePROpen: the spoke opened a PR that is being watched.
	PhasePROpen PhaseStatus = "pr_open"
	// PhaseMerged: the node's PR merged. Terminal success.
	PhaseMerged PhaseStatus = "merged"
	// PhaseBlocked: the node cannot proceed (a dependency failed). Terminal.
	PhaseBlocked PhaseStatus = "blocked"
	// PhaseSkipped: the node was intentionally not run. Terminal.
	PhaseSkipped PhaseStatus = "skipped"
)

// PlanPhase is one node of the frozen, approved plan. These fields are
// immutable for the life of a run: they are materialized into write-once
// columns on orchestration_phases and folded into the run's plan_hash. Key is
// the stable logical identity referenced by other phases' DependsOn (the DAG
// edges); Ordinal is assigned from plan order at create time.
type PlanPhase struct {
	Key       string
	Brief     string
	DependsOn []string
	Target    PhaseTarget
	// Ordinal is informational on input (assigned from plan order on create);
	// it is populated on read.
	Ordinal int
}

// Orchestration is one run: identity, ownership, the target GitHub repo, the
// frozen plan snapshot + its content hash, the run state, and approval
// provenance. Plan is the raw canonical jsonb snapshot; PlanHash content-
// addresses it.
type Orchestration struct {
	OrchestrationID   string
	OwnerEmail        string
	ApproverEmail     string
	RepoOwner         string
	RepoName          string
	IntegrationBranch string
	State             OrchestrationState
	Plan              []byte
	PlanHash          string
	PhaseCount        int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ApprovedAt        *time.Time
}

// OrchestrationPhase is one materialized DAG node: the immutable plan fields
// plus the mutable runtime state (status, the spoke session that owns it, and
// the PR/merge coordinates stamped in as it opens and merges a PR).
type OrchestrationPhase struct {
	PhaseID         string
	OrchestrationID string
	Ordinal         int
	Key             string
	Brief           string
	DependsOn       []string
	Target          PhaseTarget
	Status          PhaseStatus
	SpokeSessionID  string
	PROwner         string
	PRName          string
	PRNumber        int
	PRURL           string
	MergeSHA        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateOrchestrationRequest is the create-time insert: a run materialized from
// an approved plan. OrchestrationID is optional — if empty a fresh id is
// minted (supply one to make create idempotent on a caller-owned key). State
// defaults to draft.
type CreateOrchestrationRequest struct {
	OrchestrationID   string
	OwnerEmail        string
	ApproverEmail     string
	RepoOwner         string
	RepoName          string
	IntegrationBranch string
	State             OrchestrationState
	Phases            []PlanPhase
}

var (
	// ErrOrchestrationNotFound is returned when no run exists for an id.
	ErrOrchestrationNotFound = errors.New("orchestration not found")
	// ErrOrchestrationPhaseNotFound is returned when no phase exists for an id.
	ErrOrchestrationPhaseNotFound = errors.New("orchestration phase not found")
)

// OrchestrationStore is the durable data layer for orchestration runs. Unlike
// the session-scoped stores it carries no scope: an orchestration is owned by
// an email and targets a repo, but is not bound to a session_scope.
type OrchestrationStore struct {
	pool *pgxpool.Pool
}

func NewOrchestrationStore(pool *pgxpool.Pool) *OrchestrationStore {
	return &OrchestrationStore{pool: pool}
}

// OrchestrationPhaseID is the stable identity for a phase within a run:
// derived from (orchestration_id, phase_key) so it is reproducible and a phase
// is never duplicated. Mirrors CIWatchID.
func OrchestrationPhaseID(orchestrationID, phaseKey string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(orchestrationID),
		strings.TrimSpace(phaseKey),
	}, "\x1f")))
	return "orchphase_" + hex.EncodeToString(h[:])[:32]
}

// NewOrchestrationID returns a fresh durable run id. Callers that need the id
// before Create (for example to derive an integration branch name) can supply
// it back in CreateOrchestrationRequest.OrchestrationID.
func NewOrchestrationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "orch_" + hex.EncodeToString(b[:]), nil
}

// canonicalPlanPhase is the immutable, hashable form of one phase. Field order
// is fixed and DependsOn is sorted so the marshaled plan document — and thus
// plan_hash — is deterministic regardless of input dep ordering.
type canonicalPlanPhase struct {
	Key       string   `json:"key"`
	Ordinal   int      `json:"ordinal"`
	Brief     string   `json:"brief"`
	DependsOn []string `json:"depends_on"`
	Target    string   `json:"target"`
}

// canonicalPlan is the full frozen plan document that gets stored as the
// orchestration's plan jsonb and hashed into plan_hash.
type canonicalPlan struct {
	RepoOwner         string               `json:"repo_owner"`
	RepoName          string               `json:"repo_name"`
	IntegrationBranch string               `json:"integration_branch"`
	Phases            []canonicalPlanPhase `json:"phases"`
}

// OrchestrationPlanHash normalizes and validates a plan, then returns its
// canonical jsonb document and content-addressed hash. It is the single freeze
// point: Create stores exactly this document and hash, and callers/tests can
// recompute the hash from the same inputs to prove a run's plan is unchanged.
//
// Validation enforced here is plan-structure integrity, not run advance logic:
// every phase needs a non-empty key and brief and a valid target; keys are
// unique; depends_on edges reference real sibling keys, never the phase itself;
// and the dependency graph is acyclic (a cyclic frozen plan is permanently
// unrunnable and is rejected at the freeze point).
func OrchestrationPlanHash(repoOwner, repoName, integrationBranch string, phases []PlanPhase) (planJSON []byte, hash string, err error) {
	plan, err := buildCanonicalPlan(repoOwner, repoName, integrationBranch, phases)
	if err != nil {
		return nil, "", err
	}
	planJSON, err = json.Marshal(plan)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(planJSON)
	return planJSON, hex.EncodeToString(sum[:]), nil
}

func buildCanonicalPlan(repoOwner, repoName, integrationBranch string, phases []PlanPhase) (canonicalPlan, error) {
	repoOwner = strings.ToLower(strings.TrimSpace(repoOwner))
	repoName = strings.ToLower(strings.TrimSpace(repoName))
	integrationBranch = strings.TrimSpace(integrationBranch)
	if repoOwner == "" || repoName == "" {
		return canonicalPlan{}, errors.New("orchestration requires repo owner and name")
	}
	if len(phases) == 0 {
		return canonicalPlan{}, errors.New("orchestration plan requires at least one phase")
	}

	keyIndex := make(map[string]int, len(phases))
	out := make([]canonicalPlanPhase, 0, len(phases))
	for i, p := range phases {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			return canonicalPlan{}, fmt.Errorf("phase %d has an empty key", i)
		}
		if _, dup := keyIndex[key]; dup {
			return canonicalPlan{}, fmt.Errorf("duplicate phase key %q", key)
		}
		brief := strings.TrimSpace(p.Brief)
		if brief == "" {
			return canonicalPlan{}, fmt.Errorf("phase %q has an empty brief", key)
		}
		target := PhaseTarget(strings.TrimSpace(string(p.Target)))
		if target == "" {
			target = PhaseTargetMain
		}
		if target != PhaseTargetMain && target != PhaseTargetIntegration {
			return canonicalPlan{}, fmt.Errorf("phase %q has invalid target %q", key, target)
		}
		deps := normalizeDeps(p.DependsOn)
		for _, d := range deps {
			if d == key {
				return canonicalPlan{}, fmt.Errorf("phase %q depends on itself", key)
			}
		}
		keyIndex[key] = i
		out = append(out, canonicalPlanPhase{
			Key:       key,
			Ordinal:   i,
			Brief:     brief,
			DependsOn: deps,
			Target:    string(target),
		})
	}

	// depends_on must reference real sibling phases.
	for _, p := range out {
		for _, d := range p.DependsOn {
			if _, ok := keyIndex[d]; !ok {
				return canonicalPlan{}, fmt.Errorf("phase %q depends on unknown phase %q", p.Key, d)
			}
		}
	}
	if cyclePhase, cyclic := firstCyclicPhase(out); cyclic {
		return canonicalPlan{}, fmt.Errorf("orchestration plan has a dependency cycle involving phase %q", cyclePhase)
	}

	return canonicalPlan{
		RepoOwner:         repoOwner,
		RepoName:          repoName,
		IntegrationBranch: integrationBranch,
		Phases:            out,
	}, nil
}

// normalizeDeps trims, drops empties, de-duplicates, and sorts a dependency
// list so the canonical plan is order-independent.
func normalizeDeps(deps []string) []string {
	seen := make(map[string]struct{}, len(deps))
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// firstCyclicPhase reports the key of a phase participating in a dependency
// cycle, if any, via DFS over the depends_on edges.
func firstCyclicPhase(phases []canonicalPlanPhase) (string, bool) {
	deps := make(map[string][]string, len(phases))
	for _, p := range phases {
		deps[p.Key] = p.DependsOn
	}
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(phases))
	var found string
	var visit func(key string) bool
	visit = func(key string) bool {
		switch state[key] {
		case visiting:
			found = key
			return true
		case done:
			return false
		}
		state[key] = visiting
		for _, d := range deps[key] {
			if visit(d) {
				if found == "" {
					found = key
				}
				return true
			}
		}
		state[key] = done
		return false
	}
	for _, p := range phases {
		if visit(p.Key) {
			return found, true
		}
	}
	return "", false
}

// Create freezes an approved plan into a durable run: it inserts the
// orchestration row (with the canonical plan snapshot + content hash) and one
// orchestration_phases row per node, all in a single transaction. Phases start
// in 'pending'. Returns the run and its phases as read back from the database.
func (s *OrchestrationStore) Create(ctx context.Context, req CreateOrchestrationRequest) (Orchestration, []OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return Orchestration{}, nil, errors.New("orchestration store unavailable")
	}
	ownerEmail := strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	if ownerEmail == "" {
		return Orchestration{}, nil, errors.New("orchestration requires owner_email")
	}
	approverEmail := strings.ToLower(strings.TrimSpace(req.ApproverEmail))
	state := req.State
	if state == "" {
		state = OrchestrationDraft
	}
	if !validOrchestrationState(state) {
		return Orchestration{}, nil, fmt.Errorf("invalid orchestration state %q", state)
	}

	// Build the canonical plan once: the same validated document is hashed into
	// plan_hash, stored as the frozen plan jsonb, and used for the row inserts,
	// so the snapshot, its hash, and the materialized phase rows can never
	// disagree.
	plan, err := buildCanonicalPlan(req.RepoOwner, req.RepoName, req.IntegrationBranch, req.Phases)
	if err != nil {
		return Orchestration{}, nil, err
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return Orchestration{}, nil, err
	}
	planHashSum := sha256.Sum256(planJSON)
	planHash := hex.EncodeToString(planHashSum[:])

	orchestrationID := strings.TrimSpace(req.OrchestrationID)
	if orchestrationID == "" {
		orchestrationID, err = NewOrchestrationID()
		if err != nil {
			return Orchestration{}, nil, err
		}
	}

	var integrationBranch *string
	if plan.IntegrationBranch != "" {
		integrationBranch = &plan.IntegrationBranch
	}
	var approvedAt *time.Time
	if state != OrchestrationDraft {
		now := time.Now().UTC()
		approvedAt = &now
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Orchestration{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO orchestrations (
			orchestration_id, owner_email, approver_email, repo_owner, repo_name,
			integration_branch, state, plan, plan_hash, phase_count, approved_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11, now()
		)
	`, orchestrationID, ownerEmail, approverEmail, plan.RepoOwner, plan.RepoName,
		integrationBranch, string(state), planJSON, planHash, len(plan.Phases), approvedAt); err != nil {
		return Orchestration{}, nil, err
	}

	for _, p := range plan.Phases {
		depsJSON, err := json.Marshal(p.DependsOn)
		if err != nil {
			return Orchestration{}, nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO orchestration_phases (
				phase_id, orchestration_id, ordinal, phase_key, brief,
				depends_on, target, status, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, 'pending', now()
			)
		`, OrchestrationPhaseID(orchestrationID, p.Key), orchestrationID, p.Ordinal, p.Key, p.Brief,
			depsJSON, p.Target); err != nil {
			return Orchestration{}, nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Orchestration{}, nil, err
	}

	return s.GetWithPhases(ctx, orchestrationID)
}

func validOrchestrationState(state OrchestrationState) bool {
	switch state {
	case OrchestrationDraft, OrchestrationApproved, OrchestrationRunning,
		OrchestrationAwaitingReview, OrchestrationDone, OrchestrationFailed:
		return true
	default:
		return false
	}
}

const orchestrationColumns = `orchestration_id, owner_email, approver_email, repo_owner, repo_name,
	integration_branch, state, plan, plan_hash, phase_count, created_at, updated_at, approved_at`

const orchestrationPhaseColumns = `phase_id, orchestration_id, ordinal, phase_key, brief,
	depends_on, target, status, spoke_session_id, pr_owner, pr_name, pr_number,
	pr_url, merge_sha, created_at, updated_at`

// Get returns a single run by id (without its phases).
func (s *OrchestrationStore) Get(ctx context.Context, orchestrationID string) (Orchestration, error) {
	if s == nil || s.pool == nil {
		return Orchestration{}, errors.New("orchestration store unavailable")
	}
	const q = `SELECT ` + orchestrationColumns + ` FROM orchestrations WHERE orchestration_id = $1`
	out, err := scanOrchestration(s.pool.QueryRow(ctx, q, strings.TrimSpace(orchestrationID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Orchestration{}, ErrOrchestrationNotFound
	}
	return out, err
}

// GetWithPhases returns a run plus all of its phases (with their deps and
// statuses) ordered by ordinal — the canonical read for an orchestrator that
// needs the full DAG and current runtime state.
func (s *OrchestrationStore) GetWithPhases(ctx context.Context, orchestrationID string) (Orchestration, []OrchestrationPhase, error) {
	orch, err := s.Get(ctx, orchestrationID)
	if err != nil {
		return Orchestration{}, nil, err
	}
	phases, err := s.ListPhases(ctx, orchestrationID)
	if err != nil {
		return Orchestration{}, nil, err
	}
	return orch, phases, nil
}

// ListPhases returns a run's phases ordered by ordinal.
func (s *OrchestrationStore) ListPhases(ctx context.Context, orchestrationID string) ([]OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("orchestration store unavailable")
	}
	const q = `SELECT ` + orchestrationPhaseColumns + `
		FROM orchestration_phases
		WHERE orchestration_id = $1
		ORDER BY ordinal ASC`
	rows, err := s.pool.Query(ctx, q, strings.TrimSpace(orchestrationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrchestrationPhase
	for rows.Next() {
		phase, err := scanOrchestrationPhase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, phase)
	}
	return out, rows.Err()
}

// GetPhase returns a single phase by id.
func (s *OrchestrationStore) GetPhase(ctx context.Context, phaseID string) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	const q = `SELECT ` + orchestrationPhaseColumns + ` FROM orchestration_phases WHERE phase_id = $1`
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, strings.TrimSpace(phaseID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// UpdateState transitions a run's state. The store validates the value against
// the CHECK set; the legality of a given transition is a later slice's concern.
func (s *OrchestrationStore) UpdateState(ctx context.Context, orchestrationID string, state OrchestrationState) (Orchestration, error) {
	if s == nil || s.pool == nil {
		return Orchestration{}, errors.New("orchestration store unavailable")
	}
	if !validOrchestrationState(state) {
		return Orchestration{}, fmt.Errorf("invalid orchestration state %q", state)
	}
	const q = `
		UPDATE orchestrations
		SET state = $2, updated_at = now()
		WHERE orchestration_id = $1
		RETURNING ` + orchestrationColumns
	out, err := scanOrchestration(s.pool.QueryRow(ctx, q, strings.TrimSpace(orchestrationID), string(state)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Orchestration{}, ErrOrchestrationNotFound
	}
	return out, err
}

// Approve freezes a draft run for execution, recording the approver and first
// approval timestamp. It is intentionally narrow: phase dispatch still happens
// through the advance engine's reconcileRun so there is one DAG driver.
func (s *OrchestrationStore) Approve(ctx context.Context, orchestrationID, approverEmail string) (Orchestration, error) {
	if s == nil || s.pool == nil {
		return Orchestration{}, errors.New("orchestration store unavailable")
	}
	approverEmail = strings.ToLower(strings.TrimSpace(approverEmail))
	if approverEmail == "" {
		return Orchestration{}, errors.New("approve orchestration requires approver_email")
	}
	const q = `
		UPDATE orchestrations
		SET state = 'approved',
			approver_email = $2,
			approved_at = COALESCE(approved_at, now()),
			updated_at = now()
		WHERE orchestration_id = $1 AND state IN ('draft', 'approved')
		RETURNING ` + orchestrationColumns
	out, err := scanOrchestration(s.pool.QueryRow(ctx, q, strings.TrimSpace(orchestrationID), approverEmail))
	if errors.Is(err, pgx.ErrNoRows) {
		return Orchestration{}, ErrOrchestrationNotFound
	}
	return out, err
}

// UpdatePhaseStatus transitions a phase's status. Only the runtime status
// column moves; the frozen plan fields are never touched.
func (s *OrchestrationStore) UpdatePhaseStatus(ctx context.Context, phaseID string, status PhaseStatus) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	if !validPhaseStatus(status) {
		return OrchestrationPhase{}, fmt.Errorf("invalid phase status %q", status)
	}
	const q = `
		UPDATE orchestration_phases
		SET status = $2, updated_at = now()
		WHERE phase_id = $1
		RETURNING ` + orchestrationPhaseColumns
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, strings.TrimSpace(phaseID), string(status)))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// AttachPhaseSpoke records the spoke session spawned for a phase and moves it
// to 'running'.
func (s *OrchestrationStore) AttachPhaseSpoke(ctx context.Context, phaseID, spokeSessionID string) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	spokeSessionID = strings.TrimSpace(spokeSessionID)
	if spokeSessionID == "" {
		return OrchestrationPhase{}, errors.New("attach phase spoke requires a spoke_session_id")
	}
	const q = `
		UPDATE orchestration_phases
		SET spoke_session_id = $2, status = 'running', updated_at = now()
		WHERE phase_id = $1
		RETURNING ` + orchestrationPhaseColumns
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, strings.TrimSpace(phaseID), spokeSessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// SetPhasePRRequest carries the PR coordinates a phase's spoke opened.
type SetPhasePRRequest struct {
	PROwner  string
	PRName   string
	PRNumber int
	PRURL    string
}

// MarkPhasePROpen stamps the PR coordinates a phase opened and moves it to
// 'pr_open'. The pr_owner/pr_name/pr_number triple is what the PR->phase
// reverse lookup keys on.
func (s *OrchestrationStore) MarkPhasePROpen(ctx context.Context, phaseID string, req SetPhasePRRequest) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	prOwner := strings.ToLower(strings.TrimSpace(req.PROwner))
	prName := strings.ToLower(strings.TrimSpace(req.PRName))
	if prOwner == "" || prName == "" || req.PRNumber <= 0 {
		return OrchestrationPhase{}, errors.New("mark phase pr open requires pr owner/name/number")
	}
	const q = `
		UPDATE orchestration_phases
		SET pr_owner = $2, pr_name = $3, pr_number = $4, pr_url = $5,
			status = 'pr_open', updated_at = now()
		WHERE phase_id = $1
		RETURNING ` + orchestrationPhaseColumns
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q,
		strings.TrimSpace(phaseID), prOwner, prName, req.PRNumber, strings.TrimSpace(req.PRURL)))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// MarkPhaseMerged records the merge commit for a phase's PR and moves it to
// 'merged'. Called by the eventual merged-PR webhook handler after it maps the
// PR back to this phase via GetPhaseByPR.
func (s *OrchestrationStore) MarkPhaseMerged(ctx context.Context, phaseID, mergeSHA string) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	const q = `
		UPDATE orchestration_phases
		SET merge_sha = $2, status = 'merged', updated_at = now()
		WHERE phase_id = $1
		RETURNING ` + orchestrationPhaseColumns
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, strings.TrimSpace(phaseID), strings.TrimSpace(mergeSHA)))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// GetPhaseByPR is the PR -> phase reverse lookup: the eventual webhook handler
// receives a merged PR (repo + number) and maps it to its owning phase (which
// carries the orchestration_id). Owner/name are matched case-insensitively
// (stored lowercased); the most-recently-updated match wins, mirroring
// CIWatchStore.GetByPR.
func (s *OrchestrationStore) GetPhaseByPR(ctx context.Context, prOwner, prName string, prNumber int) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	const q = `SELECT ` + orchestrationPhaseColumns + `
		FROM orchestration_phases
		WHERE pr_owner = $1 AND pr_name = $2 AND pr_number = $3
		ORDER BY updated_at DESC
		LIMIT 1`
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q,
		strings.ToLower(strings.TrimSpace(prOwner)), strings.ToLower(strings.TrimSpace(prName)), prNumber))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// GetPhaseBySpokeSession is the session -> phase reverse lookup: a phase's
// spoke session registers a PR (via the CI-watch path) and the orchestrator
// must join that session back to its owning phase to copy the PR coordinates
// onto it. The most-recently-updated match wins, mirroring GetPhaseByPR. A
// session that is not any phase's spoke returns ErrOrchestrationPhaseNotFound.
func (s *OrchestrationStore) GetPhaseBySpokeSession(ctx context.Context, spokeSessionID string) (OrchestrationPhase, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, errors.New("orchestration store unavailable")
	}
	spokeSessionID = strings.TrimSpace(spokeSessionID)
	if spokeSessionID == "" {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	const q = `SELECT ` + orchestrationPhaseColumns + `
		FROM orchestration_phases
		WHERE spoke_session_id = $1
		ORDER BY updated_at DESC
		LIMIT 1`
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, spokeSessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, ErrOrchestrationPhaseNotFound
	}
	return out, err
}

// MarkPhaseReady promotes a phase from 'pending' to 'ready' once the advance
// loop has computed that its depends_on are all satisfied. The transition is
// guarded on status='pending' so the call is idempotent and safe under
// concurrent advancers/reconcilers: only the writer that observes the row still
// pending flips it, and the returned bool reports whether this call did. A
// phase already past pending (claimed, running, merged) is left untouched.
func (s *OrchestrationStore) MarkPhaseReady(ctx context.Context, phaseID string) (OrchestrationPhase, bool, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, false, errors.New("orchestration store unavailable")
	}
	const q = `
		UPDATE orchestration_phases
		SET status = 'ready', updated_at = now()
		WHERE phase_id = $1 AND status = 'pending'
		RETURNING ` + orchestrationPhaseColumns
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, strings.TrimSpace(phaseID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, false, nil
	}
	if err != nil {
		return OrchestrationPhase{}, false, err
	}
	return out, true, nil
}

// ClaimPhaseForSpawn atomically claims a 'ready' phase for spoke dispatch,
// moving it to 'running'. The status-keyed conditional UPDATE is the
// concurrency choke point: a duplicate merged-PR webhook, two orchestrator
// replicas, or the webhook racing the reconcile backstop can all call this for
// the same phase, but exactly one wins the ready->running transition (the
// row-level write lock serializes them and the WHERE no longer matches once the
// status has moved). The winner — the only caller that gets ok=true — then
// creates the session and records it with AttachPhaseSpoke. spoke_session_id is
// intentionally left empty by the claim so a crash between claim and attach is
// recoverable by RequeuePhaseForRespawn rather than silently stranding the run.
func (s *OrchestrationStore) ClaimPhaseForSpawn(ctx context.Context, phaseID string) (OrchestrationPhase, bool, error) {
	if s == nil || s.pool == nil {
		return OrchestrationPhase{}, false, errors.New("orchestration store unavailable")
	}
	const q = `
		UPDATE orchestration_phases
		SET status = 'running', updated_at = now()
		WHERE phase_id = $1 AND status = 'ready'
		RETURNING ` + orchestrationPhaseColumns
	out, err := scanOrchestrationPhase(s.pool.QueryRow(ctx, q, strings.TrimSpace(phaseID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestrationPhase{}, false, nil
	}
	if err != nil {
		return OrchestrationPhase{}, false, err
	}
	return out, true, nil
}

// RequeuePhaseForRespawn recovers a phase that was claimed for spawn
// ('running') but never got a spoke recorded — the spawn errored, or the
// process died between ClaimPhaseForSpawn and AttachPhaseSpoke. It moves such a
// row back to 'ready' so the next advance/reconcile pass re-claims and re-spawns
// it. Guarded on status='running' AND spoke_session_id=” so it never disturbs a
// phase whose spoke is live; the returned bool serializes recovery the same way
// the claim serializes dispatch (only the writer that flips running->ready
// proceeds to re-spawn).
func (s *OrchestrationStore) RequeuePhaseForRespawn(ctx context.Context, phaseID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("orchestration store unavailable")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE orchestration_phases
		SET status = 'ready', updated_at = now()
		WHERE phase_id = $1 AND status = 'running' AND spoke_session_id = ''
	`, strings.TrimSpace(phaseID))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListActiveOrchestrationIDs returns the ids of runs the advance loop should
// keep driving: those in 'approved' (a frozen plan whose root phases have not
// been dispatched yet) or 'running' (at least one phase in flight). Terminal
// runs (done/failed) and not-yet-approved drafts are excluded. The reconcile
// backstop walks this set to re-drive reality for runs a dropped webhook left
// stalled.
func (s *OrchestrationStore) ListActiveOrchestrationIDs(ctx context.Context) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("orchestration store unavailable")
	}
	const q = `SELECT orchestration_id FROM orchestrations
		WHERE state IN ('approved', 'running')
		ORDER BY created_at ASC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func validPhaseStatus(status PhaseStatus) bool {
	switch status {
	case PhasePending, PhaseReady, PhaseRunning, PhasePROpen,
		PhaseMerged, PhaseBlocked, PhaseSkipped:
		return true
	default:
		return false
	}
}

type orchestrationRowScanner interface {
	Scan(dest ...any) error
}

func scanOrchestration(row orchestrationRowScanner) (Orchestration, error) {
	var out Orchestration
	var state string
	var integrationBranch *string
	var approvedAt *time.Time
	err := row.Scan(
		&out.OrchestrationID,
		&out.OwnerEmail,
		&out.ApproverEmail,
		&out.RepoOwner,
		&out.RepoName,
		&integrationBranch,
		&state,
		&out.Plan,
		&out.PlanHash,
		&out.PhaseCount,
		&out.CreatedAt,
		&out.UpdatedAt,
		&approvedAt,
	)
	if err != nil {
		return Orchestration{}, err
	}
	out.State = OrchestrationState(state)
	if integrationBranch != nil {
		out.IntegrationBranch = *integrationBranch
	}
	out.ApprovedAt = approvedAt
	return out, nil
}

func scanOrchestrationPhase(row orchestrationRowScanner) (OrchestrationPhase, error) {
	var out OrchestrationPhase
	var target, status string
	var dependsOn []byte
	err := row.Scan(
		&out.PhaseID,
		&out.OrchestrationID,
		&out.Ordinal,
		&out.Key,
		&out.Brief,
		&dependsOn,
		&target,
		&status,
		&out.SpokeSessionID,
		&out.PROwner,
		&out.PRName,
		&out.PRNumber,
		&out.PRURL,
		&out.MergeSHA,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		return OrchestrationPhase{}, err
	}
	out.Target = PhaseTarget(target)
	out.Status = PhaseStatus(status)
	if len(dependsOn) > 0 {
		if err := json.Unmarshal(dependsOn, &out.DependsOn); err != nil {
			return OrchestrationPhase{}, err
		}
	}
	return out, nil
}
