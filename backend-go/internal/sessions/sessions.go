package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
)

const (
	defaultNamespace       = compat.SessionsNamespace
	nameAnnotation         = "tank-operator/display-name"
	testStateAnnotation    = "tank-operator/test-state"
	rolloutStateAnnotation = "tank-operator/rollout-state"
)

var (
	ErrNotFound = errors.New("session not found")
	ErrNotOwned = errors.New("session not owned")
)

type Info struct {
	ID           string         `json:"id"`
	PodName      *string        `json:"pod_name"`
	Owner        string         `json:"owner"`
	Status       string         `json:"status"`
	Mode         string         `json:"mode"`
	RequestedAt  *string        `json:"requested_at"`
	CreatedAt    *string        `json:"created_at"`
	ReadyAt      *string        `json:"ready_at"`
	Name         *string        `json:"name"`
	TestState    map[string]any `json:"test_state"`
	RolloutState map[string]any `json:"rollout_state"`
}

type Reader struct {
	client    kubernetes.Interface
	namespace string
}

func NewReader(client kubernetes.Interface, namespace string) *Reader {
	if namespace == "" {
		namespace = defaultNamespace
	}
	return &Reader{client: client, namespace: namespace}
}

func (r *Reader) List(ctx context.Context, owner string) ([]Info, error) {
	ownerLabel := compat.OwnerLabel(owner)
	pods, err := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "tank-operator/owner=" + ownerLabel,
	})
	if err != nil {
		return nil, err
	}

	out := make([]Info, 0, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podHasSandboxAgent(pod) {
			continue
		}
		out = append(out, infoFromPod(owner, pod))
	}
	return out, nil
}

func (r *Reader) Get(ctx context.Context, owner, sessionID string) (Info, error) {
	pod, err := r.client.CoreV1().Pods(r.namespace).Get(ctx, "session-"+sessionID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		pods, listErr := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "tank-operator/session-id=" + sessionID,
		})
		if listErr != nil {
			return Info{}, listErr
		}
		if len(pods.Items) == 0 {
			return Info{}, ErrNotFound
		}
		pod = &pods.Items[0]
		err = nil
	}
	if err != nil {
		return Info{}, err
	}
	if pod.Labels["tank-operator/owner"] != compat.OwnerLabel(owner) {
		return Info{}, ErrNotOwned
	}
	return infoFromPod(owner, pod), nil
}

func infoFromPod(owner string, pod *corev1.Pod) Info {
	podName := pod.Name
	createdAt := timeString(pod.CreationTimestamp.Time)
	readyAt := readyAt(pod)
	name := annotationString(pod.Annotations, nameAnnotation)
	return Info{
		ID:           sessionIDFromPod(pod),
		PodName:      &podName,
		Owner:        owner,
		Status:       podStatus(pod),
		Mode:         compat.NormalizeSessionMode(pod.Labels["tank-operator/mode"]),
		RequestedAt:  createdAt,
		CreatedAt:    createdAt,
		ReadyAt:      readyAt,
		Name:         name,
		TestState:    annotationObject(pod.Annotations, testStateAnnotation),
		RolloutState: annotationObject(pod.Annotations, rolloutStateAnnotation),
	}
}

func sessionIDFromPod(pod *corev1.Pod) string {
	if pod.Labels != nil && pod.Labels["tank-operator/session-id"] != "" {
		return pod.Labels["tank-operator/session-id"]
	}
	return strings.TrimPrefix(pod.Name, "session-")
}

func podHasSandboxAgent(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name != "claude" {
			continue
		}
		for _, port := range container.Ports {
			if port.Name == "sandbox-agent" {
				return true
			}
		}
		return false
	}
	return false
}

func podStatus(pod *corev1.Pod) string {
	if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
		return "Active"
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return "Failed"
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff" {
			return "Failed"
		}
	}
	return "Pending"
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning || len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if !status.Ready {
			return false
		}
	}
	return true
}

func readyAt(pod *corev1.Pod) *string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return timeString(condition.LastTransitionTime.Time)
		}
	}
	return nil
}

func timeString(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Format("2006-01-02T15:04:05+00:00")
	return &value
}

func annotationString(annotations map[string]string, key string) *string {
	if annotations == nil || annotations[key] == "" {
		return nil
	}
	value := annotations[key]
	return &value
}

func annotationObject(annotations map[string]string, key string) map[string]any {
	if annotations == nil || annotations[key] == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(annotations[key]), &out); err != nil {
		return nil
	}
	return out
}
