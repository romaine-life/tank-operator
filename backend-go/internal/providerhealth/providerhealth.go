// Package providerhealth drives the transcript-surfaced
// "<provider> sign-in expired" banner.
//
// The pipeline is two layers per the design at docs/features/transcript/contract.md:
//
//  1. A durable Postgres row in `provider_credential_health` (one per
//     provider, owner_scope) is the source of truth for whether a
//     provider's OAuth blob is currently usable. The orchestrator's
//     poller writes this row when the api-proxy reports a sustained
//     refresh failure (debounced over 30s so a single transient failure
//     does not flap the banner).
//
//  2. On every transition (healthy→failed, failed→healthy), the poller
//     fans out a session.status event into every active session whose
//     mode requires the affected provider. The events use the extended
//     session.status payload (failure_scope, failure_subject, action)
//     that the SPA's existing session.status renderer surfaces in the
//     transcript. See backend-go/internal/conversation/types.go for
//     the contract.
//
// What this package replaces: the previous SPA-side "Error" pill that
// hid the upstream cause (a String() coercion at the runner produced
// "[object Object]" in the durable payload). The pill was retired in
// PR #638; this package is the heavy version of "make sustained
// provider failures legible in the transcript" — the surface where the
// existing session.status events already render.
package providerhealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// Snapshot is the wire shape the proxy's /health/<provider> endpoint
// returns. Mirror of tank_api_proxy.server.AuthInjector.health_snapshot()
// in Python — keep field tags aligned with that endpoint's JSON output.
type Snapshot struct {
	Provider        string   `json:"provider"`
	Result          string   `json:"result"`
	Reason          string   `json:"reason"`
	Text            string   `json:"text"`
	LastAttemptedAt *float64 `json:"last_attempted_at"`
	LastSucceededAt *float64 `json:"last_succeeded_at"`
	AttemptID       int64    `json:"attempt_id"`
}

// Source is the per-provider health probe. The HTTP implementation
// targets the proxy's /health endpoint; tests use a stub.
type Source interface {
	Provider() string
	Fetch(ctx context.Context) (Snapshot, error)
}

// EventEmitter is the narrow interface the poller uses to write the
// per-session session.status:failed (or matching ready) banner events
// into the durable transcript ledger and wake any open SSE streams.
type EventEmitter interface {
	Upsert(ctx context.Context, event map[string]any) error
	Wake(ctx context.Context, storageKey string)
}

// ProviderModes maps provider names to the session modes that require
// the provider's auth blob. Updating this map is how a new provider's
// banner becomes session-scoped (claude → claude_gui / claude_cli;
// codex → codex_gui / codex_app_server). Today only codex is wired.
var ProviderModes = map[string][]string{
	"codex":  {"codex_gui", "codex_app_server"},
	"claude": {"claude_gui", "claude_cli"},
}

// Action carries the optional user-facing affordance copied into a
// session.status:failed event's payload.action. The SPA renders this
// as a button next to the system-role banner text. Empty Label/Href
// means "no action available" — the contract test rejects content-free
// banners (reason must be non-empty when no action is given).
type Action struct {
	Label string
	Href  string
}

// ProviderConfig binds a provider name to its detection source and the
// optional re-sign-in affordance. The poller iterates configs at
// startup so adding claude is mechanical (one config struct, no
// architecture change).
type ProviderConfig struct {
	Provider string
	Source   Source
	Action   Action // optional; empty Label disables the SPA button
}

// ManagerConfig wires the package's runtime dependencies. Owned by
// cmd/tank-operator/main.go.
type ManagerConfig struct {
	Store     *pgstore.ProviderCredentialHealthStore
	Pool      *pgxpool.Pool
	Emitter   EventEmitter
	Providers []ProviderConfig
	Scope     string
	// PollInterval defaults to 30s. Smaller values cost more proxy
	// requests; larger values delay the transcript banner appearing
	// after a refresh storm starts. Per the cost story documented in
	// docs/quality-timeframes.md, 30s is the negotiated minimum.
	PollInterval time.Duration
	// DebounceCycles is how many consecutive failed polls must occur
	// before the manager transitions Layer 1 to "failed". Default 1
	// (single-cycle): a single failed poll, given the 30s interval,
	// already represents 30s of sustained failure. Tests override
	// this to 0 to make transitions immediate.
	DebounceCycles int
	// Metrics is optional; nil uses a no-op recorder.
	Metrics Metrics
}

