// Package sessioncontroller — RowWriter is the single write path that
// the K8s watch and chat-activity emitter call into for pod-state and
// chat-activity transitions. Per docs/session-list-redesign.md Phase
// 3 it owns:
//
//  1. The durable session_lifecycle_events append (retained until
//     Phase 4 drops the table; not on the wire path).
//  2. The sessions row UPDATE for the columns derived from the event
//     (status, ready_at, terminating_at, activity_summary) plus the
//     row_version bump.
//  3. A row-update fan-out on the NATS row-update subject via
//     RowPublisher, carrying the post-write row state.
//
// User-action mutations (Manager.Create/Delete/SetName/etc.) take a
// parallel path: they call the registry methods directly and the
// Manager emits a row update via the same RowPublisher. The publish
// shape is identical on both paths — the SPA never knows which
// producer fired.
//
// Errors on step 2 are logged but non-fatal: the durable ledger row
// already committed and Phase 4 will drop that fallback anyway.
package sessioncontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
)

// RowWriterMetrics exposes the dual-write observability surface.
// Wired to prometheus in cmd/tank-operator/observability.go. Steady-
// state expectation: row-update failures are zero — if non-zero, the
// session row will drift from the lifecycle ledger and Phase 2's
// snapshot will return stale data.
type RowWriterMetrics interface {
	RecordRowUpdate(eventType string)
	RecordRowUpdateFailure(eventType string)
}

type noopRowWriterMetrics struct{}

func (noopRowWriterMetrics) RecordRowUpdate(_ string)        {}
func (noopRowWriterMetrics) RecordRowUpdateFailure(_ string) {}

// TransitionOutcome is what callers learn about the result of
// RecordTransition. It exposes only the bits callers care about
// (dedupe, publish-failure) without leaking the lifecycle store /
// publisher implementation details.
type TransitionOutcome string

const (
	// TransitionEmitted: ledger appended, row updated (if applicable),
	// NATS publish succeeded. Steady-state happy path.
	TransitionEmitted TransitionOutcome = "emitted"
	// TransitionDeduped: ledger Append detected an existing row with
	// the same (scope, session_id, event_id). No row update, no NATS
	// publish. Caused by informer resync re-observing the same pod
	// state, or by two orchestrator replicas racing on the same
	// transition.
	TransitionDeduped TransitionOutcome = "deduped"
	// TransitionPublishFailed: ledger row + row update committed but
	// NATS publish errored. SPA clients reconnect-resume from
	// Postgres so the failure is recoverable; the caller should not
	// retry (the durable row is already there).
	TransitionPublishFailed TransitionOutcome = "publish-failed"
)

// RowEmitter is the narrow interface RowWriter calls to fan a row
// update out to SSE clients. Satisfied by *RowPublisher in production;
// tests pass a capture stub. Phase 3 deliberately decouples the
// writer from the concrete publisher so the wire shape (NATS subject,
// row marshaling) can be tested at one layer and the writer's
// dual-write contract at another.
type RowEmitter interface {
	PublishCurrentRow(ctx context.Context, owner, sessionID string)
}

// RowWriter is constructed once and called by the K8s watch and
// chat-activity emitter. Safe for concurrent use — every call site is
// its own goroutine. Postgres serializes the row UPDATEs by primary
// key; the lifecycle store's Append is idempotent via its unique
// constraint; row-update publishes are fire-and-forget per call.
type RowWriter struct {
	Store   lifecycleevents.Store
	Emitter RowEmitter
	Pool    *pgxpool.Pool
	Metrics RowWriterMetrics
}

// NewRowWriter validates the required dependencies and applies metric
// defaults. Returns an error rather than panicking so the orchestrator
// startup path can surface misconfiguration clearly.
func NewRowWriter(store lifecycleevents.Store, emitter RowEmitter, pool *pgxpool.Pool, metrics RowWriterMetrics) (*RowWriter, error) {
	if store == nil {
		return nil, fmt.Errorf("sessioncontroller: RowWriter requires a lifecycleevents.Store")
	}
	if emitter == nil {
		return nil, fmt.Errorf("sessioncontroller: RowWriter requires a RowEmitter")
	}
	// pool may be nil in stub-mode local dev: row-column updates
	// silently skip in that case (the ledger + NATS path remains
	// fully functional, which matches the StubStore degradation
	// shape).
	if metrics == nil {
		metrics = noopRowWriterMetrics{}
	}
	return &RowWriter{
		Store:   store,
		Emitter: emitter,
		Pool:    pool,
		Metrics: metrics,
	}, nil
}

