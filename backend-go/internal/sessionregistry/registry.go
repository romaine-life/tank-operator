package sessionregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// Store is the Postgres-backed session registry. Replaces the previous
// CosmosStore (which kept sessions as documents in the profiles container).
// Sessions live in the `sessions` table; the monotonic session-id counter
// lives in `session_counters`.
type Store struct {
	pool  *pgxpool.Pool
	scope string
}

type RetiredSession struct {
	Email string
	ID    string
}

func NewPostgresStore(pool *pgxpool.Pool, scope string) *Store {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	return &Store{pool: pool, scope: scope}
}

// List returns every session record for an owner in this scope (visible
// and invisible), ordered by durable sidebar position. Callers that only
// want visible rows must filter on SessionRecord.Visible.
//
// Returning invisible rows is load-bearing for the row-update catch-up
// path: a client that disconnects during delete needs to receive the
// visible=false row and tombstone the id when it reconnects. Reader.List
// filters invisible rows for the snapshot, but debug and catch-up paths
// consume the full owner-scoped row set.
func (s *Store) List(ctx context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	normalized := strings.ToLower(strings.TrimSpace(owner))
	if normalized == "" {
		return nil, nil
	}
	const q = `
		SELECT sessions.session_id, sessions.mode, sessions.pod_name, sessions.name, sessions.visible,
			sessions.session_image,
			sessions.session_image_metadata,
			COALESCE(to_char(sessions.requested_at   AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS requested_at,
			COALESCE(to_char(sessions.created_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS created_at,
			COALESCE(to_char(sessions.updated_at     AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS updated_at,
			sessions.status,
			COALESCE(to_char(sessions.ready_at       AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS ready_at,
			COALESCE(to_char(sessions.terminating_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS terminating_at,
			sessions.activity_summary,
			sessions.test_state,
			sessions.rollout_state,
			sessions.spoke_config,
			sessions.spawned_sessions,
			COALESCE(sessions.parent_session_id, '') AS parent_session_id,
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
			sessions.runtime_provider_session_id,
			COALESCE(to_char(sessions.runtime_provider_session_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS runtime_provider_session_observed_at,
			sessions.provider_rate_limit_info,
			COALESCE(to_char(sessions.provider_rate_limit_observed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS provider_rate_limit_observed_at,
			sessions.compaction_count,
			sessions.user_message_count,
			sessions.open_target,
			COALESCE(sessions.agent_avatar_id, ''),
			COALESCE(sessions.system_avatar_id, ''),
			sessions.sidebar_position,
			sessions.row_version,
			bug_labels.id,
			bug_labels.name,
			bug_labels.slug,
			COALESCE(bug_labels.labels_json, '[]'::jsonb)
		FROM sessions
		LEFT JOIN LATERAL (
			SELECT
				(array_agg(bug_labels.id ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC))[1] AS id,
				(array_agg(bug_labels.name ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC))[1] AS name,
				(array_agg(bug_labels.slug ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC))[1] AS slug,
				jsonb_agg(
					jsonb_build_object(
						'id', bug_labels.id,
						'name', bug_labels.name,
						'slug', bug_labels.slug,
						'display_name', 'bug: ' || bug_labels.name
					)
					ORDER BY session_bug_labels.attached_at DESC, bug_labels.id DESC
				) AS labels_json
			FROM session_bug_labels
			JOIN bug_labels
				ON bug_labels.id = session_bug_labels.bug_label_id
			WHERE session_bug_labels.owner_email = sessions.email
			  AND session_bug_labels.session_scope = sessions.session_scope
			  AND session_bug_labels.session_id = sessions.session_id
		) bug_labels ON true
		WHERE sessions.email = $1 AND sessions.session_scope = $2
		ORDER BY sessions.sidebar_position DESC, sessions.created_at DESC, sessions.session_id DESC
	`
	rows, err := s.pool.Query(ctx, q, normalized, s.scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []sessionmodel.SessionRecord
	for rows.Next() {
		var (
			sessionID, mode, podName, sessionImage, requestedAt, createdAt, updatedAt string
			status, readyAt, terminatingAt                                            string
			name                                                                      string
			visible                                                                   bool
			sessionImageMetadata                                                      []byte
			activitySummary, testState, rolloutState, spokeConfig, cloneState          []byte
			spawnedSessions                                                           []byte
			parentSessionID                                                           string
			providerRateLimitInfo                                                     []byte
			repos, capabilities                                                       []string
			model, effort, runtimeModel, runtimeEffort, runtimeAt                     string
			runtimeContextWindowSource, runtimeContextWindowObservedAt                string
			runtimeProviderSessionID, runtimeProviderSessionObservedAt                string
			providerRateLimitObservedAt                                               string
			openTarget                                                                string
			agentAvatarID, systemAvatarID                                             string
			runtimeContextWindowTokens, compactionCount, userMessageCount             int64
			sidebarPosition, rowVersion                                               int64
			bugLabelID                                                                sql.NullInt64
			bugLabelName, bugLabelSlug                                                sql.NullString
			bugLabelsRaw                                                              []byte
		)
		if err := rows.Scan(
			&sessionID, &mode, &podName, &name, &visible,
			&sessionImage, &sessionImageMetadata,
			&requestedAt, &createdAt, &updatedAt,
			&status, &readyAt, &terminatingAt,
			&activitySummary, &testState, &rolloutState, &spokeConfig,
			&spawnedSessions,
			&parentSessionID,
			&repos, &cloneState, &capabilities, &model, &effort,
			&runtimeModel, &runtimeEffort, &runtimeAt,
			&runtimeContextWindowTokens, &runtimeContextWindowSource, &runtimeContextWindowObservedAt,
			&runtimeProviderSessionID, &runtimeProviderSessionObservedAt,
			&providerRateLimitInfo, &providerRateLimitObservedAt,
			&compactionCount,
			&userMessageCount,
			&openTarget,
			&agentAvatarID, &systemAvatarID,
			&sidebarPosition,
			&rowVersion,
			&bugLabelID,
			&bugLabelName,
			&bugLabelSlug,
			&bugLabelsRaw,
		); err != nil {
			return nil, err
		}
		if mode == "" {
			mode = sessionmodel.DefaultSessionMode
		}
		records = append(records, sessionmodel.SessionRecord{
			ID:                               sessionID,
			Email:                            normalized,
			Mode:                             mode,
			Scope:                            s.scope,
			PodName:                          podName,
			SessionImage:                     sessionImage,
			SessionImageMetadata:             sessionmodel.DecodeImageVersionMetadata(sessionImageMetadata),
			Name:                             name,
			Visible:                          visible,
			RequestedAt:                      requestedAt,
			CreatedAt:                        createdAt,
			UpdatedAt:                        updatedAt,
			Status:                           status,
			ReadyAt:                          readyAt,
			TerminatingAt:                    terminatingAt,
			ActivitySummary:                  activitySummary,
			TestState:                        unmarshalJSONB(testState),
			RolloutState:                     unmarshalJSONB(rolloutState),
			SpokeConfig:                      unmarshalJSONB(spokeConfig),
			SpawnedSessions:                  sessionmodel.DecodeSpawnedSessions(spawnedSessions),
			ParentSessionID:                  parentSessionID,
			Repos:                            repos,
			CloneState:                       unmarshalJSONB(cloneState),
			Capabilities:                     capabilities,
			Model:                            model,
			Effort:                           effort,
			RuntimeModel:                     runtimeModel,
			RuntimeEffort:                    runtimeEffort,
			RuntimeConfiguredAt:              runtimeAt,
			RuntimeContextWindowTokens:       runtimeContextWindowTokens,
			RuntimeContextWindowSource:       runtimeContextWindowSource,
			RuntimeContextWindowObservedAt:   runtimeContextWindowObservedAt,
			RuntimeProviderSessionID:         runtimeProviderSessionID,
			RuntimeProviderSessionObservedAt: runtimeProviderSessionObservedAt,
			ProviderRateLimitInfo:            unmarshalJSONB(providerRateLimitInfo),
			ProviderRateLimitObservedAt:      providerRateLimitObservedAt,
			CompactionCount:                  compactionCount,
			UserMessageCount:                 userMessageCount,
			OpenTarget:                       openTarget,
			AgentAvatarID:                    agentAvatarID,
			SystemAvatarID:                   systemAvatarID,
			SidebarPosition:                  sidebarPosition,
			RowVersion:                       rowVersion,
			BugLabel:                         bugLabelFromScan(bugLabelID, bugLabelName, bugLabelSlug),
			BugLabels:                        bugLabelsFromJSON(bugLabelsRaw),
		})
	}
	return records, rows.Err()
}

func bugLabelsFromJSON(raw []byte) []*sessionmodel.SessionBugLabel {
	if len(raw) == 0 {
		return nil
	}
	var labels []*sessionmodel.SessionBugLabel
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil
	}
	out := labels[:0]
	for _, label := range labels {
		if label == nil || strings.TrimSpace(label.Name) == "" || strings.TrimSpace(label.Slug) == "" {
			continue
		}
		if strings.TrimSpace(label.DisplayName) == "" {
			label.DisplayName = "bug: " + strings.TrimSpace(label.Name)
		}
		out = append(out, label)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func bugLabelFromScan(id sql.NullInt64, name, slug sql.NullString) *sessionmodel.SessionBugLabel {
	if !id.Valid || !name.Valid || strings.TrimSpace(name.String) == "" || !slug.Valid || strings.TrimSpace(slug.String) == "" {
		return nil
	}
	labelName := strings.TrimSpace(name.String)
	return &sessionmodel.SessionBugLabel{
		ID:          id.Int64,
		Name:        labelName,
		Slug:        strings.TrimSpace(slug.String),
		DisplayName: "bug: " + labelName,
	}
}

// DefaultLiveSessionRecencyWindow is the recency safety margin
// ListLiveIDsForScope unions into the visible-rows liveness set.
//
// Why 24 h: every delete path (MarkDeleted, ClaimIdleForReap,
// MarkScopeRetired) bumps updated_at = now() in the same UPDATE that
// flips visible = false, so the window — measured from updated_at —
// restarts at deletion time and keeps a just-deleted session counted
// live while its pod terminates and its runner drains in-flight
// commands against the durable consumers. The real drain tail is
// minutes (pod termination grace + final durable event publication);
// 24 h is a deliberately deep margin because the asymmetry is extreme:
// holding a dead session's consumers one extra day costs a few KB of
// JetStream metadata and one hourly sweep pass of patience, while
// deleting a consumer a still-draining runner holds strands its final
// events. A full day also rides out operator pauses, NATS/orchestrator
// restarts, and clock skew without tuning.
const DefaultLiveSessionRecencyWindow = 24 * time.Hour

// ListLiveIDsForScope returns the session_ids in this orchestrator's
// scope whose JetStream consumers the orphan-consumer sweep must treat
// as live, regardless of owner: every VISIBLE row, plus any row —
// visible or not — whose updated_at falls within updatedWithin of the
// database clock (updatedWithin <= 0 defaults to
// DefaultLiveSessionRecencyWindow).
//
// Why visibility + recency instead of row existence: sessions rows are
// never hard-deleted — deletion only flips visible = false — so the
// predecessor predicate ("row exists in this scope", the retired
// ListAllIDsForScope) classified every session id ever created as live
// forever. The sweep could not identify a single orphan, the
// tank_session_bus_orphan_consumers gauge read 0, and stranded
// consumers accumulated unchecked against the JetStream memory cap —
// the exact failure mode the sweep was built to remediate.
//
// The recency union is the delete-race half of the sweep's safety
// story and pairs with — but cannot be replaced by — the sweep's
// MinAge guard: MinAge runs on the CONSUMER's creation clock and
// protects the create race (consumer created before its session row is
// readable), but a months-old consumer passes MinAge the instant its
// row goes invisible. Only the row-side updated_at clock, which every
// delete bumps, protects the consumer-still-draining-after-delete
// window.
//
// Scope-wide rather than per-owner because consumer names encode
// (scope, session_id) only — there is no owner dimension on the
// consumer side. The comparison runs on the database clock (now())
// because updated_at is written with now() by every sessions writer,
// keeping the recency check in a single clock domain.
func (s *Store) ListLiveIDsForScope(ctx context.Context, updatedWithin time.Duration) (map[string]struct{}, error) {
	if updatedWithin <= 0 {
		updatedWithin = DefaultLiveSessionRecencyWindow
	}
	const q = `
		SELECT session_id
		FROM sessions
		WHERE session_scope = $1
		  AND (visible OR updated_at >= now() - make_interval(secs => $2))
	`
	rows, err := s.pool.Query(ctx, q, s.scope, updatedWithin.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out, rows.Err()
}

// unmarshalJSONB turns a jsonb column's raw bytes into the
// map[string]any the snapshot handler expects to render. Empty/NULL
// columns return nil, which the SPA renders as "no state set."
func unmarshalJSONB(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
