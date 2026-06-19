// Package sessioncontroller — RowWriter is the single write path that
// the K8s watch and chat-activity emitter call into. Per
// docs/session-list-redesign.md Phase 4 it owns:
//
//  1. The sessions row UPDATE for the columns derived from the
//     transition (status / ready_at / terminating_at / activity_summary)
//     plus the row_version bump.
//  2. A row-update fan-out on the NATS row-update subject via
//     RowPublisher, carrying the post-write row state.
//
// The pre-Phase-4 lifecycle ledger Append is gone; the durable row is
// now the only durable state for sidebar transitions. In-memory
// dedup against re-observed states lives in the K8s watch's
// transitionTracker (lastObservedColumns map).
package sessioncontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RowWriterMetrics exposes the row-update observability surface.
// Wired to prometheus in cmd/tank-operator/observability.go.
// Steady-state expectation: row-update failures are zero.
type RowWriterMetrics interface {
	RecordRowUpdate(eventType string)
	RecordRowUpdateFailure(eventType string)
	// RecordRowActivityWriteSuperseded fires when an activity_summary
	// write is dropped by the stale-write guard: the stored summary's
	// last_order_key is newer than the one this write derived from, so
	// last-write-wins would have regressed the durable status (the
	// "stuck working forever" class — a stale concurrent refresh
	// overwriting a terminal). Steady-state expectation: rare; sustained
	// rates mean refreshers are racing far behind the ledger.
	RecordRowActivityWriteSuperseded()
}

type noopRowWriterMetrics struct{}

func (noopRowWriterMetrics) RecordRowUpdate(_ string)          {}
func (noopRowWriterMetrics) RecordRowUpdateFailure(_ string)   {}
func (noopRowWriterMetrics) RecordRowActivityWriteSuperseded() {}

// TransitionOutcome is what callers learn about the result of
// RecordTransition. After Phase 4 there is no durable ledger to dedup
// against — the K8s watch's in-memory tracker decides when to call
// RecordTransition. The outcome distinguishes "we wrote and published"
// from "the column update changed nothing because the observed state
// matched."
type TransitionOutcome string

const (
	// TransitionEmitted: row updated AND publish succeeded.
	TransitionEmitted TransitionOutcome = "emitted"
	// TransitionNoOp: the row update affected zero columns
	// (deriveRowColumnChanges returned no-effect for this event type).
	// No NATS publish.
	TransitionNoOp TransitionOutcome = "no-op"
	// TransitionPublishFailed: row update committed but NATS publish
	// errored. SPA clients reconnect-resume from the sessions table
	// so the failure is recoverable; the caller should not retry.
	TransitionPublishFailed TransitionOutcome = "publish-failed"
)

// RowEmitter is the narrow interface RowWriter calls to fan a row
// update out to SSE clients. Satisfied by *RowPublisher in
// production; tests pass a capture stub.
type RowEmitter interface {
	PublishCurrentRow(ctx context.Context, owner, sessionID string)
}

// RowWriter is constructed once and called by the K8s watch and
// chat-activity emitter. Safe for concurrent use — every call site
// is its own goroutine. Postgres serializes row UPDATEs by primary
// key; row-update publishes are fire-and-forget per call.
type RowWriter struct {
	Emitter RowEmitter
	Pool    *pgxpool.Pool
	Metrics RowWriterMetrics
}

// NewRowWriter validates the required dependencies and applies
// metric defaults. Returns an error rather than panicking so the
// orchestrator startup path can surface misconfiguration clearly.
// pool may be nil in stub-mode local dev: row-column updates
// silently skip in that case.
func NewRowWriter(emitter RowEmitter, pool *pgxpool.Pool, metrics RowWriterMetrics) (*RowWriter, error) {
	if emitter == nil {
		return nil, fmt.Errorf("sessioncontroller: RowWriter requires a RowEmitter")
	}
	if metrics == nil {
		metrics = noopRowWriterMetrics{}
	}
	return &RowWriter{
		Emitter: emitter,
		Pool:    pool,
		Metrics: metrics,
	}, nil
}