// RecordTransition is the single entry point for every lifecycle
// producer. See package docs for the dual-write contract.
//
// The alreadyExists branch (informer resync re-observing the same pod
// state, replica race, or — critically — first deploy after the dual-
// write went live encountering pre-existing ledger rows) still runs
// the row-column update. The column write is idempotent at the
// value level (writing status='Active' twice yields the same row), and
// crucially, this is how pre-Phase-1 sessions get their columns
// populated: the K8s watch's first-sight emit produces a dedupe on the
// ledger but a fresh column write. Without it, sessions that settled
// into a terminal state before Phase 1 would render as 'Pending' in
// the Phase 2 snapshot and Phase 2's "row matches ledger" cutover gate
// would fail. Only the NATS publish is skipped on alreadyExists — that
// would re-render an old transition on connected clients.
func (w *RowWriter) RecordTransition(ctx context.Context, event lifecycleevents.Event) (TransitionOutcome, error) {
	assigned, alreadyExists, err := w.Store.Append(ctx, event)
	if err != nil {
		return "", fmt.Errorf("sessioncontroller: append lifecycle event: %w", err)
	}

	// Phase 1 dual-write: derive column changes and apply them to the
	// sessions row regardless of whether the ledger Append deduped.
	// See the function-level comment for why the alreadyExists branch
	// still runs the column update. Failures here are logged + counted
	// but not propagated — the durable ledger row is already committed
	// and the SPA's existing read path is unaffected.
	if changes, ok := deriveRowColumnChanges(assigned); ok && w.Pool != nil {
		if err := w.applyRowColumnChanges(ctx, assigned, changes); err != nil {
			w.Metrics.RecordRowUpdateFailure(assigned.Type)
			slog.Warn("sessioncontroller: row column update failed",
				"session_id", assigned.SessionID,
				"scope", assigned.SessionScope,
				"type", assigned.Type,
				"error", err,
			)
		} else {
			w.Metrics.RecordRowUpdate(assigned.Type)
		}
	}

	if alreadyExists {
		// Resync or replica race — the previous emit already published
		// the row. Skipping the wire publish here is what prevents the
		// SPA from re-rendering an already-seen transition on connected
		// clients.
		return TransitionDeduped, nil
	}

	// Publish the row's current state to the SPA. The post-update row
	// is fetched fresh so the wire payload reflects the latest
	// committed state across BOTH the column update above AND any
	// concurrent registry mutation (e.g., Manager.SetName racing with a
	// K8s pod_ready transition). The fetch is one indexed PK lookup.
	w.Emitter.PublishCurrentRow(ctx, assigned.Email, assigned.SessionID)
	return TransitionEmitted, nil
}

// rowColumnChanges captures the subset of sessions-row columns this
// event type should mutate. A nil/false return from
// deriveRowColumnChanges means "this event has no row-column effect"
// (e.g. session.created / .deleted / .name_changed — those are owned
// by sessionregistry.Store's write methods which mutate their own
// columns independently and won't be wrapped under RowWriter until
// Phase 2's snapshot cutover and the registry mutations get folded
// in).
type rowColumnChanges struct {
	status          string // empty means leave unchanged
	readyAt         *time.Time
	terminatingAt   *time.Time
	activitySummary []byte // marshaled JSON; nil means leave unchanged
}

func deriveRowColumnChanges(event lifecycleevents.Event) (rowColumnChanges, bool) {
	switch event.Type {
	case lifecycleevents.EventTypePodScheduled:
		return rowColumnChanges{status: "Pending"}, true
	case lifecycleevents.EventTypePodReady:
		readyAt := parsePayloadTime(event.Payload, "ready_at")
		return rowColumnChanges{status: "Active", readyAt: readyAt}, true
	case lifecycleevents.EventTypePodNotReady:
		return rowColumnChanges{status: "Pending"}, true
	case lifecycleevents.EventTypePodFailed:
		return rowColumnChanges{status: "Failed"}, true
	case lifecycleevents.EventTypePodTerminating:
		terminatingAt := parseRFC3339(event.OccurredAt)
		return rowColumnChanges{status: "Failed", terminatingAt: terminatingAt}, true
	case lifecycleevents.EventTypeActivityChanged:
		body, err := json.Marshal(event.Payload)
		if err != nil {
			return rowColumnChanges{}, false
		}
		return rowColumnChanges{activitySummary: body}, true
	}
	return rowColumnChanges{}, false
}

func (w *RowWriter) applyRowColumnChanges(ctx context.Context, event lifecycleevents.Event, c rowColumnChanges) error {
	setParts := []string{}
	args := []any{event.Email, event.SessionScope, event.SessionID}
	argIdx := 4
	if c.status != "" {
		setParts = append(setParts, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, c.status)
		argIdx++
	}
	if c.readyAt != nil {
		setParts = append(setParts, fmt.Sprintf("ready_at = $%d", argIdx))
		args = append(args, c.readyAt.UTC())
		argIdx++
	}
	if c.terminatingAt != nil {
		setParts = append(setParts, fmt.Sprintf("terminating_at = $%d", argIdx))
		args = append(args, c.terminatingAt.UTC())
		argIdx++
	}
	if c.activitySummary != nil {
		setParts = append(setParts, fmt.Sprintf("activity_summary = $%d", argIdx))
		args = append(args, c.activitySummary)
		argIdx++
	}
	if len(setParts) == 0 {
		return nil
	}
	// row_version + updated_at always bump alongside any column change
	// — the per-row monotonic version is what Phase 3's catch-up cursor
	// will read against, and updated_at is the human-debuggable
	// "what changed when" marker.
	setParts = append(setParts,
		"row_version = nextval('sessions_row_version_seq')",
		"updated_at = now()",
	)
	q := "UPDATE sessions SET " + strings.Join(setParts, ", ") +
		" WHERE email = $1 AND session_scope = $2 AND session_id = $3"
	_, err := w.Pool.Exec(ctx, q, args...)
	return err
}

func parsePayloadTime(payload map[string]any, key string) *time.Time {
	v, ok := payload[key].(string)
	if !ok {
		return nil
	}
	return parseRFC3339(v)
}

func parseRFC3339(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return &t
		}
	}
	return nil
}
