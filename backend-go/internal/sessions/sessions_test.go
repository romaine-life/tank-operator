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

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

func TestListReturnsOwnedSandboxAgentPods(t *testing.T) {
	client := fake.NewSimpleClientset(
		sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true),
		sessionPod("13", "nelson@romaine.life", corev1.PodRunning, false),
		sessionPod("14", "other@example.com", corev1.PodRunning, true),
	)
	reader := NewReader(client, compat.SessionsNamespace)

	got, err := reader.List(context.Background(), "nelson@romaine.life")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("session count = %d, want 1: %#v", len(got), got)
	}
	session := got[0]
	if session.ID != "12" {
		t.Fatalf("session id = %q, want 12", session.ID)
	}
	if session.Status != "Active" {
		t.Fatalf("session status = %q, want Active", session.Status)
	}
	if session.Mode != compat.CodexGUIMode {
		t.Fatalf("session mode = %q, want %q", session.Mode, compat.CodexGUIMode)
	}
	if session.PodName == nil || *session.PodName != "session-12" {
		t.Fatalf("pod name = %#v, want session-12", session.PodName)
	}
	if session.Name == nil || *session.Name != "Workbench" {
		t.Fatalf("name = %#v, want Workbench", session.Name)
	}
	if session.TestState["active"] != true {
		t.Fatalf("test state = %#v, want active true", session.TestState)
	}
	if session.RolloutState["active"] != true {
		t.Fatalf("rollout state = %#v, want active true", session.RolloutState)
	}
	if session.CreatedAt == nil || *session.CreatedAt != "2026-05-11T00:00:01+00:00" {
		t.Fatalf("created_at = %#v", session.CreatedAt)
	}
	if session.ReadyAt == nil || *session.ReadyAt != "2026-05-11T00:00:03+00:00" {
		t.Fatalf("ready_at = %#v", session.ReadyAt)
	}
}

func TestGetFallsBackToSessionIDLabel(t *testing.T) {
	pod := sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true)
	pod.Name = "session-hash-abc"
	client := fake.NewSimpleClientset(pod)
	reader := NewReader(client, compat.SessionsNamespace)

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
	reader := NewReader(client, compat.SessionsNamespace)

	_, err := reader.Get(context.Background(), "nelson@romaine.life", "12")
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("error = %v, want ErrNotOwned", err)
	}
}

func TestListMergesRegistryRecordsWithPods(t *testing.T) {
	recordedName := "Saved name"
	client := fake.NewSimpleClientset(
		sessionPod("12", "nelson@romaine.life", corev1.PodRunning, true),
		sessionPod("16", "nelson@romaine.life", corev1.PodRunning, true),
	)
	registry := registryRecords{
		{
			ID:          "12",
			Email:       "nelson@romaine.life",
			Mode:        compat.CodexGUIMode,
			PodName:     "session-12",
			Name:        &recordedName,
			RequestedAt: "2026-05-11T00:00:00+00:00",
			CreatedAt:   "2026-05-11T00:00:01+00:00",
			Visible:     true,
		},
		{
			ID:          "15",
			Email:       "nelson@romaine.life",
			Mode:        compat.ClaudeCLIMode,
			PodName:     "session-15",
			RequestedAt: "2026-05-10T00:00:00+00:00",
			CreatedAt:   "2026-05-10T00:00:01+00:00",
			Visible:     true,
		},
	}
	reader := NewReaderWithRegistry(client, compat.SessionsNamespace, registry)

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
	if len(got) != 3 {
		t.Fatalf("session count = %d, want 3: %#v", len(got), got)
	}
	if got[0].ID != "12" || got[0].Status != "Active" || got[0].Name == nil || *got[0].Name != recordedName {
		t.Fatalf("merged session = %#v", got[0])
	}
	if got[0].RequestedAt == nil || *got[0].RequestedAt != "2026-05-11T00:00:00+00:00" {
		t.Fatalf("merged requested_at = %#v", got[0].RequestedAt)
	}
	if got[1].ID != "15" || got[1].Status != "Failed" || got[1].Mode != compat.ClaudeCLIMode {
		t.Fatalf("registry-only session = %#v", got[1])
	}
	if got[2].ID != "16" || got[2].Status != "Active" {
		t.Fatalf("pod-only session = %#v", got[2])
	}
}

func TestPodStatusCompatibility(t *testing.T) {
	pending := sessionPod("12", "nelson@romaine.life", corev1.PodPending, true)
	if got := podStatus(pending); got != "Pending" {
		t.Fatalf("pending status = %q", got)
	}

	crash := sessionPod("13", "nelson@romaine.life", corev1.PodRunning, true)
	crash.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "claude",
		Ready: false,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}
	if got := podStatus(crash); got != "Failed" {
		t.Fatalf("crash status = %q", got)
	}
}

type registryRecords []compat.SessionRecord

func (r registryRecords) List(context.Context, string) ([]compat.SessionRecord, error) {
	return []compat.SessionRecord(r), nil
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
			Namespace:         compat.SessionsNamespace,
			CreationTimestamp: created,
			Labels: map[string]string{
				"tank-operator/owner":      compat.OwnerLabel(owner),
				"tank-operator/session-id": id,
				"tank-operator/mode":       compat.CodexGUIMode,
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
