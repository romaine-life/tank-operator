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

func TestManagerGetByOwnerReadsNoPodSessionsFromRegistry(t *testing.T) {
	const readyAt = "2026-05-20T01:00:00Z"
	registry := &managerTestRegistry{
		records: []sessionmodel.SessionRecord{
			{
				ID:        "108",
				Email:     "nelson@romaine.life",
				Mode:      sessionmodel.HermesGUIMode,
				Visible:   true,
				Status:    "Active",
				ReadyAt:   readyAt,
				CreatedAt: "2026-05-20T00:59:59Z",
			},
			{
				ID:      "109",
				Email:   "nelson@romaine.life",
				Mode:    sessionmodel.CodexGUIMode,
				Visible: true,
				Status:  "Active",
			},
		},
	}
	mgr := &Manager{
		client:    fake.NewSimpleClientset(),
		namespace: sessionmodel.SessionsNamespace,
		registry:  registry,
	}

	got, err := mgr.GetByOwner(context.Background(), "nelson@romaine.life", "108")
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != sessionmodel.HermesGUIMode || got.Status != "Active" || got.PodName != nil {
		t.Fatalf("no-pod session = %#v, want active hermes_gui without pod", got)
	}
	if got.ReadyAt == nil || *got.ReadyAt != readyAt {
		t.Fatalf("ready_at = %#v, want %q", got.ReadyAt, readyAt)
	}

	_, err = mgr.GetByOwner(context.Background(), "nelson@romaine.life", "109")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("pod-backed registry-only GetByOwner err = %v, want ErrNotFound", err)
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

type managerTestRegistry struct {
	records []sessionmodel.SessionRecord
	nextID  string
}

func (r *managerTestRegistry) List(_ context.Context, owner string) ([]sessionmodel.SessionRecord, error) {
	out := make([]sessionmodel.SessionRecord, 0, len(r.records))
	for _, record := range r.records {
		if strings.EqualFold(record.Email, owner) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (r *managerTestRegistry) Get(_ context.Context, owner, sessionID string) (sessionmodel.SessionRecord, bool, error) {
	for _, record := range r.records {
		if strings.EqualFold(record.Email, owner) && record.ID == sessionID {
			return record, true, nil
		}
	}
	return sessionmodel.SessionRecord{}, false, nil
}

func (r *managerTestRegistry) NextSessionID(context.Context) (string, error) {
	if r.nextID == "" {
		return "1", nil
	}
	return r.nextID, nil
}

func (r *managerTestRegistry) Upsert(_ context.Context, record sessionmodel.SessionRecord) error {
	for i, existing := range r.records {
		if strings.EqualFold(existing.Email, record.Email) && existing.ID == record.ID {
			r.records[i] = record
			return nil
		}
	}
	r.records = append(r.records, record)
	return nil
}

func (r *managerTestRegistry) SetName(context.Context, string, string, *string) error { return nil }

func (r *managerTestRegistry) SetTestState(context.Context, string, string, map[string]any) error {
	return nil
}

func (r *managerTestRegistry) SetRolloutState(context.Context, string, string, map[string]any) error {
	return nil
}

func (r *managerTestRegistry) MarkDeleted(context.Context, string, string) error { return nil }