// RecordTransition writes one transition's column changes to the
// sessions row and fans the post-write row state out on NATS. If the
// transition has no row-column effect (event-type-specific —
// e.g. session.created is owned by registry.Upsert), the call is a
// no-op.
func (w *RowWriter) RecordTransition(ctx context.Context, event Event) (TransitionOutcome, error) {
	changes, ok := deriveRowColumnChanges(event)
	if !ok {
		return TransitionNoOp, nil
	}
	if w.Pool != nil {
		if err := w.applyRowColumnChanges(ctx, event, changes); err != nil {
			w.Metrics.RecordRowUpdateFailure(event.Type)
			slog.Warn("sessioncontroller: row column update failed",
				"session_id", event.SessionID,
				"scope", event.SessionScope,
				"type", event.Type,
				"error", err,
			)
			return "", err
		}
		w.Metrics.RecordRowUpdate(event.Type)
	}
	// Publish the row's current state. The post-update row is fetched
	// fresh so the wire payload reflects the latest committed state
	// across both the column update above AND any concurrent registry
	// mutation. One indexed PK lookup.
	w.Emitter.PublishCurrentRow(ctx, event.Email, event.SessionID)
	return TransitionEmitted, nil
}

// rowColumnChanges captures the subset of sessions-row columns this
// transition should mutate. A nil/false return from
// deriveRowColumnChanges means "this transition has no row-column
// effect" (session.created / .deleted / .name_changed — those are
// owned by sessionregistry.Store's write methods).
type rowColumnChanges struct {
	status          string // empty means leave unchanged
	readyAt         *time.Time
	terminatingAt   *time.Time
	activitySummary []byte // marshaled JSON; nil means leave unchanged
	// activityLastOrderKey is the ledger high-water mark the summary in
	// activitySummary was derived from (empty when the fold saw no
	// events). It feeds the stale-write guard in applyRowColumnChanges.
	activityLastOrderKey string
	compactionCount      *int64 // nil means leave unchanged
	userMessageCount     *int64 // nil means leave unchanged
	// guardNotTerminal makes the row UPDATE additionally require
	// terminating_at IS NULL, so a non-terminal pod-state transition
	// (scheduled/ready/not-ready) cannot resurrect a session that
	// provider-fatal or pod-terminating already marked terminal. Without it a
	// doomed container that briefly passes its readiness probe between crashes
	// flips the row back to Active (the session 979 status flapping).
	guardNotTerminal bool
}

func deriveRowColumnChanges(event Event) (rowColumnChanges, bool) {
	switch event.Type {
	case EventTypePodScheduled:
		return rowColumnChanges{status: "Pending", guardNotTerminal: true}, true
	case EventTypePodReady:
		readyAt := parsePayloadTime(event.Payload, "ready_at")
		return rowColumnChanges{status: "Active", readyAt: readyAt, guardNotTerminal: true}, true
	case EventTypePodNotReady:
		return rowColumnChanges{status: "Pending", guardNotTerminal: true}, true
	case EventTypePodFailed:
		// A pod_failed (CrashLoopBackOff / nonzero exit) is NOT marked
		// terminal: a transient crash that recovers may legitimately return to
		// Active. The orchestrator restart-budget backstop converts a
		// persistent runner crash-loop into a provider_fatal (below), which IS
		// terminal.
		return rowColumnChanges{status: "Failed"}, true
	case EventTypeProviderFatal:
		// Terminal: provider-fatal is the runner (or the crash-loop backstop)
		// asserting the session cannot continue. Stamp terminating_at so it is
		// sticky — a later ready observation cannot flip it back to Active —
		// matching pod-terminating; the pod is then reaped.
		at := parseRFC3339(event.OccurredAt)
		if at == nil {
			now := time.Now().UTC()
			at = &now
		}
		return rowColumnChanges{status: "Failed", terminatingAt: at}, true
	case EventTypePodTerminating:
		terminatingAt := parseRFC3339(event.OccurredAt)
		return rowColumnChanges{status: "Failed", terminatingAt: terminatingAt}, true
	case EventTypeActivityChanged:
		body, err := json.Marshal(event.Payload)
		if err != nil {
			return rowColumnChanges{}, false
		}
		lastOrderKey, _ := event.Payload["last_order_key"].(string)
		return rowColumnChanges{activitySummary: body, activityLastOrderKey: lastOrderKey}, true
	case EventTypeCompactionChanged:
		count, ok := compactionCountFromPayload(event.Payload)
		if !ok {
			return rowColumnChanges{}, false
		}
		return rowColumnChanges{compactionCount: &count}, true
	case EventTypeUserMessageCountChanged:
		count, ok := userMessageCountFromPayload(event.Payload)
		if !ok {
			return rowColumnChanges{}, false
		}
		return rowColumnChanges{userMessageCount: &count}, true
	}
	return rowColumnChanges{}, false
}

