package sessionmodel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNormalizeSessionMode(t *testing.T) {
	tests := map[string]string{
		"":               ClaudeGUIMode,
		"codex_config":   CodexConfigMode,
		"codex_exec_gui": CodexExecGUIMode,
		"unknown":        "unknown",
	}
	for input, want := range tests {
		if got := NormalizeSessionMode(input); got != want {
			t.Fatalf("NormalizeSessionMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeBugLabelName(t *testing.T) {
	raw := "  bug:  Slow checkout   regression  "
	label, err := NormalizeBugLabelName(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if label == nil || label.Name != "Slow checkout regression" || label.Slug != "slow-checkout-regression" || label.DisplayName != "bug: Slow checkout regression" {
		t.Fatalf("label = %+v", label)
	}

	empty := "bug:"
	label, err = NormalizeBugLabelName(&empty)
	if err != nil {
		t.Fatal(err)
	}
	if label != nil {
		t.Fatalf("empty bug prefix should clear, got %+v", label)
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

func TestSessionDisplayName(t *testing.T) {
	named := "  My session  "
	if got, want := SessionDisplayName(&named, "session-622", "622"), "My session"; got != want {
		t.Fatalf("set name = %q, want %q (returned verbatim, trimmed)", got, want)
	}

	if got, want := SessionDisplayName(nil, "session-622", "622"), "622"; got != want {
		t.Fatalf("nil name = %q, want %q (derived from pod name)", got, want)
	}

	blank := "   "
	if got, want := SessionDisplayName(&blank, "session-622", "622"), "622"; got != want {
		t.Fatalf("whitespace name = %q, want %q (falls back to derived short id)", got, want)
	}

	if got, want := SessionDisplayName(nil, "", "47"), "47"; got != want {
		t.Fatalf("empty pod name = %q, want %q (derives from session id)", got, want)
	}

	if got, want := SessionDisplayName(nil, "session-0123456789abcdef", "0123456789abcdef"), "01234567"; got != want {
		t.Fatalf("long pod-derived id = %q, want %q (truncated to 8 chars after session- strip)", got, want)
	}
	if got, want := SessionDisplayName(nil, "", "0123456789abcdef"), "01234567"; got != want {
		t.Fatalf("long session id = %q, want %q (truncated to 8 chars)", got, want)
	}
}

func TestNormalizeSessionCapabilities(t *testing.T) {
	got, err := NormalizeSessionCapabilities([]string{
		"  " + SessionCapabilitySpireLensMCP + "  ",
		SessionCapabilitySpireLensMCP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != SessionCapabilitySpireLensMCP {
		t.Fatalf("capabilities = %#v, want single normalized spirelens capability", got)
	}
	if !HasSessionCapability(got, SessionCapabilitySpireLensMCP) {
		t.Fatalf("HasSessionCapability(%#v, %q) = false", got, SessionCapabilitySpireLensMCP)
	}

	if _, err := NormalizeSessionCapabilities([]string{"unknown"}); err == nil {
		t.Fatal("unknown capability accepted")
	}
	if _, err := NormalizeSessionCapabilities([]string{""}); err == nil {
		t.Fatal("empty capability accepted")
	}
}

func TestDocumentIDsAndShapes(t *testing.T) {
	if got, want := SessionDocID("default", "12"), "session:12"; got != want {
		t.Fatalf("SessionDocID(default) = %q, want %q", got, want)
	}
	if got, want := SessionDocID("slot-a", "12"), "session:slot-a:12"; got != want {
		t.Fatalf("SessionDocID(slot) = %q, want %q", got, want)
	}
	if got, want := SessionStorageKey("default", "12"), "12"; got != want {
		t.Fatalf("SessionStorageKey(default) = %q, want %q", got, want)
	}
	if got, want := SessionStorageKey("slot-a", "12"), "slot-a:12"; got != want {
		t.Fatalf("SessionStorageKey(slot) = %q, want %q", got, want)
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

}

func TestPodManifestSpireLensCapabilityWiresTailnetMCP(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", ClaudeGUIMode, ManifestOptions{
		SessionImage:                   "claude-image",
		CodexSessionImage:              "codex-image",
		Capabilities:                   []string{SessionCapabilitySpireLensMCP},
		SpireLensTailscaleOIDCClientID: "oidc-client",
		SpireLensTailscaleTailnet:      "-",
		SpireLensTailscaleAuthTag:      "tag:spirelens-orchestrator",
		SpireLensHost:                  "nelsonlaptop",
		SpireLensMCPPort:               15527,
	})

	metadata := manifest["metadata"].(map[string]any)
	annotations := metadata["annotations"].(map[string]any)
	if got, want := annotations["tank-operator/capabilities"], `["spirelens_mcp"]`; got != want {
		t.Fatalf("capabilities annotation = %v, want %q", got, want)
	}
	spec := manifest["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	claude := findContainer(t, containers, "claude")
	claudeEnv := containerEnv(claude)
	if got, want := claudeEnv["SPIRELENS_MCP_ENABLED"], "true"; got != want {
		t.Fatalf("SPIRELENS_MCP_ENABLED = %v, want %q", got, want)
	}
	if got, want := claudeEnv["SPIRELENS_TAILSCALE_OIDC_CLIENT_ID"], "oidc-client"; got != want {
		t.Fatalf("SPIRELENS_TAILSCALE_OIDC_CLIENT_ID = %v, want %q", got, want)
	}
	hostnameRef := claudeEnv["SPIRELENS_TAILSCALE_HOSTNAME"].(map[string]any)["fieldRef"].(map[string]any)
	if got, want := hostnameRef["fieldPath"], "metadata.name"; got != want {
		t.Fatalf("SPIRELENS_TAILSCALE_HOSTNAME fieldPath = %v, want %q", got, want)
	}

	proxy := findContainer(t, containers, "mcp-auth-proxy")
	proxyEnv := containerEnv(proxy)
	if got, want := proxyEnv["SPIRELENS_MCP_UPSTREAM"], "http://nelsonlaptop:15527"; got != want {
		t.Fatalf("SPIRELENS_MCP_UPSTREAM = %v, want %q", got, want)
	}
	if got, want := proxyEnv["TAILNET_HTTP_PROXY"], "http://127.0.0.1:1055"; got != want {
		t.Fatalf("TAILNET_HTTP_PROXY = %v, want %q", got, want)
	}
	assertConfigMapMountSubPath(t, claude, "/workspace/.mcp.json", "mcp.spirelens.json")
	assertVolumeMount(t, claude, "auth-romaine-sa-token")
	assertConfigMapMountSubPath(t, proxy, "/workspace/.mcp.json", "mcp.spirelens.json")
}

func TestPodManifestCompatibilityCore(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
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
	if got, want := labels["tank-operator/session-scope"], "default"; got != want {
		t.Fatalf("session scope label = %v, want %q", got, want)
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
	mcpProxy := containers[0].(map[string]any)
	// SA token audience-pinned to auth.romaine.life — used by the
	// sidecar to exchange for a role=service JWT against
	// /api/auth/exchange/k8s. See romaine-life/tank-operator#486.
	assertVolumeMount(t, mcpProxy, "auth-romaine-sa-token")
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
	codexRunnerResources := codexRunner["resources"].(map[string]any)
	if got, want := codexRunnerResources["limits"].(map[string]any)["memory"], "3072Mi"; got != want {
		t.Fatalf("codex-runner memory limit = %v, want %q", got, want)
	}
	assertVolumeMount(t, codexRunner, "tank-operator-sa-token")
	assertVolumeMount(t, codexRunner, "auth-romaine-sa-token")
	volumes := spec["volumes"].([]any)
	// codex_gui adds session-config + Tank attestation token +
	// auth.romaine.life attestation token + workspace emptyDir. Codex
	// auth is proxy-owned, so the real codex-credentials Secret is not
	// mounted. The auth.romaine.life token is the inbound to
	// /api/auth/exchange/k8s — see romaine-life/tank-operator#486.
	if got, want := len(volumes), 4; got != want {
		t.Fatalf("volume count = %d, want %d", got, want)
	}
	assertVolume(t, volumes, "tank-operator-sa-token")
	assertVolume(t, volumes, "auth-romaine-sa-token")
}

func TestPodManifestAntigravityConfigUsesGlibcImageWithoutSidecar(t *testing.T) {
	manifest := PodManifest("77", "nelson@romaine.life", AntigravityConfigMode, ManifestOptions{
		SessionImage:            "claude-image",
		CodexSessionImage:       "codex-image",
		AntigravitySessionImage: "antigravity-image",
	})

	if got, want := manifest["metadata"].(map[string]any)["labels"].(map[string]any)["tank-operator/mode"], AntigravityConfigMode; got != want {
		t.Fatalf("mode label = %v, want %q", got, want)
	}

	spec := manifest["spec"].(map[string]any)
	containers := spec["containers"].([]any)

	// antigravity_config is a single-container terminal-login pod. The glibc
	// antigravity image lacks the (Python) mcp-auth-proxy binary, and a
	// credential-mint login needs neither the MCP gateway sidecar nor an SDK
	// runner — so the pod is just the `claude` (sandbox-agent terminal)
	// container. Regression guard for the sidecar-gating branch.
	if got, want := len(containers), 1; got != want {
		t.Fatalf("container count = %d, want %d (claude only; no mcp-auth-proxy, no runner)", got, want)
	}
	claude := containers[0].(map[string]any)
	if got, want := claude["name"], "claude"; got != want {
		t.Fatalf("sole container name = %v, want %q", got, want)
	}
	// Stamps the dedicated glibc image, not SessionImage/CodexSessionImage.
	if got, want := claude["image"], "antigravity-image"; got != want {
		t.Fatalf("antigravity_config image = %v, want %q (must use AntigravitySessionImage, not the claude/codex image)", got, want)
	}
	for _, c := range containers {
		if name := c.(map[string]any)["name"]; name == "mcp-auth-proxy" {
			t.Fatal("antigravity_config must not carry the mcp-auth-proxy sidecar — the glibc image has no such binary")
		}
	}
}

func TestPodManifestDisplayNameAnnotation(t *testing.T) {
	name := "  Launch draft  "
	manifest := PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
		Name:              &name,
	})
	metadata := manifest["metadata"].(map[string]any)
	annotations := metadata["annotations"].(map[string]any)
	if got, want := annotations["tank-operator/display-name"], "Launch draft"; got != want {
		t.Fatalf("display-name annotation = %v, want %q", got, want)
	}

	blank := "   "
	manifest = PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
		Name:              &blank,
	})
	metadata = manifest["metadata"].(map[string]any)
	annotations = metadata["annotations"].(map[string]any)
	if _, ok := annotations["tank-operator/display-name"]; ok {
		t.Fatal("blank display name should not create a pod annotation")
	}
}

func TestPodManifestMaterializesTankDocsBeforeSandboxAgent(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
	})

	spec := manifest["spec"].(map[string]any)
	cmd := claudeCommand(spec["containers"].([]any))
	if len(cmd) != 3 || cmd[0] != "bash" || cmd[1] != "-lc" {
		t.Fatalf("claude command = %v, want bash -lc script", cmd)
	}
	script := cmd[2].(string)
	if !strings.Contains(script, "/opt/tank/session-config/install-tank-docs.sh") {
		t.Fatalf("claude command does not materialize Tank docs: %s", script)
	}
	if !strings.Contains(script, "exec $sandbox_agent_cmd server") {
		t.Fatalf("claude command no longer execs sandbox-agent: %s", script)
	}
}

func TestPodManifestSelectedReposAddsRepoClonerInitContainer(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:            "claude-image",
		CodexSessionImage:       "codex-image",
		TankOperatorInternalURL: "http://tank-operator.test",
		Repos:                   []string{"romaine-life/tank-operator"},
	})

	spec := manifest["spec"].(map[string]any)
	initContainers := spec["initContainers"].([]any)
	if got, want := len(initContainers), 1; got != want {
		t.Fatalf("init container count = %d, want %d", got, want)
	}
	cloner := initContainers[0].(map[string]any)
	if got, want := cloner["name"], "repo-cloner"; got != want {
		t.Fatalf("init container name = %v, want %q", got, want)
	}
	if got, want := cloner["image"], "codex-image"; got != want {
		t.Fatalf("repo-cloner image = %v, want %q", got, want)
	}
	cmd := cloner["command"].([]any)
	if len(cmd) != 2 || cmd[0] != "bash" || cmd[1] != "/opt/tank/repo-cloner.sh" {
		t.Fatalf("repo-cloner command = %v, want [bash /opt/tank/repo-cloner.sh]", cmd)
	}
	env := containerEnv(cloner)
	if got, want := env["TANK_REPOS_JSON"], "[\"romaine-life/tank-operator\"]"; got != want {
		t.Fatalf("TANK_REPOS_JSON = %v, want %q", got, want)
	}
	if got, want := env["TANK_OPERATOR_INTERNAL_URL"], "http://tank-operator.test"; got != want {
		t.Fatalf("TANK_OPERATOR_INTERNAL_URL = %v, want %q", got, want)
	}
	sessionIDRef := env["SESSION_ID"].(map[string]any)["fieldRef"].(map[string]any)
	if got, want := sessionIDRef["fieldPath"], "metadata.labels['tank-operator/session-id']"; got != want {
		t.Fatalf("SESSION_ID fieldPath = %v, want %q", got, want)
	}
	assertVolumeMount(t, cloner, "session-config")
	assertVolumeMount(t, cloner, "workspace")
	assertVolumeMount(t, cloner, "auth-romaine-sa-token")
}

func TestPodManifestWithoutSelectedReposOmitsRepoCloner(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", CodexGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
	})

	spec := manifest["spec"].(map[string]any)
	if _, present := spec["initContainers"]; present {
		t.Fatalf("initContainers present without selected repos: %v", spec["initContainers"])
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

func TestPodManifestCodexRunnerAlwaysUsesAppServerTransport(t *testing.T) {
	for _, mode := range []string{CodexGUIMode, CodexAppServerMode} {
		t.Run(mode, func(t *testing.T) {
			manifest := PodManifest("12", "nelson@romaine.life", mode, ManifestOptions{
				SessionImage:      "claude-image",
				CodexSessionImage: "codex-image",
			})
			spec := manifest["spec"].(map[string]any)
			containers := spec["containers"].([]any)
			env := containerEnv(findContainer(t, containers, "codex-runner"))
			if got, want := env["CODEX_RUNNER_TRANSPORT"], "app-server"; got != want {
				t.Fatalf("CODEX_RUNNER_TRANSPORT = %v, want %q", got, want)
			}
		})
	}
}

func TestPodManifestCodexExecGUIUsesLegacyTransport(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", CodexExecGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
	})
	spec := manifest["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	env := containerEnv(findContainer(t, containers, "codex-runner"))
	if got, present := env["CODEX_RUNNER_TRANSPORT"]; present {
		t.Fatalf("CODEX_RUNNER_TRANSPORT = %v, want unset for legacy transport fallback", got)
	}
}

