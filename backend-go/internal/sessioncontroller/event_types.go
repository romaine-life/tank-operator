// Internal event types the K8s watch and the chat-activity emitter
// use to talk to RowWriter. Post-Phase-4 (docs/session-list-redesign
// .md) these are NOT a wire shape — there is no typed-event NATS
// subject and no durable per-owner ledger. The Event struct is a
// pure in-process data carrier so the K8s watch can describe a pod
// transition ("status=Active, ready_at=...") to RowWriter through
// one signature without forcing every caller to know about the row-
// column SQL. EventType strings are internal discriminators —
// renames are free.
package sessioncontroller

// Event is the in-process descriptor for one transition. The Type
// field discriminates inside deriveRowColumnChanges and nothing
// else; the field is not serialized to any wire or table.
type Event struct {
	Email        string
	SessionScope string
	SessionID    string
	Type         string
	OccurredAt   string
	Payload      map[string]any
}

// EventType discriminators. Internal to sessioncontroller; the wire
// shape is the row, not the event.
const (
	// User-action transitions. sessions.Manager owns these end-to-end
	// (registry.Upsert / .MarkDeleted / .SetName + a direct
	// RowPublisher.PublishCurrentRow call) — no Event is ever built
	// for them in steady state. The constants stay declared so
	// writer_test.go can pin the contract that deriveRowColumnChanges
	// returns no-effect for them; deleting them would let a future
	// regression silently introduce a row-column write under one of
	// these names without the test catching it.
	EventTypeCreated     = "session.created"
	EventTypeDeleted     = "session.deleted"
	EventTypeNameChanged = "session.name_changed"

	// Pod-state transitions written by the K8s watch.
	EventTypePodScheduled   = "session.pod_scheduled"
	EventTypePodReady       = "session.pod_ready"
	EventTypePodNotReady    = "session.pod_not_ready"
	EventTypePodFailed      = "session.pod_failed"
	EventTypePodTerminating = "session.pod_terminating"

	// Chat-derived activity-summary delta written by the
	// ChatActivityEmitter on each indicator-affecting chat event.
	EventTypeActivityChanged = "session.activity_changed"
)
