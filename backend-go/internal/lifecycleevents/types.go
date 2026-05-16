// Package lifecycleevents owns the per-owner durable ledger that drives the
// sidebar session list. It replaces the prior opaque wake subject + SSE
// resync trigger and the activity-polling endpoint with the same shape
// session_events uses for chat: typed events with a per-owner monotonic
// order_key, cursor-resumable SSE, explicit resync on unknown cursor.
// See docs/product-inspirations.md for the architectural constraints
// this is the load-bearing implementation of, and tank-operator#83 for
// the migration that introduced it.
//
// One row per durable transition. Producers:
//
//   - sessions.Manager       → session.created / .deleted / .name_changed /
//                              .test_state_changed / .rollout_state_changed
//   - podinformer            → session.pod_scheduled / .pod_ready /
//                              .pod_not_ready / .pod_failed / .pod_terminating
//   - sessionbus persister   → session.activity_changed (chat-derived
//                              activity-summary deltas)
package lifecycleevents

// Event types. The strings are wire identifiers — the SSE typed payload
// carries them as the `type` field and the frontend reducer switches on
// them, so any rename here is a coordinated change with App.tsx.
const (
	// User-action lifecycle (sessions.Manager writes these inline alongside
	// the corresponding Kubernetes pod mutation).
	EventTypeCreated             = "session.created"
	EventTypeDeleted             = "session.deleted"
	EventTypeNameChanged         = "session.name_changed"
	EventTypeTestStateChanged    = "session.test_state_changed"
	EventTypeRolloutStateChanged = "session.rollout_state_changed"

	// Pod-state lifecycle (podinformer leader writes these on phase /
	// Ready-condition / deletionTimestamp transitions). The frontend
	// derives the durable "status" field from the latest of these.
	EventTypePodScheduled   = "session.pod_scheduled"
	EventTypePodReady       = "session.pod_ready"
	EventTypePodNotReady    = "session.pod_not_ready"
	EventTypePodFailed      = "session.pod_failed"
	EventTypePodTerminating = "session.pod_terminating"

	// Chat-derived activity summary delta. Persister writes one each time
	// a chat lifecycle event would change any of the sidebar's per-session
	// indicator fields (status / active_turn_id / needs_input / failed /
	// unread_count / last_order_key). Name is intentionally distinct from
	// the deleted phantom `session.activity_updated`
	// (scripts/check-removed-chat-runtime.mjs).
	EventTypeActivityChanged = "session.activity_changed"
)

// PodEventTypes lists the pod-state event types in order of severity for
// the SQL partial index and for the status derivation in handleListSessions.
var PodEventTypes = []string{
	EventTypePodScheduled,
	EventTypePodReady,
	EventTypePodNotReady,
	EventTypePodFailed,
	EventTypePodTerminating,
}

// Event is the durable row shape, also the wire shape the SSE handler
// emits unchanged (the frontend reducer consumes this struct verbatim).
type Event struct {
	OrderKey     string         `json:"order_key"`
	Email        string         `json:"email"`
	SessionScope string         `json:"session_scope"`
	SessionID    string         `json:"session_id"`
	Type         string         `json:"type"`
	EventID      string         `json:"event_id"`
	Payload      map[string]any `json:"payload"`
	OccurredAt   string         `json:"occurred_at"`
}

// Cursor identifies a position in a per-owner ledger. Empty AfterOrderKey
// means "from the beginning"; the SSE handler treats that as the
// snapshot-bootstrap case.
type Cursor struct {
	AfterOrderKey string
}

// Page is the paginated read response from ListByOwner. NextOrderKey is the
// largest order_key in the page; HasMore signals there's at least one row
// past the limit so the caller can continue paginating.
type Page struct {
	Events       []Event
	NextOrderKey string
	HasMore      bool
}

// ActivitySummary is the per-session fold the sidebar renders. The persister
// computes one each time a chat lifecycle event lands and writes it into
// the payload of a session.activity_changed Event row.
type ActivitySummary struct {
	Status       string  `json:"status"`
	ActiveTurnID *string `json:"active_turn_id"`
	NeedsInput   bool    `json:"needs_input"`
	Failed       bool    `json:"failed"`
	LastOrderKey *string `json:"last_order_key"`
	UnreadCount  int     `json:"unread_count"`
	UpdatedAt    *string `json:"updated_at"`
}

// PodStatusSummary is the per-session pod-state snapshot derived from the
// latest session.pod_* event. The sidebar's durable "status" field comes
// from this — replaces the live podStatus() compute that read the pod
// object on every List() call. Status values mirror the prior strings
// ("Pending" / "Active" / "Failed") so the frontend rendering stays the
// same.
type PodStatusSummary struct {
	Status     string  `json:"status"`
	ReadyAt    *string `json:"ready_at"`
	OccurredAt string  `json:"occurred_at"`
}
