package sessions

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/lifecycleevents"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// fakeLifecycleSource is the test stand-in for the production
// lifecycleevents.Store that drives the Reader's Status + Activity
// hydration. Maps session_id → canned values; missing entries return
// nil/nil so the Reader leaves the default Status alone (the legacy
// behavior).
type fakeLifecycleSource struct {
	pod      map[string]*lifecycleevents.PodStatusSummary
	activity map[string]*lifecycleevents.ActivitySummary
}

func (f fakeLifecycleSource) LatestPodStatus(_ context.Context, _, sessionID string) (*lifecycleevents.PodStatusSummary, error) {
	return f.pod[sessionID], nil
}

func (f fakeLifecycleSource) LatestActivity(_ context.Context, _, sessionID string) (*lifecycleevents.ActivitySummary, error) {
	return f.activity[sessionID], nil
}

// readyAtPtr / activeSummary build the fixtures the merge test expects.
func readyAtPtr(t string) *string { v := t; return &v }

// TestListRequiresRegistry pins the post-#83 invariant that the registry
// is the durable enumeration source. A Reader without a registry must
// fail loud on List — the prior pod-only enumeration fallback was the
// path that re-added still-terminating pods after Manager.Delete had
// already tombstoned them, leaving the SPA sidebar with "stuck deleting"
// rows. See docs/migration-policy.md ("no fallback paths") and
// scripts/check-removed-chat-runtime.mjs.
func TestListRequiresRegistry(t *testing.T) {
	client := fake.NewSimpleClientset(
		sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true),
	)
	reader := NewReader(client, sessionmodel.SessionsNamespace)

	_, err := reader.List(context.Background(), "nelson@romaine.life")
	if !errors.Is(err, ErrRegistryRequired) {
		t.Fatalf("List error = %v, want ErrRegistryRequired", err)
	}
}

func TestGetFallsBackToSessionIDLabel(t *testing.T) {
	pod := sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true)
	pod.Name = "session-hash-abc"
	client := fake.NewSimpleClientset(pod)
	reader := NewReader(client, sessionmodel.SessionsNamespace)

	got, err := reader.Get(context.Background(), "nelson@romaine.life", "12")
	if err != nil {
		t.Fatal(err)
	}
	if got.PodName == nil || *got.PodName != "session-hash-abc" {
		t.Fatalf("pod name = %#v, want fallback pod", got.PodName)
	}
}

func TestGetRejectsWrongOwner(t *testing.T) {
	client := fake.NewSimpleClientset(sessionPod("12", "other@example.com", corev1.PodRunning, true))
	reader := NewReader(client, sessionmodel.SessionsNamespace)

	_, err := reader.Get(context.Background(), "nelson@romaine.life", "12")
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("error = %v, want ErrNotOwned", err)
	}
}

// TestListEnumeratesVisibleRegistryRowsHydratedWithPods exercises the
// registry-only enumeration: visible records produce list rows
// (hydrated with the matching pod's annotations + the lifecycle ledger's
// Status), records without a live pod still appear (per the
// infoFromRecord fallback), and pods owned by tombstoned (visible=false)
// records are dropped silently.
func TestListEnumeratesVisibleRegistryRowsHydratedWithPods(t *testing.T) {
	recordedName := "Saved name"
	client := fake.NewSimpleClientset(
		sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true),
		// session-99: pod still in K8s with deletionTimestamp set (the
		// "terminating after Manager.Delete" case that produced the
		// stuck-deleting bug). The registry has it tombstoned, so the
		// Reader must drop it silently — not surface it, not count it as
		// an orphan.
		terminatingSessionPod("99", "nelson@romaine.life"),
	)
	registry := registryRecords{
		{
			ID:          "12",
			Email:       "nelson@romaine.life",
			Mode:        sessionmodel.CodexGUIMode,
			PodName:     "session-12",
			Name:        &recordedName,
			RequestedAt: "2026-05-11T00:00:00+00:00",
			CreatedAt:   "2026-05-11T00:00:01+00:00",
			Visible:     true,
		},
		{
			ID:          "15",
			Email:       "nelson@romaine.life",
			Mode:        sessionmodel.ClaudeCLIMode,
			PodName:     "session-15",
			RequestedAt: "2026-05-10T00:00:00+00:00",
			CreatedAt:   "2026-05-10T00:00:01+00:00",
			Visible:     true,
		},
		{
			// Tombstoned (visible=false). The matching session-99 pod is
			// still in K8s above. The Reader must NOT re-surface this
			// session — pre-fix, the pod-loop fallback would have added
			// it back and the SPA snapshot would lie about the delete.
			ID:          "99",
			Email:       "nelson@romaine.life",
			Mode:        sessionmodel.ClaudeGUIMode,
			PodName:     "session-99",
			RequestedAt: "2026-05-09T00:00:00+00:00",
			CreatedAt:   "2026-05-09T00:00:01+00:00",
			Visible:     false,
		},
	}
	lifecycle := fakeLifecycleSource{
		pod: map[string]*lifecycleevents.PodStatusSummary{
			"12": {Status: "Active", ReadyAt: readyAtPtr("2026-05-11T00:00:03+00:00")},
			// 15 has no pod and no lifecycle row — the infoFromRecord
			// fallback path stamps "Failed", which is what the test
			// expects.
		},
	}
	orphanMetrics := &recordingMetrics{}
	reader := NewReaderFull(client, sessionmodel.SessionsNamespace, registry, lifecycle, "default").
		WithMetrics(orphanMetrics)

	got, err := reader.List(context.Background(), "nelson@romaine.life")
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got, func(a, b Info) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if len(got) != 2 {
		t.Fatalf("session count = %d, want 2: %#v", len(got), got)
	}
	if got[0].ID != "12" || got[0].Status != "Active" || got[0].Name == nil || *got[0].Name != recordedName {
		t.Fatalf("hydrated session = %#v", got[0])
	}
	if got[0].RequestedAt == nil || *got[0].RequestedAt != "2026-05-11T00:00:00+00:00" {
		t.Fatalf("hydrated requested_at = %#v", got[0].RequestedAt)
	}
	if got[1].ID != "15" || got[1].Status != "Failed" || got[1].Mode != sessionmodel.ClaudeCLIMode {
		t.Fatalf("registry-only session = %#v", got[1])
	}
	if orphanMetrics.orphans != 0 {
		t.Fatalf("orphan pod count = %d, want 0 (the tombstoned pod is known to the registry, not an orphan)", orphanMetrics.orphans)
	}
}

