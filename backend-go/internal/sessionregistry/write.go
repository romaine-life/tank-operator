package sessionregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// NextSessionID atomically allocates the next monotonic session id for this
// scope. One UPSERT increments and returns in a single statement; no retry
// loop or advisory lock is needed because the row-level conflict resolution
// is serial inside Postgres.
//
// The returned value matches the value the Cosmos impl returned: the number
// allocated by THIS call (i.e. the row's `next_session_number` is incremented
// to N+1 for the next caller, and we return N).
func (s *Store) NextSessionID(ctx context.Context) (string, error) {
	const q = `
		INSERT INTO session_counters (session_scope, next_session_number, updated_at)
		VALUES ($1, 2, now())
		ON CONFLICT (session_scope) DO UPDATE
		SET next_session_number = session_counters.next_session_number + 1,
			updated_at = now()
		RETURNING next_session_number - 1
	`
	var allocated int64
	if err := s.pool.QueryRow(ctx, q, s.scope).Scan(&allocated); err != nil {
		return "", fmt.Errorf("allocate session id: %w", err)
	}
	return fmt.Sprintf("%d", allocated), nil
}

// Upsert writes or overwrites a session record. created_at/updated_at
// default to now() on insert; on conflict (same primary key) we keep
// the existing created_at, advance updated_at, and bump row_version so
// downstream cursor readers see the change. The row_version bump on
// UPDATE is what makes Manager-driven mutations (create-with-existing-
// id, name set, mark-deleted) visible to the Phase 3 per-row UPDATE
// wire alongside the SessionController-driven writes.
func (s *Store) Upsert(ctx context.Context, record sessionmodel.SessionRecord) error {
	if err := validateSessionRecordForWrite(record); err != nil {
		return err
	}
	normalized := strings.ToLower(record.Email)
	scope := record.Scope
	if scope == "" {
		scope = s.scope
	}
	mode := record.Mode
	if mode == "" {
		mode = sessionmodel.DefaultSessionMode
	}
	now := time.Now().UTC()
	requestedAt := parseTimestamp(record.RequestedAt)
	createdAt := parseTimestamp(record.CreatedAt)
	updatedAt := parseTimestamp(record.UpdatedAt)
	if updatedAt.IsZero() {
		updatedAt = now
	}
	status := strings.TrimSpace(record.Status)
	readyAt := parseTimestamp(record.ReadyAt)
	// Determine the effective visible value (default true if unset).
	visible := record.Visible

	// `repos` and `capabilities` are written on INSERT (the user's selection at
	// create time) and intentionally NOT overwritten on conflict — the row
	// is owned by the create call; subsequent manager updates
	// (SetName, mark-deleted, lifecycle row writes) must not stomp
	// the selection. `clone_state` is not touched here at all; the
	// repo-cloner init container writes it via its own service-principal
	// path. Empty slice serializes to `{}` which
	// matches the schema default.
	repos := record.Repos
	if repos == nil {
		repos = []string{}
	}
	capabilities := record.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	sidebarPosition := record.SidebarPosition
	const q = `
		INSERT INTO sessions (
			email, session_scope, session_id,
			mode, pod_name, name, visible,
			requested_at, created_at, updated_at,
			status, ready_at,
			repos, capabilities, model, effort, agent_avatar_id, system_avatar_id, sidebar_position
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, COALESCE($9, now()), $10,
			COALESCE(NULLIF($11, ''), 'Pending'), $12,
			$13, $14, $15, $16, NULLIF($17, ''), NULLIF($18, ''), COALESCE(NULLIF($19, 0), nextval('sessions_row_version_seq'))
		)
		ON CONFLICT (email, session_scope, session_id) DO UPDATE
		SET mode         = EXCLUDED.mode,
			pod_name     = EXCLUDED.pod_name,
			name         = EXCLUDED.name,
			visible      = EXCLUDED.visible,
			requested_at = COALESCE(EXCLUDED.requested_at, sessions.requested_at),
			status       = CASE
				WHEN NULLIF($11, '') IS NULL THEN sessions.status
				ELSE EXCLUDED.status
			END,
			ready_at     = COALESCE(EXCLUDED.ready_at, sessions.ready_at),
			agent_avatar_id = COALESCE(EXCLUDED.agent_avatar_id, sessions.agent_avatar_id),
			system_avatar_id = COALESCE(EXCLUDED.system_avatar_id, sessions.system_avatar_id),
			updated_at   = EXCLUDED.updated_at,
			row_version  = nextval('sessions_row_version_seq')
	`
	_, err := s.pool.Exec(ctx, q,
		normalized,
		scope,
		record.ID,
		mode,
		record.PodName,
		record.Name,
		visible,
		nullableTimestamp(requestedAt),
		nullableTimestamp(createdAt),
		updatedAt,
		status,
		nullableTimestamp(readyAt),
		repos,
		capabilities,
		strings.TrimSpace(record.Model),
		strings.TrimSpace(record.Effort),
		strings.TrimSpace(record.AgentAvatarID),
		strings.TrimSpace(record.SystemAvatarID),
		sidebarPosition,
	)
	return err
}

