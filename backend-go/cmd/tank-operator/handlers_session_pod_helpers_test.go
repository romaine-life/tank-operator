package main

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// sessionPodFixture builds a healthy session pod for handler tests. It
// used to live in the deleted handlers_activity_test.go as
// activitySessionPod (the helper name is preserved for back-compat with
// existing call sites in handlers_read_state_test.go); a future cleanup
// can rename it once those sites are touched again.
func activitySessionPod(id, owner string) *corev1.Pod {
	created := metav1.NewTime(time.Date(2026, 5, 12, 0, 0, 1, 0, time.UTC))
	ready := metav1.NewTime(time.Date(2026, 5, 12, 0, 0, 3, 0, time.UTC))
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
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "mcp-auth-proxy"},
				{Name: "claude", Ports: []corev1.ContainerPort{{Name: "sandbox-agent", ContainerPort: 2468}}},
				{Name: "codex-runner"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: ready,
			}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "mcp-auth-proxy", Ready: true},
				{Name: "claude", Ready: true},
				{Name: "codex-runner", Ready: true},
			},
		},
	}
}
