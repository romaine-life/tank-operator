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
	"errors"
	"strings"
	"unicode"
)

var jsonUnmarshal = json.Unmarshal

const (
	APIKeyMode         = "api_key"
	ClaudeCLIMode      = "claude_cli"
	ClaudeGUIMode      = "claude_gui"
	ConfigMode         = "config"
	CodexConfigMode    = "codex_config"
	CodexCLIMode       = "codex_cli"
	CodexGUIMode       = "codex_gui"
	CodexExecGUIMode   = "codex_exec_gui"
	CodexAppServerMode = "codex_app_server"
	// AntigravityConfigMode is the credential-mint terminal mode for
	// Antigravity (Gemini-Ultra via Google's Antigravity CLI). The user runs
	// `agy` in the terminal, completes the Google/Ultra login, and the
	// save-credentials harvest writes the resulting OAuth token file to KV.
	// Runs on the glibc antigravity-container image (agy + sandbox-agent).
	AntigravityConfigMode = "antigravity_config"
	// AntigravityGUIMode is the GUI chat mode for Antigravity (Gemini-Ultra).
	// A pod-side antigravity-runner drives agy and maps its structured
	// transcript stream onto the Tank conversation protocol. Runs on the glibc
	// antigravity-container image, mounts the harvested OAuth credential from
	// KV, and carries the mcp-auth-proxy sidecar like the other GUI modes.
	AntigravityGUIMode    = "antigravity_gui"
	DefaultSessionMode    = ClaudeGUIMode
	MaxNameLength         = 80
	SessionsNamespace     = "tank-operator-sessions"
	SessionServiceAccount = "claude-session"
	SessionConfigMap      = "tank-session-config"
	SandboxAgentPort      = 2468
	// SessionCapabilitySpireLensMCP opts a pod into the SpireLens game-host
	// MCP path. The default session surface stays cluster-local; this rare
	// capability joins the tailnet and mounts an MCP config with
	// spire-lens-mcp on localhost :9997.
	SessionCapabilitySpireLensMCP = "spirelens_mcp"
	DefaultSpireLensMCPPort       = 15527
	DefaultSpireLensTailscaleTag  = "tag:spirelens-orchestrator"
	// Per-container metrics ports inside session pods. The
	// k8s/templates/podmonitor-sessions.yaml PodMonitor scrapes each by
	// the named container port (mcp-auth-proxy: "metrics", runners:
	// "runner-metrics"). Numbers are documented in docs/observability.md;
	// changing them requires bumping both the values here and the
	// PodMonitor port-name references.
	MCPAuthProxyMetricsPort      = 9990
	AgentRunnerMetricsPort       = 9095
	CodexRunnerMetricsPort       = 9096
	AntigravityRunnerMetricsPort = 9097
	// No DefaultSessionImage constants. The Helm chart owns image tags
	// (k8s/values.yaml's session.* keys are bumped per-commit to
	// fingerprinted tags by .github/workflows/claude-container-build.yml),
	// passes them in via SESSION_IMAGE / CODEX_SESSION_IMAGE env vars.
	// A `:latest` fallback here would silently
	// pin every session pod to whichever stale image happened to carry
	// that tag — which is exactly what bricked claude_gui session creation
	// for the 15h between the Go cutover and the env-var wiring.
	DefaultOAuthGatewayCA = "claude-oauth-ca"
	SessionConfigDirMount = "/opt/tank/session-config"
)

var (
	ErrSessionOrderConflict = errors.New("session order conflict")

	sessionModes = map[string]struct{}{
		APIKeyMode:            {},
		ClaudeCLIMode:         {},
		ClaudeGUIMode:         {},
		ConfigMode:            {},
		CodexConfigMode:       {},
		CodexCLIMode:          {},
		CodexGUIMode:          {},
		CodexExecGUIMode:      {},
		CodexAppServerMode:    {},
		AntigravityConfigMode: {},
		AntigravityGUIMode:    {},
	}

	sessionCapabilities = map[string]struct{}{
		SessionCapabilitySpireLensMCP: {},
	}
)