// Metrics is the prometheus-shaped sink the manager calls on every
// poll + transition. See cmd/tank-operator/observability.go for the
// promauto-backed implementation.
type Metrics interface {
	RecordPoll(provider string, ok bool)
	RecordHealthStatus(provider, scope, status string)
	RecordTransition(provider, scope, from, to, reason string)
	RecordFailureDuration(provider, scope string, seconds float64)
	RecordFanout(provider, scope string, sessions int)
}

type noopMetrics struct{}

func (noopMetrics) RecordPoll(string, bool)                  {}
func (noopMetrics) RecordHealthStatus(string, string, string) {}
func (noopMetrics) RecordTransition(string, string, string, string, string) {}
func (noopMetrics) RecordFailureDuration(string, string, float64) {}
func (noopMetrics) RecordFanout(string, string, int)          {}

// Manager runs the per-provider polling loop and writes Layer 1 + the
// per-session fan-out. Use NewManager to construct one.
type Manager struct {
	cfg              ManagerConfig
	failedCycles     map[string]int
	failureStartedAt map[string]time.Time
}

func NewManager(cfg ManagerConfig) *Manager {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.DebounceCycles < 0 {
		cfg.DebounceCycles = 0
	}
	if cfg.Metrics == nil {
		cfg.Metrics = noopMetrics{}
	}
	return &Manager{
		cfg:              cfg,
		failedCycles:     map[string]int{},
		failureStartedAt: map[string]time.Time{},
	}
}

// Run starts the polling loop. Returns when ctx is cancelled. The
// manager is configured for multiple providers; today only "codex" is
// wired, but the loop handles any number.
func (m *Manager) Run(ctx context.Context) error {
	if m == nil || m.cfg.Store == nil {
		return errors.New("providerhealth: store is required")
	}
	if len(m.cfg.Providers) == 0 {
		slog.Info("providerhealth manager started with no providers configured; idle")
		<-ctx.Done()
		return ctx.Err()
	}
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()
	// Run an initial poll immediately so a freshly-booted orchestrator
	// reflects the proxy's current state without waiting for the first
	// tick. The 30s interval is for sustained observation, not first
	// detection.
	for _, p := range m.cfg.Providers {
		m.pollOnce(ctx, p)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for _, p := range m.cfg.Providers {
				m.pollOnce(ctx, p)
			}
		}
	}
}

// pollOnce runs one poll cycle for one provider. Public for tests.
func (m *Manager) PollOnce(ctx context.Context, provider ProviderConfig) {
	m.pollOnce(ctx, provider)
}

