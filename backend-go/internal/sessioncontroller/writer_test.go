package sessioncontroller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
)

// TestDeriveRowColumnChangesPerEventType is the Phase 1 dual-write
// invariant: every lifecycle event type that the controller emits
// produces the exact row-column mapping the Phase 2 snapshot cutover
// will read against. The mapping must match what the existing
// pre-Phase-2 ledger-hydration / LatestPodStatus / LatestActivity path
// computes today, so Phase 2 can switch the read source over without
// changing what the SPA renders.
func TestDeriveRowColumnChangesPerEventType(t *testing.T) {
	cases := []struct {
		name        string
		event       lifecycleevents.Event
		wantOK      bool
		wantStatus  string
		wantReady   bool
		wantTerm    bool
		wantSummary bool
	}{
		{
			name:       "pod_scheduled → status Pending",
			event:      lifecycleevents.Event{Type: lifecycleevents.EventTypePodScheduled},
			wantOK:     true,
			wantStatus: "Pending",
		},
		{
			name: "pod_ready → status Active + ready_at",
			event: lifecycleevents.Event{
				Type:    lifecycleevents.EventTypePodReady,
				Payload: map[string]any{"ready_at": "2026-05-18T04:30:00Z"},
			},
			wantOK:     true,
			wantStatus: "Active",
			wantReady:  true,
		},
		{
			name:       "pod_not_ready → status Pending",
			event:      lifecycleevents.Event{Type: lifecycleevents.EventTypePodNotReady},
			wantOK:     true,
			wantStatus: "Pending",
		},
		{
			name:       "pod_failed → status Failed",
			event:      lifecycleevents.Event{Type: lifecycleevents.EventTypePodFailed},
			wantOK:     true,
			wantStatus: "Failed",
		},
		{
			name: "pod_terminating → status Failed + terminating_at",
			event: lifecycleevents.Event{
				Type:       lifecycleevents.EventTypePodTerminating,
				OccurredAt: "2026-05-18T04:30:00Z",
			},
			wantOK:     true,
			wantStatus: "Failed",
			wantTerm:   true,
		},
		{
			name: "activity_changed → activity_summary",
			event: lifecycleevents.Event{
				Type:    lifecycleevents.EventTypeActivityChanged,
				Payload: map[string]any{"status": "ready", "unread_count": 0},
			},
			wantOK:      true,
			wantSummary: true,
		},
		{
			// session.created has no row-column effect — the
			// registry.Upsert call earlier in Manager.Create writes
			// the row's identity columns. The controller's dual-write
			// path correctly returns false here so we don't double-
			// write.
			name:   "session.created → no row update",
			event:  lifecycleevents.Event{Type: lifecycleevents.EventTypeCreated},
			wantOK: false,
		},
		{
			// session.deleted has no row-column effect either —
			// registry.MarkDeleted writes visible=false in its own
			// call. Phase 4 of the redesign will fold delete into the
			// row directly; for Phase 1 the registry mutation owns
			// the row.
			name:   "session.deleted → no row update",
			event:  lifecycleevents.Event{Type: lifecycleevents.EventTypeDeleted},
			wantOK: false,
		},
		{
			name:   "session.name_changed → no row update",
			event:  lifecycleevents.Event{Type: lifecycleevents.EventTypeNameChanged},
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

// TestRowWriterRecordTransitionDedupes verifies the (scope, session_id,
// event_id) idempotency contract: a repeated emit of the same event
// (informer resync, replica race) returns TransitionDeduped and skips
// both the row update AND the NATS publish. This is the same
// invariant the pre-consolidation K8s watch test pinned, now
// asserted through the RowWriter surface.
func TestRowWriterRecordTransitionDedupes(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	writer, err := NewRowWriter(store, pub, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	event := lifecycleevents.Event{
		Email:        "u@example.com",
		SessionScope: "default",
		SessionID:    "42",
		Type:         lifecycleevents.EventTypePodReady,
		EventID:      "pod_ready:uid:0",
		OccurredAt:   "2026-05-18T04:30:00Z",
		Payload:      map[string]any{"status": "Active", "ready_at": "2026-05-18T04:30:00Z"},
	}

	outcome, err := writer.RecordTransition(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != TransitionEmitted {
		t.Fatalf("first outcome = %q, want emitted", outcome)
	}
	if len(pub.payloads) != 1 {
		t.Fatalf("first publish count = %d, want 1", len(pub.payloads))
	}

	outcome2, err := writer.RecordTransition(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if outcome2 != TransitionDeduped {
		t.Fatalf("repeat outcome = %q, want deduped", outcome2)
	}
	if len(pub.payloads) != 1 {
		t.Fatalf("repeat publish count = %d, want 1 (deduped must skip publish)", len(pub.payloads))
	}
}

// TestRowWriterPublishesAssignedPayload confirms the wire payload is
// the lifecycle-store-stamped Event (with OrderKey assigned), not the
// caller's input. The SPA's reducer keys on order_key for cursor
// advance; without this contract a publish-before-append regression
// would silently lose ordering.
func TestRowWriterPublishesAssignedPayload(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	writer, _ := NewRowWriter(store, pub, nil, nil)

	event := lifecycleevents.Event{
		Email:        "u@example.com",
		SessionScope: "default",
		SessionID:    "42",
		Type:         lifecycleevents.EventTypePodScheduled,
		EventID:      "pod_scheduled:uid:0",
	}
	_, err := writer.RecordTransition(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.payloads) != 1 {
		t.Fatalf("publish count = %d, want 1", len(pub.payloads))
	}
	var probe struct {
		OrderKey string `json:"order_key"`
	}
	if err := json.Unmarshal(pub.payloads[0].raw, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.OrderKey == "" {
		t.Fatalf("published payload missing order_key — the wire shape must carry the lifecycle store's assigned value, not the caller's empty one")
	}
}