type SessionRecord struct {
	ID      string
	Email   string
	Mode    string
	Scope   string
	PodName string
	// Name is the session's human-facing title. It is always present
	// (NON-NULL): Manager.Create assigns the canonical SessionDisplayName
	// default when the user supplies none, and SetName reassigns that
	// default when cleared. The durable sessions.name column is NOT NULL.
	Name        string
	Visible     bool
	RequestedAt string
	CreatedAt   string
	UpdatedAt   string

	// Sidebar-visible columns added in
	// docs/session-list-redesign.md Phase 1 dual-write and consumed
	// directly by Reader.List as of Phase 2's snapshot cutover. Status
	// is non-empty in steady state ('Pending' / 'Active' / 'Failed');
	// the other fields are pointers because they're optional (no
	// ready_at until the pod first reached Ready, etc.).
	Status          string
	ReadyAt         string
	TerminatingAt   string
	ActivitySummary []byte         // JSON-marshaled; nil when no chat activity yet
	TestState       map[string]any // jsonb column, materialized for the handler layer
	RolloutState    map[string]any // jsonb column

	// Repos is the list of "owner/name" slugs selected at session
	// creation. Empty slice is the steady-state "no auto-cloning"
	// shape; a non-empty slice is read by the repo-cloner init
	// container at pod boot. The slugs are
	// validated at the handler boundary (handlers_sessions.go) and
	// the durable column is the source of truth for the picker chips
	// on existing sessions; the SPA never re-derives this from
	// localStorage.
	Repos []string
	// CloneState is the per-repo init-container outcome, keyed by
	// slug, value {status, error?, started_at?, finished_at?}. nil
	// until the init container writes back the first state.
	CloneState map[string]any
	// Capabilities is the durable per-session opt-in list. Empty means
	// the default pod surface. Values are normalized through
	// NormalizeSessionCapabilities before Manager.Create writes the row.
	Capabilities []string
	// BugLabel is the most recent Tank-native bug bucket attached by the user.
	// BugLabels carries the full set when a session is linked to multiple bug
	// buckets. BugLabel remains populated for older clients that read one label.
	BugLabel  *SessionBugLabel
	BugLabels []*SessionBugLabel

	// Model/Effort are the session-owned run configuration accepted at
	// create time. Runners consume these values through submit_turn
	// commands rather than trusting per-turn browser state.
	Model  string
	Effort string

	// RuntimeModel/RuntimeEffort are written by the runner after it
	// hands options to the provider executable/SDK. This is the applied
	// configuration surface the UI renders in the composer footer.
	RuntimeModel                   string
	RuntimeEffort                  string
	RuntimeConfiguredAt            string
	RuntimeContextWindowTokens     int64
	RuntimeContextWindowSource     string
	RuntimeContextWindowObservedAt string
	ProviderRateLimitInfo          map[string]any
	ProviderRateLimitObservedAt    string

	// CompactionCount is the durable count of context.compacted events the
	// runner has recorded for this session. It is a projection over the
	// append-only session_events ledger — the chat-activity emitter recomputes
	// it on each compaction upsert — surfaced on the row so the composer's
	// compaction metric hydrates from durable row metadata, stable across
	// reload and identical in a fresh tab (the same model the runtime context
	// window uses). Monotonic: it only ever advances over a session's life.
	CompactionCount int64

	// Avatar IDs are assigned by the backend from a durable shuffled deck.
	// Visible production rows must have an agent avatar id before publication.
	// Empty values are legacy/incomplete state; clients render them as missing
	// identity instead of inventing a local fallback.
	AgentAvatarID  string
	SystemAvatarID string

	// SidebarPosition is the durable user-facing sort key for the
	// session list. Larger values render earlier. It is intentionally
	// separate from RowVersion: row_version is the live-update cursor
	// and must be free to advance on test/rollout/activity changes
	// without changing the sidebar order.
	SidebarPosition int64
	RowVersion      int64
}