func TestPodManifestSDKRunnersReceiveSessionBusEnv(t *testing.T) {
	tests := map[string]string{
		ClaudeGUIMode: "agent-runner",
		CodexGUIMode:  "codex-runner",
	}
	for mode, runnerName := range tests {
		t.Run(mode, func(t *testing.T) {
			manifest := PodManifest("12", "nelson@romaine.life", mode, ManifestOptions{
				SessionImage:            "claude-image",
				CodexSessionImage:       "codex-image",
				SessionScope:            "slot-a",
				NATSURL:                 "nats://tank-nats.tank-operator.svc.cluster.local:4222",
				NATSStream:              "TANK_SESSION_BUS",
				NATSAuthSecret:          "tank-nats-auth",
				TankOperatorInternalURL: "http://tank-operator.tank-operator.svc.cluster.local",
			})
			spec := manifest["spec"].(map[string]any)
			containers := spec["containers"].([]any)
			env := containerEnv(findContainer(t, containers, runnerName))
			if got, want := env["NATS_URL"], "nats://tank-nats.tank-operator.svc.cluster.local:4222"; got != want {
				t.Fatalf("NATS_URL = %v, want %q", got, want)
			}
			if got, want := env["NATS_STREAM"], "TANK_SESSION_BUS"; got != want {
				t.Fatalf("NATS_STREAM = %v, want %q", got, want)
			}
			if got, want := env["TANK_SESSION_STORAGE_KEY"], "slot-a:12"; got != want {
				t.Fatalf("TANK_SESSION_STORAGE_KEY = %v, want %q", got, want)
			}
			tokenRef := env["NATS_TOKEN"].(map[string]any)["secretKeyRef"].(map[string]any)
			if got, want := tokenRef["name"], "tank-nats-auth"; got != want {
				t.Fatalf("NATS_TOKEN secret name = %v, want %q", got, want)
			}
			if got, want := tokenRef["key"], "token"; got != want {
				t.Fatalf("NATS_TOKEN secret key = %v, want %q", got, want)
			}
			if got, want := env["TANK_OPERATOR_INTERNAL_URL"], "http://tank-operator.tank-operator.svc.cluster.local"; got != want {
				t.Fatalf("TANK_OPERATOR_INTERNAL_URL = %v, want %q", got, want)
			}
			if got, want := env["TANK_OPERATOR_TOKEN_PATH"], "/var/run/secrets/tank-operator/token"; got != want {
				t.Fatalf("TANK_OPERATOR_TOKEN_PATH = %v, want %q", got, want)
			}
			assertVolumeMount(t, findContainer(t, containers, runnerName), "tank-operator-sa-token")
			assertVolumeMount(t, findContainer(t, containers, runnerName), "auth-romaine-sa-token")
		})
	}
}