// TestListCountsOrphanPodsButDoesNotSurfaceThem verifies the orphan-pod
// observability counter ticks when a pod is observed for a session_id
// the registry has never seen — without surfacing the pod to the
// user-facing list. Steady-state expectation is zero orphans; this test
// simulates a "Manager.Create wrote the pod but the registry insert
// failed" condition and asserts the read path swallows the row and
// records it as an anomaly.
func TestListCountsOrphanPodsButDoesNotSurfaceThem(t *testing.T) {
	client := fake.NewSimpleClientset(
		sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true),
		// Pod 42 has no registry row at all (visible OR tombstoned). The
		// Reader must drop it from the list and tick the orphan counter.
		sessionPod("42", "nelson@romaine.life", corev1.PodRunning, true),
	)
	registry := registryRecords{
		{
			ID:          "12",
			Email:       "nelson@romaine.life",
			Mode:        sessionmodel.CodexGUIMode,
			PodName:     "session-12",
			RequestedAt: "2026-05-11T00:00:00+00:00",
			CreatedAt:   "2026-05-11T00:00:01+00:00",
			Visible:     true,
		},
	}
	orphanMetrics := &recordingMetrics{}
	reader := NewReaderFull(client, sessionmodel.SessionsNamespace, registry, fakeLifecycleSource{}, "default").
		WithMetrics(orphanMetrics)

	got, err := reader.List(context.Background(), "nelson@romaine.life")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "12" {
		t.Fatalf("list = %#v, want only session 12 (42 is an orphan)", got)
	}
	if orphanMetrics.orphans != 1 {
		t.Fatalf("orphan pod count = %d, want 1 (pod 42 has no registry row)", orphanMetrics.orphans)
	}
}

// TestPodStatusCompatibility was deleted in tank-operator#83 along with
// the podStatus() helper it pinned. Status is now derived from the
// session_lifecycle_events ledger via Reader.hydrateLifecycle and tested
// end-to-end through TestListReturnsOwnedSandboxAgentPods (which wires a
// fakeLifecycleSource). Re-introducing this test would resurrect the
// retired path the migration-guard forbids; the equivalent pod-state
// derivation is now tested in internal/podinformer.

type registryRecords []sessionmodel.SessionRecord

func (r registryRecords) List(context.Context, string) ([]sessionmodel.SessionRecord, error) {
	return []sessionmodel.SessionRecord(r), nil
}

// recordingMetrics is the test stand-in for the production
// sessions.Metrics adapter. Counts orphan-pod observations so tests can
// assert "exactly N orphans for this owner".
type recordingMetrics struct {
	orphans int
}

func (m *recordingMetrics) RecordOrphanPod() { m.orphans++ }

// terminatingSessionPod returns the same shape as sessionPod but with a
// non-nil DeletionTimestamp — the K8s API state of a pod between
// pod.Delete (which sets the timestamp) and the kubelet's actual reap
// after terminationGracePeriodSeconds. Reader.List must drop these when
// the registry has tombstoned the matching session_id.
func terminatingSessionPod(id, owner string) *corev1.Pod {
	pod := sessionPod(id, owner, corev1.PodRunning, true)
	now := metav1.NewTime(time.Date(2026, 5, 12, 0, 0, 1, 0, time.UTC))
	pod.DeletionTimestamp = &now
	return pod
}

func sessionPod(id, owner string, phase corev1.PodPhase, sandboxAgent bool) *corev1.Pod {
	created := metav1.NewTime(time.Date(2026, 5, 11, 0, 0, 1, 0, time.UTC))
	ready := metav1.NewTime(time.Date(2026, 5, 11, 0, 0, 3, 0, time.UTC))
	ports := []corev1.ContainerPort{}
	if sandboxAgent {
		ports = append(ports, corev1.ContainerPort{Name: "sandbox-agent", ContainerPort: 2468})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "session-" + id,
			Namespace:         sessionmodel.SessionsNamespace,
			CreationTimestamp: created,
			Labels: map[string]string{
				"tank-operator/owner":      sessionmodel.OwnerLabel(owner),
				"tank-operator/session-id": id,
				"tank-operator/mode":       sessionmodel.CodexGUIMode,
			},
			Annotations: map[string]string{
				nameAnnotation:         "Workbench",
				testStateAnnotation:    `{"active":true}`,
				rolloutStateAnnotation: `{"active":true}`,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "mcp-auth-proxy"},
				{Name: "claude", Ports: ports},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: ready,
			}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "mcp-auth-proxy", Ready: true},
				{Name: "claude", Ready: true},
			},
		},
	}
}
