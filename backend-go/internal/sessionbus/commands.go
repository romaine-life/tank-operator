package sessionbus

import "strings"

const (
	CommandSubmitTurn         = "submit_turn"
	CommandInterrupt          = "interrupt_turn"
	CommandInputReply         = "input_reply"
	CommandStopBackgroundTask = "stop_background_task"
)

type InputReplyAnnotation struct {
	Preview string `json:"preview,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

type Command struct {
	SchemaVersion     int    `json:"schema_version"`
	CommandID         string `json:"command_id"`
	Type              string `json:"type"`
	SessionID         string `json:"session_id"`
	SessionStorageKey string `json:"session_storage_key"`
	Email             string `json:"email"`
	Provider          string `json:"provider"`
	Source            string `json:"source,omitempty"`
	TurnID            string `json:"turn_id,omitempty"`
	ClientNonce       string `json:"client_nonce,omitempty"`
	Prompt            string `json:"prompt,omitempty"`
	Model             string `json:"model,omitempty"`
	ProviderSessionID string `json:"provider_session_id,omitempty"`
	// Effort is the reasoning effort level requested by the user at
	// session creation. Claude accepts "low" | "medium" | "high" |
	// "xhigh" | "max"; Codex accepts "low" | "medium" | "high" |
	// "xhigh". Pinned by the runner from the first submit_turn that
	// carries a value; subsequent overrides are ignored because the SDK
	// options object is sealed for the runner's lifetime. Empty string
	// means "use the runner's baked-in default". Allowlist enforcement
	// lives in middleware.go's validateEffort — that's the single point
	// of truth; this field is treated as already-validated when it lands
	// on the wire.
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	SkillName      string `json:"skill_name,omitempty"`
	FollowUp       bool   `json:"follow_up,omitempty"`
	TargetTurnID   string `json:"target_turn_id,omitempty"`
	// TargetTimelineID / TargetProviderItemID identify a specific item a
	// command targets — today the stop_background_task command pointing at
	// a shell-task item (its Tank timeline_id + provider item id).
	TargetTimelineID     string                          `json:"target_timeline_id,omitempty"`
	TargetProviderItemID string                          `json:"target_provider_item_id,omitempty"`
	TargetTaskID         string                          `json:"target_task_id,omitempty"`
	TargetProcessID      string                          `json:"target_process_id,omitempty"`
	Answers              map[string][]string             `json:"answers,omitempty"`
	Annotations          map[string]InputReplyAnnotation `json:"annotations,omitempty"`
	// MCPActivateName / MCPActivateURL ride a break-glass approval submit_turn
	// to auto-surface an MCP server in the runner's registry at the next idle
	// boundary — the activation half of break-glass, so the agent need not
	// re-request. Empty on ordinary turns. Consumed by claude-runner's
	// acceptCommandTurn (registerBreakGlassMcpFromRecord); the per-call grant
	// check in mcp-azure-personal remains the security boundary.
	MCPActivateName string `json:"mcp_activate_name,omitempty"`
	MCPActivateURL  string `json:"mcp_activate_url,omitempty"`
	CreatedAt       string `json:"created_at"`
}

func (c Command) Normalize() Command {
	c.SchemaVersion = 1
	c.Type = strings.TrimSpace(c.Type)
	c.CommandID = strings.TrimSpace(c.CommandID)
	c.SessionID = strings.TrimSpace(c.SessionID)
	c.SessionStorageKey = strings.TrimSpace(c.SessionStorageKey)
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	c.Provider = strings.TrimSpace(c.Provider)
	c.Source = strings.TrimSpace(c.Source)
	c.TurnID = strings.TrimSpace(c.TurnID)
	c.ClientNonce = strings.TrimSpace(c.ClientNonce)
	c.Prompt = strings.TrimSpace(c.Prompt)
	c.Model = strings.TrimSpace(c.Model)
	c.ProviderSessionID = strings.TrimSpace(c.ProviderSessionID)
	c.Effort = strings.TrimSpace(c.Effort)
	c.PermissionMode = strings.TrimSpace(c.PermissionMode)
	c.SkillName = strings.TrimSpace(c.SkillName)
	c.TargetTurnID = strings.TrimSpace(c.TargetTurnID)
	c.TargetTimelineID = strings.TrimSpace(c.TargetTimelineID)
	c.TargetProviderItemID = strings.TrimSpace(c.TargetProviderItemID)
	c.TargetTaskID = strings.TrimSpace(c.TargetTaskID)
	c.TargetProcessID = strings.TrimSpace(c.TargetProcessID)
	c.MCPActivateName = strings.TrimSpace(c.MCPActivateName)
	c.MCPActivateURL = strings.TrimSpace(c.MCPActivateURL)
	return c
}
