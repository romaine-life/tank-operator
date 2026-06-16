// Package conversation contains Tank's provider-neutral conversation event
// envelope. The JSON Schema is the source of truth; contract tests keep these
// enums in sync with schemas/tank-conversation-event.schema.json.
package conversation

import (
	"fmt"
	"regexp"
	"strings"
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

// AuthorKind classifies the principal that authored a user-side turn when it
// is NOT the interactive human session owner. It rides as the top-level
// `author_kind` field on user_message.created (and turn.submitted) events and
// is orthogonal to origin_session_id:
//
//   - absent          -> the interactive human owner typed it; the renderer
//     draws the owner's Gravatar (unchanged default).
//   - AuthorKindSystem -> a non-interactive principal submitted it: the
//     k8s-exchange service identity that launches
//     sessions (role=service) or a human-minted
//     break-glass token (purpose=bot). The renderer
//     draws the session's system identity instead of
//     the human owner. See cmd/tank-operator
//     authorKindForUser for the edge mapping.
//
// origin_session_id (a sibling tank-operator session via the mcp-tank-operator
// handoff) takes precedence when both are present. AuthorKind carries no
// authority; it is purely a durable authorship signal for the transcript.
type AuthorKind string

const (
	AuthorKindSystem AuthorKind = "system"
)

type Source string

const (
	SourceTank   Source = "tank"
	SourceClaude Source = "claude"
	SourceCodex  Source = "codex"
)

// TurnSubmittedSource is an optional payload-level provenance marker for
// backend-originated self-submit paths. The event envelope source remains
// SourceTank for turn.submitted events.
type TurnSubmittedSource string

const (
	TurnSubmittedSourceScheduleWakeup        TurnSubmittedSource = "schedule-wakeup"
	TurnSubmittedSourceBackgroundTask        TurnSubmittedSource = "background-task"
	TurnSubmittedSourceLaunchDispatch        TurnSubmittedSource = "launch-dispatch"
	TurnSubmittedSourceBreakGlassApproval    TurnSubmittedSource = "break-glass-approval"
	TurnSubmittedSourceTestSlotModelApproval TurnSubmittedSource = "test-slot-model-approval"
)

type Visibility string

const (
	VisibilityDurable Visibility = "durable"
)

type EventType string

const (
	EventUserMessageCreated          EventType = "user_message.created"
	EventAssistantMessageCreated     EventType = "assistant_message.created"
	EventTurnSubmitted               EventType = "turn.submitted"
	EventTurnClaimed                 EventType = "turn.claimed"
	EventTurnStarted                 EventType = "turn.started"
	EventTurnUsage                   EventType = "turn.usage"
	EventTurnCompleted               EventType = "turn.completed"
	EventTurnFailed                  EventType = "turn.failed"
	EventTurnCommandFailed           EventType = "turn.command_failed"
	EventTurnInterruptRequested      EventType = "turn.interrupt_requested"
	EventTurnInterrupted             EventType = "turn.interrupted"
	EventTurnInputAnswered           EventType = "turn.input_answered"
	EventContextCompacted            EventType = "context.compacted"
	EventSessionStatus               EventType = "session.status"
	EventItemStarted                 EventType = "item.started"
	EventItemCompleted               EventType = "item.completed"
	EventItemFailed                  EventType = "item.failed"
	EventShellTaskStarted            EventType = "shell_task.started"
	EventShellTaskUpdated            EventType = "shell_task.updated"
	EventShellTaskExited             EventType = "shell_task.exited"
	EventScheduledWakeupUpdated      EventType = "scheduled_wakeup.updated"
	EventTurnAwaitingInput           EventType = "turn.awaiting_input"
	EventTurnAwaitingInputInvocation EventType = "turn.awaiting_input.invocation"
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
	case EventAssistantMessageCreated:
		return validateAssistantMessageCreated(event)
	case EventTurnSubmitted:
		if err := requireFields(event, "turn_id", "client_nonce"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("turn.submitted must be actor=runner source=tank")
		}
		return validateTurnSubmittedPayload(event)
	case EventTurnClaimed, EventTurnStarted, EventTurnFailed, EventTurnInterrupted:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
	case EventTurnUsage:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
		return validateTurnUsagePayload(event)
	case EventTurnCompleted:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
		return validateTurnCompletedPayload(event)
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
	case EventTurnInputAnswered:
		if err := requireFields(event, "turn_id", "timeline_id", "client_nonce"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorUser || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("turn.input_answered must be actor=user source=tank")
		}
		return validateTurnInputAnsweredPayload(event)
	case EventContextCompacted:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
		return validateContextCompactedPayload(event)
	case EventSessionStatus:
		if err := requireFields(event, "timeline_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorSystem || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("session.status must be actor=system source=tank")
		}
		return validateSessionStatusPayload(event)
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
	case EventScheduledWakeupUpdated:
		if err := requireFields(event, "timeline_id", "client_nonce"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorSystem || Source(stringField(event, "source")) != SourceTank {
			return fmt.Errorf("scheduled_wakeup.updated must be actor=system source=tank")
		}
		return validateScheduledWakeupPayload(event)
	case EventTurnAwaitingInput:
		if err := requireFields(event, "turn_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
		return validateAwaitingInputPayload(event)
	case EventTurnAwaitingInputInvocation:
		if err := requireFields(event, "turn_id", "timeline_id"); err != nil {
			return err
		}
		if Actor(stringField(event, "actor")) != ActorRunner {
			return fmt.Errorf("%s must be actor=runner", eventType)
		}
		return validateAwaitingInputPayload(event)
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
	if kind := stringField(event, "author_kind"); kind != "" && AuthorKind(kind) != AuthorKindSystem {
		return fmt.Errorf("author_kind must be empty or %q, got %q", AuthorKindSystem, kind)
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
		return validateUserMessageAttachments(payload["attachments"])
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
		return validateUserMessageAttachments(payload["attachments"])
	default:
		return fmt.Errorf("payload.display.kind must be plain or skill_invocation")
	}
}

func validateAssistantMessageCreated(event map[string]any) error {
	if err := requireFields(event, "turn_id", "timeline_id"); err != nil {
		return err
	}
	if Actor(stringField(event, "actor")) != ActorAssistant {
		return fmt.Errorf("assistant_message.created must be actor=assistant")
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
	case "plain", "ask_user_question":
	default:
		return fmt.Errorf("payload.display.kind must be plain or ask_user_question")
	}
	if raw, ok := payload["awaiting_input"]; ok {
		awaiting, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("payload.awaiting_input must be an object")
		}
		if _, err := awaitingInputQuestions(awaiting); err != nil {
			return fmt.Errorf("payload.awaiting_input.%w", err)
		}
	}
	return nil
}

func validateUserMessageAttachments(raw any) error {
	if raw == nil {
		return nil
	}
	items, ok := raw.([]map[string]any)
	if !ok {
		if generic, isGeneric := raw.([]any); isGeneric {
			items = make([]map[string]any, 0, len(generic))
			for _, item := range generic {
				record, isRecord := item.(map[string]any)
				if !isRecord {
					return fmt.Errorf("payload.attachments items must be objects")
				}
				items = append(items, record)
			}
		} else {
			return fmt.Errorf("payload.attachments must be an array")
		}
	}
	if len(items) > 32 {
		return fmt.Errorf("payload.attachments must have at most 32 items")
	}
	for _, item := range items {
		if stringField(item, "label") == "" {
			return fmt.Errorf("payload.attachments.label is required")
		}
		if stringField(item, "name") == "" {
			return fmt.Errorf("payload.attachments.name is required")
		}
		kind := stringField(item, "kind")
		if kind != "image" && kind != "file" {
			return fmt.Errorf("payload.attachments.kind must be image or file")
		}
		if value, ok := item["path"]; ok {
			if text, isString := value.(string); !isString || strings.TrimSpace(text) == "" {
				return fmt.Errorf("payload.attachments.path must be a non-empty string")
			}
		}
		if value, ok := item["absPath"]; ok {
			if text, isString := value.(string); !isString || strings.TrimSpace(text) == "" {
				return fmt.Errorf("payload.attachments.absPath must be a non-empty string")
			}
		}
		if value, ok := item["size"]; ok {
			if size, isNumber := numericField(value); !isNumber || size < 0 {
				return fmt.Errorf("payload.attachments.size must be non-negative")
			}
		}
	}
	return nil
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

func validateTurnSubmittedPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	if stringField(payload, "status") == "" {
		return fmt.Errorf("payload.status is required for %s", stringField(event, "type"))
	}
	if source := stringField(payload, "source"); source != "" && !validTurnSubmittedSource(TurnSubmittedSource(source)) {
		return fmt.Errorf("payload.source %q is not valid for %s", source, stringField(event, "type"))
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

// validateContextCompactedPayload enforces the context.compacted contract:
// every notice must name WHY the agent's memory changed (trigger), so the
// transcript row is self-describing per docs/quality-timeframes.md's
// "Failure ... states are designed and visible" — compaction is a
// context-state change the user is entitled to see, not silent bookkeeping.
func validateContextCompactedPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	switch stringField(payload, "trigger") {
	case "auto", "manual":
	default:
		return fmt.Errorf("payload.trigger must be auto or manual for %s", stringField(event, "type"))
	}
	if raw, ok := payload["pre_tokens"]; ok {
		if n, isNumber := numericField(raw); !isNumber || n < 0 {
			return fmt.Errorf("payload.pre_tokens must be a non-negative number for %s", stringField(event, "type"))
		}
	}
	return nil
}

func validateSessionStatusPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	status := stringField(payload, "status")
	switch status {
	case "loading", "ready", "failed":
	default:
		return fmt.Errorf("payload.status must be loading, ready, or failed for %s", stringField(event, "type"))
	}
	if stringField(payload, "text") == "" {
		return fmt.Errorf("payload.text is required for %s", stringField(event, "type"))
	}
	if status == "failed" {
		if err := validateSessionStatusFailedExtension(event, payload); err != nil {
			return err
		}
	}
	return nil
}

func validateTurnCompletedPayload(event map[string]any) error {
	rawPayload, ok := event["payload"]
	if !ok {
		return nil
	}
	payload, ok := rawPayload.(map[string]any)
	if !ok {
		return fmt.Errorf("payload must be an object for %s", stringField(event, "type"))
	}
	rawFinal, ok := payload["final_answer"]
	if !ok {
		return nil
	}
	finalAnswer, ok := rawFinal.(map[string]any)
	if !ok {
		return fmt.Errorf("payload.final_answer must be an object for %s", stringField(event, "type"))
	}
	if err := requireNonEmptyStringArray(finalAnswer, "timeline_ids"); err != nil {
		return fmt.Errorf("payload.final_answer.%w", err)
	}
	if _, ok := finalAnswer["provider_item_ids"]; ok {
		if err := requireNonEmptyStringArray(finalAnswer, "provider_item_ids"); err != nil {
			return fmt.Errorf("payload.final_answer.%w", err)
		}
	}
	return nil
}

func validateTurnUsagePayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	usage, ok := payload["usage"]
	if !ok {
		return fmt.Errorf("payload.usage is required for %s", stringField(event, "type"))
	}
	if _, ok := usage.(map[string]any); !ok {
		return fmt.Errorf("payload.usage must be an object for %s", stringField(event, "type"))
	}
	return nil
}

func requireNonEmptyStringArray(record map[string]any, key string) error {
	raw, ok := record[key]
	if !ok {
		return fmt.Errorf("%s is required", key)
	}
	var items []string
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s must be a non-empty string array", key)
			}
			items = append(items, text)
		}
	case []string:
		items = value
	default:
		return fmt.Errorf("%s must be a non-empty string array", key)
	}
	if len(items) == 0 {
		return fmt.Errorf("%s must be a non-empty string array", key)
	}
	for _, text := range items {
		if text == "" {
			return fmt.Errorf("%s must be a non-empty string array", key)
		}
	}
	return nil
}

