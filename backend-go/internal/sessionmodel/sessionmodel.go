// Package sessionmodel owns the shared session-pod data shape used by the
// orchestrator, the bus, the stores, and the session registry: mode
// constants, the SessionRecord row, manifest-option struct, normalize
// helpers, and the PodManifest builder. Originally landed during the
// Python → Go orchestrator rewrite (#373) under the name `compat`; the
// name was retired by docs/migration-policy.md (compat is a deletion
// target) once the package became the real shared model, not a Python
// shape-mirroring layer.
package sessionmodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	DefaultSessionMode    = ClaudeGUIMode
	MaxNameLength         = 80
	SessionsNamespace     = "tank-operator-sessions"
	SessionServiceAccount = "claude-session"
	SessionConfigMap      = "tank-session-config"
	SandboxAgentPort      = 2468
	// Per-container metrics ports inside session pods. The
	// k8s/templates/podmonitor-sessions.yaml PodMonitor scrapes each by
	// the named container port (mcp-auth-proxy: "metrics", runners:
	// "runner-metrics"). Numbers are documented in docs/observability.md;
	// changing them requires bumping both the values here and the
	// PodMonitor port-name references.
	MCPAuthProxyMetricsPort = 9990
	AgentRunnerMetricsPort  = 9095
	CodexRunnerMetricsPort  = 9096
	// No DefaultSessionImage constants. The Helm chart owns image tags
	// (k8s/values.yaml's session.* keys are bumped per-commit to
	// fingerprinted tags by .github/workflows/claude-container-build.yml),
	// passes them in via SESSION_IMAGE / CODEX_SESSION_IMAGE /
	// PI_SESSION_IMAGE env vars. A `:latest` fallback here would silently
	// pin every session pod to whichever stale image happened to carry
	// that tag — which is exactly what bricked claude_gui session creation
	// for the 15h between the Go cutover and the env-var wiring.
	DefaultGitHubAppSecret = "github-app-creds"
	DefaultOAuthGatewayCA  = "claude-oauth-ca"
	SessionConfigDirMount  = "/opt/tank/session-config"
)

