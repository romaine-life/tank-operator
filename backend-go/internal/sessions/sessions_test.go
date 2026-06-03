package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// TestListReadsEverythingFromTheRegistryRow is the Phase 2 cutover
// contract: Reader.List returns the sidebar snapshot purely from the
// sessions row. K8s is not consulted (a fake client with zero pods
// still produces the full Info); no lifecycle-store hydration is
// involved; status / ready_at / test_state / rollout_state /
// activity_summary all come straight off the row columns Phase 1
// populated.
func TestListReadsEverythingFromTheRegistryRow(t *testing.T) {
	activity, _ := json.Marshal(map[string]any{
		"status":       "running",
		"unread_count": 3,
	})
	registry := registryRecords{
		{
			ID:              "12",
			Email:           "nelson@romaine.life",
			Mode:            sessionmodel.CodexGUIMode,
			PodName:         "session-12",
			Name:            stringPtr("Workbench"),
			Visible:         true,
			RequestedAt:     "2026-05-11T00:00:00+00:00",
			CreatedAt:       "2026-05-11T00:00:01+00:00",
			Status:          "Active",
			ReadyAt:         "2026-05-11T00:00:03+00:00",
			ActivitySummary: activity,
			TestState:       map[string]any{"active": true},
			RolloutState:    map[string]any{"active": true},
			Capabilities:    []string{sessionmodel.SessionCapabilitySpireLensMCP},
		},
	}
	// Empty K8s client — proves the snapshot doesn't touch K8s.
	client := fake.NewSimpleClientset()
	reader := NewReaderFull(client, sessionmodel.SessionsNamespace, registry, "default")

	got, err := reader.List(context.Background(), "nelson@romaine.life")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("session count = %d, want 1: %#v", len(got), got)
	}
	session := got[0]
	if session.ID != "12" || session.Status != "Active" || session.Mode != sessionmodel.CodexGUIMode {
		t.Fatalf("session = %#v", session)
	}
	if session.Name == nil || *session.Name != "Workbench" {
		t.Fatalf("name = %#v, want Workbench", session.Name)
	}
	if session.PodName == nil || *session.PodName != "session-12" {
		t.Fatalf("pod name = %#v, want session-12", session.PodName)
	}
	if session.ReadyAt == nil || *session.ReadyAt != "2026-05-11T00:00:03+00:00" {
		t.Fatalf("ready_at = %#v", session.ReadyAt)
	}
	if session.TestState["active"] != true {
		t.Fatalf("test state = %#v, want active true", session.TestState)
	}
	if session.RolloutState["active"] != true {
		t.Fatalf("rollout state = %#v, want active true", session.RolloutState)
	}
	if session.Activity == nil || session.Activity.UnreadCount != 3 {
		t.Fatalf("activity = %#v, want unread_count=3", session.Activity)
	}
	if !slices.Equal(session.Capabilities, []string{sessionmodel.SessionCapabilitySpireLensMCP}) {
		t.Fatalf("capabilities = %#v, want spirelens_mcp", session.Capabilities)
	}
	// Verify no K8s API calls were made.
	if actions := client.Actions(); len(actions) > 0 {
		t.Fatalf("Reader.List made %d K8s calls; the Phase 2 snapshot must not touch K8s: %#v", len(actions), actions)
	}
}

// TestListSkipsInvisibleRows confirms visible=false rows are excluded
// from the snapshot. Together with TestListReadsEverythingFromTheRegistryRow,
// this nails down the row-only read shape that retired the pod-
// fallback loop responsible for tank-operator#525's 75s resurrection
// window.
func TestListSkipsInvisibleRows(t *testing.T) {
	registry := registryRecords{
		{
			ID:      "12",
			Email:   "nelson@romaine.life",
			Mode:    sessionmodel.CodexGUIMode,
			Visible: true,
			Status:  "Active",
		},
		{
			ID:      "13",
			Email:   "nelson@romaine.life",
			Mode:    sessionmodel.CodexGUIMode,
			Visible: false, // Manager.Delete just ran; pod may still be Terminating
			Status:  "Failed",
		},
	}
	reader := NewReaderFull(fake.NewSimpleClientset(), sessionmodel.SessionsNamespace, registry, "default")
	got, err := reader.List(context.Background(), "nelson@romaine.life")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("count = %d, want 1 (only visible row): %#v", len(got), got)
	}
	if got[0].ID != "12" {
		t.Fatalf("id = %q, want 12", got[0].ID)
	}
}

// TestListSortsByRegistryOrder pins that the snapshot preserves the
// row order returned by registry.List. The registry owns durable
// sidebar_position ordering; row_version changes for status/test/
// rollout updates must not become the render order.
func TestListSortsByRegistryOrder(t *testing.T) {
	registry := registryRecords{
		{ID: "31", Email: "u@example.com", Visible: true, Status: "Active", SidebarPosition: 3, RowVersion: 1},
		{ID: "21", Email: "u@example.com", Visible: true, Status: "Active", SidebarPosition: 2, RowVersion: 99},
		{ID: "11", Email: "u@example.com", Visible: true, Status: "Active", SidebarPosition: 1, RowVersion: 2},
	}
	reader := NewReaderFull(fake.NewSimpleClientset(), sessionmodel.SessionsNamespace, registry, "default")
	got, err := reader.List(context.Background(), "u@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, info := range got {
		ids = append(ids, info.ID)
	}
	if !slices.Equal(ids, []string{"31", "21", "11"}) {
		t.Fatalf("ids = %v, want [31 21 11] in registry order", ids)
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

func stringPtr(s string) *string { return &s }

type registryRecords []sessionmodel.SessionRecord

func (r registryRecords) List(context.Context, string) ([]sessionmodel.SessionRecord, error) {
	return []sessionmodel.SessionRecord(r), nil
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