// validateSessionStatusFailedExtension enforces the contract for the
// extended payload that ships alongside a session.status:failed event.
// The extension lets the SPA render an actionable banner in the
// transcript (e.g. "Codex sign-in expired — Re-sign-in to Codex"). The
// contract intentionally rejects content-free failed banners: every
// failed event must name what's broken (failure_scope + failure_subject)
// and provide either an action affordance OR a non-trivial reason — see
// docs/quality-timeframes.md's "Failure ... states are designed and
// visible". The extension is optional only when no extension fields are
// set at all, which is the legacy boot-status path (pod failure surfaces
// as a bare failed status). Once any extension field is set, the full
// shape is required so future writers can't half-populate.
func validateSessionStatusFailedExtension(event map[string]any, payload map[string]any) error {
	scope := stringField(payload, "failure_scope")
	subject := stringField(payload, "failure_subject")
	reason := stringField(payload, "failure_reason")
	actionRaw, hasAction := payload["action"]
	hasExtension := scope != "" || subject != "" || reason != "" || hasAction

	if !hasExtension {
		// Legacy bare failed status (pod lifecycle) — no extension required.
		return nil
	}

	switch scope {
	case "provider", "session", "pod":
	default:
		return fmt.Errorf("payload.failure_scope must be provider, session, or pod for %s when extension fields are present", stringField(event, "type"))
	}
	if subject == "" {
		return fmt.Errorf("payload.failure_subject is required for %s when failure_scope is set", stringField(event, "type"))
	}

	if hasAction {
		action, ok := actionRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("payload.action must be an object for %s", stringField(event, "type"))
		}
		label := stringField(action, "label")
		href := stringField(action, "href")
		if label == "" || href == "" {
			return fmt.Errorf("payload.action.label and payload.action.href are required for %s", stringField(event, "type"))
		}
	}

	// Reject content-free banners: the user-facing line must explain
	// either WHY (reason) or WHAT TO DO (action). Both is fine; neither
	// is not — that would be a regression to the "Error" pill we just
	// retired.
	if reason == "" && !hasAction {
		return fmt.Errorf("payload.failure_reason or payload.action is required for %s when failure_scope is set", stringField(event, "type"))
	}
	return nil
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

func validateScheduledWakeupPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	if stringField(payload, "kind") != "scheduled_wakeup" {
		return fmt.Errorf("payload.kind must be scheduled_wakeup for %s", stringField(event, "type"))
	}
	if stringField(payload, "wakeup_id") == "" {
		return fmt.Errorf("payload.wakeup_id is required for %s", stringField(event, "type"))
	}
	switch stringField(payload, "status") {
	case "scheduled", "claiming", "fired", "failed", "cancelled":
	default:
		return fmt.Errorf("payload.status is invalid for %s", stringField(event, "type"))
	}
	if stringField(payload, "due_at") == "" {
		return fmt.Errorf("payload.due_at is required for %s", stringField(event, "type"))
	}
	return nil
}

func validateTurnInputAnsweredPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	if stringField(payload, "question_timeline_id") == "" {
		return fmt.Errorf("payload.question_timeline_id is required for %s", stringField(event, "type"))
	}
	if stringField(payload, "provider_item_id") == "" {
		return fmt.Errorf("payload.provider_item_id is required for %s", stringField(event, "type"))
	}
	rawAnswers, ok := payload["answers"].(map[string]any)
	if !ok || len(rawAnswers) == 0 {
		return fmt.Errorf("payload.answers is required for %s", stringField(event, "type"))
	}
	return nil
}