type SessionBugLabel struct {
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type SessionAvatarAssignment struct {
	AgentAvatarID  string
	SystemAvatarID string
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
	{"repo-cloner.sh", "/opt/tank/repo-cloner.sh"},
	{"session-pod-bootstrap.sh", "/opt/tank/session-pod-bootstrap.sh"},
}

// noClaudeHijackModes are modes that should not receive the OAuth gateway / api proxy host aliases.
var noClaudeHijackModes = map[string]bool{
	ConfigMode:         true,
	CodexConfigMode:    true,
	CodexCLIMode:       true,
	CodexGUIMode:       true,
	CodexExecGUIMode:   true,
	CodexAppServerMode: true,
	// Antigravity talks to Google directly; no Claude OAuth-gateway aliases.
	AntigravityConfigMode: true,
	AntigravityGUIMode:    true,
}

type ManifestOptions struct {
	SessionImage            string
	CodexSessionImage       string
	AntigravitySessionImage string
	// Secret name (ESO-synced from KV) holding the harvested Antigravity OAuth
	// token, mounted into antigravity_gui runner pods. Empty for other modes.
	AntigravityCredentialsSecret string
	SessionsNamespace            string
	SessionScope                 string
	SessionServiceAccount        string
	SessionConfigMap             string
	ArgoCDTrackingApp            string
	SandboxAgentPort             int
	TankOperatorInternalURL      string
	// Optional: in-cluster Service IPs for host alias injection.
	OAuthGatewayIP  string
	APIProxyIP      string
	CodexAPIProxyIP string
	// ConfigMap name for the OAuth gateway CA cert.
	OAuthGatewayCAConfigMap string
	// SDK runners use NATS JetStream for durable command/event delivery.
	NATSURL        string
	NATSStream     string
	NATSAuthSecret string
	// GlimmungContext JSON-serialized dict (may be empty).
	GlimmungContextJSON string
	// Repos is the validated owner/name slug list selected at session
	// create time. PodManifest passes it to the repo-cloner init
	// container as JSON; empty means no init container.
	Repos []string
	// Name is the optional user-facing session title selected before
	// creation. It is stamped on the pod annotation so degraded pod-only
	// reads match the registry row.
	Name *string
	// Capabilities is the normalized per-session capability list for the pod
	// being materialized. The registry persists the same list so the runtime
	// surface stays inspectable after creation.
	Capabilities []string
	// Non-secret SpireLens tailnet configuration. Only used when
	// Capabilities includes SessionCapabilitySpireLensMCP.
	SpireLensTailscaleOIDCClientID string
	SpireLensTailscaleTailnet      string
	SpireLensTailscaleAuthTag      string
	SpireLensHost                  string
	SpireLensMCPPort               int
	// HotSwapAgentRunner gates the test-slot hot-swap surface on SDK
	// runner containers. When true, PodManifest attaches a writable
	// emptyDir at /var/run/<runner>-hot, mounts it on the active runner
	// container, and sets GLIMMUNG_SUPERVISOR_CHILD/HOT_ARTIFACT env vars
	// so the launch script execs tank-supervisor (instead of node) as PID 1.
	// Default false; the orchestrator's deployment.yaml sets this to true
	// only when the chart runs in hot test-slot mode. Production sessions see no
	// behavioral change. See scripts/check-session-pod-hot-swap-migration.mjs
	// for the completion contract.
	HotSwapAgentRunner bool
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

// IsCodexMode reports whether a session mode runs the Codex agent, and so is
// stamped with CodexSessionImage rather than SessionImage. This is the single
// source of truth for the codex-vs-claude image split (used by PodManifest and
// by the session-image override resolver in internal/sessions).
func IsCodexMode(mode string) bool {
	switch NormalizeSessionMode(mode) {
	case CodexConfigMode, CodexCLIMode, CodexGUIMode, CodexExecGUIMode, CodexAppServerMode:
		return true
	default:
		return false
	}
}

// IsAntigravityMode reports whether a session mode runs the Antigravity agent
// (agy) and so is stamped with AntigravitySessionImage. Covers both the
// credential-mint terminal mode and the GUI chat mode.
func IsAntigravityMode(mode string) bool {
	switch NormalizeSessionMode(mode) {
	case AntigravityConfigMode, AntigravityGUIMode:
		return true
	default:
		return false
	}
}

func NormalizeSessionCapabilities(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{}, nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		capability := strings.ToLower(strings.TrimSpace(raw))
		if capability == "" {
			return nil, errors.New("session capability cannot be empty")
		}
		if _, ok := sessionCapabilities[capability]; !ok {
			return nil, errors.New("unknown session capability: " + capability)
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out, nil
}

func HasSessionCapability(capabilities []string, capability string) bool {
	capability = strings.ToLower(strings.TrimSpace(capability))
	for _, item := range capabilities {
		if strings.ToLower(strings.TrimSpace(item)) == capability {
			return true
		}
	}
	return false
}

func OwnerLabel(email string) string {
	sum := sha256.Sum256([]byte(email))
	return "u-" + hex.EncodeToString(sum[:])[:16]
}

// SessionDisplayName is the single source of truth for a session's
// human-facing label: the user-set name when present, else a short id
// derived from the pod name (falling back to the session id). Every wire
// payload (Info, row updates, snapshot) derives display_name from this so
// unnamed sessions render identically across surfaces.
func SessionDisplayName(name *string, podName, id string) string {
	if name != nil {
		if trimmed := strings.TrimSpace(*name); trimmed != "" {
			return trimmed
		}
	}
	base := podName
	if base == "" {
		base = id
	}
	base = strings.TrimPrefix(base, "session-")
	if len(base) > 8 {
		base = base[:8]
	}
	return base
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

func NormalizeBugLabelName(raw *string) (*SessionBugLabel, error) {
	if raw == nil {
		return nil, nil
	}
	name := strings.TrimSpace(*raw)
	if name == "" {
		return nil, nil
	}
	if len(name) >= 4 && strings.EqualFold(strings.TrimSpace(name[:4]), "bug:") {
		name = strings.TrimSpace(name[4:])
	}
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return nil, nil
	}
	name = strings.Join(fields, " ")
	const maxBugLabelRunes = 80
	runes := []rune(name)
	if len(runes) > maxBugLabelRunes {
		name = strings.TrimSpace(string(runes[:maxBugLabelRunes]))
	}
	slug := bugLabelSlug(name)
	if slug == "" {
		return nil, errors.New("bug label must include a letter or number")
	}
	return &SessionBugLabel{
		Name:        name,
		Slug:        slug,
		DisplayName: "bug: " + name,
	}, nil
}

func bugLabelSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
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
	if IsAntigravityMode(mode) {
		sessionImage = opts.AntigravitySessionImage
	}
	if IsCodexMode(mode) {
		sessionImage = opts.CodexSessionImage
	}

	// Build configmap volume mounts for both containers.
	spireLensMCPEnabled := HasSessionCapability(opts.Capabilities, SessionCapabilitySpireLensMCP)
	mcpConfigKey := "mcp.json"
	if spireLensMCPEnabled {
		mcpConfigKey = "mcp.spirelens.json"
	}
	configMounts := buildConfigMounts(opts.SessionConfigMap, mcpConfigKey)

	// Environment variables for the claude container.
	env := []any{
		map[string]any{"name": "SANDBOX_AGENT_PORT", "value": itoa(opts.SandboxAgentPort)},
		// TANK_SESSION_MODE drives session-pod-bootstrap.sh's per-mode
		// seeding (~/.codex/config.toml, ~/.claude/settings.json, etc.).
		// Also surfaced inside the user's shell, so `echo $TANK_SESSION_MODE`
		// works when debugging in-pod.
		map[string]any{"name": "TANK_SESSION_MODE", "value": mode},
		map[string]any{"name": "TANK_GLIMMUNG_CONTEXT_JSON", "value": opts.GlimmungContextJSON},
		map[string]any{"name": "TANK_GLIMMUNG_RUN_REF", "value": glimmungField(opts.GlimmungContextJSON, "glimmung_run_ref")},
		map[string]any{"name": "TANK_GLIMMUNG_ISSUE_REF", "value": glimmungField(opts.GlimmungContextJSON, "glimmung_issue_ref")},
		map[string]any{"name": "TANK_GLIMMUNG_TOUCHPOINT_REF", "value": glimmungField(opts.GlimmungContextJSON, "glimmung_touchpoint_ref")},
		map[string]any{"name": "TANK_GLIMMUNG_VALIDATION_URL", "value": glimmungField(opts.GlimmungContextJSON, "validation_url")},
		map[string]any{"name": "FORCE_HYPERLINK", "value": "1"},
		map[string]any{"name": "CLAUDE_CODE_NO_FLICKER", "value": "1"},
	}
	if spireLensMCPEnabled {
		env = append(env,
			map[string]any{"name": "SPIRELENS_MCP_ENABLED", "value": "true"},
			map[string]any{"name": "SPIRELENS_TAILSCALE_OIDC_CLIENT_ID", "value": opts.SpireLensTailscaleOIDCClientID},
			map[string]any{"name": "SPIRELENS_TAILSCALE_TAILNET", "value": opts.SpireLensTailscaleTailnet},
			map[string]any{"name": "SPIRELENS_TAILSCALE_AUTH_TAG", "value": opts.SpireLensTailscaleAuthTag},
			map[string]any{"name": "SPIRELENS_TAILSCALE_SOCKET", "value": "/tmp/tailscaled.sock"},
			map[string]any{"name": "SPIRELENS_TAILSCALE_STATE_DIR", "value": "/workspace/.tailscale-state"},
			map[string]any{"name": "SPIRELENS_TAILSCALE_OUTBOUND_HTTP_PROXY_LISTEN", "value": "127.0.0.1:1055"},
			map[string]any{"name": "AUTH_ROMAINE_TOKEN_PATH", "value": "/var/run/secrets/auth.romaine.life/token"},
			map[string]any{
				"name": "SPIRELENS_TAILSCALE_HOSTNAME",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{
						"fieldPath": "metadata.name",
					},
				},
			},
		)
	}

	claudeVolumeMounts := append([]any{}, configMounts...)
	if spireLensMCPEnabled {
		claudeVolumeMounts = append(claudeVolumeMounts, map[string]any{
			"name":      "auth-romaine-sa-token",
			"mountPath": "/var/run/secrets/auth.romaine.life",
			"readOnly":  true,
		})
	}
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
		// Second projected SA token, audience-pinned to auth.romaine.life.
		// In-pod code POSTs this token to auth.romaine.life's
		// /api/auth/exchange/k8s to receive an auth.romaine.life JWT with
		// role=service that tank-operator's /api/internal/sessions/*
		// handlers accept. Distinct audience (and distinct file path) from
		// the tank-operator token above so a stolen token cannot be
		// replayed across services. See romaine-life/tank-operator#486.
		map[string]any{
			"name": "auth-romaine-sa-token",
			"projected": map[string]any{
				"sources": []any{
					map[string]any{
						"serviceAccountToken": map[string]any{
							"audience":          "https://auth.romaine.life",
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
	// workspace state" semantics. claude_gui uses agent-runner;
	// Codex GUI modes use codex-runner. Both need the shared mount.
	wantAgentRunner := mode == ClaudeGUIMode
	wantCodexRunner := mode == CodexGUIMode || mode == CodexExecGUIMode || mode == CodexAppServerMode
	wantAntigravityRunner := mode == AntigravityGUIMode
	wantSDKRunner := wantAgentRunner || wantCodexRunner || wantAntigravityRunner
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

	initContainers := []any{}
	if wantSDKRunner && len(opts.Repos) > 0 {
		reposJSON, _ := json.Marshal(opts.Repos)
		initContainers = append(initContainers, map[string]any{
			"name":            "repo-cloner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/repo-cloner.sh"},
			"env": []any{
				map[string]any{
					"name": "SESSION_ID",
					"valueFrom": map[string]any{
						"fieldRef": map[string]any{
							"fieldPath": "metadata.labels['tank-operator/session-id']",
						},
					},
				},
				map[string]any{"name": "TANK_REPOS_JSON", "value": string(reposJSON)},
				map[string]any{"name": "WORKSPACE", "value": "/workspace"},
				map[string]any{"name": "AUTH_ROMAINE_TOKEN_PATH", "value": "/var/run/secrets/auth.romaine.life/token"},
				map[string]any{"name": "AUTH_ROMAINE_EXCHANGE_URL", "value": "https://auth.romaine.life/api/auth/exchange/k8s"},
				map[string]any{"name": "MCP_GITHUB_URL", "value": "http://mcp-github.mcp-github.svc:80"},
				map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
			},
			"volumeMounts": []any{
				map[string]any{
					"name":      "session-config",
					"mountPath": "/opt/tank/repo-cloner.sh",
					"subPath":   "repo-cloner.sh",
					"readOnly":  true,
				},
				map[string]any{
					"name":      "workspace",
					"mountPath": "/workspace",
				},
				map[string]any{
					"name":      "auth-romaine-sa-token",
					"mountPath": "/var/run/secrets/auth.romaine.life",
					"readOnly":  true,
				},
			},
			"resources": sandboxAgentResources(),
		})
	}

	// OAuth gateway + API proxy host aliases and CA cert.
	var hostAliases []any
	if !noClaudeHijackModes[mode] && (opts.OAuthGatewayIP != "" || opts.APIProxyIP != "") {
		if opts.OAuthGatewayIP != "" {
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
	if (mode == CodexCLIMode || mode == CodexGUIMode || mode == CodexExecGUIMode || mode == CodexAppServerMode) && opts.CodexAPIProxyIP != "" {
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

	claudeContainer := map[string]any{
		"name":            "claude",
		"image":           sessionImage,
		"imagePullPolicy": "Always",
		"command": []any{
			"bash", "-lc",
			"if [ -f /opt/tank/session-config/install-tank-docs.sh ]; then sh /opt/tank/session-config/install-tank-docs.sh || true; fi; " +
				"if [ -f /opt/tank/session-pod-bootstrap.sh ]; then bash /opt/tank/session-pod-bootstrap.sh || true; fi; " +
				"if command -v sandbox-agent >/dev/null 2>&1; then sandbox_agent_cmd=sandbox-agent; else sandbox_agent_cmd='npx -y @sandbox-agent/cli@0.4.2'; fi; " +
				"exec $sandbox_agent_cmd server --host 0.0.0.0 --port " + itoa(opts.SandboxAgentPort) + " --no-token --no-telemetry",
		},
		"ports":        []any{map[string]any{"name": "sandbox-agent", "containerPort": opts.SandboxAgentPort}},
		"env":          env,
		"volumeMounts": claudeVolumeMounts,
		"resources":    sandboxAgentResources(),
	}

	mcpProxyVolumeMounts := append([]any{}, configMounts...)
	mcpProxyVolumeMounts = append(mcpProxyVolumeMounts, map[string]any{
		"name":      "auth-romaine-sa-token",
		"mountPath": "/var/run/secrets/auth.romaine.life",
		"readOnly":  true,
	})
	mcpProxyEnv := []any{
		map[string]any{"name": "MCP_AUTH_PROXY_METRICS_PORT", "value": itoa(MCPAuthProxyMetricsPort)},
		// Originating session id forwarded as
		// X-Tank-Origin-Session-Id on outbound calls to
		// mcp-tank-operator. mcp-tank-operator threads it
		// onto handoff messages so the orchestrator stamps
		// the persisted user_message.created event and the
		// frontend renders the parent session's avatar on
		// the user bubble in the target session. Sourced
		// from the same downward-API path the runners use
		// (metadata.labels['tank-operator/session-id']) so
		// the sidecar agrees with the runner on which
		// session it lives in. Absent SESSION_ID, the
		// proxy omits the header and the orchestrator
		// falls back to the human-Gravatar rendering.
		map[string]any{
			"name": "SESSION_ID",
			"valueFrom": map[string]any{
				"fieldRef": map[string]any{
					"fieldPath": "metadata.labels['tank-operator/session-id']",
				},
			},
		},
	}
	if spireLensMCPEnabled {
		mcpProxyEnv = append(mcpProxyEnv,
			map[string]any{"name": "SPIRELENS_MCP_UPSTREAM", "value": spireLensMCPUpstream(opts.SpireLensHost, opts.SpireLensMCPPort)},
			map[string]any{"name": "TAILNET_HTTP_PROXY", "value": "http://127.0.0.1:1055"},
		)
	}

	mcpAuthProxyContainer := map[string]any{
		"name":            "mcp-auth-proxy",
		"image":           sessionImage,
		"imagePullPolicy": "Always",
		"command":         []any{"mcp-auth-proxy"},
		"env":             mcpProxyEnv,
		// The metrics port is exposed as a named container port so the
		// k8s/templates/podmonitor-sessions.yaml PodMonitor can scrape
		// it by name without hard-coding numbers. Listens on 0.0.0.0;
		// the proxy's MCP listeners stay on 127.0.0.1.
		"ports": []any{
			map[string]any{"name": "metrics", "containerPort": MCPAuthProxyMetricsPort},
		},
		"volumeMounts": mcpProxyVolumeMounts,
		"resources":    mcpAuthProxyResources(),
	}
	containers := []any{}
	// The glibc antigravity-container image does not bake the (Python)
	// mcp-auth-proxy binary, and the antigravity_config credential-mint mode
	// is a terminal login that needs no MCP gateway. Skip the sidecar there;
	// antigravity_gui will bake mcp-auth-proxy into its image and restore it.
	if mode != AntigravityConfigMode {
		containers = append(containers, mcpAuthProxyContainer)
	}
	containers = append(containers, claudeContainer)

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
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "auth-romaine-sa-token",
			"mountPath": "/var/run/secrets/auth.romaine.life",
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

		// Test-slot agent-runner hot-swap wiring. Gated on
		// opts.HotSwapAgentRunner so production session pods see no
		// behavioral change. When enabled:
		//   - GLIMMUNG_SUPERVISOR_CHILD points at the baked launch shim
		//     (/app/agent-runner-launch-binary.sh) which exec node from
		//     the baked /opt/agent-runner/dist path.
		//   - GLIMMUNG_SUPERVISOR_HOT_ARTIFACT points at the writable
		//     shim path; the hot-swap operator writes a sibling shim and
		//     the new dist to /var/run/agent-runner-hot/. The supervisor's
		//     existing "hot-if-present, baked-if-missing" resolution
		//     picks the right shim with zero supervisor code change.
		//   - The agent-runner-launch.sh script branches on the env var
		//     and exec's /app/tank-supervisor instead of node, putting
		//     the supervisor at PID 1 in the container.
		// See scripts/check-session-pod-hot-swap-migration.mjs.
		if opts.HotSwapAgentRunner {
			volumes = append(volumes, map[string]any{
				"name":     "agent-runner-hot",
				"emptyDir": map[string]any{},
			})
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name":      "agent-runner-hot",
				"mountPath": "/var/run/agent-runner-hot",
			})
			runnerEnv = append(runnerEnv,
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_CHILD", "value": "/app/agent-runner-launch-binary.sh"},
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_HOT_ARTIFACT", "value": "/var/run/agent-runner-hot/agent-runner-launch-binary.sh"},
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_RESTART_ENABLED", "value": "true"},
			)
		}

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
			"resources": agentRunnerResources(),
		}
		containers = append(containers, runnerContainer)
	}

	// Codex-runner sidecar. Sibling of agent-runner:
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
		runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
			"name":      "auth-romaine-sa-token",
			"mountPath": "/var/run/secrets/auth.romaine.life",
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
		// App-server is the primary Codex GUI transport. The legacy
		// SDK/codex exec transport rejects request_user_input at the
		// binary layer, so keep it behind CodexExecGUIMode as a fallback.
		if mode == CodexGUIMode || mode == CodexAppServerMode {
			codexRunnerEnv = append(codexRunnerEnv, map[string]any{
				"name": "CODEX_RUNNER_TRANSPORT", "value": "app-server",
			})
		}
		if opts.HotSwapAgentRunner {
			volumes = append(volumes, map[string]any{
				"name":     "codex-runner-hot",
				"emptyDir": map[string]any{},
			})
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name":      "codex-runner-hot",
				"mountPath": "/var/run/codex-runner-hot",
			})
			codexRunnerEnv = append(codexRunnerEnv,
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_CHILD", "value": "/app/codex-runner-launch-binary.sh"},
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_HOT_ARTIFACT", "value": "/var/run/codex-runner-hot/codex-runner-launch-binary.sh"},
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_RESTART_ENABLED", "value": "true"},
			)
		}
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
			"resources": codexRunnerResources(),
		}
		containers = append(containers, codexRunnerContainer)
	}

	// Antigravity-runner sidecar (antigravity_gui). Drives agy (Gemini-Ultra)
	// and maps its structured transcript onto the Tank conversation protocol.
	// Same workspace + session-bus contract as the other runners. The runner
	// reads the KV-mounted OAuth credential (read-only secret volume); the
	// launch script copies it into agy's writable data dir before exec'ing the
	// runner, because agy refreshes the access token in place.
	if wantAntigravityRunner {
		// The runner is self-contained: it drives agy + the session bus and
		// needs no session ConfigMap (no Tank mcp.json — agy owns its MCP). Its
		// launch script is baked into the antigravity image at /opt/tank, so the
		// pod has no ConfigMap-launch-script coupling.
		runnerVolumeMounts := []any{
			map[string]any{"name": "workspace", "mountPath": "/workspace"},
			map[string]any{"name": "tank-operator-sa-token", "mountPath": "/var/run/secrets/tank-operator", "readOnly": true},
			map[string]any{"name": "auth-romaine-sa-token", "mountPath": "/var/run/secrets/auth.romaine.life", "readOnly": true},
		}
		if opts.AntigravityCredentialsSecret != "" {
			volumes = append(volumes, map[string]any{
				"name": "antigravity-cred",
				"secret": map[string]any{
					"secretName": opts.AntigravityCredentialsSecret,
					"items": []any{
						map[string]any{"key": "antigravity-oauth-token", "path": "antigravity-oauth-token"},
					},
				},
			})
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name": "antigravity-cred", "mountPath": "/var/run/antigravity-cred", "readOnly": true,
			})
		}
		antigravityRunnerEnv := []any{
			map[string]any{
				"name": "SESSION_ID",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{"fieldPath": "metadata.labels['tank-operator/session-id']"},
				},
			},
			map[string]any{"name": "TANK_SESSION_STORAGE_KEY", "value": storageKey},
			map[string]any{
				"name": "POD_OWNER_EMAIL",
				"valueFrom": map[string]any{
					"fieldRef": map[string]any{"fieldPath": "metadata.annotations['tank-operator/owner-email']"},
				},
			},
			map[string]any{"name": "NATS_URL", "value": opts.NATSURL},
			map[string]any{"name": "NATS_STREAM", "value": opts.NATSStream},
			map[string]any{
				"name": "NATS_TOKEN",
				"valueFrom": map[string]any{
					"secretKeyRef": map[string]any{"name": opts.NATSAuthSecret, "key": "token"},
				},
			},
			map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
			map[string]any{"name": "TANK_OPERATOR_TOKEN_PATH", "value": "/var/run/secrets/tank-operator/token"},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
			map[string]any{"name": "ANTIGRAVITY_CRED_FILE", "value": "/var/run/antigravity-cred/antigravity-oauth-token"},
			map[string]any{"name": "TANK_RUNNER_METRICS_PORT", "value": itoa(AntigravityRunnerMetricsPort)},
		}
		antigravityRunnerContainer := map[string]any{
			"name":            "antigravity-runner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/antigravity-runner-launch.sh"},
			"env":             antigravityRunnerEnv,
			"volumeMounts":    runnerVolumeMounts,
			"ports": []any{
				map[string]any{"name": "runner-metrics", "containerPort": AntigravityRunnerMetricsPort},
			},
			"resources": codexRunnerResources(),
		}
		containers = append(containers, antigravityRunnerContainer)
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
	if len(initContainers) > 0 {
		spec["initContainers"] = initContainers
	}
	if len(hostAliases) > 0 {
		spec["hostAliases"] = hostAliases
	}

	// auth.romaine.life's /api/auth/exchange/k8s reads per-session lineage
	// from pod annotations (see romaine-life/auth → src/k8s-pod.ts). The
	// `tank-operator/owner-email` annotation was already here, but
	// `tank-operator/session-id` was only emitted as a label, which made
	// the auth handler reject every service-token exchange with
	// `denied_annotation_missing`. Both annotations are required for the
	// per-session exchange path; stamp them at pod creation time so the
	// MCP auth proxy sidecar can mint service JWTs immediately.
	annotations := map[string]any{
		"tank-operator/owner-email":      owner,
		"tank-operator/session-id":       sessionID,
		"argocd.argoproj.io/tracking-id": argoTrackingID,
	}
	if len(opts.Capabilities) > 0 {
		raw, _ := json.Marshal(opts.Capabilities)
		annotations["tank-operator/capabilities"] = string(raw)
	}
	if name := NormalizeName(opts.Name); name != nil {
		annotations["tank-operator/display-name"] = *name
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
func buildConfigMounts(configMapName string, mcpConfigKey string) []any {
	_ = configMapName // name is in the volume declaration, not the mount
	if strings.TrimSpace(mcpConfigKey) == "" {
		mcpConfigKey = "mcp.json"
	}
	mounts := make([]any, 0, len(sessionConfigMounts)+1)
	for _, m := range sessionConfigMounts {
		key := m.key
		if m.mountPath == "/workspace/.mcp.json" {
			key = mcpConfigKey
		}
		mounts = append(mounts, map[string]any{
			"name":      "session-config",
			"mountPath": m.mountPath,
			"subPath":   key,
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

func spireLensMCPUpstream(host string, port int) string {
	host = strings.TrimSpace(host)
	if port <= 0 {
		port = DefaultSpireLensMCPPort
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	return "http://" + host + ":" + itoa(port)
}

func SpireLensMCPConfigured(opts ManifestOptions) bool {
	return strings.TrimSpace(opts.SpireLensTailscaleOIDCClientID) != "" &&
		strings.TrimSpace(opts.SpireLensTailscaleTailnet) != "" &&
		strings.TrimSpace(opts.SpireLensHost) != ""
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
	if opts.NATSStream == "" {
		opts.NATSStream = "TANK_SESSION_BUS"
	}
	if opts.NATSAuthSecret == "" {
		opts.NATSAuthSecret = "tank-nats-auth"
	}
	if opts.SpireLensTailscaleAuthTag == "" {
		opts.SpireLensTailscaleAuthTag = DefaultSpireLensTailscaleTag
	}
	if opts.SpireLensMCPPort == 0 {
		opts.SpireLensMCPPort = DefaultSpireLensMCPPort
	}
	return opts
}

// Per-container resource budgets for session pods. Session pods were
// previously BestEffort (no requests, no limits), which made them the
// first eviction target whenever the node hit memory pressure — the
// concrete failure that brought down session 21 in
// tank-operator#83. Adding requests upgrades the pod to Burstable QoS,
// so the kubelet has to find a true BestEffort victim before reaping
// it; limits cap a runaway runner at the agreed budget so one
// misbehaving session can't blast its noisy neighbors.
//
// Memory budgets are calibrated against observed steady-state usage
// from the 7-day sample around the eviction (agent-runner at ~150-300
// MiB steady, ~795 MiB at the failure point; claude/sandbox-agent at
// ~14 MiB; mcp-auth-proxy at ~27 MiB). Limits leave 4-5x headroom on
// every container so normal-but-bursty sessions don't OOMKill.
//
// CPU intentionally has requests but no limits: limits cause CFS
// throttling that creates the appearance of latency bugs in agent
// streaming, and the node-level CPU budget is enforced by the
// scheduler's request packing.
//
// CPU requests were retuned 2026-05 after FailedScheduling pressure on
// the 3-node Standard_B2s_v2 cluster (prod sessions + 10 test slots
// sharing one node pool). Per-pod sample across the live namespaces:
// idle session pods drew ~10m total (agent-runner ~9m, sandbox-agent
// ~0m, mcp-auth-proxy ~1m); active turns peaked ~120m on agent-runner.
// The prior 100m/50m/25m=175m budget packed every pod as if it were
// active, blocking new sessions from scheduling even though actual node
// CPU sat under 35%. Lowered to 50m/25m/10m=85m: ~5x headroom over
// idle, peaks still allowed via burstability (no CPU limit). The
// `tank:session_pod_spawn_seconds:*` recording rules in
// k8s/templates/observability.yaml are the regression detector — if
// scheduling pressure returns, p50/p95 spawn time rises and the
// existing TankSessionSpawnSlow* alerts fire.
func agentRunnerResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{
			"cpu":    "50m",
			"memory": "512Mi",
		},
		"limits": map[string]any{
			"memory": "1536Mi",
		},
	}
}

func codexRunnerResources() map[string]any {
	resources := agentRunnerResources()
	limits := resources["limits"].(map[string]any)
	// Codex app-server compaction runs in the runner container. Session
	// 146 showed that the shared 1536Mi Claude-runner cap can OOMKill a
	// live Codex thread during compaction, so Codex gets more burst
	// headroom while keeping the same scheduling request.
	limits["memory"] = "3072Mi"
	return resources
}

func sandboxAgentResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{
			"cpu":    "25m",
			"memory": "64Mi",
		},
		"limits": map[string]any{
			"memory": "256Mi",
		},
	}
}

func mcpAuthProxyResources() map[string]any {
	return map[string]any{
		"requests": map[string]any{
			"cpu":    "10m",
			"memory": "64Mi",
		},
		"limits": map[string]any{
			"memory": "256Mi",
		},
	}
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
