package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

var jsonUnmarshal = json.Unmarshal

const (
	APIKeyMode            = "api_key"
	ClaudeCLIMode         = "claude_cli"
	ClaudeGUIMode         = "claude_gui"
	ConfigMode            = "config"
	CodexConfigMode       = "codex_config"
	CodexCLIMode          = "codex_cli"
	CodexGUIMode          = "codex_gui"
	PiConfigMode          = "pi_config"
	PiCLIMode             = "pi_cli"
	DefaultSessionMode    = ClaudeCLIMode
	MaxNameLength         = 80
	SessionsNamespace     = "tank-operator-sessions"
	SessionServiceAccount = "claude-session"
	SessionConfigMap      = "tank-session-config"
	SandboxAgentPort      = 2468
	AgentRunnerWSPort     = 8090
	// No DefaultSessionImage constants. The Helm chart owns image tags
	// (k8s/values.yaml's session.* keys are bumped per-commit to
	// fingerprinted tags by .github/workflows/claude-container-build.yml),
	// passes them in via SESSION_IMAGE / CODEX_SESSION_IMAGE /
	// PI_SESSION_IMAGE env vars. A `:latest` fallback here would silently
	// pin every session pod to whichever stale image happened to carry
	// that tag — which is exactly what bricked claude_gui session creation
	// for the 15h between the Go cutover and the env-var wiring.
	DefaultGitHubAppSecret          = "github-app-creds"
	DefaultCodexCredsSecret         = "codex-credentials"
	DefaultOAuthGatewayCA           = "claude-oauth-ca"
	DefaultSessionAzureConfigSecret = "tank-session-azure-config"
	SessionConfigDirMount           = "/opt/tank/session-config"
)

var (
	sessionModeAliases = map[string]string{
		"subscription":          ClaudeCLIMode,
		"subscription_headless": ClaudeGUIMode,
		"codex_subscription":    CodexCLIMode,
		"codex_headless":        CodexGUIMode,
		"pi_subscription":       PiCLIMode,
	}
	sessionModes = map[string]struct{}{
		APIKeyMode:      {},
		ClaudeCLIMode:   {},
		ClaudeGUIMode:   {},
		ConfigMode:      {},
		CodexConfigMode: {},
		CodexCLIMode:    {},
		CodexGUIMode:    {},
		PiConfigMode:    {},
		PiCLIMode:       {},
	}
	runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
)

type SessionRecord struct {
	ID          string
	Email       string
	Mode        string
	Scope       string
	PodName     string
	Name        *string
	Visible     bool
	RequestedAt string
	CreatedAt   string
	UpdatedAt   string
}

type ActiveRunRecord struct {
	SessionID   string
	Email       string
	RunID       string
	PodName     string
	Provider    string
	Status      string
	StreamPath  string
	PIDPath     string
	StartedAt   string
	UpdatedAt   string
	CompletedAt *string
}

// sessionConfigMounts mirrors Python's SESSION_CONFIG_MOUNTS + the dir mount.
var sessionConfigMounts = []struct{ key, mountPath string }{
	{"mcp.json", "/workspace/.mcp.json"},
	{"default-claude.md", "/workspace/CLAUDE.md"},
	{"default-claude.md", "/workspace/AGENTS.md"},
	{"write-glimmung-context.sh", "/opt/tank/write-glimmung-context.sh"},
	{"tank-bootstrap.sh", "/opt/tank/bootstrap.sh"},
	{"headless-run.sh", "/opt/tank/headless-run.sh"},
	{"agent-runner-launch.sh", "/opt/tank/agent-runner-launch.sh"},
	{"codex-runner-launch.sh", "/opt/tank/codex-runner-launch.sh"},
}

// noClaudeHijackModes are modes that should not receive the OAuth gateway / api proxy host aliases.
var noClaudeHijackModes = map[string]bool{
	ConfigMode:      true,
	CodexConfigMode: true,
	CodexCLIMode:    true,
	CodexGUIMode:    true,
	PiConfigMode:    true,
}

