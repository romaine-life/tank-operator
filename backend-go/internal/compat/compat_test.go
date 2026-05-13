package compat

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNormalizeSessionMode(t *testing.T) {
	tests := map[string]string{
		"":                      ClaudeCLIMode,
		"subscription":          ClaudeCLIMode,
		"subscription_headless": ClaudeGUIMode,
		"codex_subscription":    CodexCLIMode,
		"codex_headless":        CodexGUIMode,
		"pi_subscription":       PiCLIMode,
		"codex_config":          CodexConfigMode,
		"unknown":               "unknown",
	}
	for input, want := range tests {
		if got := NormalizeSessionMode(input); got != want {
			t.Fatalf("NormalizeSessionMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOwnerLabelMatchesPython(t *testing.T) {
	if got, want := OwnerLabel("nelson@romaine.life"), "u-db1458e0eb6e9e75"; got != want {
		t.Fatalf("OwnerLabel() = %q, want %q", got, want)
	}
	if got, want := OwnerLabel("User@Example.COM"), "u-857296a3c8a81182"; got != want {
		t.Fatalf("OwnerLabel() = %q, want %q", got, want)
	}
}

func TestNormalizeName(t *testing.T) {
	blank := " \t\n "
	if got := NormalizeName(&blank); got != nil {
		t.Fatalf("blank name normalized to %q, want nil", *got)
	}
	long := strings.Repeat("x", MaxNameLength+5)
	got := NormalizeName(&long)
	if got == nil {
		t.Fatal("long name normalized to nil")
	}
	if len(*got) != MaxNameLength {
		t.Fatalf("normalized name length = %d, want %d", len(*got), MaxNameLength)
	}
}

func TestRunIDsAndPaths(t *testing.T) {
	valid := []string{"abc123", "run_abc-123.4", strings.Repeat("a", 80)}
	for _, value := range valid {
		if !ValidateRunID(value) {
			t.Fatalf("ValidateRunID(%q) = false, want true", value)
		}
	}
	invalid := []string{"", strings.Repeat("a", 81), "../../bad", "has space"}
	for _, value := range invalid {
		if ValidateRunID(value) {
			t.Fatalf("ValidateRunID(%q) = true, want false", value)
		}
	}
	if got, want := RunStreamPath("abc"), "/tmp/tank-run-abc.stream"; got != want {
		t.Fatalf("RunStreamPath() = %q, want %q", got, want)
	}
	if got, want := RunPIDPath("abc"), "/tmp/tank-run-abc.pid"; got != want {
		t.Fatalf("RunPIDPath() = %q, want %q", got, want)
	}
}

func TestDocumentIDsAndShapes(t *testing.T) {
	if got, want := SessionDocID("default", "12"), "session:12"; got != want {
		t.Fatalf("SessionDocID(default) = %q, want %q", got, want)
	}
	if got, want := SessionDocID("slot-a", "12"), "session:slot-a:12"; got != want {
		t.Fatalf("SessionDocID(slot) = %q, want %q", got, want)
	}
	if got, want := SessionCounterDocID("default"), "session-counter"; got != want {
		t.Fatalf("SessionCounterDocID(default) = %q, want %q", got, want)
	}
	if got, want := SessionCounterDocID("slot-a"), "session-counter:slot-a"; got != want {
		t.Fatalf("SessionCounterDocID(slot) = %q, want %q", got, want)
	}

	sessionDoc := SessionDoc(SessionRecord{
		ID:      "12",
		Email:   "USER@example.COM",
		Mode:    ClaudeCLIMode,
		Scope:   "default",
		PodName: "session-12",
		Visible: true,
	})
	if got, want := sessionDoc["id"], "session:12"; got != want {
		t.Fatalf("session doc id = %v, want %q", got, want)
	}
	if got, want := sessionDoc["email"], "USER@example.COM"; got != want {
		t.Fatalf("session doc email = %v, want %q", got, want)
	}

	runDoc := ActiveRunDoc(ActiveRunRecord{
		SessionID:  "12",
		Email:      "USER@example.COM",
		RunID:      "run_12",
		PodName:    "session-12",
		Provider:   "codex",
		StreamPath: RunStreamPath("run_12"),
		PIDPath:    RunPIDPath("run_12"),
	})
	if got, want := runDoc["id"], "12"; got != want {
		t.Fatalf("active run id = %v, want %q", got, want)
	}
	if got, want := runDoc["status"], "running"; got != want {
		t.Fatalf("active run status = %v, want %q", got, want)
	}
	if got, want := runDoc["email"], "USER@example.COM"; got != want {
		t.Fatalf("active run email = %v, want %q", got, want)
	}
}

func TestPodManifestCompatibilityCore(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", "codex_headless", ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
		PiSessionImage:    "pi-image",
	})

	metadata := manifest["metadata"].(map[string]any)
	if got, want := metadata["name"], "session-12"; got != want {
		t.Fatalf("pod name = %v, want %q", got, want)
	}
	labels := metadata["labels"].(map[string]any)
	if got, want := labels["tank-operator/owner"], "u-db1458e0eb6e9e75"; got != want {
		t.Fatalf("owner label = %v, want %q", got, want)
	}
	if got, want := labels["tank-operator/mode"], CodexGUIMode; got != want {
		t.Fatalf("mode label = %v, want %q", got, want)
	}
	annotations := metadata["annotations"].(map[string]any)
	if got, want := annotations["tank-operator/owner-email"], "nelson@romaine.life"; got != want {
		t.Fatalf("owner annotation = %v, want %q", got, want)
	}

	spec := manifest["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	// codex_gui now adds a third container, codex-runner (the @openai/
	// codex-sdk runner), so the pod has 3 containers, not 2.
	if got, want := len(containers), 3; got != want {
		t.Fatalf("container count = %d, want %d", got, want)
	}
	if got, want := containers[0].(map[string]any)["name"], "mcp-auth-proxy"; got != want {
		t.Fatalf("sidecar container name = %v, want %q", got, want)
	}
	claude := containers[1].(map[string]any)
	if got, want := claude["name"], "claude"; got != want {
		t.Fatalf("main container name = %v, want %q", got, want)
	}
	if got, want := claude["image"], "codex-image"; got != want {
		t.Fatalf("main container image = %v, want %q", got, want)
	}
	ports := claude["ports"].([]any)
	if got, want := ports[0].(map[string]any)["name"], "sandbox-agent"; got != want {
		t.Fatalf("main container port name = %v, want %q", got, want)
	}
	codexRunner := containers[2].(map[string]any)
	if got, want := codexRunner["name"], "codex-runner"; got != want {
		t.Fatalf("codex-runner container name = %v, want %q", got, want)
	}
	if got, want := codexRunner["image"], "codex-image"; got != want {
		t.Fatalf("codex-runner image = %v, want %q (same image as the user container; the runner is a multi-stage build into the same image)", got, want)
	}
	volumes := spec["volumes"].([]any)
	// codex_gui adds session-config + workspace emptyDir (shared between
	// the claude container and the codex-runner sidecar). Codex auth is
	// proxy-owned, so the real codex-credentials Secret is not mounted.
	if got, want := len(volumes), 2; got != want {
		t.Fatalf("volume count = %d, want %d", got, want)
	}
}

func TestPodManifestCodexUsesAPIProxyWithoutCredentialSecret(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:            "claude-image",
		CodexSessionImage:       "codex-image",
		CodexAPIProxyIP:         "10.0.0.50",
		OAuthGatewayCAConfigMap: "claude-oauth-ca",
	})

	spec := manifest["spec"].(map[string]any)
	assertHostAlias(t, spec, "10.0.0.50", "chatgpt.com")
	assertNoVolume(t, spec["volumes"].([]any), "codex-creds")
	assertVolume(t, spec["volumes"].([]any), "oauth-gateway-ca")

	containers := spec["containers"].([]any)
	claudeEnv := containerEnv(findContainer(t, containers, "claude"))
	if got, want := claudeEnv["CODEX_CA_CERTIFICATE"], "/etc/oauth-gateway-ca/ca.crt"; got != want {
		t.Fatalf("claude CODEX_CA_CERTIFICATE = %v, want %q", got, want)
	}
	codexRunner := findContainer(t, containers, "codex-runner")
	runnerEnv := containerEnv(codexRunner)
	if got, want := runnerEnv["CODEX_CA_CERTIFICATE"], "/etc/oauth-gateway-ca/ca.crt"; got != want {
		t.Fatalf("runner CODEX_CA_CERTIFICATE = %v, want %q", got, want)
	}
	assertNoVolumeMount(t, codexRunner, "codex-creds")
	assertVolumeMount(t, codexRunner, "oauth-gateway-ca")
}

