package sessionbus

import "strings"

const (
	CommandSubmitTurn = "submit_turn"
	CommandInterrupt  = "interrupt_turn"
	CommandInputReply = "input_reply"
)

type Command struct {
	SchemaVersion        int    `json:"schema_version"`
	CommandID            string `json:"command_id"`
	Type                 string `json:"type"`
	SessionID            string `json:"session_id"`
	SessionStorageKey    string `json:"session_storage_key"`
	Email                string `json:"email"`
	Provider             string `json:"provider"`
	Source               string `json:"source,omitempty"`
	TurnID               string `json:"turn_id,omitempty"`
	ClientNonce          string `json:"client_nonce,omitempty"`
	Prompt               string `json:"prompt,omitempty"`
	Model                string `json:"model,omitempty"`
	PermissionMode       string `json:"permission_mode,omitempty"`
	SkillName            string `json:"skill_name,omitempty"`
	FollowUp             bool   `json:"follow_up,omitempty"`
	TargetTurnID         string `json:"target_turn_id,omitempty"`
	// TargetTimelineID is the Tank-owned timeline_id of the item the
	// command targets (e.g., an input_reply pointing at the
	// AskUserQuestion tool item). Was named `target_item_id` until the
	// migration from `item_id` to `timeline_id` (#448); renamed to match
	// its content semantics. No consumer reads this field today — it's
	// preserved on the wire for audit/debug visibility.
	TargetTimelineID     string `json:"target_timeline_id,omitempty"`
	TargetProviderItemID string `json:"target_provider_item_id,omitempty"`
	InputReply           string `json:"input_reply,omitempty"`
	CreatedAt            string `json:"created_at"`
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
	c.PermissionMode = strings.TrimSpace(c.PermissionMode)
	c.SkillName = strings.TrimSpace(c.SkillName)
	c.TargetTurnID = strings.TrimSpace(c.TargetTurnID)
	c.TargetTimelineID = strings.TrimSpace(c.TargetTimelineID)
	c.TargetProviderItemID = strings.TrimSpace(c.TargetProviderItemID)
	c.InputReply = strings.TrimSpace(c.InputReply)
	return c
}