func TestManifestFixture(t *testing.T) {
	fixture := loadFixture(t)

	for _, item := range fixture["owner_labels"].([]any) {
		row := item.(map[string]any)
		email := row["email"].(string)
		if got, want := OwnerLabel(email), row["label"]; got != want {
			t.Fatalf("OwnerLabel(%q) = %q, want %q", email, got, want)
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
		Name:        name,
		Visible:     true,
		RequestedAt: "2026-05-11T00:00:00+00:00",
		CreatedAt:   "2026-05-11T00:00:01+00:00",
		UpdatedAt:   "2026-05-11T00:00:02+00:00",
	}), fixture["session_doc"])

	core := fixture["pod_manifest_core"].(map[string]any)
	input := core["input"].(map[string]any)
	// Inject the same image strings the fixture asserts on. The
	// orchestrator's runtime path gets these from the chart's
	// SESSION_IMAGE / CODEX_SESSION_IMAGE env vars
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
	data, err := os.ReadFile("testdata/manifest_fixture.json")
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

// TestPodManifestSlotModeAttachesAgentRunnerHotSwap pins Checkbox 1 of
// scripts/check-session-pod-hot-swap-migration.mjs: with testEnv enabled
// (HotSwapAgentRunner=true), the agent-runner container gets the writable
// /var/run/agent-runner-hot volume, the matching volumeMount, and the
// supervisor env vars that tell the launch script to exec tank-supervisor.
func TestPodManifestSlotModeAttachesAgentRunnerHotSwap(t *testing.T) {
	manifest := PodManifest("63", "user@example.com", ClaudeGUIMode, ManifestOptions{
		SessionImage:       "claude-image",
		CodexSessionImage:  "codex-image",
		HotSwapAgentRunner: true,
	})

	spec := manifest["spec"].(map[string]any)
	volumes := spec["volumes"].([]any)
	assertVolume(t, volumes, "agent-runner-hot")

	containers := spec["containers"].([]any)
	runner := findContainer(t, containers, "agent-runner")
	assertVolumeMount(t, runner, "agent-runner-hot")

	mounts := runner["volumeMounts"].([]any)
	var hotMountPath string
	for _, m := range mounts {
		mm := m.(map[string]any)
		if mm["name"] == "agent-runner-hot" {
			hotMountPath, _ = mm["mountPath"].(string)
		}
	}
	if hotMountPath != "/var/run/agent-runner-hot" {
		t.Fatalf("agent-runner-hot mountPath = %q, want /var/run/agent-runner-hot", hotMountPath)
	}

	env := containerEnv(runner)
	if got, want := env["GLIMMUNG_SUPERVISOR_CHILD"], "/app/agent-runner-launch-binary.sh"; got != want {
		t.Fatalf("GLIMMUNG_SUPERVISOR_CHILD = %v, want %q", got, want)
	}
	if got, want := env["GLIMMUNG_SUPERVISOR_HOT_ARTIFACT"], "/var/run/agent-runner-hot/agent-runner-launch-binary.sh"; got != want {
		t.Fatalf("GLIMMUNG_SUPERVISOR_HOT_ARTIFACT = %v, want %q", got, want)
	}
	if got, want := env["GLIMMUNG_SUPERVISOR_RESTART_ENABLED"], "true"; got != want {
		t.Fatalf("GLIMMUNG_SUPERVISOR_RESTART_ENABLED = %v, want %q", got, want)
	}

	// The launch-script command is unchanged — the bash script itself
	// branches on the env var. This pins the property that the supervisor
	// wiring is purely additive: command stays the same, only env + volume
	// are new.
	cmd := runner["command"].([]any)
	if len(cmd) != 2 || cmd[0] != "bash" || cmd[1] != "/opt/tank/agent-runner-launch.sh" {
		t.Fatalf("agent-runner command = %v, want [bash /opt/tank/agent-runner-launch.sh]", cmd)
	}
}