func TestPodManifestSDKRunnersReceiveTurnQueueEnv(t *testing.T) {
	tests := map[string]string{
		ClaudeGUIMode: "agent-runner",
		CodexGUIMode:  "codex-runner",
	}
	for mode, runnerName := range tests {
		t.Run(mode, func(t *testing.T) {
			manifest := PodManifest("12", "nelson@romaine.life", mode, ManifestOptions{
				SessionImage:                 "claude-image",
				CodexSessionImage:            "codex-image",
				CosmosEndpoint:               "https://cosmos.example",
				CosmosDatabase:               "tank-db",
				CosmosSessionEventsContainer: "events",
				CosmosTurnQueueContainer:     "turns",
			})
			spec := manifest["spec"].(map[string]any)
			containers := spec["containers"].([]any)
			env := containerEnv(findContainer(t, containers, runnerName))
			if got, want := env["COSMOS_ENDPOINT"], "https://cosmos.example"; got != want {
				t.Fatalf("COSMOS_ENDPOINT = %v, want %q", got, want)
			}
			if got, want := env["COSMOS_DATABASE"], "tank-db"; got != want {
				t.Fatalf("COSMOS_DATABASE = %v, want %q", got, want)
			}
			if got, want := env["COSMOS_SESSION_EVENTS_CONTAINER"], "events"; got != want {
				t.Fatalf("COSMOS_SESSION_EVENTS_CONTAINER = %v, want %q", got, want)
			}
			if got, want := env["COSMOS_TURN_QUEUE_CONTAINER"], "turns"; got != want {
				t.Fatalf("COSMOS_TURN_QUEUE_CONTAINER = %v, want %q", got, want)
			}
		})
	}
}

