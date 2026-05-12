// Package conversation contains Tank's provider-neutral conversation event
// envelope. Keep these types in sync with
// schemas/tank-conversation-event.schema.json.
package conversation

type Actor string

const (
	ActorUser      Actor = "user"
	ActorAssistant Actor = "assistant"
	ActorSystem    Actor = "system"
	ActorTool      Actor = "tool"
	ActorRunner    Actor = "runner"
)

type Source string

const (
	SourceTank      Source = "tank"
	SourceClaude    Source = "claude"
	SourceCodex     Source = "codex"
	SourceLegacyRun Source = "legacy-run"
)

type Visibility string

const (
	VisibilityDurable  Visibility = "durable"
	VisibilityLiveOnly Visibility = "live-only"
	VisibilityAudit    Visibility = "audit-only"
)

type EventType string

const (
	EventConversationStarted  EventType = "conversation.started"
	EventConversationArchived EventType = "conversation.archived"
	EventUserMessageCreated   EventType = "user_message.created"
	EventTurnSubmitted        EventType = "turn.submitted"
	EventTurnStarted          EventType = "turn.started"
	EventTurnCompleted        EventType = "turn.completed"
	EventTurnFailed           EventType = "turn.failed"
	EventTurnInterrupted      EventType = "turn.interrupted"
	EventItemStarted          EventType = "item.started"
	EventItemDelta            EventType = "item.delta"
	EventItemCompleted        EventType = "item.completed"
	EventItemFailed           EventType = "item.failed"
	EventApprovalRequested    EventType = "tool.approval_requested"
	EventApprovalResolved     EventType = "tool.approval_resolved"
	EventActivityUpdated      EventType = "session.activity_updated"
	EventReadStateUpdated     EventType = "read_state.updated"
)

type ProducerMetadata struct {
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	ProviderEventID string `json:"provider_event_id,omitempty"`
}

type Event struct {
	EventID        string           `json:"event_id"`
	OrderKey       string           `json:"order_key,omitempty"`
	Sequence       *int64           `json:"sequence,omitempty"`
	ConversationID string           `json:"conversation_id,omitempty"`
	SessionID      string           `json:"session_id"`
	TurnID         string           `json:"turn_id,omitempty"`
	ItemID         string           `json:"item_id,omitempty"`
	ParentID       string           `json:"parent_id,omitempty"`
	ClientNonce    string           `json:"client_nonce,omitempty"`
	Actor          Actor            `json:"actor"`
	Source         Source           `json:"source"`
	Type           EventType        `json:"type"`
	CreatedAt      string           `json:"created_at"`
	Producer       ProducerMetadata `json:"producer,omitempty"`
	Visibility     Visibility       `json:"visibility"`
	Payload        map[string]any   `json:"payload,omitempty"`
}
