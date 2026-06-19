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
	APIKeyMode                = "api_key"
	ClaudeCLIMode             = "claude_cli"
	ClaudeGUIMode             = "claude_gui"
	ConfigMode                = "config"
	ClaudeSecondaryCLIMode    = "claude_secondary_cli"
	ClaudeSecondaryGUIMode    = "claude_secondary_gui"
	ClaudeSecondaryConfigMode = "claude_secondary_config"
	CodexConfigMode           = "codex_config"
	CodexCLIMode              = "codex_cli"
	CodexGUIMode              = "codex_gui"
	CodexExecGUIMode          = "codex_exec_gui"
	CodexAppServerMode        = "codex_app_server"
	DefaultSessionMode        = ClaudeGUIMode
	MaxNameLength             = 80
	SessionsNamespace         = "tank-operator-sessions"
	SessionServiceAccount     = "claude-session"
	SessionConfigMap          = "tank-session-config"
	SandboxAgentPort          = 2468
	// SessionCapabilitySpireLensMCP opts a pod into the SpireLens game-host
	// MCP path. The default session surface stays cluster-local; this rare
	// capability joins the tailnet and mounts an MCP config with
	// spire-lens-mcp on localhost :9997.
	SessionCapabilitySpireLensMCP = "spirelens_mcp"
	// SessionCapabilityRestrictedGit opts a pod into the experimental
	// governed Git surface: Tank-owned session branches, guarded MCP writes,
	// post-commit publishing, and PR lane approvals. Service-created
	// repo-capable sessions are defaulted into this capability by the
	// orchestrator; sessions without it are the explicit unrestricted
	// exception and keep the historical direct Git/GitHub behavior.
	SessionCapabilityRestrictedGit = "restricted_git"
	DefaultSpireLensMCPPort        = 15527
	DefaultSpireLensTailscaleTag   = "tag:spirelens-orchestrator"
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
		APIKeyMode:                {},
		ClaudeCLIMode:             {},
		ClaudeGUIMode:             {},
		ConfigMode:                {},
		ClaudeSecondaryCLIMode:    {},
		ClaudeSecondaryGUIMode:    {},
		ClaudeSecondaryConfigMode: {},
		CodexConfigMode:           {},
		CodexCLIMode:              {},
		CodexGUIMode:              {},
		CodexExecGUIMode:          {},
		CodexAppServerMode:        {},
	}

	sessionCapabilities = map[string]struct{}{
		SessionCapabilitySpireLensMCP:  {},
		SessionCapabilityRestrictedGit: {},
	}
)

// ImageVersionMetadata is the human-facing release context attached to an
// immutable image ref. Keys are snake_case so the map can be passed straight
// through JSON to the frontend and through Helm values/env vars.
type ImageVersionMetadata map[string]string

var imageVersionMetadataKeys = map[string]struct{}{
	"actor":            {},
	"built_at":         {},
	"commit_url":       {},
	"git_ref":          {},
	"git_sha":          {},
	"pr_number":        {},
	"pr_url":           {},
	"repository":       {},
	"source":           {},
	"workflow_run_url": {},
}