func validateSessionRecordForWrite(record sessionmodel.SessionRecord) error {
	if record.Visible && strings.TrimSpace(record.AgentAvatarID) == "" {
		return fmt.Errorf("visible session %q is missing durable agent avatar id", record.ID)
	}
	return nil
}

// SetRuntimeConfig records the model/effort options the pod-side runner
// actually handed to the provider executable or SDK. The intended session
// config is immutable after create; this applied surface is allowed to
// update on runner restart so the UI reflects the current process.
func (s *Store) SetRuntimeConfig(ctx context.Context, email, sessionID, model, effort string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" {
		return nil
	}
	const q = `
		UPDATE sessions
		SET runtime_model         = $4,
			runtime_effort        = $5,
			runtime_configured_at = now(),
			updated_at            = now(),
			row_version           = nextval('sessions_row_version_seq')
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID, strings.TrimSpace(model), strings.TrimSpace(effort))
	return err
}

// SetRuntimeContextWindow records the first provider-observed model context
// window for a session. The requested session model is immutable after create,
// so later differing values are treated as provider/runtime anomalies and do
// not silently change the durable UI denominator.
func (s *Store) SetRuntimeContextWindow(ctx context.Context, email, sessionID string, tokens int64, source string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	sessionID = strings.TrimSpace(sessionID)
	source = strings.TrimSpace(source)
	if normalized == "" || sessionID == "" || tokens <= 0 {
		return nil
	}
	const q = `
		UPDATE sessions
		SET runtime_context_window_tokens      = $4,
			runtime_context_window_source      = $5,
			runtime_context_window_observed_at = COALESCE(runtime_context_window_observed_at, now()),
			updated_at                         = now(),
			row_version                        = nextval('sessions_row_version_seq')
		WHERE email = $1
			AND session_scope = $2
			AND session_id = $3
			AND runtime_context_window_tokens = 0
	`
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID, tokens, source)
	return err
}

// SetProviderRateLimitInfo stores the latest sanitized provider rate-limit
// payload observed by the session runner. Unlike the context window, this is
// latest-observed-wins so an admin can see the current rejected/overage shape.
func (s *Store) SetProviderRateLimitInfo(ctx context.Context, email, sessionID string, info map[string]any) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" || len(info) == 0 {
		return nil
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("sessionregistry: marshal provider rate-limit info: %w", err)
	}
	const q = `
		UPDATE sessions
		SET provider_rate_limit_info        = $4,
			provider_rate_limit_observed_at = now(),
			updated_at                      = now(),
			row_version                     = nextval('sessions_row_version_seq')
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	_, err = s.pool.Exec(ctx, q, normalized, s.scope, sessionID, raw)
	return err
}