func TestPythonCompatFixture(t *testing.T) {
	fixture := loadFixture(t)

	modeAliases := fixture["mode_aliases"].(map[string]any)
	for input, want := range modeAliases {
		if got := NormalizeSessionMode(input); got != want {
			t.Fatalf("NormalizeSessionMode(%q) = %q, want %q", input, got, want)
		}
	}

	for _, item := range fixture["owner_labels"].([]any) {
		row := item.(map[string]any)
		email := row["email"].(string)
		if got, want := OwnerLabel(email), row["label"]; got != want {
			t.Fatalf("OwnerLabel(%q) = %q, want %q", email, got, want)
		}
	}

	runIDs := fixture["run_ids"].(map[string]any)
	for _, value := range runIDs["valid"].([]any) {
		if !ValidateRunID(value.(string)) {
			t.Fatalf("ValidateRunID(%q) = false, want true", value)
		}
	}
	for _, value := range runIDs["invalid"].([]any) {
		if ValidateRunID(value.(string)) {
			t.Fatalf("ValidateRunID(%q) = true, want false", value)
		}
	}

	for _, item := range fixture["run_paths"].([]any) {
		row := item.(map[string]any)
		runID := row["run_id"].(string)
		if got, want := RunStreamPath(runID), row["stream_path"]; got != want {
			t.Fatalf("RunStreamPath(%q) = %q, want %q", runID, got, want)
		}
		if got, want := RunPIDPath(runID), row["pid_path"]; got != want {
			t.Fatalf("RunPIDPath(%q) = %q, want %q", runID, got, want)
		}
	}

	for _, item := range fixture["session_doc_ids"].([]any) {
		row := item.(map[string]any)
		scope := row["scope"].(string)
		sessionID := row["session_id"].(string)
		if got, want := SessionDocID(scope, sessionID), row["doc_id"]; got != want {
			t.Fatalf("SessionDocID(%q, %q) = %q, want %q", scope, sessionID, got, want)
		}
		if got, want := SessionCounterDocID(scope), row["counter_id"]; got != want {
			t.Fatalf("SessionCounterDocID(%q) = %q, want %q", scope, got, want)
		}
	}

	name := "Workbench"
	assertCanonicalJSON(t, SessionDoc(SessionRecord{
		ID:          "12",
		Email:       "USER@example.COM",
		Mode:        ClaudeCLIMode,
		Scope:       "default",
		PodName:     "session-12",
		Name:        &name,
		Visible:     true,
		RequestedAt: "2026-05-11T00:00:00+00:00",
		CreatedAt:   "2026-05-11T00:00:01+00:00",
		UpdatedAt:   "2026-05-11T00:00:02+00:00",
	}), fixture["session_doc"])

	assertCanonicalJSON(t, ActiveRunDoc(ActiveRunRecord{
		SessionID:  "12",
		Email:      "USER@example.COM",
		RunID:      "run_12",
		PodName:    "session-12",
		Provider:   "codex",
		Status:     "running",
		StreamPath: RunStreamPath("run_12"),
		PIDPath:    RunPIDPath("run_12"),
		StartedAt:  "2026-05-11T00:01:00+00:00",
		UpdatedAt:  "2026-05-11T00:01:01+00:00",
	}), fixture["active_run_doc"])

	core := fixture["pod_manifest_core"].(map[string]any)
	input := core["input"].(map[string]any)
	// Inject the same image strings the fixture asserts on. The
	// orchestrator's runtime path gets these from the chart's
	// SESSION_IMAGE / CODEX_SESSION_IMAGE / PI_SESSION_IMAGE env vars
	// (see cmd/tank-operator/main.go); the test stands in for that
	// wiring with literals so the manifest contract is exercised
	// without dragging Helm into the test.
	manifest := PodManifest(
		input["session_id"].(string),
		input["owner"].(string),
		input["mode"].(string),
		ManifestOptions{
			SessionImage:      "romainecr.azurecr.io/claude-container:latest",
			CodexSessionImage: "romainecr.azurecr.io/codex-container:latest",
			PiSessionImage:    "romainecr.azurecr.io/pi-container:latest",
		},
	)
	spec := manifest["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	assertCanonicalJSON(t, map[string]any{
		"input":            input,
		"metadata":         manifest["metadata"],
		"service_account":  spec["serviceAccountName"],
		"security_context": spec["securityContext"],
		"container_names":  containerNames(containers),
		"container_images": containerImages(containers),
		"claude_command":   claudeCommand(containers),
		"claude_env":       claudeEnv(containers),
	}, core)
}