// codexModes need the codex-credentials secret mount.
var codexModes = map[string]bool{
	CodexConfigMode: false, // codex_config harvests; no mount
	CodexCLIMode:    true,
	CodexGUIMode:    true,
}

// piCLIMode also mounts codex creds (for Pi auth translation).
const piCLIMode = PiCLIMode

type ManifestOptions struct {
	SessionImage          string
	CodexSessionImage     string
	PiSessionImage        string
	SessionsNamespace     string
	SessionServiceAccount string
	SessionConfigMap      string
	ArgoCDTrackingApp     string
	SandboxAgentPort      int
	// Optional: in-cluster Service IPs for host alias injection.
	OAuthGatewayIP string
	APIProxyIP     string
	// ConfigMap name for the OAuth gateway CA cert.
	OAuthGatewayCAConfigMap string
	// Secret name for codex credentials.
	CodexCredsSecret string
	// Secret name for GitHub App credentials (envFrom on claude container).
	GitHubAppSecret string
	// Secret name for pod-side Azure workload-identity config
	// (AZURE_CLIENT_ID + AZURE_TENANT_ID). envFrom on the claude container
	// so SDK runners can talk to Cosmos via federated SA token. May be empty
	// in test envs where the ExternalSecret isn't wired.
	SessionAzureConfigSecret string
	// SDK runners need Cosmos config for session-events and the turn queue.
	// AgentRunnerWSPort is the localhost port the runner's WebSocket listens
	// on; the orchestrator reverse-proxies /agent-ws onto it.
	CosmosEndpoint               string
	CosmosDatabase               string
	CosmosSessionEventsContainer string
	CosmosTurnQueueContainer     string
	AgentRunnerWSPort            int
	// GlimmungContext JSON-serialized dict (may be empty).
	GlimmungContextJSON string
}

func NormalizeSessionMode(mode string) string {
	if mode == "" {
		mode = DefaultSessionMode
	}
	if canonical, ok := sessionModeAliases[mode]; ok {
		return canonical
	}
	return mode
}

func IsSessionMode(mode string) bool {
	_, ok := sessionModes[NormalizeSessionMode(mode)]
	return ok
}

func OwnerLabel(email string) string {
	sum := sha256.Sum256([]byte(email))
	return "u-" + hex.EncodeToString(sum[:])[:16]
}

func NormalizeName(name *string) *string {
	if name == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*name)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > MaxNameLength {
		trimmed = trimmed[:MaxNameLength]
	}
	return &trimmed
}

func ValidateRunID(value string) bool {
	return runIDPattern.MatchString(value)
}

func RunStreamPath(runID string) string {
	return "/tmp/tank-run-" + runID + ".stream"
}

func RunPIDPath(runID string) string {
	return "/tmp/tank-run-" + runID + ".pid"
}

func SessionDocID(scope, sessionID string) string {
	if scope == "" || scope == "default" {
		return "session:" + sessionID
	}
	return "session:" + scope + ":" + sessionID
}

func SessionCounterDocID(scope string) string {
	if scope == "" || scope == "default" {
		return "session-counter"
	}
	return "session-counter:" + scope
}

func ActiveRunDoc(record ActiveRunRecord) map[string]any {
	status := record.Status
	if status == "" {
		status = "running"
	}
	return map[string]any{
		"id":           record.SessionID,
		"type":         "active_run",
		"session_id":   record.SessionID,
		"email":        record.Email,
		"run_id":       record.RunID,
		"pod_name":     record.PodName,
		"provider":     record.Provider,
		"status":       status,
		"stream_path":  record.StreamPath,
		"pid_path":     record.PIDPath,
		"started_at":   record.StartedAt,
		"updated_at":   record.UpdatedAt,
		"completed_at": record.CompletedAt,
	}
}