// SetName updates the display name. Missing-session is a no-op
// (matches the previous Cosmos impl, which swallowed not-found there).
// Bumps row_version so the per-row update cursor advances.
func (s *Store) SetName(ctx context.Context, email, sessionID string, name *string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	const q = `
		UPDATE sessions
		SET name        = $4,
			updated_at  = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID, name)
	return err
}

// SetBugLabel attaches or clears the user's Tank-native bug label for one
// session. Missing-session errors are left to Postgres' FK/UPDATE behavior:
// callers validate ownership through Manager before writing. The sessions
// row_version bump is load-bearing for row-update catch-up.
func (s *Store) SetBugLabel(ctx context.Context, email, sessionID string, label *sessionmodel.SessionBugLabel) error {
	if label == nil {
		return s.SetBugLabels(ctx, email, sessionID, nil)
	}
	return s.SetBugLabels(ctx, email, sessionID, []*sessionmodel.SessionBugLabel{label})
}

// SetBugLabels replaces the user's Tank-native bug labels for one session.
// The session_bug_labels primary key includes bug_label_id, so this method can
// attach multiple labels while preserving SetBugLabel's replace-all semantics.
func (s *Store) SetBugLabels(ctx context.Context, email, sessionID string, labels []*sessionmodel.SessionBugLabel) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	sessionID = strings.TrimSpace(sessionID)
	if normalized == "" || sessionID == "" {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		DELETE FROM session_bug_labels
		WHERE owner_email = $1 AND session_scope = $2 AND session_id = $3
	`, normalized, s.scope, sessionID); err != nil {
		return err
	}

	seen := map[string]struct{}{}
	for _, label := range labels {
		if label == nil {
			continue
		}
		slug := strings.TrimSpace(label.Slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		var labelID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO bug_labels (owner_email, session_scope, name, slug, updated_at, archived_at)
			VALUES ($1, $2, $3, $4, now(), NULL)
			ON CONFLICT (owner_email, session_scope, slug) DO UPDATE
			SET name = EXCLUDED.name,
				updated_at = now(),
				archived_at = NULL
			RETURNING id
		`, normalized, s.scope, strings.TrimSpace(label.Name), slug).Scan(&labelID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO session_bug_labels (owner_email, session_scope, session_id, bug_label_id, attached_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (owner_email, session_scope, session_id, bug_label_id) DO UPDATE
			SET attached_at = now()
		`, normalized, s.scope, sessionID, labelID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET updated_at = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`, normalized, s.scope, sessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetTestState replaces the row's test_state jsonb. nil clears the
// column. Pod annotations are patched separately by Manager — the
// session-agent reads the annotation at runtime via the projected
// downward-API volume; this column is the snapshot-facing replica so
// Reader.List doesn't need a pod read. Bumps row_version.
func (s *Store) SetTestState(ctx context.Context, email, sessionID string, state map[string]any) error {
	clearColumn := ""
	if skillStateActive(state) {
		clearColumn = "rollout_state"
	}
	return s.setJSONBColumn(ctx, "test_state", clearColumn, email, sessionID, state)
}

// SetRolloutState replaces the row's rollout_state jsonb. Same shape
// and rationale as SetTestState.
func (s *Store) SetRolloutState(ctx context.Context, email, sessionID string, state map[string]any) error {
	clearColumn := ""
	if skillStateActive(state) {
		clearColumn = "test_state"
	}
	return s.setJSONBColumn(ctx, "rollout_state", clearColumn, email, sessionID, state)
}

func skillStateActive(state map[string]any) bool {
	active, _ := state["active"].(bool)
	return active
}

// SetCloneState replaces the row's clone_state jsonb. Written by the
// repo-cloner init container via the internal service-principal API so
// partial clone progress/failures are visible in the durable session row.
func (s *Store) SetCloneState(ctx context.Context, email, sessionID string, state map[string]any) error {
	return s.setJSONBColumn(ctx, "clone_state", "", email, sessionID, state)
}

func (s *Store) setJSONBColumn(ctx context.Context, column, clearColumn, email, sessionID string, state map[string]any) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	var payload any
	if state != nil {
		raw, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("sessionregistry: marshal %s: %w", column, err)
		}
		payload = raw
	}
	// Column is parameterized via constant strings only; no SQL
	// injection risk because the callers supply literal column names
	// from the known state-column methods above.
	assignments := []string{fmt.Sprintf("%s = $4", column)}
	if clearColumn != "" {
		assignments = append(assignments, fmt.Sprintf("%s = NULL", clearColumn))
	}
	assignments = append(assignments,
		"updated_at = now()",
		"row_version = nextval('sessions_row_version_seq')",
	)
	q := fmt.Sprintf(`
		UPDATE sessions
		SET %s
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`, strings.Join(assignments, ",\n\t\t\t"))
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID, payload)
	return err
}

// Get returns one session row, or (zero, nil) when the row is absent.
// Used by the row-update publisher to read the post-write state and
// fan it out on NATS. Reads every sidebar-visible column so the wire
// payload is a complete row snapshot.
func (s *Store) Get(ctx context.Context, owner, sessionID string) (sessionmodel.SessionRecord, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	if normalized == "" || strings.TrimSpace(sessionID) == "" {
		return sessionmodel.SessionRecord{}, false, nil
	}
	const q = `
		SELECT sessions.mode, sessions.pod_name, sessions.name, sessions.visible,
			COALESCE(to_char(sessions.requested_at   AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS requested_at,
			COALESCE(to_char(sessions.created_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS created_at,
			COALESCE(to_char(sessions.updated_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at,
			sessions.status,
			COALESCE(to_char(sessions.ready_at       AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS ready_at,
			COALESCE(to_char(sessions.terminating_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS terminating_at,
			sessions.activity_summary,
			sessions.test_state,
			sessions.rollout_state,
			COALESCE(sessions.repos, '{}'::text[]),
			sessions.clone_state,
			COALESCE(sessions.capabilities, '{}'::text[]),
			sessions.model,
			sessions.effort,
			sessions.runtime_model,
			sessions.runtime_effort,
			COALESCE(to_char(sessions.runtime_configured_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_configured_at,
			sessions.runtime_context_window_tokens,
			sessions.runtime_context_window_source,
			COALESCE(to_char(sessions.runtime_context_window_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_context_window_observed_at,
			sessions.provider_rate_limit_info,
			COALESCE(to_char(sessions.provider_rate_limit_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS provider_rate_limit_observed_at,
			sessions.compaction_count,
			COALESCE(sessions.agent_avatar_id, ''),
			COALESCE(sessions.system_avatar_id, ''),
			sessions.sidebar_position,
			sessions.row_version,
			bug_labels.id,
			bug_labels.name,
			bug_labels.slug
		FROM sessions
		LEFT JOIN LATERAL (
			SELECT bug_labels.id, bug_labels.name, bug_labels.slug
			FROM session_bug_labels
			JOIN bug_labels
				ON bug_labels.id = session_bug_labels.bug_label_id
			WHERE session_bug_labels.owner_email = sessions.email
			  AND session_bug_labels.session_scope = sessions.session_scope
			  AND session_bug_labels.session_id = sessions.session_id
			ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC
			LIMIT 1
		) bug_labels ON true
		WHERE sessions.email = $1 AND sessions.session_scope = $2 AND sessions.session_id = $3
	`
	var (
		mode, podName, requestedAt, createdAt, updatedAt      string
		status, readyAt, terminatingAt                        string
		name                                                  *string
		visible                                               bool
		activitySummary, testState, rolloutState, cloneState  []byte
		providerRateLimitInfo                                 []byte
		repos, capabilities                                   []string
		model, effort, runtimeModel, runtimeEffort, runtimeAt string
		runtimeContextWindowSource, runtimeContextWindowAt    string
		providerRateLimitObservedAt                           string
		agentAvatarID, systemAvatarID                         string
		runtimeContextWindowTokens, compactionCount           int64
		sidebarPosition, rowVersion                           int64
		bugLabelID                                            sql.NullInt64
		bugLabelName, bugLabelSlug                            sql.NullString
	)
	err := s.pool.QueryRow(ctx, q, normalized, s.scope, sessionID).Scan(
		&mode, &podName, &name, &visible,
		&requestedAt, &createdAt, &updatedAt,
		&status, &readyAt, &terminatingAt,
		&activitySummary, &testState, &rolloutState,
		&repos, &cloneState, &capabilities, &model, &effort,
		&runtimeModel, &runtimeEffort, &runtimeAt,
		&runtimeContextWindowTokens, &runtimeContextWindowSource, &runtimeContextWindowAt,
		&providerRateLimitInfo, &providerRateLimitObservedAt,
		&compactionCount,
		&agentAvatarID, &systemAvatarID,
		&sidebarPosition,
		&rowVersion,
		&bugLabelID,
		&bugLabelName,
		&bugLabelSlug,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionmodel.SessionRecord{}, false, nil
	}
	if err != nil {
		return sessionmodel.SessionRecord{}, false, err
	}
	if mode == "" {
		mode = sessionmodel.DefaultSessionMode
	}
	record := sessionmodel.SessionRecord{
		ID:                             sessionID,
		Email:                          normalized,
		Mode:                           mode,
		Scope:                          s.scope,
		PodName:                        podName,
		Name:                           name,
		Visible:                        visible,
		RequestedAt:                    requestedAt,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
		Status:                         status,
		ReadyAt:                        readyAt,
		TerminatingAt:                  terminatingAt,
		ActivitySummary:                activitySummary,
		TestState:                      unmarshalJSONB(testState),
		RolloutState:                   unmarshalJSONB(rolloutState),
		Repos:                          repos,
		CloneState:                     unmarshalJSONB(cloneState),
		Capabilities:                   capabilities,
		Model:                          model,
		Effort:                         effort,
		RuntimeModel:                   runtimeModel,
		RuntimeEffort:                  runtimeEffort,
		RuntimeConfiguredAt:            runtimeAt,
		RuntimeContextWindowTokens:     runtimeContextWindowTokens,
		RuntimeContextWindowSource:     runtimeContextWindowSource,
		RuntimeContextWindowObservedAt: runtimeContextWindowAt,
		ProviderRateLimitInfo:          unmarshalJSONB(providerRateLimitInfo),
		ProviderRateLimitObservedAt:    providerRateLimitObservedAt,
		CompactionCount:                compactionCount,
		AgentAvatarID:                  agentAvatarID,
		SystemAvatarID:                 systemAvatarID,
		SidebarPosition:                sidebarPosition,
		RowVersion:                     rowVersion,
		BugLabel:                       bugLabelFromScan(bugLabelID, bugLabelName, bugLabelSlug),
	}
	return record, true, nil
}

// Reorder replaces the visible sidebar order for one owner/scope. The
// caller must send a complete permutation of the currently-visible ids;
// partial orders are rejected instead of letting a stale browser tab
// overwrite a newer durable list.
func (s *Store) Reorder(ctx context.Context, email string, orderedIDs []string) ([]string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return nil, nil
	}
	cleaned := make([]string, 0, len(orderedIDs))
	seen := map[string]struct{}{}
	for _, id := range orderedIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return nil, sessionmodel.ErrSessionOrderConflict
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	const listQ = `
		SELECT session_id, visible
		FROM sessions
		WHERE email = $1 AND session_scope = $2
		ORDER BY sidebar_position DESC, created_at DESC, session_id DESC
	`
	rows, err := tx.Query(ctx, listQ, normalized, s.scope)
	if err != nil {
		return nil, err
	}
	var current []string
	for rows.Next() {
		var id string
		var visible bool
		if err := rows.Scan(&id, &visible); err != nil {
			rows.Close()
			return nil, err
		}
		if visible {
			current = append(current, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(cleaned) != len(current) {
		return nil, sessionmodel.ErrSessionOrderConflict
	}
	currentSet := map[string]struct{}{}
	for _, id := range current {
		currentSet[id] = struct{}{}
	}
	for _, id := range cleaned {
		if _, ok := currentSet[id]; !ok {
			return nil, sessionmodel.ErrSessionOrderConflict
		}
	}

	positions := make([]int64, len(cleaned))
	for i := range cleaned {
		positions[i] = int64(len(cleaned) - i)
	}
	const updateQ = `
		WITH updated AS (
			UPDATE sessions
			SET sidebar_position = v.position,
				updated_at = now(),
				row_version = nextval('sessions_row_version_seq')
			FROM unnest($4::text[], $5::bigint[]) AS v(session_id, position)
			WHERE sessions.email = $1
			  AND sessions.session_scope = $2
			  AND sessions.visible = true
			  AND sessions.session_id = v.session_id
			RETURNING sessions.session_id, sessions.row_version
		)
		SELECT session_id
		FROM updated
		ORDER BY row_version ASC
	`
	rows, err = tx.Query(ctx, updateQ, normalized, s.scope, cleaned, positions)
	if err != nil {
		return nil, err
	}
	var publishIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		publishIDs = append(publishIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(publishIDs) != len(cleaned) {
		return nil, sessionmodel.ErrSessionOrderConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return publishIDs, nil
}

// OwnerForSession returns the owner email associated with the given
// session id in this scope, or empty when no such session is registered.
// Used by sessioncontroller.ChatActivityEmitter to resolve which per-owner SSE subject a
// chat-derived activity delta should land on — the chat event payload
// itself carries only `session_id`, not the email, since `tank_session_id`
// is the durable routing key on the event bus.
func (s *Store) OwnerForSession(ctx context.Context, scope, sessionID string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = s.scope
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	const q = `
		SELECT email
		FROM sessions
		WHERE session_scope = $1 AND session_id = $2
		LIMIT 1
	`
	var email string
	switch err := s.pool.QueryRow(ctx, q, scope, sessionID).Scan(&email); {
	case err == nil:
		return strings.ToLower(strings.TrimSpace(email)), nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	default:
		return "", err
	}
}

// MarkDeleted sets visible=false. Missing-session is a no-op. Bumps
// row_version so the per-row update cursor surfaces the deletion to
// the Phase 3 wire — that's how the SPA's SessionStore learns to
// tombstone the id on the live transport (alongside the existing
// session.deleted lifecycle event during the dual-write window).
func (s *Store) MarkDeleted(ctx context.Context, email, sessionID string) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	const q = `
		UPDATE sessions
		SET visible     = false,
			updated_at  = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE email = $1 AND session_scope = $2 AND session_id = $3
	`
	_, err := s.pool.Exec(ctx, q, normalized, s.scope, sessionID)
	return err
}

// MarkScopeRetired hides every visible session row in this store's scope and
// returns the affected owner/id pairs so callers can publish row tombstones.
// It is used by test-slot teardown, where Glimmung removes the K8s runtime
// directly instead of going through Manager.Delete for each session.
func (s *Store) MarkScopeRetired(ctx context.Context) ([]RetiredSession, error) {
	const q = `
		UPDATE sessions
		SET visible     = false,
			updated_at  = now(),
			row_version = nextval('sessions_row_version_seq')
		WHERE session_scope = $1
		  AND visible = true
		RETURNING email, session_id
	`
	rows, err := s.pool.Query(ctx, q, s.scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var retired []RetiredSession
	for rows.Next() {
		var row RetiredSession
		if err := rows.Scan(&row.Email, &row.ID); err != nil {
			return nil, err
		}
		row.Email = strings.ToLower(strings.TrimSpace(row.Email))
		row.ID = strings.TrimSpace(row.ID)
		if row.Email == "" || row.ID == "" {
			continue
		}
		retired = append(retired, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return retired, nil
}

func parseTimestamp(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func nullableTimestamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