func TestPodManifestSlotModeAttachesCodexRunnerHotSwap(t *testing.T) {
	manifest := PodManifest("63", "user@example.com", CodexGUIMode, ManifestOptions{
		SessionImage:       "claude-image",
		CodexSessionImage:  "codex-image",
		HotSwapAgentRunner: true,
	})

	spec := manifest["spec"].(map[string]any)
	volumes := spec["volumes"].([]any)
	assertVolume(t, volumes, "codex-runner-hot")

	containers := spec["containers"].([]any)
	runner := findContainer(t, containers, "codex-runner")
	assertVolumeMount(t, runner, "codex-runner-hot")

	mounts := runner["volumeMounts"].([]any)
	var hotMountPath string
	for _, m := range mounts {
		mm := m.(map[string]any)
		if mm["name"] == "codex-runner-hot" {
			hotMountPath, _ = mm["mountPath"].(string)
		}
	}
	if hotMountPath != "/var/run/codex-runner-hot" {
		t.Fatalf("codex-runner-hot mountPath = %q, want /var/run/codex-runner-hot", hotMountPath)
	}

	env := containerEnv(runner)
	if got, want := env["GLIMMUNG_SUPERVISOR_CHILD"], "/app/codex-runner-launch-binary.sh"; got != want {
		t.Fatalf("GLIMMUNG_SUPERVISOR_CHILD = %v, want %q", got, want)
	}
	if got, want := env["GLIMMUNG_SUPERVISOR_HOT_ARTIFACT"], "/var/run/codex-runner-hot/codex-runner-launch-binary.sh"; got != want {
		t.Fatalf("GLIMMUNG_SUPERVISOR_HOT_ARTIFACT = %v, want %q", got, want)
	}
	if got, want := env["GLIMMUNG_SUPERVISOR_RESTART_ENABLED"], "true"; got != want {
		t.Fatalf("GLIMMUNG_SUPERVISOR_RESTART_ENABLED = %v, want %q", got, want)
	}

	cmd := runner["command"].([]any)
	if len(cmd) != 2 || cmd[0] != "bash" || cmd[1] != "/opt/tank/codex-runner-launch.sh" {
		t.Fatalf("codex-runner command = %v, want [bash /opt/tank/codex-runner-launch.sh]", cmd)
	}
}

