package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	APIKeyMode               = "api_key"
	ClaudeCLIMode            = "claude_cli"
	ClaudeGUIMode            = "claude_gui"
	ConfigMode               = "config"
	CodexConfigMode          = "codex_config"
	CodexCLIMode             = "codex_cli"
	CodexGUIMode             = "codex_gui"
	PiConfigMode             = "pi_config"
	PiCLIMode                = "pi_cli"
	DefaultSessionMode       = ClaudeCLIMode
	MaxNameLength            = 80
	SessionsNamespace        = "tank-operator-sessions"
	SessionServiceAccount    = "claude-session"
	SessionConfigMap         = "tank-session-config"
	TerminalProxyConfigMap   = "tank-terminal-proxy"
	TerminalProxyImage       = "quay.io/brancz/kube-rbac-proxy:v0.22.0"
	TerminalDPort            = 7680
	TerminalProxyPort        = 7681
	SandboxAgentPort         = 2468
	DefaultSessionImage      = "romainecr.azurecr.io/claude-container:latest"
	DefaultCodexSessionImage = "romainecr.azurecr.io/codex-container:latest"
	DefaultPiSessionImage    = "romainecr.azurecr.io/pi-container:latest"
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

type ManifestOptions struct {
	SessionImage           string
	CodexSessionImage      string
	PiSessionImage         string
	SessionsNamespace      string
	SessionServiceAccount  string
	SessionConfigMap       string
	TerminalProxyConfigMap string
	TerminalProxyImage     string
	ArgoCDTrackingApp      string
	TerminalDPort          int
	TerminalProxyPort      int
	SandboxAgentPort       int
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
		"email":        strings.ToLower(record.Email),
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
		"email":         strings.ToLower(record.Email),
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
	sessionImage := opts.SessionImage
	if mode == CodexConfigMode || mode == CodexCLIMode || mode == CodexGUIMode {
		sessionImage = opts.CodexSessionImage
	}
	if mode == PiConfigMode || mode == PiCLIMode {
		sessionImage = opts.PiSessionImage
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
			"annotations": map[string]any{
				"tank-operator/owner-email": owner,
				"argocd.argoproj.io/tracking-id": opts.ArgoCDTrackingApp +
					":/Pod:" + opts.SessionsNamespace + "/" + podName,
			},
		},
		"spec": map[string]any{
			"serviceAccountName": opts.SessionServiceAccount,
			"securityContext": map[string]any{
				"runAsNonRoot": true,
				"runAsUser":    1000,
				"runAsGroup":   1000,
				"fsGroup":      1000,
			},
			"containers": []any{
				map[string]any{
					"name":            "terminal-proxy",
					"image":           opts.TerminalProxyImage,
					"imagePullPolicy": "IfNotPresent",
					"args": []any{
						"--insecure-listen-address=0.0.0.0:" + itoa(opts.TerminalProxyPort),
						"--upstream=http://127.0.0.1:" + itoa(opts.TerminalDPort) + "/",
						"--config-file=/etc/kube-rbac-proxy/config.yaml",
						"--v=2",
					},
					"ports": []any{
						map[string]any{"name": "terminal", "containerPort": opts.TerminalProxyPort},
					},
				},
				map[string]any{
					"name":            "mcp-auth-proxy",
					"image":           sessionImage,
					"imagePullPolicy": "Always",
					"command":         []any{"mcp-auth-proxy"},
				},
				map[string]any{
					"name":            "claude",
					"image":           sessionImage,
					"imagePullPolicy": "Always",
					"env": []any{
						map[string]any{"name": "TERMINALD_PORT", "value": itoa(opts.TerminalDPort)},
						map[string]any{"name": "SANDBOX_AGENT_PORT", "value": itoa(opts.SandboxAgentPort)},
						map[string]any{"name": "TANK_SESSION_MODE", "value": mode},
						map[string]any{"name": "TANK_GLIMMUNG_CONTEXT_JSON", "value": ""},
						map[string]any{"name": "FORCE_HYPERLINK", "value": "1"},
						map[string]any{"name": "CLAUDE_CODE_NO_FLICKER", "value": "1"},
					},
				},
			},
			"volumes": []any{
				map[string]any{"name": "session-config", "configMap": map[string]any{"name": opts.SessionConfigMap}},
				map[string]any{"name": "terminal-proxy-config", "configMap": map[string]any{"name": opts.TerminalProxyConfigMap}},
			},
		},
	}
}

func withManifestDefaults(opts ManifestOptions) ManifestOptions {
	if opts.SessionImage == "" {
		opts.SessionImage = DefaultSessionImage
	}
	if opts.CodexSessionImage == "" {
		opts.CodexSessionImage = DefaultCodexSessionImage
	}
	if opts.PiSessionImage == "" {
		opts.PiSessionImage = DefaultPiSessionImage
	}
	if opts.SessionsNamespace == "" {
		opts.SessionsNamespace = SessionsNamespace
	}
	if opts.SessionServiceAccount == "" {
		opts.SessionServiceAccount = SessionServiceAccount
	}
	if opts.SessionConfigMap == "" {
		opts.SessionConfigMap = SessionConfigMap
	}
	if opts.TerminalProxyConfigMap == "" {
		opts.TerminalProxyConfigMap = TerminalProxyConfigMap
	}
	if opts.TerminalProxyImage == "" {
		opts.TerminalProxyImage = TerminalProxyImage
	}
	if opts.ArgoCDTrackingApp == "" {
		opts.ArgoCDTrackingApp = "tank-operator-sessions"
	}
	if opts.TerminalDPort == 0 {
		opts.TerminalDPort = TerminalDPort
	}
	if opts.TerminalProxyPort == 0 {
		opts.TerminalProxyPort = TerminalProxyPort
	}
	if opts.SandboxAgentPort == 0 {
		opts.SandboxAgentPort = SandboxAgentPort
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
