// Package sessioncontroller — RowPublisher reads the post-write state
// of a session row from the registry and publishes it on the per-
// (owner, scope) NATS row-update subject. Every lifecycle producer
// (Manager user-actions, K8s watch, chat-activity emitter) funnels
// through this after its respective write so the wire shape is always
// "session N's current row is X" — the SPA's SessionStore is a row
// cache that replaces-by-id, no event-type discriminator.
//
// docs/session-list-redesign.md Phase 3 replaces the typed-event wire
// with this row-update path. Publish failures are logged + counted
// but non-fatal: the durable row is already committed, and the SPA
// catches up from the sessions table on reconnect.
package sessioncontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// RowFetcher reads a single sessions row by (owner, sessionID). The
// row-update publisher calls this after every write so the wire
// payload reflects post-commit state. sessionregistry.Store satisfies
// this directly.
type RowFetcher interface {
	Get(ctx context.Context, owner, sessionID string) (sessionmodel.SessionRecord, bool, error)
}

// RowUpdatePublisher is the narrow NATS publish surface for row-
// update payloads. *sessionbus.Bus satisfies it via
// PublishSessionRowUpdate.
type RowUpdatePublisher interface {
	PublishSessionRowUpdate(ctx context.Context, email, scope string, payload []byte) error
}

// SessionEventWakePublisher is the optional per-session transcript
// wake surface. sessions status DB triggers write durable
// session.status rows as part of registry updates; this wake lets an
// already-open transcript SSE read those rows immediately.
type SessionEventWakePublisher interface {
	PublishSessionEventWake(ctx context.Context, storageKey string) error
}

// RowPublisher fans a single row's post-write state out on NATS.
// Stateless beyond its dependencies; safe for concurrent use.
type RowPublisher struct {
	Fetcher   RowFetcher
	Publisher RowUpdatePublisher
	Scope     string
}

// PublishCurrentRow reads the row's current state from the registry
// and publishes it on the per-(owner, scope) row-update subject. A
// missing row is not an error — Manager.Create's intermediate states
// can race with a concurrent MarkDeleted, and the next snapshot is
// authoritative anyway.
func (p *RowPublisher) PublishCurrentRow(ctx context.Context, owner, sessionID string) {
	if p == nil || p.Fetcher == nil || p.Publisher == nil {
		return
	}
	owner = strings.ToLower(strings.TrimSpace(owner))
	sessionID = strings.TrimSpace(sessionID)
	if owner == "" || sessionID == "" {
		return
	}
	record, ok, err := p.Fetcher.Get(ctx, owner, sessionID)
	if err != nil {
		slog.Warn("sessioncontroller: row fetch for publish failed",
			"owner", owner, "scope", p.Scope, "session_id", sessionID, "error", err)
		return
	}
	if !ok {
		return
	}
	defer p.publishSessionEventWake(ctx, sessionID)
	payload, err := MarshalRowUpdate(record)
	if err != nil {
		slog.Warn("sessioncontroller: row marshal for publish failed",
			"owner", owner, "scope", p.Scope, "session_id", sessionID, "error", err)
		return
	}
	if err := p.Publisher.PublishSessionRowUpdate(ctx, owner, p.Scope, payload); err != nil {
		slog.Warn("sessioncontroller: row update publish failed",
			"owner", owner, "scope", p.Scope, "session_id", sessionID, "error", err)
	}
}

func (p *RowPublisher) publishSessionEventWake(ctx context.Context, sessionID string) {
	waker, ok := p.Publisher.(SessionEventWakePublisher)
	if !ok {
		return
	}
	storageKey := sessionmodel.SessionStorageKey(p.Scope, sessionID)
	if err := waker.PublishSessionEventWake(ctx, storageKey); err != nil {
		slog.Warn("sessioncontroller: session event wake publish failed",
			"scope", p.Scope, "session_id", sessionID, "storage_key", storageKey, "error", err)
	}
}