var (
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

// sessionConfigMounts is the canonical list of files mounted into every
// session pod from the tank-session-config ConfigMap. Adding to this list
// surfaces a new in-pod file at the listed mountPath.
var sessionConfigMounts = []struct{ key, mountPath string }{
	{"mcp.json", "/workspace/.mcp.json"},
	{"default-claude.md", "/workspace/CLAUDE.md"},
	{"default-claude.md", "/workspace/AGENTS.md"},
	{"write-glimmung-context.sh", "/opt/tank/write-glimmung-context.sh"},
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

type ManifestOptions struct {
	SessionImage            string
	CodexSessionImage       string
	PiSessionImage          string
	SessionsNamespace       string
	SessionScope            string
	SessionServiceAccount   string
	SessionConfigMap        string
	ArgoCDTrackingApp       string
	SandboxAgentPort        int
	TankOperatorInternalURL string
	// Optional: in-cluster Service IPs for host alias injection.
	OAuthGatewayIP  string
	APIProxyIP      string
	CodexAPIProxyIP string
	// ConfigMap name for the OAuth gateway CA cert.
	OAuthGatewayCAConfigMap string
	// Secret name for GitHub App credentials (envFrom on claude container).
	GitHubAppSecret string
	// SDK runners use NATS JetStream for durable command/event delivery.
	NATSURL        string
	NATSStream     string
	NATSAuthSecret string
	// GlimmungContext JSON-serialized dict (may be empty).
	GlimmungContextJSON string
}

func NormalizeSessionMode(mode string) string {
	if mode == "" {
		mode = DefaultSessionMode
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

func SessionStorageKey(scope, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "default" {
		return sessionID
	}
	return scope + ":" + sessionID
}

func SessionDocID(scope, sessionID string) string {
	return "session:" + SessionStorageKey(scope, sessionID)
}

func SessionCounterDocID(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "default" {
		return "session-counter"
	}
	return "session-counter:" + scope
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
	storageKey := SessionStorageKey(opts.SessionScope, sessionID)
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
		map[string]any{
			"name": "tank-operator-sa-token",
			"projected": map[string]any{
				"sources": []any{
					map[string]any{
						"serviceAccountToken": map[string]any{
							"audience":          "tank-operator",
							"expirationSeconds": 3600,
							"path":              "token",
						},
					},
				},
			},
		},
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

	// Codex API proxy host alias and CA cert. Codex is a Rust binary, so
	// NODE_EXTRA_CA_CERTS is not enough; CODEX_CA_CERTIFICATE is the env var
	// the upstream client uses for reqwest and websocket TLS.
	if (mode == CodexCLIMode || mode == CodexGUIMode) && opts.CodexAPIProxyIP != "" {
		hostAliases = append(hostAliases, map[string]any{
			"ip":        opts.CodexAPIProxyIP,
			"hostnames": []any{"chatgpt.com"},
		})
		if opts.OAuthGatewayCAConfigMap != "" {
			env = append(env,
				map[string]any{"name": "NODE_EXTRA_CA_CERTS", "value": "/etc/oauth-gateway-ca/ca.crt"},
				map[string]any{"name": "CODEX_CA_CERTIFICATE", "value": "/etc/oauth-gateway-ca/ca.crt"},
			)
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

	// envFrom on the claude container. GitHub App is used for git auth.
	envFrom := []any{}
	if opts.GitHubAppSecret != "" {
		envFrom = append(envFrom, map[string]any{"secretRef": map[string]any{"name": opts.GitHubAppSecret}})
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

	mcpProxyVolumeMounts := append([]any{}, configMounts...)
	mcpProxyVolumeMounts = append(mcpProxyVolumeMounts, map[string]any{
		"name":      "tank-operator-sa-token",
		"mountPath": "/var/run/secrets/tank-operator",
		"readOnly":  true,
	})

	containers := []any{
		map[string]any{
			"name":            "mcp-auth-proxy",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"mcp-auth-proxy"},
			"env": []any{
				map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
				map[string]any{"name": "TANK_SESSION_ATTESTATION_TOKEN_PATH", "value": "/var/run/secrets/tank-operator/token"},
				map[string]any{"name": "MCP_AUTH_PROXY_METRICS_PORT", "value": itoa(MCPAuthProxyMetricsPort)},
			},
			// The metrics port is exposed as a named container port so the
			// k8s/templates/podmonitor-sessions.yaml PodMonitor can scrape
			// it by name without hard-coding numbers. Listens on 0.0.0.0;
			// the proxy's MCP listeners stay on 127.0.0.1.
			"ports": []any{
				map[string]any{"name": "metrics", "containerPort": MCPAuthProxyMetricsPort},
			},
			"volumeMounts": mcpProxyVolumeMounts,
		},
		claudeContainer,
	}

	// SDK agent-runner sidecar - claude_gui only. Shares /workspace
	// with the claude container via the emptyDir above so the agent's
	// edits show up in the terminal pane. Same image (binary baked in
	// via the Dockerfile multi-stage build); different command + env.
	// The runner consumes durable session commands and publishes canonical
	// transcript events to the session bus.
	if wantAgentRunner {
		runnerVolumeMounts := append([]any{}, configMounts...)
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "workspace",
			"mountPath": "/workspace",
		})
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "tank-operator-sa-token",
			"mountPath": "/var/run/secrets/tank-operator",
			"readOnly":  true,
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
			map[string]any{"name": "TANK_SESSION_STORAGE_KEY", "value": storageKey},
			map[string]any{
				"name": "POD_OWNER_EMAIL",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.annotations['tank-operator/owner-email']",
					},
				},
			},
			map[string]any{"name": "NATS_URL", "value": opts.NATSURL},
			map[string]any{"name": "NATS_STREAM", "value": opts.NATSStream},
			map[string]any{
				"name": "NATS_TOKEN",
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{
						"name": opts.NATSAuthSecret,
						"key":  "token",
					},
				},
			},
			map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
			map[string]any{"name": "TANK_OPERATOR_TOKEN_PATH", "value": "/var/run/secrets/tank-operator/token"},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
			map[string]any{"name": "MCP_CONFIG", "value": "/workspace/.mcp.json"},
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

		runnerEnv = append(runnerEnv, map[string]any{
			"name": "TANK_RUNNER_METRICS_PORT", "value": itoa(AgentRunnerMetricsPort),
		})
		runnerContainer := map[string]any{
			"name":            "agent-runner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/agent-runner-launch.sh"},
			"env":             runnerEnv,
			"volumeMounts":    runnerVolumeMounts,
			"ports": []any{
				map[string]any{"name": "runner-metrics", "containerPort": AgentRunnerMetricsPort},
			},
		}
		if len(envFrom) > 0 {
			runnerContainer["envFrom"] = envFrom
		}
		containers = append(containers, runnerContainer)
	}

	// Codex-runner sidecar — codex_gui only. Sibling of agent-runner:
	// same workspace mount and same session-bus command/event contract
	// (only one runner per pod, never both). Different SDK underneath. Auth is
	// a pod-local placeholder auth.json plus codex-api-proxy injection;
	// the runner never mounts the real codex-credentials Secret.
	if wantCodexRunner {
		runnerVolumeMounts := append([]any{}, configMounts...)
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "workspace",
			"mountPath": "/workspace",
		})
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "tank-operator-sa-token",
			"mountPath": "/var/run/secrets/tank-operator",
			"readOnly":  true,
		})
		if opts.CodexAPIProxyIP != "" && opts.OAuthGatewayCAConfigMap != "" {
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name":      "oauth-gateway-ca",
				"mountPath": "/etc/oauth-gateway-ca",
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
			map[string]any{"name": "TANK_SESSION_STORAGE_KEY", "value": storageKey},
			map[string]any{
				"name": "POD_OWNER_EMAIL",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.annotations['tank-operator/owner-email']",
					},
				},
			},
			map[string]any{"name": "NATS_URL", "value": opts.NATSURL},
			map[string]any{"name": "NATS_STREAM", "value": opts.NATSStream},
			map[string]any{
				"name": "NATS_TOKEN",
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{
						"name": opts.NATSAuthSecret,
						"key":  "token",
					},
				},
			},
			map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
			map[string]any{"name": "TANK_OPERATOR_TOKEN_PATH", "value": "/var/run/secrets/tank-operator/token"},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
		}
		if opts.CodexAPIProxyIP != "" && opts.OAuthGatewayCAConfigMap != "" {
			codexRunnerEnv = append(codexRunnerEnv,
				map[string]any{"name": "NODE_EXTRA_CA_CERTS", "value": "/etc/oauth-gateway-ca/ca.crt"},
				map[string]any{"name": "CODEX_CA_CERTIFICATE", "value": "/etc/oauth-gateway-ca/ca.crt"},
			)
		}
		codexRunnerEnv = append(codexRunnerEnv, map[string]any{
			"name": "TANK_RUNNER_METRICS_PORT", "value": itoa(CodexRunnerMetricsPort),
		})
		codexRunnerContainer := map[string]any{
			"name":            "codex-runner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/codex-runner-launch.sh"},
			"env":             codexRunnerEnv,
			"volumeMounts":    runnerVolumeMounts,
			"ports": []any{
				map[string]any{"name": "runner-metrics", "containerPort": CodexRunnerMetricsPort},
			},
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
				"tank-operator/session-scope":  opts.SessionScope,
				"tank-operator/mode":           mode,
			},
			"annotations": annotations,
		},
		"spec": spec,
	}
}

// buildConfigMounts returns the volumeMount entries for the session ConfigMap.
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
	if opts.SessionScope == "" {
		opts.SessionScope = "default"
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
	if opts.TankOperatorInternalURL == "" {
		opts.TankOperatorInternalURL = "http://tank-operator.tank-operator.svc.cluster.local"
	}
	if opts.OAuthGatewayCAConfigMap == "" {
		opts.OAuthGatewayCAConfigMap = DefaultOAuthGatewayCA
	}
	if opts.GitHubAppSecret == "" {
		opts.GitHubAppSecret = DefaultGitHubAppSecret
	}
	if opts.NATSStream == "" {
		opts.NATSStream = "TANK_SESSION_BUS"
	}
	if opts.NATSAuthSecret == "" {
		opts.NATSAuthSecret = "tank-nats-auth"
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