func SessionDoc(record SessionRecord) map[string]any {
	scope := record.Scope
	if scope == "" {
		scope = "default"
	}
	return map[string]any{
		"id":            SessionDocID(scope, record.ID),
		"type":          "session",
		"email":         record.Email,
		"session_scope": scope,
		"session_id":    record.ID,
		"mode":          record.Mode,
		"pod_name":      record.PodName,
		"name":          record.Name,
		"visible":       record.Visible,
		"requested_at":  record.RequestedAt,
		"created_at":    record.CreatedAt,
		"updated_at":    record.UpdatedAt,
	}
}

func PodManifest(sessionID, owner, mode string, opts ManifestOptions) map[string]any {
	opts = withManifestDefaults(opts)
	mode = NormalizeSessionMode(mode)
	podName := "session-" + sessionID
	argoTrackingID := opts.ArgoCDTrackingApp + ":/Pod:" + opts.SessionsNamespace + "/" + podName

	sessionImage := opts.SessionImage
	if mode == CodexConfigMode || mode == CodexCLIMode || mode == CodexGUIMode {
		sessionImage = opts.CodexSessionImage
	}
	if mode == PiConfigMode || mode == PiCLIMode {
		sessionImage = opts.PiSessionImage
	}

	// Build configmap volume mounts for both containers.
	configMounts := buildConfigMounts(opts.SessionConfigMap)

	// Environment variables for the claude container.
	env := []any{
		map[string]any{"name": "SANDBOX_AGENT_PORT", "value": itoa(opts.SandboxAgentPort)},
		map[string]any{"name": "TANK_SESSION_MODE", "value": mode},
		map[string]any{"name": "TANK_GLIMMUNG_CONTEXT_JSON", "value": opts.GlimmungContextJSON},
		map[string]any{"name": "TANK_GLIMMUNG_RUN_REF", "value": glimmungField(opts.GlimmungContextJSON, "glimmung_run_ref")},
		map[string]any{"name": "TANK_GLIMMUNG_ISSUE_REF", "value": glimmungField(opts.GlimmungContextJSON, "glimmung_issue_ref")},
		map[string]any{"name": "TANK_GLIMMUNG_TOUCHPOINT_REF", "value": glimmungField(opts.GlimmungContextJSON, "glimmung_touchpoint_ref")},
		map[string]any{"name": "TANK_GLIMMUNG_VALIDATION_URL", "value": glimmungField(opts.GlimmungContextJSON, "validation_url")},
		map[string]any{"name": "FORCE_HYPERLINK", "value": "1"},
		map[string]any{"name": "CLAUDE_CODE_NO_FLICKER", "value": "1"},
	}

	claudeVolumeMounts := append([]any{}, configMounts...)
	volumes := []any{
		map[string]any{"name": "session-config", "configMap": map[string]any{"name": opts.SessionConfigMap}},
	}

	// Shared /workspace across the user-facing container and the
	// SDK runner sidecar. Without this, the agent's writes wouldn't be
	// visible in the in-browser terminal and vice versa. emptyDir lives
	// for the pod's lifetime, matching today's "pod restart loses
	// workspace state" semantics. claude_gui uses agent-runner,
	// codex_gui uses codex-runner; both need the shared mount.
	wantAgentRunner := mode == ClaudeGUIMode
	wantCodexRunner := mode == CodexGUIMode
	wantSDKRunner := wantAgentRunner || wantCodexRunner
	if wantSDKRunner {
		volumes = append(volumes, map[string]any{
			"name":     "workspace",
			"emptyDir": map[string]any{},
		})
		claudeVolumeMounts = append(claudeVolumeMounts, map[string]any{
			"name":      "workspace",
			"mountPath": "/workspace",
		})
	}

	// OAuth gateway + API proxy host aliases and CA cert.
	var hostAliases []any
	if !noClaudeHijackModes[mode] && (opts.OAuthGatewayIP != "" || opts.APIProxyIP != "") {
		if mode != PiCLIMode && opts.OAuthGatewayIP != "" {
			hostAliases = append(hostAliases, map[string]any{
				"ip":        opts.OAuthGatewayIP,
				"hostnames": []any{"platform.claude.com"},
			})
		}
		if opts.APIProxyIP != "" {
			hostAliases = append(hostAliases, map[string]any{
				"ip":        opts.APIProxyIP,
				"hostnames": []any{"api.anthropic.com"},
			})
		}
		if opts.OAuthGatewayCAConfigMap != "" {
			env = append(env, map[string]any{"name": "NODE_EXTRA_CA_CERTS", "value": "/etc/oauth-gateway-ca/ca.crt"})
			claudeVolumeMounts = append(claudeVolumeMounts, map[string]any{
				"name":      "oauth-gateway-ca",
				"mountPath": "/etc/oauth-gateway-ca",
				"readOnly":  true,
			})
			volumes = append(volumes, map[string]any{
				"name":      "oauth-gateway-ca",
				"configMap": map[string]any{"name": opts.OAuthGatewayCAConfigMap},
			})
		}
	}

	// Codex credentials secret mount (codex_cli, codex_gui, pi_cli).
	if (mode == CodexCLIMode || mode == CodexGUIMode || mode == PiCLIMode) && opts.CodexCredsSecret != "" {
		claudeVolumeMounts = append(claudeVolumeMounts, map[string]any{
			"name":      "codex-creds",
			"mountPath": "/etc/codex-creds",
			"readOnly":  true,
		})
		volumes = append(volumes, map[string]any{
			"name":   "codex-creds",
			"secret": map[string]any{"secretName": opts.CodexCredsSecret, "optional": true},
		})
	}

	// envFrom on the claude container. GitHub App for git auth; session-azure
	// config provides AZURE_CLIENT_ID + AZURE_TENANT_ID so DefaultAzureCredential
	// can mint a federated Cosmos token from the projected SA token (the WI
	// webhook injects AZURE_FEDERATED_TOKEN_FILE via the pod's
	// azure.workload.identity/use=true label).
	envFrom := []any{}
	if opts.GitHubAppSecret != "" {
		envFrom = append(envFrom, map[string]any{"secretRef": map[string]any{"name": opts.GitHubAppSecret}})
	}
	if opts.SessionAzureConfigSecret != "" {
		envFrom = append(envFrom, map[string]any{"secretRef": map[string]any{"name": opts.SessionAzureConfigSecret}})
	}

	claudeContainer := map[string]any{
		"name":            "claude",
		"image":           sessionImage,
		"imagePullPolicy": "Always",
		"command": []any{
			"bash", "-lc",
			"if command -v sandbox-agent >/dev/null 2>&1; then sandbox_agent_cmd=sandbox-agent; else sandbox_agent_cmd='npx -y @sandbox-agent/cli@0.4.2'; fi; exec $sandbox_agent_cmd server --host 0.0.0.0 --port " + itoa(opts.SandboxAgentPort) + " --no-token --no-telemetry",
		},
		"ports":        []any{map[string]any{"name": "sandbox-agent", "containerPort": opts.SandboxAgentPort}},
		"env":          env,
		"volumeMounts": claudeVolumeMounts,
	}
	if len(envFrom) > 0 {
		claudeContainer["envFrom"] = envFrom
	}

	containers := []any{
		map[string]any{
			"name":            "mcp-auth-proxy",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"mcp-auth-proxy"},
			"volumeMounts":    configMounts,
		},
		claudeContainer,
	}

	// SDK agent-runner sidecar - claude_gui only. Shares /workspace
	// with the claude container via the emptyDir above so the agent's
	// edits show up in the terminal pane. Same image (binary baked in
	// via the Dockerfile multi-stage build); different command + env.
	// The runner claims durable turn-queue rows and serves live events
	// through the WebSocket port.
	if wantAgentRunner {
		runnerVolumeMounts := append([]any{}, configMounts...)
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "workspace",
			"mountPath": "/workspace",
		})
		runnerEnv := []any{
			map[string]any{
				"name": "SESSION_ID",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.labels['tank-operator/session-id']",
					},
				},
			},
			map[string]any{
				"name": "POD_OWNER_EMAIL",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.annotations['tank-operator/owner-email']",
					},
				},
			},
			map[string]any{"name": "COSMOS_ENDPOINT", "value": opts.CosmosEndpoint},
			map[string]any{"name": "COSMOS_DATABASE", "value": opts.CosmosDatabase},
			map[string]any{"name": "COSMOS_SESSION_EVENTS_CONTAINER", "value": opts.CosmosSessionEventsContainer},
			map[string]any{"name": "COSMOS_TURN_QUEUE_CONTAINER", "value": opts.CosmosTurnQueueContainer},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
			map[string]any{"name": "MCP_CONFIG", "value": "/workspace/.mcp.json"},
			map[string]any{"name": "AGENT_RUNNER_WS_PORT", "value": itoa(opts.AgentRunnerWSPort)},
		}
		// NODE_EXTRA_CA_CERTS — same gateway-CA injection the claude
		// container gets, so the SDK's spawned claude binary trusts the
		// OAuth gateway's self-signed cert.
		if !noClaudeHijackModes[mode] && opts.OAuthGatewayCAConfigMap != "" {
			runnerEnv = append(runnerEnv,
				map[string]any{"name": "NODE_EXTRA_CA_CERTS", "value": "/etc/oauth-gateway-ca/ca.crt"},
			)
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name":      "oauth-gateway-ca",
				"mountPath": "/etc/oauth-gateway-ca",
				"readOnly":  true,
			})
		}

		runnerContainer := map[string]any{
			"name":            "agent-runner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/agent-runner-launch.sh"},
			"ports": []any{map[string]any{
				"name":          "agent-ws",
				"containerPort": opts.AgentRunnerWSPort,
			}},
			"env":          runnerEnv,
			"volumeMounts": runnerVolumeMounts,
		}
		if len(envFrom) > 0 {
			runnerContainer["envFrom"] = envFrom
		}
		containers = append(containers, runnerContainer)
	}

	// Codex-runner sidecar — codex_gui only. Sibling of agent-runner:
	// same workspace mount, same Cosmos endpoint, same WS port (only
	// one runner per pod, never both). Different SDK underneath, plus
	// the codex-credentials secret mount so the SDK-spawned codex CLI
	// can read auth.json.
	if wantCodexRunner {
		runnerVolumeMounts := append([]any{}, configMounts...)
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "workspace",
			"mountPath": "/workspace",
		})
		if opts.CodexCredsSecret != "" {
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name":      "codex-creds",
				"mountPath": "/etc/codex-creds",
				"readOnly":  true,
			})
		}
		codexRunnerEnv := []any{
			map[string]any{
				"name": "SESSION_ID",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.labels['tank-operator/session-id']",
					},
				},
			},
			map[string]any{
				"name": "POD_OWNER_EMAIL",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.annotations['tank-operator/owner-email']",
					},
				},
			},
			map[string]any{"name": "COSMOS_ENDPOINT", "value": opts.CosmosEndpoint},
			map[string]any{"name": "COSMOS_DATABASE", "value": opts.CosmosDatabase},
			map[string]any{"name": "COSMOS_SESSION_EVENTS_CONTAINER", "value": opts.CosmosSessionEventsContainer},
			map[string]any{"name": "COSMOS_TURN_QUEUE_CONTAINER", "value": opts.CosmosTurnQueueContainer},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
			map[string]any{"name": "AGENT_RUNNER_WS_PORT", "value": itoa(opts.AgentRunnerWSPort)},
		}
		codexRunnerContainer := map[string]any{
			"name":            "codex-runner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/codex-runner-launch.sh"},
			"ports": []any{map[string]any{
				"name":          "agent-ws",
				"containerPort": opts.AgentRunnerWSPort,
			}},
			"env":          codexRunnerEnv,
			"volumeMounts": runnerVolumeMounts,
		}
		if len(envFrom) > 0 {
			codexRunnerContainer["envFrom"] = envFrom
		}
		containers = append(containers, codexRunnerContainer)
	}

	spec := map[string]any{
		"serviceAccountName": opts.SessionServiceAccount,
		"securityContext": map[string]any{
			"runAsNonRoot": true,
			"runAsUser":    1000,
			"runAsGroup":   1000,
			"fsGroup":      1000,
		},
		"containers": containers,
		"volumes":    volumes,
	}
	if len(hostAliases) > 0 {
		spec["hostAliases"] = hostAliases
	}

	annotations := map[string]any{
		"tank-operator/owner-email":      owner,
		"argocd.argoproj.io/tracking-id": argoTrackingID,
	}
	if opts.GlimmungContextJSON != "" {
		annotations["tank-operator/glimmung-context"] = opts.GlimmungContextJSON
	}

	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      podName,
			"namespace": opts.SessionsNamespace,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "tank-operator",
				"app.kubernetes.io/instance":   opts.ArgoCDTrackingApp,
				"tank-operator/owner":          OwnerLabel(owner),
				"tank-operator/session-id":     sessionID,
				"tank-operator/mode":           mode,
				"azure.workload.identity/use":  "true",
			},
			"annotations": annotations,
		},
		"spec": spec,
	}
}