// RowUpdatePayload is the wire shape SSE clients receive. The SPA's
// SessionStore looks at Row.Visible to decide between "replace cached
// row" and "remove + tombstone" — no separate `deleted: true` shape
// because visibility on the row carries the same signal with no
// duplicate type discriminator. Cursor is row_version as a stringified
// int (mirrors the chat-window order_key shape).
type RowUpdatePayload struct {
	Row    rowWireShape `json:"row"`
	Cursor string       `json:"cursor"`
}

// rowWireShape is the SessionRecord projection that goes on the wire.
// JSON field names match the snapshot's Info struct one-for-one so
// the SPA can parse either /api/sessions (snapshot) or
// /api/sessions/events (live) payloads through the same parser into
// one SessionRow shape. Internal — callers go through
// MarshalRowUpdate.
type rowWireShape struct {
	ID              string         `json:"id"`
	Owner           string         `json:"owner"`
	Mode            string         `json:"mode"`
	Scope           string         `json:"session_scope"`
	PodName         string         `json:"pod_name,omitempty"`
	Name            *string        `json:"name,omitempty"`
	Visible         bool           `json:"visible"`
	Status          string         `json:"status"`
	RequestedAt     string         `json:"requested_at,omitempty"`
	CreatedAt       string         `json:"created_at,omitempty"`
	UpdatedAt       string         `json:"updated_at,omitempty"`
	ReadyAt         string         `json:"ready_at,omitempty"`
	TerminatingAt   string         `json:"terminating_at,omitempty"`
	ActivitySummary map[string]any `json:"activity_summary,omitempty"`
	TestState       map[string]any `json:"test_state,omitempty"`
	RolloutState    map[string]any `json:"rollout_state,omitempty"`
	// Repos and CloneState: always-emit / omit-when-nil to mirror
	// the snapshot Info struct field-for-field (see
	// sessions/sessions.go → Info). Repos is non-nil-on-the-wire so
	// the SPA never has to special-case "absent vs. empty"; clone
	// state is omitted until the repo-cloner init container writes back.
	Repos           []string       `json:"repos"`
	CloneState      map[string]any `json:"clone_state,omitempty"`
	SidebarPosition int64          `json:"sidebar_position"`
	RowVersion      int64          `json:"row_version"`
}

// MarshalRowUpdate produces the JSON wire payload for a single row.
// Exported so the SSE catch-up loop can reuse the same shape it
// fans out on NATS — the SPA only ever sees one wire shape.
func MarshalRowUpdate(record sessionmodel.SessionRecord) ([]byte, error) {
	var activity map[string]any
	if len(record.ActivitySummary) > 0 {
		if err := json.Unmarshal(record.ActivitySummary, &activity); err != nil {
			return nil, fmt.Errorf("row update marshal: activity_summary: %w", err)
		}
	}
	repos := record.Repos
	if repos == nil {
		repos = []string{}
	}
	wire := RowUpdatePayload{
		Row: rowWireShape{
			ID:              record.ID,
			Owner:           record.Email,
			Mode:            record.Mode,
			Scope:           record.Scope,
			PodName:         record.PodName,
			Name:            record.Name,
			Visible:         record.Visible,
			Status:          record.Status,
			RequestedAt:     record.RequestedAt,
			CreatedAt:       record.CreatedAt,
			UpdatedAt:       record.UpdatedAt,
			ReadyAt:         record.ReadyAt,
			TerminatingAt:   record.TerminatingAt,
			ActivitySummary: activity,
			TestState:       record.TestState,
			RolloutState:    record.RolloutState,
			Repos:           repos,
			CloneState:      record.CloneState,
			SidebarPosition: record.SidebarPosition,
			RowVersion:      record.RowVersion,
		},
		Cursor: fmt.Sprintf("%d", record.RowVersion),
	}
	return json.Marshal(wire)
}