// TestPodManifestProdLeavesAgentRunnerUnchanged pins Checkbox 2 of
// scripts/check-session-pod-hot-swap-migration.mjs: with testEnv disabled
// (HotSwapAgentRunner=false, the default), the agent-runner container has
// NO agent-runner-hot volume, NO volumeMount, and NO supervisor env vars.
// Production sessions are byte-identical to pre-PR behavior.
func TestPodManifestProdLeavesAgentRunnerUnchanged(t *testing.T) {
	manifest := PodManifest("63", "user@example.com", ClaudeGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
		// HotSwapAgentRunner intentionally left false.
	})

	spec := manifest["spec"].(map[string]any)
	volumes := spec["volumes"].([]any)
	assertNoVolume(t, volumes, "agent-runner-hot")

	containers := spec["containers"].([]any)
	runner := findContainer(t, containers, "agent-runner")
	assertNoVolumeMount(t, runner, "agent-runner-hot")

	env := containerEnv(runner)
	for _, name := range []string{
		"GLIMMUNG_SUPERVISOR_CHILD",
		"GLIMMUNG_SUPERVISOR_HOT_ARTIFACT",
		"GLIMMUNG_SUPERVISOR_RESTART_ENABLED",
	} {
		if _, present := env[name]; present {
			t.Fatalf("env %s leaked into prod (HotSwapAgentRunner=false); value=%v", name, env[name])
		}
	}
}

