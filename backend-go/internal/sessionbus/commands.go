package sessionbus

import "strings"

const (
	CommandSubmitTurn         = "submit_turn"
	CommandInterrupt          = "interrupt_turn"
	CommandInputReply         = "input_reply"
	CommandStopBackgroundTask = "stop_background_task"
)

// InputReplyAnnotation mirrors the Claude Agent SDK AskUserQuestion
// annotation schema: `preview` is the option's preview content (HTML
// fragment, if the question used previews) and `notes` is free-text the
// user attached to the selected option. Both are optional. See
// docs/tank-conversation-protocol.md "Durable Claude AskUserQuestion
// answer" for the end-to-end shape.
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
	// TargetTimelineID is the Tank-owned timeline_id of the item the
	// command targets (e.g., an input_reply pointing at the
	// AskUserQuestion tool item). Was named `target_item_id` until the
	// migration from `item_id` to `timeline_id` (#448); renamed to match
	// its content semantics. No consumer reads this field today — it's
	// preserved on the wire for audit/debug visibility.
	TargetTimelineID     string `json:"target_timeline_id,omitempty"`
	TargetProviderItemID string `json:"target_provider_item_id,omitempty"`
	TargetTaskID         string `json:"target_task_id,omitempty"`
	TargetProcessID      string `json:"target_process_id,omitempty"`
	// Answers carries the user's AskUserQuestion selections keyed by
	// question text. Each value is the list of chosen option labels:
	// single-select questions emit a one-element slice, multi-select
	// questions emit one entry per checked box. The runner forwards this
	// shape to the SDK's canUseTool callback as
	// `updatedInput.answers = {question: labels.join(", ")}` (the SDK's
	// own zod preprocess joins arrays with ", "). Empty maps are
	// rejected at the HTTP layer.
	Answers map[string][]string `json:"answers,omitempty"`
	// Annotations carries optional preview/notes the user attached per
	// question, keyed by question text. Passes through verbatim into the
	// SDK's canUseTool `updatedInput.annotations`.
	Annotations map[string]InputReplyAnnotation `json:"annotations,omitempty"`
	CreatedAt   string                          `json:"created_at"`
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
	c.Effort = strings.TrimSpace(c.Effort)
	c.PermissionMode = strings.TrimSpace(c.PermissionMode)
	c.SkillName = strings.TrimSpace(c.SkillName)
	c.TargetTurnID = strings.TrimSpace(c.TargetTurnID)
	c.TargetTimelineID = strings.TrimSpace(c.TargetTimelineID)
	c.TargetProviderItemID = strings.TrimSpace(c.TargetProviderItemID)
	c.TargetTaskID = strings.TrimSpace(c.TargetTaskID)
	c.TargetProcessID = strings.TrimSpace(c.TargetProcessID)
	if len(c.Answers) > 0 {
		normalized := make(map[string][]string, len(c.Answers))
		for question, labels := range c.Answers {
			trimmedQuestion := strings.TrimSpace(question)
			if trimmedQuestion == "" {
				continue
			}
			cleaned := make([]string, 0, len(labels))
			for _, label := range labels {
				trimmed := strings.TrimSpace(label)
				if trimmed != "" {
					cleaned = append(cleaned, trimmed)
				}
			}
			if len(cleaned) > 0 {
				normalized[trimmedQuestion] = cleaned
			}
		}
		if len(normalized) > 0 {
			c.Answers = normalized
		} else {
			c.Answers = nil
		}
	}
	if len(c.Annotations) > 0 {
		normalized := make(map[string]InputReplyAnnotation, len(c.Annotations))
		for question, ann := range c.Annotations {
			trimmedQuestion := strings.TrimSpace(question)
			if trimmedQuestion == "" {
				continue
			}
			cleaned := InputReplyAnnotation{
				Preview: strings.TrimSpace(ann.Preview),
				Notes:   strings.TrimSpace(ann.Notes),
			}
			if cleaned.Preview != "" || cleaned.Notes != "" {
				normalized[trimmedQuestion] = cleaned
			}
		}
		if len(normalized) > 0 {
			c.Annotations = normalized
		} else {
			c.Annotations = nil
		}
	}
	return c
}
