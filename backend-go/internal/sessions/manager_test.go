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

func TestManagerActiveSkillStateClearsOppositeAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		apply       func(context.Context, *Manager) (Info, error)
		wantTest    bool
		wantRollout bool
	}{
		{
			name: "test active clears rollout",
			apply: func(ctx context.Context, mgr *Manager) (Info, error) {
				return mgr.SetTestState(ctx, "nelson@romaine.life", "8", true, nil, nil)
			},
			wantTest:    true,
			wantRollout: false,
		},
		{
			name: "rollout active clears test",
			apply: func(ctx context.Context, mgr *Manager) (Info, error) {
				return mgr.SetRolloutState(ctx, "nelson@romaine.life", "8", true)
			},
			wantTest:    false,
			wantRollout: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pod := sessionPod("8", "nelson@romaine.life", corev1.PodRunning, true)
			client := fake.NewSimpleClientset(pod)
			mgr := &Manager{client: client, namespace: sessionmodel.SessionsNamespace}

			info, err := tc.apply(context.Background(), mgr)
			if err != nil {
				t.Fatal(err)
			}
			assertSkillStateActive(t, "info test_state", info.TestState, tc.wantTest)
			assertSkillStateActive(t, "info rollout_state", info.RolloutState, tc.wantRollout)

			updated, err := client.CoreV1().Pods(sessionmodel.SessionsNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			assertSkillStateActive(t, "pod test annotation", annotationObject(updated.Annotations, testStateAnnotation), tc.wantTest)
			assertSkillStateActive(t, "pod rollout annotation", annotationObject(updated.Annotations, rolloutStateAnnotation), tc.wantRollout)
		})
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

func TestManagerReorderPersistsAndPublishesEveryRow(t *testing.T) {
	registry := &managerTestRegistry{
		records: []sessionmodel.SessionRecord{
			{ID: "1", Email: "nelson@romaine.life", Visible: true, SidebarPosition: 1},
			{ID: "2", Email: "nelson@romaine.life", Visible: true, SidebarPosition: 2},
			{ID: "3", Email: "nelson@romaine.life", Visible: true, SidebarPosition: 3},
		},
	}
	emitter := &recordingRowEmitter{}
	mgr := &Manager{
		client:    fake.NewSimpleClientset(),
		namespace: sessionmodel.SessionsNamespace,
		registry:  registry,
		emitter:   emitter,
	}

	if err := mgr.ReorderSessions(context.Background(), "nelson@romaine.life", []string{"2", "3", "1"}); err != nil {
		t.Fatal(err)
	}
	wantPositions := map[string]int64{"2": 3, "3": 2, "1": 1}
	for _, record := range registry.records {
		if got := record.SidebarPosition; got != wantPositions[record.ID] {
			t.Fatalf("session %s sidebar position = %d, want %d", record.ID, got, wantPositions[record.ID])
		}
	}
	if strings.Join(emitter.ids, ",") != "2,3,1" {
		t.Fatalf("published ids = %v, want [2 3 1]", emitter.ids)
	}
}

func assertSkillStateActive(t *testing.T, label string, state map[string]any, want bool) {
	t.Helper()
	if got := state["active"]; got != want {
		t.Fatalf("%s active = %#v, want %v (state=%#v)", label, got, want, state)
	}
}

type recordingRowEmitter struct {
	ids []string
}

func (r *recordingRowEmitter) PublishCurrentRow(_ context.Context, _ string, sessionID string) {
	r.ids = append(r.ids, sessionID)
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

func (r *managerTestRegistry) Reorder(_ context.Context, _ string, orderedIDs []string) ([]string, error) {
	positions := map[string]int64{}
	for i, id := range orderedIDs {
		positions[id] = int64(len(orderedIDs) - i)
	}
	for i, record := range r.records {
		if pos, ok := positions[record.ID]; ok {
			r.records[i].SidebarPosition = pos
		}
	}
	return orderedIDs, nil
}

func (r *managerTestRegistry) MarkDeleted(context.Context, string, string) error { return nil }