func loadFixture(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("testdata/python_compat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertCanonicalJSON(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("json mismatch\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

func containerNames(containers []any) []any {
	out := make([]any, 0, len(containers))
	for _, item := range containers {
		container := item.(map[string]any)
		out = append(out, container["name"])
	}
	return out
}

func containerImages(containers []any) map[string]any {
	out := map[string]any{}
	for _, item := range containers {
		container := item.(map[string]any)
		out[container["name"].(string)] = container["image"]
	}
	return out
}

func claudeEnv(containers []any) map[string]any {
	for _, item := range containers {
		container := item.(map[string]any)
		if container["name"] != "claude" {
			continue
		}
		return containerEnv(container)
	}
	return nil
}

func findContainer(t *testing.T, containers []any, name string) map[string]any {
	t.Helper()
	for _, item := range containers {
		container := item.(map[string]any)
		if container["name"] == name {
			return container
		}
	}
	t.Fatalf("container %q not found", name)
	return nil
}

func assertHostAlias(t *testing.T, spec map[string]any, ip, hostname string) {
	t.Helper()
	for _, item := range spec["hostAliases"].([]any) {
		alias := item.(map[string]any)
		if alias["ip"] != ip {
			continue
		}
		for _, host := range alias["hostnames"].([]any) {
			if host == hostname {
				return
			}
		}
	}
	t.Fatalf("hostAlias %s -> %s not found", hostname, ip)
}

func assertVolume(t *testing.T, volumes []any, name string) {
	t.Helper()
	for _, item := range volumes {
		volume := item.(map[string]any)
		if volume["name"] == name {
			return
		}
	}
	t.Fatalf("volume %q not found", name)
}

func assertNoVolume(t *testing.T, volumes []any, name string) {
	t.Helper()
	for _, item := range volumes {
		volume := item.(map[string]any)
		if volume["name"] == name {
			t.Fatalf("volume %q should not be present", name)
		}
	}
}

func assertVolumeMount(t *testing.T, container map[string]any, name string) {
	t.Helper()
	for _, item := range container["volumeMounts"].([]any) {
		mount := item.(map[string]any)
		if mount["name"] == name {
			return
		}
	}
	t.Fatalf("volumeMount %q not found", name)
}

func assertNoVolumeMount(t *testing.T, container map[string]any, name string) {
	t.Helper()
	for _, item := range container["volumeMounts"].([]any) {
		mount := item.(map[string]any)
		if mount["name"] == name {
			t.Fatalf("volumeMount %q should not be present", name)
		}
	}
}

func containerEnv(container map[string]any) map[string]any {
	out := map[string]any{}
	for _, envItem := range container["env"].([]any) {
		env := envItem.(map[string]any)
		out[env["name"].(string)] = env["value"]
	}
	return out
}

func claudeCommand(containers []any) []any {
	for _, item := range containers {
		container := item.(map[string]any)
		if container["name"] == "claude" {
			return container["command"].([]any)
		}
	}
	return nil
}