// buildConfigMounts returns the volumeMount entries for the session ConfigMap,
// matching Python's _session_config_mounts().
func buildConfigMounts(configMapName string) []any {
	_ = configMapName // name is in the volume declaration, not the mount
	mounts := make([]any, 0, len(sessionConfigMounts)+1)
	for _, m := range sessionConfigMounts {
		mounts = append(mounts, map[string]any{
			"name":      "session-config",
			"mountPath": m.mountPath,
			"subPath":   m.key,
			"readOnly":  true,
		})
	}
	mounts = append(mounts, map[string]any{
		"name":      "session-config",
		"mountPath": SessionConfigDirMount,
		"readOnly":  true,
	})
	return mounts
}

// glimmungField extracts a string field from a compact JSON object string.
func glimmungField(contextJSON, field string) string {
	if contextJSON == "" {
		return ""
	}
	var m map[string]any
	if err := jsonUnmarshal([]byte(contextJSON), &m); err != nil {
		return ""
	}
	v, _ := m[field].(string)
	return v
}

func withManifestDefaults(opts ManifestOptions) ManifestOptions {
	// Session images are caller-required. The chart owns the
	// fingerprinted tag; the orchestrator's job is to plumb it through,
	// not to invent a fallback. See main.go for the startup-time check
	// that fails the pod loudly when the env vars are missing.
	if opts.SessionsNamespace == "" {
		opts.SessionsNamespace = SessionsNamespace
	}
	if opts.SessionServiceAccount == "" {
		opts.SessionServiceAccount = SessionServiceAccount
	}
	if opts.SessionConfigMap == "" {
		opts.SessionConfigMap = SessionConfigMap
	}
	if opts.ArgoCDTrackingApp == "" {
		opts.ArgoCDTrackingApp = "tank-operator-sessions"
	}
	if opts.SandboxAgentPort == 0 {
		opts.SandboxAgentPort = SandboxAgentPort
	}
	if opts.OAuthGatewayCAConfigMap == "" {
		opts.OAuthGatewayCAConfigMap = DefaultOAuthGatewayCA
	}
	if opts.CodexCredsSecret == "" {
		opts.CodexCredsSecret = DefaultCodexCredsSecret
	}
	if opts.GitHubAppSecret == "" {
		opts.GitHubAppSecret = DefaultGitHubAppSecret
	}
	if opts.SessionAzureConfigSecret == "" {
		opts.SessionAzureConfigSecret = DefaultSessionAzureConfigSecret
	}
	if opts.CosmosDatabase == "" {
		opts.CosmosDatabase = "tank-operator"
	}
	if opts.CosmosSessionEventsContainer == "" {
		opts.CosmosSessionEventsContainer = "session-events"
	}
	if opts.CosmosTurnQueueContainer == "" {
		opts.CosmosTurnQueueContainer = "turn-queue"
	}
	if opts.AgentRunnerWSPort == 0 {
		opts.AgentRunnerWSPort = AgentRunnerWSPort
	}
	return opts
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