func TestPodManifestProdLeavesCodexRunnerUnchanged(t *testing.T) {
	manifest := PodManifest("63", "user@example.com", CodexGUIMode, ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
	})

	spec := manifest["spec"].(map[string]any)
	volumes := spec["volumes"].([]any)
	assertNoVolume(t, volumes, "codex-runner-hot")

	containers := spec["containers"].([]any)
	runner := findContainer(t, containers, "codex-runner")
	assertNoVolumeMount(t, runner, "codex-runner-hot")

	env := containerEnv(runner)
	for _, name := range []string{
		"GLIMMUNG_SUPERVISOR_CHILD",
		"GLIMMUNG_SUPERVISOR_HOT_ARTIFACT",
		"GLIMMUNG_SUPERVISOR_RESTART_ENABLED",
	} {
		if _, present := env[name]; present {
			t.Fatalf("env %s leaked into prod (HotSwapAgentRunner=false); value=%v", name, env[name])
		}
	}
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

func assertNoHostAliases(t *testing.T, spec map[string]any) {
	t.Helper()
	if aliases, present := spec["hostAliases"]; present {
		t.Fatalf("hostAliases should not be present: %v", aliases)
	}
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

func assertSecretVolume(t *testing.T, volumes []any, name, secretName string) {
	t.Helper()
	for _, item := range volumes {
		volume := item.(map[string]any)
		if volume["name"] != name {
			continue
		}
		secret := volume["secret"].(map[string]any)
		if got := secret["secretName"]; got != secretName {
			t.Fatalf("secret volume %q secretName = %v, want %q", name, got, secretName)
		}
		return
	}
	t.Fatalf("secret volume %q not found", name)
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

func assertConfigMapMountSubPath(t *testing.T, container map[string]any, mountPath, subPath string) {
	t.Helper()
	for _, item := range container["volumeMounts"].([]any) {
		mount := item.(map[string]any)
		if mount["mountPath"] == mountPath {
			if got := mount["subPath"]; got != subPath {
				t.Fatalf("mount %s subPath = %v, want %q", mountPath, got, subPath)
			}
			return
		}
	}
	t.Fatalf("mountPath %q not found", mountPath)
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
		if value, ok := env["value"]; ok {
			out[env["name"].(string)] = value
		} else {
			out[env["name"].(string)] = env["valueFrom"]
		}
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