// validateAwaitingInputPayload enforces the turn.awaiting_input payload: the
// AskUserQuestion pause that keeps the asking turn active must carry the
// Tank-canonical questions the user is being asked. The provider item ids ride
// as optional payload fields the answer endpoint targets.
func validateAwaitingInputPayload(event map[string]any) error {
	payload, err := requirePayload(event)
	if err != nil {
		return err
	}
	questions, err := awaitingInputQuestions(payload)
	if err != nil {
		return fmt.Errorf("%w for %s", err, stringField(event, "type"))
	}
	for _, raw := range questions {
		q, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("payload.questions items must be objects for %s", stringField(event, "type"))
		}
		if stringField(q, "question") == "" {
			return fmt.Errorf("payload.questions.question is required for %s", stringField(event, "type"))
		}
	}
	return nil
}

func awaitingInputQuestions(payload map[string]any) ([]any, error) {
	raw, ok := payload["questions"]
	if !ok {
		return nil, fmt.Errorf("payload.questions is required")
	}
	var questions []any
	switch value := raw.(type) {
	case []any:
		questions = value
	case []map[string]any:
		questions = make([]any, 0, len(value))
		for _, q := range value {
			questions = append(questions, q)
		}
	default:
		return nil, fmt.Errorf("payload.questions must be a non-empty array")
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("payload.questions must be a non-empty array")
	}
	return questions, nil
}

