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
	// SourceHermes is the bridge in backend-go/internal/hermes/ emitting
	// translated events on Hermes Agent's behalf for hermes_gui sessions.
	// Hermes runs in a separate StatefulSet; the bridge consumes its
	// /v1/runs/:id/events SSE and translates to this schema. See
	// nelsong6/tank-operator#540.
	SourceHermes Source = "hermes"
)

type Visibility string

const (
	VisibilityDurable Visibility = "durable"
)

type EventType string

const (
	EventUserMessageCreated     EventType = "user_message.created"
	EventTurnSubmitted          EventType = "turn.submitted"
	EventTurnStarted            EventType = "turn.started"
	EventTurnCompleted          EventType = "turn.completed"
	EventTurnFailed             EventType = "turn.failed"
	EventTurnCommandFailed      EventType = "turn.command_failed"
	EventTurnInterruptRequested EventType = "turn.interrupt_requested"
	EventTurnInterrupted        EventType = "turn.interrupted"
	EventItemStarted            EventType = "item.started"
	EventItemCompleted          EventType = "item.completed"
	EventItemFailed             EventType = "item.failed"
	EventShellTaskStarted       EventType = "shell_task.started"
	EventShellTaskUpdated       EventType = "shell_task.updated"
	EventShellTaskExited        EventType = "shell_task.exited"
	EventApprovalRequested      EventType = "tool.approval_requested"
	EventApprovalResolved       EventType = "tool.approval_resolved"
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
	TaskID         string           `json:"task_id,omitempty"`
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

// SchemaError wraps a permanent ValidateEventMap failure. The persister
// matches against this with errors.As to route schema rejections through a
// terminal NAK path (instead of the retry path used for transient Postgres /
// network failures). Steady-state expectation: zero SchemaErrors on the
// session bus; non-zero indicates a producer is emitting non-Tank events.
type SchemaError struct {
	Cause error
}

func (e *SchemaError) Error() string {
	if e == nil || e.Cause == nil {
		return "schema rejected"
	}
	return "schema rejected: " + e.Cause.Error()
}

func (e *SchemaError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ValidateEventMap validates the runtime JSON shape of a Tank conversation
// event. It intentionally enforces per-type requirements instead of accepting
// any object that happens to have the broad envelope fields. Failures are
// wrapped in SchemaError so callers can branch on permanent vs transient
// failures.
func ValidateEventMap(event map[string]any) error {
	if err := validateEventMap(event); err != nil {
		return &SchemaError{Cause: err}
	}
	return nil
}

func validateEventMap(event map[string]any) error {
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
	case EventTurnInterruptRequested:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorSystem || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("turn.interrupt_requested must be actor=system source=tank")
		}
	case EventItemStarted, EventItemCompleted, EventItemFailed:
		if err := requireFields(event, "turn_id", "timeline_id"); err != nil {
			return err
		}
		if err := requirePayloadString(event, "kind"); err != nil {
			return err
		}
		return validateItemOutcome(event)
	case EventShellTaskStarted, EventShellTaskUpdated, EventShellTaskExited:
		if err := requireFields(event, "turn_id", "timeline_id", "task_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorTool {
			return fmt.Errorf("%s must be actor=tool", eventType)
		}
		return validateShellTaskPayload(event)
	case EventApprovalRequested, EventApprovalResolved:
		if err := requireFields(event, "turn_id", "timeline_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorTool {
			return fmt.Errorf("%s must be actor=tool", eventType)
		}
		if err := requirePayloadString(event, "kind"); err != nil {
			return err
		}
		return validateItemOutcome(event)
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

func validateItemOutcome(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	raw, ok := payload["outcome"]
	if !ok {
		return nil
	}
	outcome, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("payload.outcome must be an object")
	}
	kind := stringField(outcome, "kind")
	reason := stringField(outcome, "reason")
	switch kind {
	case "ok":
		if reason != "" {
			return fmt.Errorf("payload.outcome.reason must be absent for ok")
		}
		return nil
	case "result_failed":
		switch reason {
		case "claude_tool_result_is_error", "codex_item_status_failed", "exit_code":
			return nil
		default:
			return fmt.Errorf("payload.outcome.reason is required for result_failed")
		}
	case "execution_failed":
		if reason == "provider_item_error" {
			return nil
		}
		return fmt.Errorf("payload.outcome.reason is required for execution_failed")
	default:
		return fmt.Errorf("payload.outcome.kind must be ok, result_failed, or execution_failed")
	}
}

func validateShellTaskPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	if stringField(payload, "kind") != "shell_task" {
		return fmt.Errorf("payload.kind must be shell_task for %s", stringField(event, "type"))
	}
	if stringField(payload, "task_id") == "" {
		return fmt.Errorf("payload.task_id is required for %s", stringField(event, "type"))
	}
	if stringField(payload, "status") == "" {
		return fmt.Errorf("payload.status is required for %s", stringField(event, "type"))
	}
	return nil
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
	case SourceTank, SourceClaude, SourceCodex, SourceHermes:
		return true
	default:
		return false
	}
}

func validVisibility(visibility Visibility) bool {
	return visibility == VisibilityDurable
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventUserMessageCreated,
		EventTurnSubmitted,
		EventTurnStarted,
		EventTurnCompleted,
		EventTurnFailed,
		EventTurnCommandFailed,
		EventTurnInterruptRequested,
		EventTurnInterrupted,
		EventItemStarted,
		EventItemCompleted,
		EventItemFailed,
		EventShellTaskStarted,
		EventShellTaskUpdated,
		EventShellTaskExited,
		EventApprovalRequested,
		EventApprovalResolved:
		return true
	default:
		return false
	}
}
