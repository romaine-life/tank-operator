package sessioncontroller

import (
	"context"
	"testing"
)

// TestDeriveRowColumnChangesPerEventType pins the per-event-type
// column mapping the K8s watch and chat-activity emitter drive
// through RowWriter. Post-Phase-4 this is the single durable shape
// the SPA renders against; if a new event type lands, this table
// must grow with it.
func TestDeriveRowColumnChangesPerEventType(t *testing.T) {
	cases := []struct {
		name        string
		event       Event
		wantOK      bool
		wantStatus  string
		wantReady   bool
		wantTerm    bool
		wantSummary bool
	}{
		{
			name:       "pod_scheduled → status Pending",
			event:      Event{Type: EventTypePodScheduled},
			wantOK:     true,
			wantStatus: "Pending",
		},
		{
			name: "pod_ready → status Active + ready_at",
			event: Event{
				Type:    EventTypePodReady,
				Payload: map[string]any{"ready_at": "2026-05-18T04:30:00Z"},
			},
			wantOK:     true,
			wantStatus: "Active",
			wantReady:  true,
		},
		{
			name:       "pod_not_ready → status Pending",
			event:      Event{Type: EventTypePodNotReady},
			wantOK:     true,
			wantStatus: "Pending",
		},
		{
			name:       "pod_failed → status Failed",
			event:      Event{Type: EventTypePodFailed},
			wantOK:     true,
			wantStatus: "Failed",
		},
		{
			name: "pod_terminating → status Failed + terminating_at",
			event: Event{
				Type:       EventTypePodTerminating,
				OccurredAt: "2026-05-18T04:30:00Z",
			},
			wantOK:     true,
			wantStatus: "Failed",
			wantTerm:   true,
		},
		{
			name: "activity_changed → activity_summary",
			event: Event{
				Type:    EventTypeActivityChanged,
				Payload: map[string]any{"status": "ready", "unread_count": 0},
			},
			wantOK:      true,
			wantSummary: true,
		},
		{
			// session.created has no row-column effect — Manager.Create
			// owns the row identity columns via registry.Upsert.
			name:   "session.created → no row update",
			event:  Event{Type: EventTypeCreated},
			wantOK: false,
		},
		{
			// session.deleted is owned by registry.MarkDeleted (sets
			// visible=false, bumps row_version). RowWriter has no role
			// here — Manager publishes the row directly through
			// RowPublisher.
			name:   "session.deleted → no row update",
			event:  Event{Type: EventTypeDeleted},
			wantOK: false,
		},
		{
			name:   "session.name_changed → no row update",
			event:  Event{Type: EventTypeNameChanged},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := deriveRowColumnChanges(tc.event)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (event=%q)", ok, tc.wantOK, tc.event.Type)
			}
			if !ok {
				return
			}
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.status, tc.wantStatus)
			}
			if (got.readyAt != nil) != tc.wantReady {
				t.Fatalf("readyAt set = %v, want %v", got.readyAt != nil, tc.wantReady)
			}
			if (got.terminatingAt != nil) != tc.wantTerm {
				t.Fatalf("terminatingAt set = %v, want %v", got.terminatingAt != nil, tc.wantTerm)
			}
			if (got.activitySummary != nil) != tc.wantSummary {
				t.Fatalf("activitySummary set = %v, want %v", got.activitySummary != nil, tc.wantSummary)
			}
		})
	}
}

// TestRowWriterRecordTransitionNoOpSkipsPublish verifies that an
// event with no row-column effect (e.g. session.created — owned by
// the registry) returns TransitionNoOp AND skips the publish. Without
// this guard, every user-action event the chat-activity emitter
// observed before the registry write landed would fan out a stale
// row state on NATS.
func TestRowWriterRecordTransitionNoOpSkipsPublish(t *testing.T) {
	emitter := &fakeEmitter{}
	writer, err := NewRowWriter(emitter, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	event := Event{
		Email:        "u@example.com",
		SessionScope: "default",
		SessionID:    "42",
		Type:         EventTypeCreated,
	}
	outcome, err := writer.RecordTransition(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != TransitionNoOp {
		t.Fatalf("outcome = %q, want no-op", outcome)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("publish count = %d, want 0 (no-op events must not publish)", len(emitter.calls))
	}
}

// TestRowWriterPublishesByID confirms the writer hands the session
// id to the row publisher. The publisher reads the row's current
// state from the registry — passing the wrong id would publish the
// wrong row, which is the failure mode this test is the gate against.
func TestRowWriterPublishesByID(t *testing.T) {
	emitter := &fakeEmitter{}
	writer, _ := NewRowWriter(emitter, nil, nil)

	event := Event{
		Email:        "u@example.com",
		SessionScope: "default",
		SessionID:    "42",
		Type:         EventTypePodScheduled,
	}
	_, err := writer.RecordTransition(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if len(emitter.calls) != 1 {
		t.Fatalf("publish count = %d, want 1", len(emitter.calls))
	}
	if emitter.calls[0].owner != "u@example.com" || emitter.calls[0].sessionID != "42" {
		t.Fatalf("PublishCurrentRow args = %+v, want (u@example.com, 42)", emitter.calls[0])
	}
}
