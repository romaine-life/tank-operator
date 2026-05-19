package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// Earlier orchestrators created session pods named "session-<hash>" rather
// than "session-<id>". The Manager must resolve them via the session-id label,
// not by guessing the name, or terminal/file interactions 404.
func TestManagerResolvesHashSuffixedPodNameForSessionInteractions(t *testing.T) {
	pod := sessionPod("8", "nelson@romaine.life", corev1.PodRunning, true)
	pod.Name = "session-189268a4e4"
	client := fake.NewSimpleClientset(pod)
	mgr := &Manager{client: client, namespace: sessionmodel.SessionsNamespace}

	t.Run("GetPodName returns the actual pod name, not the computed one", func(t *testing.T) {
		got, err := mgr.GetPodName(context.Background(), "nelson@romaine.life", "8")
		if err != nil {
			t.Fatal(err)
		}
		if got != "session-189268a4e4" {
			t.Fatalf("pod name = %q, want %q (must read the real name from the label-selector lookup, not assume session-<id>)", got, "session-189268a4e4")
		}
	})

	t.Run("GetTerminalEndpoint returns endpoint for the resolved pod", func(t *testing.T) {
		updated := pod.DeepCopy()
		updated.Status.PodIP = "10.0.0.42"
		if _, err := client.CoreV1().Pods(sessionmodel.SessionsNamespace).UpdateStatus(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		ip, port, err := mgr.GetTerminalEndpoint(context.Background(), "nelson@romaine.life", "8")
		if err != nil {
			t.Fatal(err)
		}
		if ip != "10.0.0.42" || port != sessionmodel.SandboxAgentPort {
			t.Fatalf("endpoint = %s:%d, want 10.0.0.42:%d", ip, port, sessionmodel.SandboxAgentPort)
		}
	})
}

func TestManagerFindPodRejectsWrongOwner(t *testing.T) {
	pod := sessionPod("8", "someone-else@example.com", corev1.PodRunning, true)
	pod.Name = "session-189268a4e4"
	client := fake.NewSimpleClientset(pod)
	mgr := &Manager{client: client, namespace: sessionmodel.SessionsNamespace}

	_, err := mgr.findPodBySessionID(context.Background(), "nelson@romaine.life", "8")
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("err = %v, want ErrNotOwned (label-selector path should reject when owner label doesn't match)", err)
	}
}

func TestManagerFindPodReturnsNotFoundWhenAbsent(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := &Manager{client: client, namespace: sessionmodel.SessionsNamespace}

	_, err := mgr.findPodBySessionID(context.Background(), "nelson@romaine.life", "999")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestManagerCreateDefaultsManifestNamespaceToManagerNamespace(t *testing.T) {
	const slotNamespace = "tank-operator-slot-1-sessions"

	client := fake.NewSimpleClientset()
	mgr := NewManager(client, nil, slotNamespace, nil, nil, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionImage:      "claude-image",
			CodexSessionImage: "codex-image",
			PiSessionImage:    "pi-image",
		},
	})

	info, err := mgr.Create(context.Background(), CreateOptions{
		Owner: "nelson@romaine.life",
		Mode:  sessionmodel.ClaudeCLIMode,
	})
	if err != nil {
		t.Fatal(err)
	}

	pod, err := client.CoreV1().Pods(slotNamespace).Get(context.Background(), *info.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pod.Namespace; got != slotNamespace {
		t.Fatalf("pod namespace = %q, want %q", got, slotNamespace)
	}
	trackingID := pod.Annotations["argocd.argoproj.io/tracking-id"]
	if !strings.Contains(trackingID, ":/Pod:"+slotNamespace+"/") {
		t.Fatalf("tracking id = %q, want namespace segment %q", trackingID, slotNamespace)
	}
}
