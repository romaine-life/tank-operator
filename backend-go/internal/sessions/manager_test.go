package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
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
				return mgr.SetTestState(ctx, "nelson@romaine.life", "8", true, nil, nil, nil)
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

func TestManagerSetTestPullRequestURLPreservesTestEnvironment(t *testing.T) {
	pod := sessionPod("8", "nelson@romaine.life", corev1.PodRunning, true)
	pod.Annotations[testStateAnnotation] = `{"active":true,"slot_index":2,"url":"https://slot-2"}`
	client := fake.NewSimpleClientset(pod)
	registry := &managerTestRegistry{records: []sessionmodel.SessionRecord{{
		ID:        "8",
		Email:     "nelson@romaine.life",
		Scope:     "default",
		Mode:      sessionmodel.CodexGUIMode,
		Status:    "Active",
		Visible:   true,
		TestState: map[string]any{"active": true, "slot_index": float64(2), "url": "https://slot-2"},
	}}}
	mgr := &Manager{client: client, namespace: sessionmodel.SessionsNamespace, registry: registry}

	info, err := mgr.SetTestPullRequestURL(context.Background(), "nelson@romaine.life", "8", stringPtr("https://github.com/romaine-life/tank-operator/pull/123"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.TestState["url"]; got != "https://slot-2" {
		t.Fatalf("info test_state url = %#v, want slot URL preserved", got)
	}
	if got := info.TestState["pull_request_url"]; got != "https://github.com/romaine-life/tank-operator/pull/123" {
		t.Fatalf("info test_state pull_request_url = %#v", got)
	}
	updated, err := client.CoreV1().Pods(sessionmodel.SessionsNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	annotation := annotationObject(updated.Annotations, testStateAnnotation)
	if got := annotation["url"]; got != "https://slot-2" {
		t.Fatalf("pod test annotation url = %#v, want slot URL preserved", got)
	}
	if got := annotation["pull_request_url"]; got != "https://github.com/romaine-life/tank-operator/pull/123" {
		t.Fatalf("pod test annotation pull_request_url = %#v", got)
	}
}

func TestManagerCreateDefaultsManifestNamespaceToManagerNamespace(t *testing.T) {
	const slotNamespace = "tank-operator-slot-1-sessions"

	client := fake.NewSimpleClientset()
	mgr := NewManager(client, nil, slotNamespace, nil, nil, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionImage:      "claude-image",
			CodexSessionImage: "codex-image",
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

func TestManagerCreateThreadsSelectedReposIntoPodManifest(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := NewManager(client, nil, sessionmodel.SessionsNamespace, nil, nil, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionImage:            "claude-image",
			CodexSessionImage:       "codex-image",
			TankOperatorInternalURL: "http://tank-operator.test",
		},
	})

	info, err := mgr.Create(context.Background(), CreateOptions{
		Owner: "nelson@romaine.life",
		Mode:  sessionmodel.CodexGUIMode,
		Repos: []string{"romaine-life/tank-operator"},
	})
	if err != nil {
		t.Fatal(err)
	}

	pod, err := client.CoreV1().Pods(sessionmodel.SessionsNamespace).Get(context.Background(), *info.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(pod.Spec.InitContainers), 1; got != want {
		t.Fatalf("init container count = %d, want %d", got, want)
	}
	cloner := pod.Spec.InitContainers[0]
	if got, want := cloner.Name, "repo-cloner"; got != want {
		t.Fatalf("init container name = %q, want %q", got, want)
	}
	env := map[string]string{}
	for _, item := range cloner.Env {
		env[item.Name] = item.Value
	}
	if got, want := env["TANK_REPOS_JSON"], "[\"romaine-life/tank-operator\"]"; got != want {
		t.Fatalf("TANK_REPOS_JSON = %q, want %q", got, want)
	}
}

func TestManagerCreateThreadsSpireLensCapabilityIntoPodManifest(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := NewManager(client, nil, sessionmodel.SessionsNamespace, nil, nil, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionImage:                   "claude-image",
			CodexSessionImage:              "codex-image",
			SpireLensTailscaleOIDCClientID: "oidc-client",
			SpireLensTailscaleTailnet:      "-",
			SpireLensHost:                  "nelsonlaptop",
		},
	})

	info, err := mgr.Create(context.Background(), CreateOptions{
		Owner:        "nelson@romaine.life",
		Mode:         sessionmodel.ClaudeGUIMode,
		Capabilities: []string{sessionmodel.SessionCapabilitySpireLensMCP},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sessionmodel.HasSessionCapability(info.Capabilities, sessionmodel.SessionCapabilitySpireLensMCP) {
		t.Fatalf("info capabilities = %#v, want spirelens_mcp", info.Capabilities)
	}

	pod, err := client.CoreV1().Pods(sessionmodel.SessionsNamespace).Get(context.Background(), *info.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pod.Annotations[capabilitiesAnnotation], `["spirelens_mcp"]`; got != want {
		t.Fatalf("pod capabilities annotation = %q, want %q", got, want)
	}
	claudeEnv := containerEnvMap(t, pod, "claude")
	if got, want := claudeEnv["SPIRELENS_MCP_ENABLED"], "true"; got != want {
		t.Fatalf("SPIRELENS_MCP_ENABLED = %q, want %q", got, want)
	}
	proxyEnv := containerEnvMap(t, pod, "mcp-auth-proxy")
	if got, want := proxyEnv["SPIRELENS_MCP_UPSTREAM"], "http://nelsonlaptop:15527"; got != want {
		t.Fatalf("SPIRELENS_MCP_UPSTREAM = %q, want %q", got, want)
	}
}

func TestManagerCreateRejectsSpireLensCapabilityWhenUnconfigured(t *testing.T) {
	client := fake.NewSimpleClientset()
	mgr := NewManager(client, nil, sessionmodel.SessionsNamespace, nil, nil, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionImage:      "claude-image",
			CodexSessionImage: "codex-image",
		},
	})

	_, err := mgr.Create(context.Background(), CreateOptions{
		Owner:        "nelson@romaine.life",
		Mode:         sessionmodel.ClaudeGUIMode,
		Capabilities: []string{sessionmodel.SessionCapabilitySpireLensMCP},
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Create error = %v, want not configured", err)
	}
	pods, listErr := client.CoreV1().Pods(sessionmodel.SessionsNamespace).List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("created pods = %d, want none", len(pods.Items))
	}
}

func TestManagerCreatePersistsInitialDisplayName(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := &managerTestRegistry{
		nextID: "57",
		avatarAssignment: sessionmodel.SessionAvatarAssignment{
			AgentAvatarID:  "agent-57",
			SystemAvatarID: "system-57",
		},
	}
	mgr := NewManager(client, nil, sessionmodel.SessionsNamespace, registry, nil, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			CodexSessionImage: "codex-image",
		},
	})

	rawName := "  Launch draft  "
	info, err := mgr.Create(context.Background(), CreateOptions{
		Owner: "nelson@romaine.life",
		Mode:  sessionmodel.CodexGUIMode,
		Name:  &rawName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name == nil || *info.Name != "Launch draft" {
		t.Fatalf("info name = %#v, want normalized initial title", info.Name)
	}
	pod, err := client.CoreV1().Pods(sessionmodel.SessionsNamespace).Get(context.Background(), *info.PodName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pod.Annotations["tank-operator/display-name"], "Launch draft"; got != want {
		t.Fatalf("pod display-name annotation = %q, want %q", got, want)
	}
	if len(registry.records) != 1 {
		t.Fatalf("registry records = %d, want 1", len(registry.records))
	}
	if got := registry.records[0].Name; got == nil || *got != "Launch draft" {
		t.Fatalf("registry name = %#v, want normalized initial title", got)
	}
}

func TestManagerCreateWritesReservedAvatarsBeforeVisibleRow(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := &managerTestRegistry{
		nextID: "42",
		avatarAssignment: sessionmodel.SessionAvatarAssignment{
			AgentAvatarID:  "agent-42",
			SystemAvatarID: "system-42",
		},
	}
	emitter := &recordingRowEmitter{}
	mgr := NewManager(client, nil, sessionmodel.SessionsNamespace, registry, emitter, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			CodexSessionImage: "codex-image",
		},
	})

	info, err := mgr.Create(context.Background(), CreateOptions{
		Owner: "nelson@romaine.life",
		Mode:  sessionmodel.CodexGUIMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.AgentAvatarID != "agent-42" || info.SystemAvatarID != "system-42" {
		t.Fatalf("info avatar assignment = (%q, %q), want reserved ids", info.AgentAvatarID, info.SystemAvatarID)
	}
	if len(registry.reserveCalls) != 1 || registry.reserveCalls[0] != "42" {
		t.Fatalf("reserve calls = %#v, want [42]", registry.reserveCalls)
	}
	for _, record := range registry.upserts {
		if record.ID != "42" || !record.Visible {
			continue
		}
		if record.AgentAvatarID != "agent-42" || record.SystemAvatarID != "system-42" {
			t.Fatalf("visible create upsert missing reserved avatars: %#v", record)
		}
	}
	if len(emitter.ids) != 1 || emitter.ids[0] != "42" {
		t.Fatalf("published rows = %#v, want [42]", emitter.ids)
	}
}

func TestManagerCreateFailsBeforeVisibleRowWithoutReservedAgentAvatar(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := &managerTestRegistry{nextID: "43"}
	emitter := &recordingRowEmitter{}
	mgr := NewManager(client, nil, sessionmodel.SessionsNamespace, registry, emitter, ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			CodexSessionImage: "codex-image",
		},
	})

	_, err := mgr.Create(context.Background(), CreateOptions{
		Owner: "nelson@romaine.life",
		Mode:  sessionmodel.CodexGUIMode,
	})
	if err == nil || !strings.Contains(err.Error(), "no agent avatars available") {
		t.Fatalf("Create error = %v, want no-agent-avatar error", err)
	}
	if len(registry.upserts) != 0 {
		t.Fatalf("registry upserts = %#v, want none before avatar reservation succeeds", registry.upserts)
	}
	pods, listErr := client.CoreV1().Pods(sessionmodel.SessionsNamespace).List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("created pods = %d, want none before avatar reservation succeeds", len(pods.Items))
	}
	if len(emitter.ids) != 0 {
		t.Fatalf("published rows = %#v, want none", emitter.ids)
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

func TestManagerSetRuntimeConfigPersistsAndPublishes(t *testing.T) {
	registry := &managerTestRegistry{
		records: []sessionmodel.SessionRecord{
			{
				ID:      "8",
				Email:   "nelson@romaine.life",
				Mode:    sessionmodel.CodexGUIMode,
				Visible: true,
				Status:  "Active",
			},
		},
	}
	emitter := &recordingRowEmitter{}
	mgr := &Manager{
		client:    fake.NewSimpleClientset(),
		namespace: sessionmodel.SessionsNamespace,
		registry:  registry,
		emitter:   emitter,
	}

	info, err := mgr.SetRuntimeConfig(context.Background(), "nelson@romaine.life", "8", "gpt-5.5", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	if info.RuntimeModel != "gpt-5.5" || info.RuntimeEffort != "xhigh" || info.RuntimeConfiguredAt == nil || *info.RuntimeConfiguredAt == "" {
		t.Fatalf("runtime config info = %#v", info)
	}
	if strings.Join(emitter.ids, ",") != "8" {
		t.Fatalf("published ids = %v, want [8]", emitter.ids)
	}
}

func assertSkillStateActive(t *testing.T, label string, state map[string]any, want bool) {
	t.Helper()
	if got := state["active"]; got != want {
		t.Fatalf("%s active = %#v, want %v (state=%#v)", label, got, want, state)
	}
}

func containerEnvMap(t *testing.T, pod *corev1.Pod, containerName string) map[string]string {
	t.Helper()
	for _, container := range pod.Spec.Containers {
		if container.Name != containerName {
			continue
		}
		out := map[string]string{}
		for _, env := range container.Env {
			out[env.Name] = env.Value
		}
		return out
	}
	t.Fatalf("container %q not found", containerName)
	return nil
}

type recordingRowEmitter struct {
	ids []string
}

func (r *recordingRowEmitter) PublishCurrentRow(_ context.Context, _ string, sessionID string) {
	r.ids = append(r.ids, sessionID)
}

type managerTestRegistry struct {
	records          []sessionmodel.SessionRecord
	upserts          []sessionmodel.SessionRecord
	nextID           string
	avatarAssignment sessionmodel.SessionAvatarAssignment
	reserveCalls     []string
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
	r.upserts = append(r.upserts, record)
	for i, existing := range r.records {
		if strings.EqualFold(existing.Email, record.Email) && existing.ID == record.ID {
			r.records[i] = record
			return nil
		}
	}
	r.records = append(r.records, record)
	return nil
}

func (r *managerTestRegistry) ReserveSessionAvatars(_ context.Context, _ string, sessionID string) (sessionmodel.SessionAvatarAssignment, error) {
	r.reserveCalls = append(r.reserveCalls, sessionID)
	return r.avatarAssignment, nil
}

func (r *managerTestRegistry) SetName(context.Context, string, string, *string) error { return nil }

func (r *managerTestRegistry) SetTestState(context.Context, string, string, map[string]any) error {
	return nil
}

func (r *managerTestRegistry) SetRolloutState(context.Context, string, string, map[string]any) error {
	return nil
}

func (r *managerTestRegistry) SetCloneState(context.Context, string, string, map[string]any) error {
	return nil
}

func (r *managerTestRegistry) SetRuntimeConfig(_ context.Context, email, sessionID, model, effort string) error {
	for i, record := range r.records {
		if strings.EqualFold(record.Email, email) && record.ID == sessionID {
			r.records[i].RuntimeModel = model
			r.records[i].RuntimeEffort = effort
			r.records[i].RuntimeConfiguredAt = "2026-05-21T00:00:00Z"
			return nil
		}
	}
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
