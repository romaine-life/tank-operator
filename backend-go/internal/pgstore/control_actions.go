package pgstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ControlActionEvent struct {
	EventID       string
	InvocationID  string
	CreatedAt     time.Time
	OwnerEmail    string
	SessionScope  string
	SessionID     string
	SourceService string
	SourceTool    string
	Action        string
	Status        string
	TargetKind    string
	TargetRef     string
	RepoOwner     string
	RepoName      string
	PRNumber      *int
	ResultSHA     string
	Error         string
	Payload       []byte
}

type ControlActionStore struct {
	pool  *pgxpool.Pool
	scope string
}

func NewControlActionStore(pool *pgxpool.Pool, scope string) *ControlActionStore {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &ControlActionStore{pool: pool, scope: scope}
}

func (s *ControlActionStore) Append(ctx context.Context, event ControlActionEvent) (ControlActionEvent, error) {
	if s == nil || s.pool == nil {
		return ControlActionEvent{}, errors.New("control action store unavailable")
	}
	event = normalizeControlActionEvent(event, s.scope)
	if err := validateControlActionEvent(event); err != nil {
		return ControlActionEvent{}, err
	}
	if len(event.Payload) == 0 {
		event.Payload = []byte(`{}`)
	}
	const q = `
		INSERT INTO control_action_events (
			event_id, invocation_id, owner_email, session_scope, session_id,
			source_service, source_tool, action, status,
			target_kind, target_ref, repo_owner, repo_name, pr_number,
			result_sha, error, payload
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17
		)
		RETURNING event_id, invocation_id, created_at, owner_email, session_scope, session_id,
			source_service, source_tool, action, status, target_kind, target_ref,
			repo_owner, repo_name, pr_number, result_sha, error, payload
	`
	return scanControlActionEvent(s.pool.QueryRow(ctx, q,
		event.EventID, event.InvocationID, event.OwnerEmail, event.SessionScope, event.SessionID,
		event.SourceService, event.SourceTool, event.Action, event.Status,
		event.TargetKind, event.TargetRef, event.RepoOwner, event.RepoName, event.PRNumber,
		event.ResultSHA, event.Error, event.Payload,
	))
}