func (m *Manager) pollOnce(ctx context.Context, p ProviderConfig) {
	snapshot, err := p.Source.Fetch(ctx)
	if err != nil {
		// A transient proxy fetch failure (e.g. proxy pod restarting)
		// must not flip the banner. Skip this cycle.
		m.cfg.Metrics.RecordPoll(p.Provider, false)
		slog.Warn("providerhealth poll failed; skipping cycle",
			"provider", p.Provider, "error", err)
		return
	}
	m.cfg.Metrics.RecordPoll(p.Provider, true)
	desired := classifySnapshot(snapshot)
	current, err := m.cfg.Store.Get(ctx, p.Provider, pgstore.OwnerScopeHost)
	if err != nil && !errors.Is(err, pgstore.ErrProviderCredentialHealthNotFound) {
		slog.Warn("providerhealth layer-1 read failed",
			"provider", p.Provider, "error", err)
		return
	}
	previousStatus := current.Status
	if previousStatus == "" {
		previousStatus = pgstore.ProviderHealthStatusHealthy
	}
	if desired == pgstore.ProviderHealthStatusFailed {
		m.failedCycles[p.Provider]++
		if _, ok := m.failureStartedAt[p.Provider]; !ok {
			m.failureStartedAt[p.Provider] = time.Now().UTC()
		}
		if m.failedCycles[p.Provider] <= m.cfg.DebounceCycles {
			// Still inside the debounce window; do not transition yet.
			return
		}
	} else {
		m.failedCycles[p.Provider] = 0
	}
	if previousStatus == desired {
		// No transition; just refresh the gauge.
		m.cfg.Metrics.RecordHealthStatus(p.Provider, pgstore.OwnerScopeHost, desired)
		return
	}
	now := time.Now().UTC()
	row := pgstore.ProviderCredentialHealth{
		Provider:        p.Provider,
		OwnerScope:      pgstore.OwnerScopeHost,
		Status:          desired,
		Reason:          snapshot.Reason,
		Text:            bannerText(p, desired, snapshot),
		ActionLabel:     "",
		ActionHref:      "",
		DetectedAt:      now,
		LastAttemptedAt: unixToTime(snapshot.LastAttemptedAt, now),
		LastSucceededAt: unixToTimePtr(snapshot.LastSucceededAt),
	}
	if desired == pgstore.ProviderHealthStatusFailed {
		row.ActionLabel = p.Action.Label
		row.ActionHref = p.Action.Href
	}
	expectedVersion := current.RowVersion
	if errors.Is(err, pgstore.ErrProviderCredentialHealthNotFound) {
		expectedVersion = -1
	}
	persisted, err := m.cfg.Store.UpsertTransition(ctx, row, expectedVersion)
	if err != nil {
		if errors.Is(err, pgstore.ErrProviderCredentialHealthStale) {
			// Another replica raced us; back off and try next cycle.
			slog.Info("providerhealth transition lost race; another replica wrote first",
				"provider", p.Provider)
			return
		}
		slog.Error("providerhealth layer-1 upsert failed",
			"provider", p.Provider, "error", err)
		return
	}
	m.cfg.Metrics.RecordHealthStatus(p.Provider, pgstore.OwnerScopeHost, persisted.Status)
	m.cfg.Metrics.RecordTransition(p.Provider, pgstore.OwnerScopeHost, previousStatus, persisted.Status, persisted.Reason)
	if previousStatus == pgstore.ProviderHealthStatusFailed && persisted.Status == pgstore.ProviderHealthStatusHealthy {
		if started, ok := m.failureStartedAt[p.Provider]; ok {
			m.cfg.Metrics.RecordFailureDuration(p.Provider, pgstore.OwnerScopeHost, time.Since(started).Seconds())
			delete(m.failureStartedAt, p.Provider)
		}
	}
	if err := m.FanOut(ctx, p.Provider, persisted); err != nil {
		slog.Error("providerhealth fan-out failed",
			"provider", p.Provider, "error", err)
	}
}

// FanOut emits a session.status event for every active session whose
// mode requires the provider. Public for the session-create backfill
// path: when a session is created while Layer 1 is in a failed state,
// the create handler reads the row and calls FanOut for the single
// new session id.
//
// The transcript contract demands ordering: the failed banner must NOT
// pre-empt the user's first message; this function only emits to
// sessions whose user_message + boot events already exist. The poll
// path satisfies this implicitly (the sessions are already alive); the
// create-backfill path is responsible for sequencing the call AFTER
// the boot events land.
func (m *Manager) FanOut(ctx context.Context, provider string, row pgstore.ProviderCredentialHealth) error {
	if m == nil || m.cfg.Emitter == nil || m.cfg.Pool == nil {
		return nil
	}
	modes := ProviderModes[provider]
	if len(modes) == 0 {
		return nil
	}
	sessions, err := m.listActiveSessions(ctx, modes)
	if err != nil {
		return fmt.Errorf("list sessions for %s fan-out: %w", provider, err)
	}
	count := 0
	for _, sess := range sessions {
		if err := m.emitForSession(ctx, provider, sess, row); err != nil {
			slog.Warn("providerhealth fan-out emit failed",
				"provider", provider,
				"session_id", sess.SessionID,
				"email", sess.Email,
				"error", err)
			continue
		}
		count++
	}
	m.cfg.Metrics.RecordFanout(provider, pgstore.OwnerScopeHost, count)
	return nil
}