func NormalizeImageVersionMetadata(in ImageVersionMetadata) ImageVersionMetadata {
	if len(in) == 0 {
		return nil
	}
	out := ImageVersionMetadata{}
	for key, value := range in {
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if _, ok := imageVersionMetadataKeys[key]; !ok {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ParseImageVersionMetadata(raw string) ImageVersionMetadata {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return NormalizeImageVersionMetadata(ImageVersionMetadata(parsed))
}

func DecodeImageVersionMetadata(raw []byte) ImageVersionMetadata {
	if len(raw) == 0 {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return NormalizeImageVersionMetadata(ImageVersionMetadata(parsed))
}

func (m ImageVersionMetadata) Clone() ImageVersionMetadata {
	return NormalizeImageVersionMetadata(m)
}

// DecodeSpawnedSessions parses the sessions.spawned_sessions jsonb array
// into typed refs for the snapshot/row layers. A missing column, NULL, or
// malformed payload decodes to nil ("spawned nothing") rather than an
// error — the column is a display-only projection, never load-bearing for
// session correctness, so a bad row must not break the session list.
func DecodeSpawnedSessions(raw []byte) []SpawnedSessionRef {
	if len(raw) == 0 {
		return nil
	}
	var parsed []SpawnedSessionRef
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed
}

type SessionRecord struct {
	ID      string
	Email   string
	Mode    string
	Scope   string
	PodName string
	// SessionImage is the full sandbox/session image reference chosen at
	// create time after applying any test-slot image override. It is durable
	// session metadata; clients should display it as "what this session booted
	// from" rather than recomputing from today's chart values.
	SessionImage string
	// SessionImageMetadata is the release/build metadata that was current for
	// SessionImage at create time. It is stored with the session so old
	// sessions keep pointing at the PR, commit, workflow, and build timestamp
	// they actually booted from.
	SessionImageMetadata ImageVersionMetadata
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

	// SpawnedSessions is the durable parent→child lineage surfaced by the
	// session-bar "spawned sessions" chip: one ref per session this
	// session spawned (via spawn_run_session / spawn_test_slot_session).
	// nil/empty means this session spawned nothing. Appended id-deduped by
	// sessionregistry.AppendSpawnedSession at child-create; the snapshot/
	// RowPublisher carry it verbatim so the SPA never re-derives the
	// relationship from the event ledger. jsonb array column.
	SpawnedSessions []SpawnedSessionRef

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
	RuntimeModel                     string
	RuntimeEffort                    string
	RuntimeConfiguredAt              string
	RuntimeContextWindowTokens       int64
	RuntimeContextWindowSource       string
	RuntimeContextWindowObservedAt   string
	RuntimeProviderSessionID         string
	RuntimeProviderSessionObservedAt string
	ProviderRateLimitInfo            map[string]any
	ProviderRateLimitObservedAt      string

	// CompactionCount is the durable count of context.compacted events the
	// runner has recorded for this session. It is a projection over the
	// append-only session_events ledger — the chat-activity emitter recomputes
	// it on each compaction upsert — surfaced on the row so the composer's
	// compaction metric hydrates from durable row metadata, stable across
	// reload and identical in a fresh tab (the same model the runtime context
	// window uses). Monotonic: it only ever advances over a session's life.
	CompactionCount int64

	// UserMessageCount is the durable count of user_message.created events the
	// session has recorded — one per human back-and-forth submission. Like
	// CompactionCount it is a projection over the append-only session_events
	// ledger (the chat-activity emitter recomputes it on each
	// user_message.created upsert), surfaced on the row as durable metadata,
	// stable across reload and identical in a fresh tab. Monotonic: it only ever
	// advances. Background-task wake continuations carry their prompt on
	// turn.submitted, not user_message.created, so they are excluded.
	UserMessageCount int64

	// OpenTarget is the legacy durable per-session sidebar open-target
	// preference. Current frontend builds no longer use it for session-open
	// defaults, but it stays on the row for compatibility.
	OpenTarget string

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

// SpawnedSessionRef is one entry of SessionRecord.SpawnedSessions: a
// durable, self-contained handle to a session this session spawned, used
// by the session-bar "spawned sessions" chip. URL is absolute and stamped
// at create time by the operator that handled the spawn (so a cross-scope
// test-slot child carries its own slot host), letting the chip link out
// without the SPA re-deriving the address. ID dedupes appends.
type SpawnedSessionRef struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Mode      string   `json:"mode,omitempty"`
	Model     string   `json:"model,omitempty"`
	Repos     []string `json:"repos,omitempty"`
	URL       string   `json:"url"`
	CreatedAt string   `json:"created_at,omitempty"`
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
	{"claude-runner-launch.sh", "/opt/tank/claude-runner-launch.sh"},
	{"codex-runner-launch.sh", "/opt/tank/codex-runner-launch.sh"},
	{"repo-cloner.sh", "/opt/tank/repo-cloner.sh"},
	{"session-pod-bootstrap.sh", "/opt/tank/session-pod-bootstrap.sh"},
}

// noClaudeHijackModes are modes that must not receive the *Claude* OAuth
// gateway / api-proxy host aliases (platform.claude.com, api.anthropic.com).
// Codex routes its own provider host through codex-api-proxy, so it must not
// also get Claude's aliases.
var noClaudeHijackModes = map[string]bool{
	ConfigMode:                true,
	ClaudeSecondaryCLIMode:    true,
	ClaudeSecondaryGUIMode:    true,
	ClaudeSecondaryConfigMode: true,
	CodexConfigMode:           true,
	CodexCLIMode:              true,
	CodexGUIMode:              true,
	CodexExecGUIMode:          true,
	CodexAppServerMode:        true,
}

type ManifestOptions struct {
	SessionImage              string
	CodexSessionImage         string
	SessionImageMetadata      ImageVersionMetadata
	CodexSessionImageMetadata ImageVersionMetadata
	SessionsNamespace         string
	SessionScope              string
	SessionServiceAccount     string
	SessionConfigMap          string
	ArgoCDTrackingApp         string
	SandboxAgentPort          int
	TankOperatorInternalURL   string
	TankUIHost                string
	// Optional: in-cluster Service IPs for host alias injection.
	OAuthGatewayIP            string
	APIProxyIP                string
	ClaudeSecondaryAPIProxyIP string
	CodexAPIProxyIP           string
	// ConfigMap name for the OAuth gateway CA cert.
	OAuthGatewayCAConfigMap string
	// SDK runners use NATS JetStream for durable command/event delivery.
	NATSURL    string
	NATSStream string
	// NATSCommandStream is the WorkQueue stream the runner's durable
	// command consumers bind (issue #1076 item 2); events keep riding
	// NATSStream.
	NATSCommandStream string
	NATSAuthSecret    string
	// Model/Effort are the immutable session-owned SDK run configuration
	// accepted at create time.
	Model          string
	Effort         string
	AgentAvatarID  string
	SystemAvatarID string
	// GlimmungContext JSON-serialized dict (may be empty).
	GlimmungContextJSON string
	// Repos is the validated owner/name slug list selected at session
	// create time. PodManifest passes it to the repo-cloner init
	// container as JSON; empty means no init container.
	Repos []string
	// RepoBases optionally maps a repo slug to the branch repo-cloner should
	// use as the governed PR base instead of the repo default branch.
	RepoBases map[string]string
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

func IsClaudeSecondaryMode(mode string) bool {
	switch NormalizeSessionMode(mode) {
	case ClaudeSecondaryCLIMode, ClaudeSecondaryGUIMode, ClaudeSecondaryConfigMode:
		return true
	default:
		return false
	}
}

// ResolvedSessionImage returns the sandbox image a session mode uses from the
// already-resolved manifest options. Callers must invoke any override resolver
// before calling this when they need the create-time truth.
func ResolvedSessionImage(mode string, opts ManifestOptions) string {
	if IsCodexMode(mode) {
		return opts.CodexSessionImage
	}
	return opts.SessionImage
}

func ResolvedSessionImageMetadata(mode string, opts ManifestOptions) ImageVersionMetadata {
	if IsCodexMode(mode) {
		return opts.CodexSessionImageMetadata.Clone()
	}
	return opts.SessionImageMetadata.Clone()
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

	sessionImage := ResolvedSessionImage(mode, opts)

	// Build configmap volume mounts for both containers.
	spireLensMCPEnabled := HasSessionCapability(opts.Capabilities, SessionCapabilitySpireLensMCP)
	restrictedGitEnabled := HasSessionCapability(opts.Capabilities, SessionCapabilityRestrictedGit)
	mcpConfigKey := "mcp.json"
	if spireLensMCPEnabled {
		mcpConfigKey = "mcp.spirelens.json"
	}
	configMounts := buildConfigMounts(opts.SessionConfigMap, mcpConfigKey)

	// Environment variables for the claude container.
	env := []any{
		map[string]any{"name": "SANDBOX_AGENT_PORT", "value": itoa(opts.SandboxAgentPort)},
		map[string]any{"name": "TERM", "value": "xterm-256color"},
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
	if restrictedGitEnabled {
		env = append(env, map[string]any{"name": "TANK_RESTRICTED_GIT", "value": "true"})
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
	// workspace state" semantics. Claude SDK GUI modes use claude-runner;
	// Codex GUI modes use codex-runner. Both need the shared mount.
	wantAgentRunner := mode == ClaudeGUIMode || mode == ClaudeSecondaryGUIMode
	wantCodexRunner := mode == CodexGUIMode || mode == CodexExecGUIMode || mode == CodexAppServerMode
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

	initContainers := []any{}
	if wantSDKRunner && len(opts.Repos) > 0 {
		reposJSON, _ := json.Marshal(opts.Repos)
		repoBasesJSON, _ := json.Marshal(opts.RepoBases)
		if opts.RepoBases == nil {
			repoBasesJSON = []byte("{}")
		}
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
				map[string]any{"name": "TANK_REPO_BASES_JSON", "value": string(repoBasesJSON)},
				map[string]any{"name": "WORKSPACE", "value": "/workspace"},
				map[string]any{"name": "AUTH_ROMAINE_TOKEN_PATH", "value": "/var/run/secrets/auth.romaine.life/token"},
				map[string]any{"name": "AUTH_ROMAINE_EXCHANGE_URL", "value": "https://auth.romaine.life/api/auth/exchange/k8s"},
				map[string]any{"name": "MCP_GITHUB_URL", "value": "http://mcp-github.mcp-github.svc:80"},
				map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
				map[string]any{"name": "TANK_RESTRICTED_GIT", "value": boolEnv(restrictedGitEnabled)},
				map[string]any{"name": "AGENT_POST_COMMIT_HOOK", "value": "/opt/tank/agent-post-commit-hook.sh"},
				map[string]any{"name": "AGENT_PRE_PUSH_HOOK", "value": "/opt/tank/agent-pre-push-hook.sh"},
			},
			"volumeMounts": []any{
				map[string]any{
					"name":      "session-config",
					"mountPath": "/opt/tank/repo-cloner.sh",
					"subPath":   "repo-cloner.sh",
					"readOnly":  true,
				},
				map[string]any{
					"name":      "session-config",
					"mountPath": "/opt/tank/agent-post-commit-hook.sh",
					"subPath":   "agent-post-commit-hook.sh",
					"readOnly":  true,
				},
				map[string]any{
					"name":      "session-config",
					"mountPath": "/opt/tank/agent-pre-push-hook.sh",
					"subPath":   "agent-pre-push-hook.sh",
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

	if (mode == ClaudeSecondaryCLIMode || mode == ClaudeSecondaryGUIMode) && (opts.OAuthGatewayIP != "" || opts.ClaudeSecondaryAPIProxyIP != "") {
		if opts.OAuthGatewayIP != "" {
			hostAliases = append(hostAliases, map[string]any{
				"ip":        opts.OAuthGatewayIP,
				"hostnames": []any{"platform.claude.com"},
			})
		}
		if opts.ClaudeSecondaryAPIProxyIP != "" {
			hostAliases = append(hostAliases, map[string]any{
				"ip":        opts.ClaudeSecondaryAPIProxyIP,
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
		"name":            "sandbox",
		"image":           sessionImage,
		"imagePullPolicy": "Always",
		"command": []any{
			"bash", "-lc",
			"if [ -f /opt/tank/session-config/install-tank-docs.sh ]; then sh /opt/tank/session-config/install-tank-docs.sh; fi; " +
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
	if wantSDKRunner {
		mcpProxyVolumeMounts = append(mcpProxyVolumeMounts, map[string]any{
			"name":      "workspace",
			"mountPath": "/workspace",
		})
	}
	mcpProxyVolumeMounts = append(mcpProxyVolumeMounts, map[string]any{
		"name":      "auth-romaine-sa-token",
		"mountPath": "/var/run/secrets/auth.romaine.life",
		"readOnly":  true,
	})
	mcpProxyEnv := []any{
		map[string]any{"name": "MCP_AUTH_PROXY_METRICS_PORT", "value": itoa(MCPAuthProxyMetricsPort)},
		map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
		map[string]any{"name": "TANK_UI_HOST", "value": opts.TankUIHost},
		map[string]any{"name": "MCP_GITHUB_URL", "value": "http://mcp-github.mcp-github.svc:80"},
		map[string]any{"name": "WORKSPACE", "value": "/workspace"},
		map[string]any{"name": "TANK_RESTRICTED_GIT", "value": boolEnv(restrictedGitEnabled)},
		// Session identity forwarded by mcp-auth-proxy on outbound calls to
		// Tank/Glimmung MCP servers. SESSION_ID still feeds the older
		// X-Tank-Origin-Session-Id handoff-avatar path for mcp-tank-operator;
		// together, SESSION_ID + SESSION_SCOPE also feed caller-context
		// headers that let workflow tools bind to the current session without
		// asking the model to restate its own id.
		map[string]any{
			"name": "SESSION_ID",
			"valueFrom": map[string]any{
				"fieldRef": map[string]any{
					"fieldPath": "metadata.labels['tank-operator/session-id']",
				},
			},
		},
		map[string]any{
			"name": "SESSION_SCOPE",
			"valueFrom": map[string]any{
				"fieldRef": map[string]any{
					"fieldPath": "metadata.labels['tank-operator/session-scope']",
				},
			},
		},
		map[string]any{
			"name": "AGENT_AVATAR_ID",
			"valueFrom": map[string]any{
				"fieldRef": map[string]any{
					"fieldPath": "metadata.annotations['tank-operator/agent-avatar-id']",
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
	containers = append(containers, mcpAuthProxyContainer)
	containers = append(containers, claudeContainer)

	// SDK claude-runner sidecar - Claude SDK GUI modes only. Shares /workspace
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
			map[string]any{"name": "NATS_COMMAND_STREAM", "value": opts.NATSCommandStream},
			map[string]any{"name": "NATS_USER", "value": storageKey},
			map[string]any{"name": "NATS_PASSWORD_FILE", "value": "/var/run/secrets/auth.romaine.life/token"},
			map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
			map[string]any{"name": "TANK_OPERATOR_TOKEN_PATH", "value": "/var/run/secrets/tank-operator/token"},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
			map[string]any{"name": "MCP_CONFIG", "value": "/workspace/.mcp.json"},
			map[string]any{"name": "TANK_RESTRICTED_GIT", "value": boolEnv(restrictedGitEnabled)},
		}
		// NODE_EXTRA_CA_CERTS — same gateway-CA injection the claude
		// container gets, so the SDK's spawned claude binary trusts the
		// OAuth gateway's self-signed cert.
		if (!noClaudeHijackModes[mode] || mode == ClaudeSecondaryGUIMode) && opts.OAuthGatewayCAConfigMap != "" {
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

		// Test-slot claude-runner hot-swap wiring. Gated on
		// opts.HotSwapAgentRunner so production session pods see no
		// behavioral change. When enabled:
		//   - GLIMMUNG_SUPERVISOR_CHILD points at the baked launch shim
		//     (/app/claude-runner-launch-binary.sh) which exec node from
		//     the baked /opt/claude-runner/dist path.
		//   - GLIMMUNG_SUPERVISOR_HOT_ARTIFACT points at the writable
		//     shim path; the hot-swap operator writes a sibling shim and
		//     the new dist to /var/run/claude-runner-hot/. The supervisor's
		//     existing "hot-if-present, baked-if-missing" resolution
		//     picks the right shim with zero supervisor code change.
		//   - The claude-runner-launch.sh script branches on the env var
		//     and exec's /app/tank-supervisor instead of node, putting
		//     the supervisor at PID 1 in the container.
		// See scripts/check-session-pod-hot-swap-migration.mjs.
		if opts.HotSwapAgentRunner {
			volumes = append(volumes, map[string]any{
				"name":     "claude-runner-hot",
				"emptyDir": map[string]any{},
			})
			runnerVolumeMounts = append(runnerVolumeMounts, map[string]any{
				"name":      "claude-runner-hot",
				"mountPath": "/var/run/claude-runner-hot",
			})
			runnerEnv = append(runnerEnv,
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_CHILD", "value": "/app/claude-runner-launch-binary.sh"},
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_HOT_ARTIFACT", "value": "/var/run/claude-runner-hot/claude-runner-launch-binary.sh"},
				map[string]any{"name": "GLIMMUNG_SUPERVISOR_RESTART_ENABLED", "value": "true"},
			)
		}

		runnerContainer := map[string]any{
			"name":            "claude-runner",
			"image":           sessionImage,
			"imagePullPolicy": "Always",
			"command":         []any{"bash", "/opt/tank/claude-runner-launch.sh"},
			"env":             runnerEnv,
			"volumeMounts":    runnerVolumeMounts,
			"ports": []any{
				map[string]any{"name": "runner-metrics", "containerPort": AgentRunnerMetricsPort},
			},
			"livenessProbe": map[string]any{
				// Belt-and-braces for the runner's exit-on-permanent-close
				// path (issue #1076 item 1): /healthz returns 503 once the
				// session bus connection is permanently closed, and a wedged
				// event loop simply stops answering — both restart the
				// container. Generous thresholds: a healthy runner under
				// reconnect churn keeps answering 200, so only true zombies
				// trip this (30s + 4x30s = ~2.5 minutes of deadness).
				"httpGet": map[string]any{
					"path": "/healthz",
					"port": "runner-metrics",
				},
				"initialDelaySeconds": 30,
				"periodSeconds":       30,
				"timeoutSeconds":      5,
				"failureThreshold":    4,
			},
			"resources": agentRunnerResources(),
		}
		containers = append(containers, runnerContainer)
	}

	// Codex-runner sidecar. Sibling of claude-runner:
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
			map[string]any{"name": "NATS_COMMAND_STREAM", "value": opts.NATSCommandStream},
			map[string]any{"name": "NATS_USER", "value": storageKey},
			map[string]any{"name": "NATS_PASSWORD_FILE", "value": "/var/run/secrets/auth.romaine.life/token"},
			map[string]any{"name": "TANK_OPERATOR_INTERNAL_URL", "value": opts.TankOperatorInternalURL},
			map[string]any{"name": "TANK_OPERATOR_TOKEN_PATH", "value": "/var/run/secrets/tank-operator/token"},
			map[string]any{"name": "WORKSPACE", "value": "/workspace"},
			map[string]any{"name": "TANK_RESTRICTED_GIT", "value": boolEnv(restrictedGitEnabled)},
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
			"livenessProbe": map[string]any{
				// Same contract as the claude-runner probe above.
				"httpGet": map[string]any{
					"path": "/healthz",
					"port": "runner-metrics",
				},
				"initialDelaySeconds": 30,
				"periodSeconds":       30,
				"timeoutSeconds":      5,
				"failureThreshold":    4,
			},
			"resources": codexRunnerResources(),
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
	if opts.AgentAvatarID != "" {
		annotations["tank-operator/agent-avatar-id"] = opts.AgentAvatarID
	}
	if opts.SystemAvatarID != "" {
		annotations["tank-operator/system-avatar-id"] = opts.SystemAvatarID
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
	if opts.TankUIHost == "" {
		opts.TankUIHost = "https://tank.romaine.life"
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
// from the 7-day sample around the eviction (claude-runner at ~150-300
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
// idle session pods drew ~10m total (claude-runner ~9m, sandbox-agent
// ~0m, mcp-auth-proxy ~1m); active turns peaked ~120m on claude-runner.
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

func boolEnv(value bool) string {
	if value {
		return "true"
	}
	return "false"
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