func (s *ControlActionStore) ListBySession(ctx context.Context, ownerEmail, sessionScope, sessionID string, limit int) ([]ControlActionEvent, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("control action store unavailable")
	}
	ownerEmail = strings.ToLower(strings.TrimSpace(ownerEmail))
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if ownerEmail == "" || sessionID == "" {
		return nil, errors.New("owner_email and session_id are required")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	const q = `
		SELECT event_id, invocation_id, created_at, owner_email, session_scope, session_id,
			source_service, source_tool, action, status, target_kind, target_ref,
			repo_owner, repo_name, pr_number, result_sha, error, payload
		FROM control_action_events
		WHERE owner_email = $1
		  AND session_scope = $2
		  AND session_id = $3
		ORDER BY created_at DESC, event_id DESC
		LIMIT $4
	`
	rows, err := s.pool.Query(ctx, q, ownerEmail, sessionScope, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ControlActionEvent
	for rows.Next() {
		event, err := scanControlActionEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *ControlActionStore) GetBySessionEvent(ctx context.Context, sessionScope, sessionID, eventID string) (ControlActionEvent, error) {
	if s == nil || s.pool == nil {
		return ControlActionEvent{}, errors.New("control action store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	eventID = strings.TrimSpace(eventID)
	if sessionID == "" || eventID == "" {
		return ControlActionEvent{}, errors.New("session_id and event_id are required")
	}
	const q = `
		SELECT event_id, invocation_id, created_at, owner_email, session_scope, session_id,
			source_service, source_tool, action, status, target_kind, target_ref,
			repo_owner, repo_name, pr_number, result_sha, error, payload
		FROM control_action_events
		WHERE session_scope = $1
		  AND session_id = $2
		  AND event_id = $3
	`
	return scanControlActionEvent(s.pool.QueryRow(ctx, q, sessionScope, sessionID, eventID))
}

func (s *ControlActionStore) BreakGlassDecisionForRequest(ctx context.Context, sessionScope, sessionID, requestEventID string) (ControlActionEvent, error) {
	if s == nil || s.pool == nil {
		return ControlActionEvent{}, errors.New("control action store unavailable")
	}
	sessionScope = strings.TrimSpace(sessionScope)
	if sessionScope == "" {
		sessionScope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	requestEventID = strings.TrimSpace(requestEventID)
	if sessionID == "" || requestEventID == "" {
		return ControlActionEvent{}, errors.New("session_id and request_event_id are required")
	}
	const q = `
		SELECT event_id, invocation_id, created_at, owner_email, session_scope, session_id,
			source_service, source_tool, action, status, target_kind, target_ref,
			repo_owner, repo_name, pr_number, result_sha, error, payload
		FROM control_action_events
		WHERE session_scope = $1
		  AND session_id = $2
		  AND payload->>'request_event_id' = $3
		  AND action IN (
			'github.break_glass.grant',
			'github.break_glass.deny',
			'azure.break_glass.grant',
			'azure.break_glass.deny'
		  )
		ORDER BY created_at ASC, event_id ASC
		LIMIT 1
	`
	return scanControlActionEvent(s.pool.QueryRow(ctx, q, sessionScope, sessionID, requestEventID))
}

func normalizeControlActionEvent(event ControlActionEvent, defaultScope string) ControlActionEvent {
	event.EventID = strings.TrimSpace(event.EventID)
	event.InvocationID = strings.TrimSpace(event.InvocationID)
	event.OwnerEmail = strings.ToLower(strings.TrimSpace(event.OwnerEmail))
	event.SessionScope = strings.TrimSpace(event.SessionScope)
	if event.SessionScope == "" {
		event.SessionScope = defaultScope
	}
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.SourceService = strings.TrimSpace(event.SourceService)
	event.SourceTool = strings.TrimSpace(event.SourceTool)
	event.Action = strings.TrimSpace(event.Action)
	event.Status = strings.TrimSpace(event.Status)
	event.TargetKind = strings.TrimSpace(event.TargetKind)
	event.TargetRef = strings.TrimSpace(event.TargetRef)
	event.RepoOwner = strings.TrimSpace(event.RepoOwner)
	event.RepoName = strings.TrimSpace(event.RepoName)
	event.ResultSHA = strings.TrimSpace(event.ResultSHA)
	event.Error = strings.TrimSpace(event.Error)
	return event
}

func validateControlActionEvent(event ControlActionEvent) error {
	switch {
	case event.EventID == "":
		return errors.New("event_id is required")
	case event.InvocationID == "":
		return errors.New("invocation_id is required")
	case event.OwnerEmail == "":
		return errors.New("owner_email is required")
	case event.SessionScope == "":
		return errors.New("session_scope is required")
	case event.SessionID == "":
		return errors.New("session_id is required")
	case event.SourceService == "":
		return errors.New("source_service is required")
	case event.SourceTool == "":
		return errors.New("source_tool is required")
	case event.Action == "":
		return errors.New("action is required")
	case event.Status == "":
		return errors.New("status is required")
	case event.TargetKind == "":
		return errors.New("target_kind is required")
	case event.TargetRef == "":
		return errors.New("target_ref is required")
	}
	return nil
}

type controlActionScanner interface {
	Scan(dest ...any) error
}

func scanControlActionEvent(row controlActionScanner) (ControlActionEvent, error) {
	var event ControlActionEvent
	if err := row.Scan(
		&event.EventID,
		&event.InvocationID,
		&event.CreatedAt,
		&event.OwnerEmail,
		&event.SessionScope,
		&event.SessionID,
		&event.SourceService,
		&event.SourceTool,
		&event.Action,
		&event.Status,
		&event.TargetKind,
		&event.TargetRef,
		&event.RepoOwner,
		&event.RepoName,
		&event.PRNumber,
		&event.ResultSHA,
		&event.Error,
		&event.Payload,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ControlActionEvent{}, err
		}
		return ControlActionEvent{}, err
	}
	return event, nil
}