// EmitForSession emits a single session.status event for one session
// based on the current Layer 1 row. Used by the session-create backfill
// path. The event respects the transcript contract — caller is
// responsible for ordering it after the session's boot events.
func (m *Manager) EmitForSession(ctx context.Context, provider, sessionID, email string, row pgstore.ProviderCredentialHealth) error {
	if m == nil || m.cfg.Emitter == nil {
		return nil
	}
	return m.emitForSession(ctx, provider, activeSession{SessionID: sessionID, Email: email, Scope: m.cfg.Scope}, row)
}

type activeSession struct {
	SessionID string
	Email     string
	Scope     string
}

func (m *Manager) listActiveSessions(ctx context.Context, modes []string) ([]activeSession, error) {
	// Active for provider-banner fan-out = (Pending|Active) AND mode
	// requires this provider. Scope is bound to the orchestrator's
	// scope so test slots don't fan out into prod. Visibility is
	// filtered Go-side per docs/session-list-redesign.md Phase 2 (the
	// SQL returns every matching row; the Visible column is read into
	// the struct and consulted by the caller). Soft-deleted sessions
	// (visible=false) are skipped — a banner emitted into a
	// just-deleted session would be cosmetic noise.
	rows, err := m.cfg.Pool.Query(ctx, `
		SELECT email, session_id, visible
		FROM sessions
		WHERE session_scope = $1
		  AND status IN ('Pending', 'Active')
		  AND mode = ANY($2::text[])
	`, m.cfg.Scope, modes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeSession
	for rows.Next() {
		var email, sessionID string
		var visible bool
		if err := rows.Scan(&email, &sessionID, &visible); err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		out = append(out, activeSession{
			SessionID: sessionID,
			Email:     email,
			Scope:     m.cfg.Scope,
		})
	}
	return out, rows.Err()
}

func (m *Manager) emitForSession(ctx context.Context, provider string, sess activeSession, row pgstore.ProviderCredentialHealth) error {
	storageKey := sessionmodel.SessionStorageKey(sess.Scope, sess.SessionID)
	timelineID := fmt.Sprintf("session:%s:provider:%s:status", sess.SessionID, provider)
	statusKey := row.Status
	// The schema only emits transcript events for the failed and ready
	// values; degraded does not produce a banner (intermediate state).
	if statusKey != pgstore.ProviderHealthStatusFailed && statusKey != pgstore.ProviderHealthStatusHealthy {
		return nil
	}
	mappedStatus := "ready"
	text := fmt.Sprintf("%s sign-in is back online.", capitalize(provider))
	payload := map[string]any{
		"status": mappedStatus,
		"text":   text,
	}
	if statusKey == pgstore.ProviderHealthStatusFailed {
		mappedStatus = "failed"
		text = row.Text
		if text == "" {
			text = fmt.Sprintf("%s sign-in is unavailable. Re-authenticate to restore service.", capitalize(provider))
		}
		payload = map[string]any{
			"status":           mappedStatus,
			"text":             text,
			"failure_scope":    "provider",
			"failure_subject":  provider,
			"failure_reason":   firstNonEmpty(row.Reason, "unknown"),
		}
		if row.ActionLabel != "" && row.ActionHref != "" {
			payload["action"] = map[string]any{
				"label": row.ActionLabel,
				"href":  row.ActionHref,
			}
		}
	}
	// order_key matches the session.status convention used by the
	// existing pgstore migration trigger so a fresh banner inserted
	// alongside boot events sorts deterministically. status_sequence
	// 00000003 keeps provider-status events after loading (0) and
	// ready (1) boot events.
	now := time.Now().UTC()
	orderKey := fmt.Sprintf("%013d-00000003-%s",
		now.UnixMilli(),
		timelineID,
	)
	event := map[string]any{
		"event_id":        timelineID,
		"uuid":            timelineID,
		"id":              timelineID,
		"order_key":       orderKey,
		"conversation_id": sess.SessionID,
		"session_id":      sess.SessionID,
		"tank_session_id": storageKey,
		"timeline_id":     timelineID,
		"actor":           string(conversation.ActorSystem),
		"source":          string(conversation.SourceTank),
		"type":            string(conversation.EventSessionStatus),
		"created_at":      now.Format("2006-01-02T15:04:05.000Z"),
		"written_at":      now.Format("2006-01-02T15:04:05.000Z"),
		"visibility":      "durable",
		"producer": map[string]any{
			"name": "tank-operator",
		},
		"payload": payload,
	}
	if sess.Email != "" {
		event["email"] = sess.Email
	}
	if err := m.cfg.Emitter.Upsert(ctx, event); err != nil {
		return fmt.Errorf("upsert session.status event: %w", err)
	}
	m.cfg.Emitter.Wake(ctx, storageKey)
	return nil
}

// CurrentHealth returns the Layer 1 row for the given provider, or a
// healthy zero-value row when no entry exists yet. Used by the
// session-create backfill path: a brand-new codex_gui session reads
// this and emits a session.status:failed when needed.
func (m *Manager) CurrentHealth(ctx context.Context, provider string) (pgstore.ProviderCredentialHealth, bool, error) {
	if m == nil || m.cfg.Store == nil {
		return pgstore.ProviderCredentialHealth{}, false, nil
	}
	row, err := m.cfg.Store.Get(ctx, provider, pgstore.OwnerScopeHost)
	if errors.Is(err, pgstore.ErrProviderCredentialHealthNotFound) {
		return pgstore.ProviderCredentialHealth{
			Provider:   provider,
			OwnerScope: pgstore.OwnerScopeHost,
			Status:     pgstore.ProviderHealthStatusHealthy,
		}, false, nil
	}
	if err != nil {
		return pgstore.ProviderCredentialHealth{}, false, err
	}
	return row, true, nil
}

// HTTPSource is the real Source: GETs /health/<provider> on the proxy.
type HTTPSource struct {
	provider string
	url      string
	client   *http.Client
}

func NewHTTPSource(provider, url string, client *http.Client) *HTTPSource {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &HTTPSource{provider: provider, url: url, client: client}
}

func (h *HTTPSource) Provider() string { return h.provider }

func (h *HTTPSource) Fetch(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Snapshot{}, fmt.Errorf("provider health %s: status %d", h.url, resp.StatusCode)
	}
	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode provider health %s: %w", h.url, err)
	}
	if snap.Provider == "" {
		snap.Provider = h.provider
	}
	return snap, nil
}

// classifySnapshot maps a proxy snapshot to a Layer 1 status.
// - success → healthy
// - http_error / request_failed / no_refresh_token → failed
// - unknown (no refresh attempted yet) → healthy (cached blob still serving)
func classifySnapshot(s Snapshot) string {
	switch s.Result {
	case "success", "unknown":
		return pgstore.ProviderHealthStatusHealthy
	case "http_error", "request_failed", "no_refresh_token":
		return pgstore.ProviderHealthStatusFailed
	}
	return pgstore.ProviderHealthStatusHealthy
}

func bannerText(p ProviderConfig, status string, snap Snapshot) string {
	if status != pgstore.ProviderHealthStatusFailed {
		return ""
	}
	if snap.Text != "" {
		return snap.Text
	}
	return fmt.Sprintf("%s sign-in is unavailable. Re-authenticate to restore service.", capitalize(p.Provider))
}

func unixToTime(value *float64, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	sec := int64(*value)
	nsec := int64((*value - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

func unixToTimePtr(value *float64) *time.Time {
	if value == nil {
		return nil
	}
	t := unixToTime(value, time.Time{})
	return &t
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
