// Package conversation contains Tank's provider-neutral conversation event
// envelope. The JSON Schema is the source of truth; contract tests keep these
// enums in sync with schemas/tank-conversation-event.schema.json.
package conversation

import (
	"fmt"
	"regexp"
	"time"
)

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
	SourceTank   Source = "tank"
	SourceClaude Source = "claude"
	SourceCodex  Source = "codex"
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
	EventTurnCommandFailed    EventType = "turn.command_failed"
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
	TimelineID     string           `json:"timeline_id,omitempty"`
	ProviderItemID string           `json:"provider_item_id,omitempty"`
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

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidateEventMap validates the runtime JSON shape of a Tank conversation
// event. It intentionally enforces per-type requirements instead of accepting
// any object that happens to have the broad envelope fields.
func ValidateEventMap(event map[string]any) error {
	if event == nil {
		return fmt.Errorf("event is required")
	}
	for _, field := range []string{"event_id", "session_id", "actor", "source", "type", "created_at", "visibility"} {
		if stringField(event, field) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	if stringField(event, "order_key") == "" {
		return fmt.Errorf("order_key is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, stringField(event, "created_at")); err != nil {
		return fmt.Errorf("created_at must be RFC3339: %w", err)
	}

	eventType := EventType(stringField(event, "type"))
	if !validEventType(eventType) {
		return fmt.Errorf("unknown event type %q", eventType)
	}
	if !validActor(Actor(stringField(event, "actor"))) {
		return fmt.Errorf("unknown actor %q", stringField(event, "actor"))
	}
	if !validSource(Source(stringField(event, "source"))) {
		return fmt.Errorf("unknown source %q", stringField(event, "source"))
	}
	if !validVisibility(Visibility(stringField(event, "visibility"))) {
		return fmt.Errorf("unknown visibility %q", stringField(event, "visibility"))
	}

	switch eventType {
	case EventUserMessageCreated:
		return validateUserMessageCreated(event)
	case EventTurnSubmitted:
		if err := requireFields(event, "turn_id", "client_nonce"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("turn.submitted must be actor=runner source=tank")
		}
		return requirePayloadString(event, "status")
	case EventTurnStarted, EventTurnCompleted, EventTurnFailed, EventTurnInterrupted:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
	case EventTurnCommandFailed:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorSystem || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("turn.command_failed must be actor=system source=tank")
		}
		return requirePayloadString(event, "reason")
	case EventItemStarted, EventItemDelta, EventItemCompleted, EventItemFailed:
		if err := requireFields(event, "turn_id", "timeline_id"); err != nil {
			return err
		}
		return requirePayloadString(event, "kind")
	case EventApprovalRequested, EventApprovalResolved:
		if err := requireFields(event, "turn_id", "timeline_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorTool {
			return fmt.Errorf("%s must be actor=tool", eventType)
		}
		return requirePayloadString(event, "kind")
	case EventActivityUpdated:
		return requirePayloadString(event, "status")
	case EventReadStateUpdated:
		return requirePayloadString(event, "last_read_order_key")
	}
	return nil
}

func validateUserMessageCreated(event map[string]any) error {
	if err := requireFields(event, "turn_id", "timeline_id", "client_nonce"); err != nil {
		return err
	}
	if Actor(stringField(event, "actor")) != ActorUser || Source(stringField(event, "source")) != SourceTank {
		return fmt.Errorf("user_message.created must be actor=user source=tank")
	}
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	if stringField(payload, "text") == "" {
		return fmt.Errorf("payload.text is required")
	}
	display, ok := payload["display"].(map[string]any)
	if !ok {
		return fmt.Errorf("payload.display is required")
	}
	switch stringField(display, "kind") {
	case "plain":
		return nil
	case "skill_invocation":
		skillName := stringField(display, "skill_name")
		if !skillNamePattern.MatchString(skillName) {
			return fmt.Errorf("payload.display.skill_name is required and must match skill syntax")
		}
		if _, ok := display["supplemental_text"]; ok {
			if _, isString := display["supplemental_text"].(string); !isString {
				return fmt.Errorf("payload.display.supplemental_text must be a string")
			}
		}
		return nil
	default:
		return fmt.Errorf("payload.display.kind must be plain or skill_invocation")
	}
}

func requireFields(event map[string]any, fields ...string) error {
	for _, field := range fields {
		if stringField(event, field) == "" {
			return fmt.Errorf("%s is required for %s", field, stringField(event, "type"))
		}
	}
	return nil
}

func requirePayloadString(event map[string]any, key string) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	if stringField(payload, key) == "" {
		return fmt.Errorf("payload.%s is required for %s", key, stringField(event, "type"))
	}
	return nil
}

func requirePayload(event map[string]any) (map[string]any, error) {
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload is required for %s", stringField(event, "type"))
	}
	return payload, nil
}

func stringField(event map[string]any, field string) string {
	value, _ := event[field].(string)
	return value
}

func validActor(actor Actor) bool {
	switch actor {
	case ActorUser, ActorAssistant, ActorSystem, ActorTool, ActorRunner:
		return true
	default:
		return false
	}
}

func validSource(source Source) bool {
	switch source {
	case SourceTank, SourceClaude, SourceCodex:
		return true
	default:
		return false
	}
}

func validVisibility(visibility Visibility) bool {
	switch visibility {
	case VisibilityDurable, VisibilityLiveOnly, VisibilityAudit:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventConversationStarted,
		EventConversationArchived,
		EventUserMessageCreated,
		EventTurnSubmitted,
		EventTurnStarted,
		EventTurnCompleted,
		EventTurnFailed,
		EventTurnCommandFailed,
		EventTurnInterrupted,
		EventItemStarted,
		EventItemDelta,
		EventItemCompleted,
		EventItemFailed,
		EventApprovalRequested,
		EventApprovalResolved,
		EventActivityUpdated,
		EventReadStateUpdated:
		return true
	default:
		return false
	}
}