// compactionCountFromPayload extracts the recomputed compaction total the
// ChatActivityEmitter stamps on an EventTypeCompactionChanged transition. The
// emitter builds the payload in-process with an int64, but tolerate the JSON
// number shapes so a future direct caller can't silently zero the column.
func compactionCountFromPayload(payload map[string]any) (int64, bool) {
	if payload == nil {
		return 0, false
	}
	switch v := payload["compaction_count"].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}

// userMessageCountFromPayload extracts the recomputed user-message total the
// ChatActivityEmitter stamps on an EventTypeUserMessageCountChanged transition.
// Mirrors compactionCountFromPayload: the emitter builds the payload in-process
// with an int64, but tolerate the JSON number shapes so a future direct caller
// can't silently zero the column.
func userMessageCountFromPayload(payload map[string]any) (int64, bool) {
	if payload == nil {
		return 0, false
	}
	switch v := payload["user_message_count"].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}

func (w *RowWriter) applyRowColumnChanges(ctx context.Context, event Event, c rowColumnChanges) error {
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
	if c.compactionCount != nil {
		setParts = append(setParts, fmt.Sprintf("compaction_count = $%d", argIdx))
		args = append(args, *c.compactionCount)
		argIdx++
	}
	if c.userMessageCount != nil {
		setParts = append(setParts, fmt.Sprintf("user_message_count = $%d", argIdx))
		args = append(args, *c.userMessageCount)
		argIdx++
	}
	if len(setParts) == 0 {
		return nil
	}
	setParts = append(setParts,
		"row_version = nextval('sessions_row_version_seq')",
		"updated_at = now()",
	)
	where := " WHERE email = $1 AND session_scope = $2 AND session_id = $3"
	if c.guardNotTerminal {
		// Don't resurrect a terminally-Failed session: a non-terminal pod-state
		// transition (scheduled/ready/not-ready) applies only while
		// terminating_at is unset. Kills the crash-loop status flapping where a
		// doomed-but-briefly-ready container flipped the row back to Active.
		where += " AND terminating_at IS NULL"
	}
	if c.activitySummary != nil {
		// Stale-write guard for the activity summary. Refreshes are
		// concurrent by design (per-event persister workers on two
		// replicas, the read-state HTTP path, wake/cancel paths) and each
		// is a read-fold-write with no transaction spanning the read and
		// the write — unguarded, a refresh that folded an older ledger
		// tail can land last and durably overwrite a terminal status
		// ("stuck working forever", a wake-fire gate that defers
		// forever). Only a summary derived from an equal-or-newer ledger
		// position may replace the stored one. Equal must pass: read-state
		// refreshes legitimately recompute unread counts against the same
		// tail. A keyless new summary (fold saw no events) may never
		// replace a keyed one.
		args = append(args, c.activityLastOrderKey)
		where += fmt.Sprintf(
			" AND (activity_summary IS NULL OR COALESCE(activity_summary ->> 'last_order_key', '') <= $%d)",
			len(args),
		)
	}
	q := "UPDATE sessions SET " + strings.Join(setParts, ", ") + where
	tag, err := w.Pool.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if c.activitySummary != nil && tag.RowsAffected() == 0 {
		// Either the row is gone (already a silent no-op before the
		// guard) or a newer summary superseded this write. The durable
		// state is correct either way; surface it for operators only.
		w.Metrics.RecordRowActivityWriteSuperseded()
		slog.Debug("sessioncontroller: stale activity summary write dropped",
			"session_id", event.SessionID,
			"scope", event.SessionScope,
			"last_order_key", c.activityLastOrderKey,
		)
	}
	return nil
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