func stringField(event map[string]any, field string) string {
	value, _ := event[field].(string)
	return value
}

func numericField(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
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

func validTurnSubmittedSource(source TurnSubmittedSource) bool {
	switch source {
	case TurnSubmittedSourceScheduleWakeup,
		TurnSubmittedSourceBackgroundTask,
		TurnSubmittedSourceLaunchDispatch,
		TurnSubmittedSourceBreakGlassApproval,
		TurnSubmittedSourceTestSlotModelApproval:
		return true
	default:
		return false
	}
}

func validVisibility(visibility Visibility) bool {
	return visibility == VisibilityDurable
}

// IsTurnLifecycleEvent reports whether eventType bounds a turn — i.e.,
// it is the open boundary (turn.submitted) or one of the four terminal
// types. The silent-stranding alert
// (k8s/templates/observability.yaml → TankTurnSilentStranding) compares
// the counts of these types, so the set must stay tight: turn.claimed and
// turn.started are intermediate progress events (not boundaries);
// turn.interrupt_requested is a stop request, not a stop completion; item.* /
// tool.* / session.* are not turn boundaries. Both the runner-side persister
// (sessionbus.persistOneEvent) and the backend-direct path
// (cmd/tank-operator.persistBackendEvent) filter on this predicate
// before incrementing tank_turn_lifecycle_total. See
// docs/features/claude-runners/contract.md → Observability.
func IsTurnLifecycleEvent(eventType EventType) bool {
	if eventType == EventTurnSubmitted {
		return true
	}
	return IsTurnTerminalEvent(eventType)
}

// IsTurnTerminalEvent reports whether eventType closes a turn. These are
// the events that must correlate back to the submitted client_nonce for the
// browser's local run latch and queued-follow-up release logic.
func IsTurnTerminalEvent(eventType EventType) bool {
	switch eventType {
	case EventTurnCompleted,
		EventTurnFailed,
		EventTurnCommandFailed,
		EventTurnInterrupted:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventUserMessageCreated,
		EventAssistantMessageCreated,
		EventTurnSubmitted,
		EventTurnClaimed,
		EventTurnStarted,
		EventTurnUsage,
		EventTurnCompleted,
		EventTurnFailed,
		EventTurnCommandFailed,
		EventTurnInterruptRequested,
		EventTurnInterrupted,
		EventTurnInputAnswered,
		EventContextCompacted,
		EventSessionStatus,
		EventItemStarted,
		EventItemCompleted,
		EventItemFailed,
		EventShellTaskStarted,
		EventShellTaskUpdated,
		EventShellTaskExited,
		EventScheduledWakeupUpdated,
		EventTurnAwaitingInput,
		EventTurnAwaitingInputInvocation:
		return true
	default:
		return false
	}
}
