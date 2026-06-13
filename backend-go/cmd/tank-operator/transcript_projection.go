package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
)

type transcriptProjection struct {
	Entries        []map[string]any
	ActivityBodies map[string]turnActivityBody
}

type turnActivityBody struct {
	TurnID            string
	Status            string
	Entries           []map[string]any
	CompactedEntryIDs []string
	Summary           map[string]any
}

type projectedEntryItem struct {
	entry    map[string]any
	orderKey string
	index    int
}

type projectionState struct {
	messages              []projectedEntryItem
	items                 []*projectionItem
	itemIndex             map[string]int
	backgroundTasks       []*projectionBackgroundTask
	backgroundIndex       map[string]int
	scheduledWakeups      []*projectionScheduledWakeup
	scheduledWakeupIndex  map[string]int
	interruptRequests     []projectedEntryItem
	contextCompactions    []projectedEntryItem
	wakePrompts           []projectedEntryItem
	turnProgress          []projectedEntryItem
	turnUsages            map[string]turnUsageProjection
	turnTerminals         map[string]turnTerminalProjection
	backgroundWakeTurns   map[string]bool
	backgroundWakeTaskIDs map[string]string
	continuationTurns     map[string]bool
	backgroundWakeParents map[string]string
	awaitingInputs        []projectionAwaitingInput
	awaitingInputTools    []projectedEntryItem
	answeredQuestions     map[string]projectionAnsweredInput
	// dismissedQuestions maps a QUESTION turn id to the order key of the
	// terminal that closed it unanswered (Stop dismissal or a superseded
	// restart re-ask — issue #1078; surfaced here per issue #1077 item 4 so
	// the card renders non-answerable and its row's end_order_key advances
	// for SSE delivery).
	dismissedQuestions map[string]string
	runStatus          string
	activeTurnID       string
	activeItemID       string
	needsInput         bool
}

type projectionItem struct {
	ID             string
	TurnID         string
	ParentID       string
	ProviderItemID string
	Actor          string
	Kind           string
	Status         string
	Title          string
	Text           string
	Payload        map[string]any
	OrderKey       string
	SourceEventID  string
	CreatedAt      string
	StartedAt      string
	CompletedAt    string
}

type projectionBackgroundTask struct {
	ID                string
	TaskID            string
	TurnID            string
	ProviderItemID    string
	ToolUseID         string
	Status            string
	Summary           string
	Description       string
	LastToolName      string
	Command           string
	CWD               string
	ProcessID         string
	Output            string
	ExitCode          any
	DurationMS        any
	RawItem           any
	Error             any
	OrderKey          string
	SourceEventID     string
	CreatedAt         string
	StartedAt         string
	UpdatedAt         string
	CompletedAt       string
	CompletedOrderKey string
}

type projectionScheduledWakeup struct {
	ID                string
	WakeupID          string
	TurnID            string
	ClientNonce       string
	ProviderItemID    string
	Status            string
	Prompt            string
	ScheduledAt       string
	DueAt             string
	FiredTurnID       string
	LastError         string
	AttemptCount      any
	OrderKey          string
	SourceEventID     string
	CreatedAt         string
	UpdatedAt         string
	CompletedAt       string
	CompletedOrderKey string
}

type turnTerminalProjection struct {
	TurnID           string
	Status           string
	ClientNonce      string
	OrderKey         string
	Time             string
	SourceEventID    string
	Detail           string
	Usage            any
	UsageObservation any
	FinalAnswerIDs   map[string]bool
}

type turnUsageProjection struct {
	TurnID           string
	OrderKey         string
	EndOrderKey      string
	Time             string
	UpdatedAt        string
	SourceEventID    string
	Usage            any
	UsageObservation any
}

// projectionAwaitingInput captures a turn.awaiting_input handoff: the agent
// asked the user a question as the Tank-visible response. The question-only
// turn's pages render from this (questions + ids), and "answered" is derived
// from a later turn.input_answered event targeting the same question set.
type projectionAwaitingInput struct {
	AskingTurnID       string
	QuestionTurnID     string
	ProviderItemID     string
	ProviderTimelineID string
	TimelineID         string
	Questions          []any
	Plan               string
	QuestionIndex      int
	QuestionSet        int
	OrderKey           string
	Time               string
	SourceEventID      string
}

type projectionAnsweredInput struct {
	Answered    bool
	Answers     map[string]any
	Annotations map[string]any
	// OrderKey of the turn.input_answered event — lifts the awaiting CARD's
	// end_order_key past open SSE cursors when the answered flag flips
	// (issue #1077 item 4).
	OrderKey string
}

func newProjectionState() projectionState {
	return projectionState{
		itemIndex:             map[string]int{},
		backgroundIndex:       map[string]int{},
		scheduledWakeupIndex:  map[string]int{},
		turnUsages:            map[string]turnUsageProjection{},
		turnTerminals:         map[string]turnTerminalProjection{},
		backgroundWakeTurns:   map[string]bool{},
		backgroundWakeTaskIDs: map[string]string{},
		runStatus:             "ready",
	}
}

func projectTranscriptEvents(events []map[string]any) transcriptProjection {
	state := newProjectionState()
	for _, event := range orderedTranscriptEvents(events) {
		state.apply(event)
	}
	state.continuationTurns = state.backgroundTaskContinuationTurns()
	state.backgroundWakeParents = state.backgroundTaskWakeParentTurns()
	flat := state.projectFlatEntries()
	assignSessionStatusOwnership(flat)
	flat = dropOrphanSessionLifecycle(flat)
	projection := compactProjectedTranscript(flat, state.activeTurnID, state.runStatus, state.turnTerminals, state.backgroundWakeTurns, state.continuationTurns, state.backgroundWakeParents)
	projection = foldBackgroundWakeContinuationActivities(projection, state.backgroundWakeParents)
	projection.Entries = filterMainTranscriptQuestionTurnRows(projection.Entries)
	return projection
}

func orderedTranscriptEvents(events []map[string]any) []map[string]any {
	out := append([]map[string]any(nil), events...)
	sort.SliceStable(out, func(i, j int) bool {
		return transcriptEventSortKey(out[i]) < transcriptEventSortKey(out[j])
	})
	return out
}

func transcriptEventSortKey(event map[string]any) string {
	seq := ""
	switch value := event["sequence"].(type) {
	case float64:
		seq = strconv.FormatInt(int64(value), 10)
	case int64:
		seq = strconv.FormatInt(value, 10)
	case int:
		seq = strconv.Itoa(value)
	case string:
		seq = value
	}
	if seq != "" {
		seq = strings.Repeat("0", max(0, 12-len(seq))) + seq
	}
	return strings.Join([]string{
		transcriptString(event, "order_key"),
		transcriptString(event, "created_at"),
		seq,
		transcriptString(event, "event_id"),
	}, "\x1f")
}

func (s *projectionState) apply(event map[string]any) {
	switch transcriptString(event, "type") {
	case "user_message.created":
		s.applyUserMessage(event)
	case "assistant_message.created":
		s.applyAssistantMessage(event)
	case "turn.submitted":
		turnID := transcriptString(event, "turn_id")
		if isBackgroundTaskWakeTurnEvent(event) {
			firstWakeSubmit := !s.backgroundWakeTurns[turnID]
			s.backgroundWakeTurns[turnID] = true
			if taskID := transcriptPayloadString(event, "task_id"); taskID != "" {
				s.backgroundWakeTaskIDs[turnID] = taskID
			}
			// A wake turn carries exactly one logical system-user prompt. If the
			// fire path re-submitted the same continuation (a stale-claim
			// re-fire publishes a second turn.submitted with the same turn_id),
			// only the first projects a prompt — later duplicates must not stack
			// a second identical "A background task you started…" row in the
			// originating turn's activity.
			if firstWakeSubmit {
				s.applyBackgroundWakePrompt(event)
			}
		}
		if _, terminal := s.turnTerminals[turnID]; !terminal {
			s.runStatus = "submitted"
			s.activeTurnID = turnID
			s.needsInput = false
		}
		s.applyTurnProgress(event, "submitted")
	case "turn.claimed":
		if _, terminal := s.turnTerminals[transcriptString(event, "turn_id")]; !terminal {
			s.runStatus = "claimed"
			s.activeTurnID = transcriptString(event, "turn_id")
			s.needsInput = false
		}
		s.applyTurnProgress(event, "claimed")
	case "turn.started":
		if _, terminal := s.turnTerminals[transcriptString(event, "turn_id")]; !terminal {
			s.runStatus = "streaming"
			s.activeTurnID = transcriptString(event, "turn_id")
			s.needsInput = false
		}
		s.applyTurnProgress(event, "started")
	case "turn.usage":
		s.applyTurnUsage(event)
	case "turn.completed":
		s.applyTurnTerminal(event, "completed")
		s.runStatus = "ready"
		s.activeTurnID = ""
		s.activeItemID = ""
		s.needsInput = false
	case "turn.failed", "turn.command_failed":
		s.applyTurnTerminal(event, "failed")
		s.noteQuestionDismissal(event)
		s.runStatus = "error"
		s.activeTurnID = ""
		s.activeItemID = ""
		s.needsInput = false
	case "turn.interrupt_requested":
		s.applyInterruptRequested(event)
	case "turn.interrupted":
		s.applyTurnTerminal(event, "interrupted")
		s.noteQuestionDismissal(event)
		s.runStatus = "stopped"
		s.activeTurnID = ""
		s.activeItemID = ""
		s.needsInput = false
	case "context.compacted":
		s.applyContextCompacted(event)
	case "session.status":
		s.applySessionStatus(event)
	case "item.started":
		s.upsertItem(event, "started")
	case "item.completed":
		s.upsertItem(event, completedProjectionItemStatus(event))
		if s.activeItemID == transcriptString(event, "timeline_id") {
			s.activeItemID = ""
		}
	case "item.failed":
		s.upsertItem(event, "failed")
		if s.activeItemID == transcriptString(event, "timeline_id") {
			s.activeItemID = ""
		}
	case "shell_task.started":
		s.upsertBackgroundTask(event, "running")
	case "shell_task.updated":
		s.upsertBackgroundTask(event, normalizeProjectionBackgroundStatus(transcriptPayloadString(event, "status")))
	case "shell_task.exited":
		status := normalizeProjectionBackgroundStatus(transcriptPayloadString(event, "status"))
		if status == "running" || status == "unknown" {
			status = "completed"
		}
		s.upsertBackgroundTask(event, status)
	case "scheduled_wakeup.updated":
		s.upsertScheduledWakeup(event)
	case "turn.awaiting_input":
		// The agent asked the user a question as the Tank-visible response.
		// The durable question set owns the Turn question page; the main
		// transcript uses the derived assistant_message.created event as the
		// terminal assistant response.
		s.applyAwaitingInput(event)
		s.runStatus = "needs_input"
		s.needsInput = true
		if questionTurnID := transcriptPayloadString(event, "question_turn_id"); questionTurnID != "" {
			s.activeTurnID = questionTurnID
		} else {
			s.activeTurnID = transcriptString(event, "turn_id")
		}
		s.activeItemID = ""
	case "turn.awaiting_input.invocation":
		s.applyAwaitingInputInvocation(event)
	case "turn.input_answered":
		s.applyInputAnswered(event)
		s.applyTurnTerminal(event, "answered")
		s.runStatus = "ready"
		s.activeTurnID = ""
		s.needsInput = false
		s.activeItemID = ""
	}
}

func (s *projectionState) applyUserMessage(event map[string]any) {
	entry := projectUserMessageEvent(event)
	if entry == nil {
		return
	}
	s.messages = append(s.messages, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.messages),
	})
}

func projectUserMessageEvent(event map[string]any) map[string]any {
	text := transcriptPayloadString(event, "text")
	if text == "" {
		text = transcriptPayloadString(event, "message")
	}
	if transcriptString(event, "timeline_id") == "" || transcriptString(event, "turn_id") == "" || transcriptString(event, "client_nonce") == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	entry := map[string]any{
		"id":            transcriptString(event, "timeline_id"),
		"kind":          "message",
		"role":          "user",
		"text":          strings.TrimSpace(text),
		"turnId":        transcriptString(event, "turn_id"),
		"clientNonce":   transcriptString(event, "client_nonce"),
		"time":          transcriptString(event, "created_at"),
		"sourceEventId": transcriptString(event, "event_id"),
		"orderKey":      transcriptString(event, "order_key"),
	}
	if display := transcriptPayloadMap(event, "display"); display != nil {
		entry["display"] = display
	}
	if attachments := transcriptPayloadAttachments(event); len(attachments) > 0 {
		entry["attachments"] = attachments
	}
	if origin := transcriptString(event, "origin_session_id"); origin != "" {
		entry["originSessionId"] = origin
	}
	if originAvatar := transcriptString(event, "origin_session_avatar_id"); originAvatar != "" {
		entry["originSessionAvatarId"] = originAvatar
	}
	// author_kind marks a turn authored by a non-interactive principal (a
	// bot token). The renderer maps it to the session's system identity so
	// the user bubble does not borrow the human owner's Gravatar.
	if authorKind := transcriptString(event, "author_kind"); authorKind != "" {
		entry["authorKind"] = authorKind
	}
	return entry
}

func (s *projectionState) applyAssistantMessage(event map[string]any) {
	text := transcriptPayloadString(event, "text")
	if text == "" {
		text = transcriptPayloadString(event, "message")
	}
	if transcriptString(event, "timeline_id") == "" || transcriptString(event, "turn_id") == "" || strings.TrimSpace(text) == "" {
		return
	}
	entry := map[string]any{
		"id":            transcriptString(event, "timeline_id"),
		"kind":          "message",
		"role":          "assistant",
		"text":          strings.TrimSpace(text),
		"turnId":        transcriptString(event, "turn_id"),
		"time":          transcriptString(event, "created_at"),
		"sourceEventId": transcriptString(event, "event_id"),
		"orderKey":      transcriptString(event, "order_key"),
	}
	if display := transcriptPayloadMap(event, "display"); display != nil {
		entry["display"] = display
	}
	if awaiting := transcriptPayloadMap(event, "awaiting_input"); awaiting != nil {
		entry["awaitingInput"] = projectionAwaitingInputPayloadFromMap(awaiting, false, projectionAnsweredInput{})
	}
	s.messages = append(s.messages, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.messages),
	})
}

func (s *projectionState) applyInputAnswered(event map[string]any) {
	payload := transcriptPayload(event)
	timelineID := transcriptMapString(payload, "question_timeline_id")
	if timelineID == "" {
		return
	}
	if s.answeredQuestions == nil {
		s.answeredQuestions = map[string]projectionAnsweredInput{}
	}
	s.answeredQuestions[timelineID] = projectionAnsweredInput{
		Answered:    true,
		Answers:     transcriptAnyMap(payload["answers"]),
		Annotations: transcriptAnyMap(payload["annotations"]),
		OrderKey:    transcriptString(event, "order_key"),
	}
	for idx := range s.messages {
		awaiting, _ := s.messages[idx].entry["awaitingInput"].(map[string]any)
		if transcriptMapString(awaiting, "timelineId") != timelineID {
			continue
		}
		awaiting["answered"] = true
		if answers := transcriptAnyMap(payload["answers"]); answers != nil {
			awaiting["answers"] = answers
		}
		if annotations := transcriptAnyMap(payload["annotations"]); annotations != nil {
			awaiting["annotations"] = annotations
		}
		// The flip changes the row's payload without any new event for the
		// row itself — advance contentOrderKey so the materialized row's
		// end_order_key moves past open SSE cursors (issue #1077 item 4:
		// a second tab used to keep an answerable card forever).
		markEntryContentOrderKey(s.messages[idx].entry, transcriptString(event, "order_key"))
	}
}

// markEntryContentOrderKey records the order key of the latest event that
// mutated an entry's payload IN PLACE (answered/dismissed flips). The
// transcript-row store lifts it into end_order_key — row identity and
// transcript position (start_order_key / row_cursor) stay untouched.
func markEntryContentOrderKey(entry map[string]any, orderKey string) {
	if entry == nil || orderKey == "" {
		return
	}
	if existing, _ := entry["contentOrderKey"].(string); orderKey > existing {
		entry["contentOrderKey"] = orderKey
	}
}

// noteQuestionDismissal marks a question turn's awaiting cards dismissed
// when a non-answer terminal closes the question turn (Stop dismissal,
// superseded restart re-ask, or a sweep terminal on the shell). Answered
// cards never flip to dismissed — the answer wins.
func (s *projectionState) noteQuestionDismissal(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	isQuestionTurn := false
	for _, awaiting := range s.awaitingInputs {
		if awaiting.QuestionTurnID == turnID {
			isQuestionTurn = true
			break
		}
	}
	if !isQuestionTurn {
		return
	}
	orderKey := transcriptString(event, "order_key")
	if s.dismissedQuestions == nil {
		s.dismissedQuestions = map[string]string{}
	}
	s.dismissedQuestions[turnID] = orderKey
	for idx := range s.messages {
		awaiting, _ := s.messages[idx].entry["awaitingInput"].(map[string]any)
		if awaiting == nil || transcriptMapString(awaiting, "questionTurnId") != turnID {
			continue
		}
		if answered, _ := awaiting["answered"].(bool); answered {
			continue
		}
		awaiting["dismissed"] = true
		markEntryContentOrderKey(s.messages[idx].entry, orderKey)
	}
}

func (s *projectionState) applyTurnProgress(event map[string]any, status string) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	title := "Turn queued"
	detail := "Waiting for the session runner."
	metaKind := "turn_progress"
	switch status {
	case "claimed":
		title = "Turn accepted"
		detail = "Waiting for provider output."
	case "started":
		title = "Turn started"
		detail = "Provider stream is active."
	}
	entry := map[string]any{
		"id":       transcriptString(event, "event_id"),
		"kind":     "meta",
		"metaKind": metaKind,
		"turnId":   turnID,
		"meta": map[string]any{
			"title":    title,
			"detail":   detail,
			"severity": "info",
		},
		"clientNonce":    transcriptString(event, "client_nonce"),
		"time":           transcriptString(event, "created_at"),
		"sourceEventId":  transcriptString(event, "event_id"),
		"orderKey":       transcriptString(event, "order_key"),
		"progressStatus": status,
	}
	s.turnProgress = append(s.turnProgress, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.turnProgress),
	})
}

func (s *projectionState) applySessionStatus(event map[string]any) {
	text := transcriptPayloadString(event, "text")
	if transcriptString(event, "timeline_id") == "" || strings.TrimSpace(text) == "" {
		return
	}
	entry := map[string]any{
		"id":            transcriptString(event, "timeline_id"),
		"kind":          "message",
		"role":          "system",
		"text":          strings.TrimSpace(text),
		"time":          transcriptString(event, "created_at"),
		"sourceEventId": transcriptString(event, "event_id"),
		"orderKey":      transcriptString(event, "order_key"),
	}
	status := transcriptPayloadString(event, "status")
	// Only a plain session-startup notice (Session is loading./ready.) is turn
	// noise that folds into the owning turn. A provider credential banner uses a
	// ".../provider/.../status" timeline — including the recovery "back online"
	// ready, which carries status=ready but must stay visible — and any failed
	// status stays promoted as a top-level system message. Marking only the
	// foldable startup notices keeps both banner classes out of the fold.
	if (status == "loading" || status == "ready") &&
		!strings.Contains(transcriptString(event, "timeline_id"), ":provider:") {
		entry["sessionStatus"] = status
	}
	if status == "failed" {
		entry["severity"] = "error"
	} else {
		entry["severity"] = "info"
	}
	if action := transcriptPayloadMap(event, "action"); action != nil {
		entry["action"] = action
	}
	s.messages = append(s.messages, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.messages),
	})
}

// applyBackgroundWakePrompt records the wake/continuation boundary as a META
// chip, not a user message. The prompt text on turn.submitted.payload.prompt
// is AGENT-DIRECTED harness instruction ("Review the task's output…"); a
// prior design rendered it as a system-authored user bubble, which put raw
// operational instructions in the user's chat voice — the "wake-notice prose
// rendered raw" defect. The chip names what happened in user terms; the full
// agent prompt stays on the entry payload as audit/debug detail.
func (s *projectionState) applyBackgroundWakePrompt(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	text := strings.TrimSpace(transcriptPayloadString(event, "prompt"))
	if turnID == "" || text == "" {
		return
	}
	taskID := strings.TrimSpace(transcriptPayloadString(event, "task_id"))
	title := "Background task finished — agent re-invoked"
	detail := "Tank re-invoked the agent to pick up the result and report the outcome."
	if strings.HasPrefix(text, backgroundWakeLostObservabilityPromptPrefix) {
		// The unknown-status wake (lost observability / runner restart) must
		// not claim the task finished — nobody observed that. The composer
		// (buildBackgroundTaskWakePromptForProvider) and this prefix are
		// pinned together by test.
		title = "Background task lost from view — agent re-invoked"
		detail = "Tank could no longer observe the task and re-invoked the agent to verify its real state and report."
	}
	if transcriptPayloadString(event, "source") == string(conversation.TurnSubmittedSourceAgentContinuation) {
		title = "Agent continued on its own"
		detail = "The agent resumed by itself after its background task finished."
	}
	if taskID != "" {
		detail = "Task " + taskID + ": " + detail
	}
	payload := map[string]any{"prompt": text}
	if taskID != "" {
		payload["task_id"] = taskID
	}
	entry := map[string]any{
		"id":       transcriptString(event, "event_id") + ":wake_prompt",
		"kind":     "meta",
		"metaKind": "background_task_wake",
		"turnId":   turnID,
		"meta": map[string]any{
			"title":    title,
			"detail":   detail,
			"severity": "info",
		},
		"payload":       payload,
		"clientNonce":   transcriptString(event, "client_nonce"),
		"time":          transcriptString(event, "created_at"),
		"sourceEventId": transcriptString(event, "event_id"),
		"orderKey":      transcriptString(event, "order_key"),
		"turnOnly":      true,
		"wakePrompt":    true,
	}
	s.wakePrompts = append(s.wakePrompts, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.wakePrompts),
	})
}

func (s *projectionState) applyTurnTerminal(event map[string]any, status string) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	s.turnTerminals[turnID] = turnTerminalProjection{
		TurnID:           turnID,
		Status:           status,
		ClientNonce:      transcriptString(event, "client_nonce"),
		OrderKey:         transcriptString(event, "order_key"),
		Time:             transcriptString(event, "created_at"),
		SourceEventID:    transcriptString(event, "event_id"),
		Detail:           projectionErrorText(event),
		Usage:            transcriptPayloadValue(event, "usage"),
		UsageObservation: transcriptPayloadValue(event, "usage_observation"),
		FinalAnswerIDs:   projectionFinalAnswerIDs(event),
	}
}

func (s *projectionState) applyTurnUsage(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	usage := transcriptPayloadValue(event, "usage")
	if turnID == "" || usage == nil {
		return
	}
	orderKey := transcriptString(event, "order_key")
	eventTime := transcriptString(event, "created_at")
	existing, ok := s.turnUsages[turnID]
	if ok {
		existing.EndOrderKey = projectionFirstNonEmpty(orderKey, existing.EndOrderKey, existing.OrderKey)
		existing.UpdatedAt = projectionFirstNonEmpty(eventTime, existing.UpdatedAt, existing.Time)
		existing.Usage = usage
		existing.UsageObservation = transcriptPayloadValue(event, "usage_observation")
		s.turnUsages[turnID] = existing
		return
	}
	s.turnUsages[turnID] = turnUsageProjection{
		TurnID:           turnID,
		OrderKey:         orderKey,
		EndOrderKey:      orderKey,
		Time:             eventTime,
		UpdatedAt:        eventTime,
		SourceEventID:    transcriptString(event, "event_id"),
		Usage:            usage,
		UsageObservation: transcriptPayloadValue(event, "usage_observation"),
	}
}

func (s *projectionState) applyAwaitingInput(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	questions := projectionAwaitingInputQuestions(event)
	if len(questions) == 0 {
		return
	}
	payload := transcriptPayload(event)
	s.awaitingInputs = append(s.awaitingInputs, projectionAwaitingInput{
		AskingTurnID:       projectionFirstNonEmpty(transcriptMapString(payload, "asking_turn_id"), turnID),
		QuestionTurnID:     turnID,
		ProviderItemID:     transcriptMapString(payload, "provider_item_id"),
		ProviderTimelineID: transcriptMapString(payload, "provider_timeline_id"),
		TimelineID:         projectionFirstNonEmpty(transcriptMapString(payload, "timeline_id"), transcriptString(event, "timeline_id")),
		Questions:          questions,
		Plan:               transcriptMapString(payload, "plan"),
		QuestionIndex:      projectionAwaitingInputQuestionIndex(event),
		QuestionSet:        projectionAwaitingInputQuestionSet(event),
		OrderKey:           transcriptString(event, "order_key"),
		Time:               transcriptString(event, "created_at"),
		SourceEventID:      transcriptString(event, "event_id"),
	})
}

func projectionAwaitingInputQuestionIndex(event map[string]any) int {
	if raw, ok := transcriptNumeric(transcriptPayloadValue(event, "question_index")); ok {
		return int(raw)
	}
	return 0
}

func projectionAwaitingInputQuestionSet(event map[string]any) int {
	if raw, ok := transcriptNumeric(transcriptPayloadValue(event, "question_set")); ok {
		return int(raw)
	}
	return 0
}

func (s *projectionState) applyAwaitingInputInvocation(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	questions := projectionAwaitingInputQuestions(event)
	if len(questions) == 0 {
		return
	}
	summary := awaitingInputSummary(questions)
	anchor := transcriptPayloadString(event, "timeline_id")
	if anchor == "" {
		anchor = transcriptString(event, "timeline_id")
	}
	if anchor == "" {
		anchor = transcriptString(event, "event_id")
	}
	orderKey := transcriptString(event, "order_key")
	if orderKey != "" {
		orderKey = orderKey + "~ask_user_question"
	}
	entry := map[string]any{
		"id":             anchor + ":ask_user_question_invocation",
		"kind":           "tool",
		"toolName":       "AskUserQuestion",
		"toolStatus":     "completed",
		"toolInput":      projectionFormatValue(map[string]any{"questions": questions}),
		"toolOutput":     "Question set opened on the next turn page.",
		"turnId":         turnID,
		"providerItemId": transcriptPayloadString(event, "provider_item_id"),
		"time":           transcriptString(event, "created_at"),
		"startedAt":      transcriptString(event, "created_at"),
		"completedAt":    transcriptString(event, "created_at"),
		"sourceEventId":  transcriptString(event, "event_id"),
		"orderKey":       orderKey,
	}
	if summary != "" {
		entry["toolSummary"] = summary
	}
	s.awaitingInputTools = append(s.awaitingInputTools, projectedEntryItem{
		entry:    entry,
		orderKey: orderKey,
		index:    len(s.awaitingInputTools),
	})
}

func projectionAwaitingInputQuestions(event map[string]any) []any {
	raw := transcriptPayloadValue(event, "questions")
	questions, _ := raw.([]any)
	return questions
}

func (s *projectionState) applyInterruptRequested(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	entry := map[string]any{
		"id":     transcriptString(event, "event_id"),
		"kind":   "meta",
		"turnId": turnID,
		"meta": map[string]any{
			"title":    "Stop requested",
			"detail":   "Terminating the active turn.",
			"severity": "info",
		},
		"clientNonce":   transcriptString(event, "client_nonce"),
		"time":          transcriptString(event, "created_at"),
		"sourceEventId": transcriptString(event, "event_id"),
		"orderKey":      transcriptString(event, "order_key"),
	}
	s.interruptRequests = append(s.interruptRequests, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.interruptRequests),
	})
	if s.runStatus == "submitted" || s.runStatus == "claimed" || s.runStatus == "streaming" || s.runStatus == "needs_input" {
		s.runStatus = "stopping"
	}
}

// applyContextCompacted records a durable context.compacted event as an
// ordinary mid-turn Turn-activity row. Compaction is intra-turn system noise —
// the same tier as tool calls and reasoning, not part of the settled
// conversation — so it is folded into the turn's collapsed activity shell like
// any other non-final-answer row and is absent from the settled transcript.
// The entry carries its real order_key, so it sorts at the point in the turn
// where compaction happened. It is left out of the settled surface purely by
// being a normal compactable activity row; there is no promotion path and no
// activity-compact opt-out (the prior isProjectionContextCompacted exclusion
// was the bug that made it flash-then-vanish on the turn-detail screen).
func (s *projectionState) applyContextCompacted(event map[string]any) {
	turnID := transcriptString(event, "turn_id")
	if turnID == "" {
		return
	}
	detail := "Earlier context was automatically summarized to reclaim space."
	if transcriptPayloadString(event, "trigger") == "manual" {
		detail = "Earlier context was summarized on request to reclaim space."
	}
	if pre, ok := transcriptPayloadValue(event, "pre_tokens").(float64); ok && pre > 0 {
		detail += " (~" + compactTokenLabel(pre) + " tokens before compaction)"
	}
	entry := map[string]any{
		"id":       transcriptString(event, "event_id"),
		"kind":     "meta",
		"metaKind": "context_compacted",
		"turnId":   turnID,
		"meta": map[string]any{
			"title":    "Context compacted",
			"detail":   detail,
			"severity": "info",
		},
		"clientNonce":   transcriptString(event, "client_nonce"),
		"time":          transcriptString(event, "created_at"),
		"sourceEventId": transcriptString(event, "event_id"),
		"orderKey":      transcriptString(event, "order_key"),
	}
	s.contextCompactions = append(s.contextCompactions, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.contextCompactions),
	})
}

func compactTokenLabel(n float64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", n/1_000_000)
	case n >= 1_000:
		return strconv.Itoa(int(n/1_000)) + "k"
	default:
		return strconv.Itoa(int(n))
	}
}

func (s *projectionState) upsertItem(event map[string]any, status string) {
	if projectionIsCodexUserMessageEcho(event) {
		return
	}
	id := transcriptString(event, "timeline_id")
	turnID := transcriptString(event, "turn_id")
	if id == "" || turnID == "" {
		return
	}
	existingIdx, exists := s.itemIndex[id]
	var existing *projectionItem
	if exists {
		existing = s.items[existingIdx]
	}
	preserveTerminal := existing != nil && status == "started" && isTerminalProjectionItemStatus(existing.Status)
	resolvedStatus := status
	if preserveTerminal {
		resolvedStatus = existing.Status
	}
	payload := map[string]any{}
	if existing != nil {
		for k, v := range existing.Payload {
			payload[k] = v
		}
	}
	for k, v := range transcriptPayload(event) {
		if preserveTerminal {
			if _, ok := payload[k]; !ok {
				payload[k] = v
			}
			continue
		}
		payload[k] = v
	}
	actor := transcriptString(event, "actor")
	if actor == "user" {
		actor = "runner"
	}
	item := &projectionItem{
		ID:             id,
		TurnID:         turnID,
		ParentID:       projectionFirstNonEmpty(existingString(existing, "ParentID"), transcriptString(event, "parent_id")),
		ProviderItemID: projectionFirstNonEmpty(existingString(existing, "ProviderItemID"), transcriptString(event, "provider_item_id")),
		Actor:          actor,
		Kind:           projectionFirstNonEmpty(transcriptPayloadString(event, "kind"), existingString(existing, "Kind"), defaultProjectionItemKind(event)),
		Status:         resolvedStatus,
		Title:          projectionFirstNonEmpty(transcriptPayloadString(event, "title"), existingString(existing, "Title")),
		Text:           projectionFirstNonEmpty(transcriptPayloadString(event, "text"), existingString(existing, "Text")),
		Payload:        payload,
		OrderKey:       projectionFirstNonEmpty(transcriptString(event, "order_key"), existingString(existing, "OrderKey")),
		SourceEventID:  transcriptString(event, "event_id"),
		CreatedAt:      projectionFirstNonEmpty(transcriptString(event, "created_at"), existingString(existing, "CreatedAt")),
		StartedAt:      projectionFirstNonEmpty(existingString(existing, "StartedAt"), existingString(existing, "CreatedAt"), transcriptString(event, "created_at")),
		CompletedAt:    existingString(existing, "CompletedAt"),
	}
	if status == "started" && !preserveTerminal {
		item.StartedAt = transcriptString(event, "created_at")
	}
	if isTerminalProjectionItemStatus(resolvedStatus) {
		item.CompletedAt = projectionFirstNonEmpty(existingString(existing, "CompletedAt"), transcriptString(event, "created_at"))
	}
	if preserveTerminal && existing != nil {
		item.Actor = existing.Actor
		item.Kind = existing.Kind
		item.Title = projectionFirstNonEmpty(existing.Title, transcriptPayloadString(event, "title"))
		item.Text = projectionFirstNonEmpty(existing.Text, transcriptPayloadString(event, "text"))
		item.OrderKey = projectionFirstNonEmpty(existing.OrderKey, transcriptString(event, "order_key"))
		item.SourceEventID = existing.SourceEventID
		item.CreatedAt = projectionFirstNonEmpty(existing.CreatedAt, transcriptString(event, "created_at"))
	}
	if exists {
		s.items[existingIdx] = item
	} else {
		s.itemIndex[id] = len(s.items)
		s.items = append(s.items, item)
	}
	if resolvedStatus == "started" {
		s.activeItemID = id
	}
}

func (s *projectionState) upsertBackgroundTask(event map[string]any, status string) {
	taskID := projectionFirstNonEmpty(transcriptString(event, "task_id"), transcriptPayloadString(event, "task_id"))
	id := transcriptString(event, "timeline_id")
	turnID := transcriptString(event, "turn_id")
	if taskID == "" || id == "" || turnID == "" {
		return
	}
	existingIdx, exists := s.backgroundIndex[id]
	var existing *projectionBackgroundTask
	if exists {
		existing = s.backgroundTasks[existingIdx]
	}
	nextStatus := status
	if existing != nil && isTerminalProjectionBackgroundStatus(existing.Status) && status == "running" {
		nextStatus = existing.Status
	}
	command := transcriptPayloadString(event, "command")
	task := &projectionBackgroundTask{
		ID:                id,
		TaskID:            taskID,
		TurnID:            turnID,
		ProviderItemID:    projectionFirstNonEmpty(transcriptString(event, "provider_item_id"), existingBackgroundString(existing, "ProviderItemID")),
		ToolUseID:         projectionFirstNonEmpty(transcriptPayloadString(event, "tool_use_id"), existingBackgroundString(existing, "ToolUseID")),
		Status:            nextStatus,
		Summary:           projectionFirstNonEmpty(transcriptPayloadString(event, "summary"), command, existingBackgroundString(existing, "Summary")),
		Description:       projectionFirstNonEmpty(transcriptPayloadString(event, "description"), existingBackgroundString(existing, "Description")),
		LastToolName:      projectionFirstNonEmpty(transcriptPayloadString(event, "last_tool_name"), existingBackgroundString(existing, "LastToolName")),
		Command:           projectionFirstNonEmpty(command, existingBackgroundString(existing, "Command")),
		CWD:               projectionFirstNonEmpty(transcriptPayloadString(event, "cwd"), existingBackgroundString(existing, "CWD")),
		ProcessID:         projectionFirstNonEmpty(transcriptPayloadString(event, "process_id"), transcriptPayloadString(event, "processId"), existingBackgroundString(existing, "ProcessID")),
		Output:            projectionFirstNonEmpty(transcriptPayloadString(event, "output"), existingBackgroundString(existing, "Output")),
		ExitCode:          firstNonNil(transcriptPayloadValue(event, "exit_code"), transcriptPayloadValue(event, "exitCode"), existingBackgroundAny(existing, "ExitCode")),
		DurationMS:        firstNonNil(transcriptPayloadValue(event, "duration_ms"), transcriptPayloadValue(event, "durationMs"), existingBackgroundAny(existing, "DurationMS")),
		RawItem:           firstNonNil(transcriptPayloadValue(event, "raw_item"), existingBackgroundAny(existing, "RawItem")),
		Error:             firstNonNil(transcriptPayloadValue(event, "error"), existingBackgroundAny(existing, "Error")),
		OrderKey:          projectionFirstNonEmpty(existingBackgroundString(existing, "OrderKey"), transcriptString(event, "order_key")),
		SourceEventID:     transcriptString(event, "event_id"),
		CreatedAt:         projectionFirstNonEmpty(existingBackgroundString(existing, "CreatedAt"), transcriptString(event, "created_at")),
		StartedAt:         projectionFirstNonEmpty(existingBackgroundString(existing, "StartedAt"), existingBackgroundString(existing, "CreatedAt"), transcriptString(event, "created_at")),
		UpdatedAt:         projectionFirstNonEmpty(transcriptString(event, "created_at"), existingBackgroundString(existing, "UpdatedAt")),
		CompletedAt:       existingBackgroundString(existing, "CompletedAt"),
		CompletedOrderKey: existingBackgroundString(existing, "CompletedOrderKey"),
	}
	if transcriptString(event, "type") == "shell_task.started" {
		task.StartedAt = transcriptString(event, "created_at")
	}
	if isTerminalProjectionBackgroundStatus(nextStatus) {
		task.CompletedAt = projectionFirstNonEmpty(existingBackgroundString(existing, "CompletedAt"), transcriptString(event, "created_at"))
		task.CompletedOrderKey = projectionFirstNonEmpty(existingBackgroundString(existing, "CompletedOrderKey"), transcriptString(event, "order_key"))
	}
	if exists {
		s.backgroundTasks[existingIdx] = task
	} else {
		s.backgroundIndex[id] = len(s.backgroundTasks)
		s.backgroundTasks = append(s.backgroundTasks, task)
	}
}

func (s *projectionState) upsertScheduledWakeup(event map[string]any) {
	payload := transcriptPayload(event)
	wakeupID := transcriptMapString(payload, "wakeup_id")
	if wakeupID == "" {
		return
	}
	id := transcriptString(event, "timeline_id")
	if id == "" {
		id = "scheduled-wakeup:" + wakeupID
	}
	status := normalizeProjectionScheduledWakeupStatus(transcriptMapString(payload, "status"))
	existingIdx, exists := s.scheduledWakeupIndex[id]
	var existing *projectionScheduledWakeup
	if exists {
		existing = s.scheduledWakeups[existingIdx]
	}
	wakeup := &projectionScheduledWakeup{
		ID:                id,
		WakeupID:          wakeupID,
		TurnID:            projectionFirstNonEmpty(transcriptString(event, "turn_id"), transcriptMapString(payload, "scheduled_turn_id"), existingScheduledWakeupString(existing, "TurnID")),
		ClientNonce:       projectionFirstNonEmpty(transcriptString(event, "client_nonce"), existingScheduledWakeupString(existing, "ClientNonce")),
		ProviderItemID:    projectionFirstNonEmpty(transcriptString(event, "provider_item_id"), transcriptMapString(payload, "provider_item_id"), existingScheduledWakeupString(existing, "ProviderItemID")),
		Status:            status,
		Prompt:            projectionFirstNonEmpty(transcriptMapString(payload, "prompt"), existingScheduledWakeupString(existing, "Prompt")),
		ScheduledAt:       projectionFirstNonEmpty(transcriptMapString(payload, "scheduled_at"), existingScheduledWakeupString(existing, "ScheduledAt")),
		DueAt:             projectionFirstNonEmpty(transcriptMapString(payload, "due_at"), existingScheduledWakeupString(existing, "DueAt")),
		FiredTurnID:       projectionFirstNonEmpty(transcriptMapString(payload, "fired_turn_id"), existingScheduledWakeupString(existing, "FiredTurnID")),
		LastError:         projectionFirstNonEmpty(transcriptMapString(payload, "last_error"), existingScheduledWakeupString(existing, "LastError")),
		AttemptCount:      firstNonNil(payload["attempt_count"], existingScheduledWakeupAny(existing, "AttemptCount")),
		OrderKey:          projectionFirstNonEmpty(transcriptString(event, "order_key"), existingScheduledWakeupString(existing, "OrderKey")),
		SourceEventID:     transcriptString(event, "event_id"),
		CreatedAt:         projectionFirstNonEmpty(existingScheduledWakeupString(existing, "CreatedAt"), transcriptString(event, "created_at")),
		UpdatedAt:         projectionFirstNonEmpty(transcriptString(event, "created_at"), existingScheduledWakeupString(existing, "UpdatedAt")),
		CompletedAt:       existingScheduledWakeupString(existing, "CompletedAt"),
		CompletedOrderKey: existingScheduledWakeupString(existing, "CompletedOrderKey"),
	}
	if isTerminalProjectionScheduledWakeupStatus(status) {
		wakeup.CompletedAt = projectionFirstNonEmpty(existingScheduledWakeupString(existing, "CompletedAt"), transcriptString(event, "created_at"))
		wakeup.CompletedOrderKey = projectionFirstNonEmpty(existingScheduledWakeupString(existing, "CompletedOrderKey"), transcriptString(event, "order_key"))
	}
	if exists {
		s.scheduledWakeups[existingIdx] = wakeup
	} else {
		s.scheduledWakeupIndex[id] = len(s.scheduledWakeups)
		s.scheduledWakeups = append(s.scheduledWakeups, wakeup)
	}
}

func (s *projectionState) projectFlatEntries() []map[string]any {
	items := make([]projectedEntryItem, 0, len(s.messages)+len(s.items)+len(s.backgroundTasks)+len(s.scheduledWakeups)+len(s.interruptRequests)+len(s.contextCompactions)+len(s.wakePrompts)+len(s.turnUsages)+len(s.turnTerminals)+len(s.awaitingInputTools))
	items = append(items, s.messages...)
	baseIndex := len(items)
	backgroundProviderIDs := s.backgroundProviderItemIDs()
	for idx, item := range s.items {
		if item.ProviderItemID != "" && backgroundProviderIDs[item.ProviderItemID] {
			continue
		}
		if entry := projectProjectionItem(item); entry != nil {
			items = append(items, projectedEntryItem{
				entry:    entry,
				orderKey: item.OrderKey,
				index:    baseIndex + idx,
			})
		}
	}
	baseIndex += len(s.items)
	for idx, task := range s.backgroundTasks {
		entry := projectProjectionBackgroundTask(task)
		items = append(items, projectedEntryItem{
			entry:    entry,
			orderKey: task.OrderKey,
			index:    baseIndex + idx,
		})
	}
	baseIndex += len(s.backgroundTasks)
	for idx, wakeup := range s.scheduledWakeups {
		entry := projectProjectionScheduledWakeup(wakeup)
		items = append(items, projectedEntryItem{
			entry:    entry,
			orderKey: wakeup.OrderKey,
			index:    baseIndex + idx,
		})
	}
	baseIndex += len(s.scheduledWakeups)
	for idx, request := range s.interruptRequests {
		request.index = baseIndex + idx
		items = append(items, request)
	}
	baseIndex += len(s.interruptRequests)
	for idx, notice := range s.contextCompactions {
		notice.index = baseIndex + idx
		items = append(items, notice)
	}
	baseIndex += len(s.contextCompactions)
	for idx, prompt := range s.wakePrompts {
		prompt.index = baseIndex + idx
		items = append(items, prompt)
	}
	baseIndex += len(s.wakePrompts)
	for idx, progress := range s.turnProgress {
		progress.index = baseIndex + idx
		items = append(items, progress)
	}
	baseIndex += len(s.turnProgress)
	offset := 0
	for _, usage := range s.turnUsages {
		entry := projectTurnUsage(usage)
		items = append(items, projectedEntryItem{
			entry:    entry,
			orderKey: usage.OrderKey,
			index:    baseIndex + offset,
		})
		offset += 1
	}
	baseIndex += len(s.turnUsages)
	offset = 0
	for _, terminal := range s.turnTerminals {
		if terminal.Status == "completed" || terminal.Status == "answered" {
			continue
		}
		isFailed := terminal.Status == "failed"
		title := "Stopped"
		detail := "Turn stopped by user."
		severity := "info"
		if isFailed {
			title = "Turn failed"
			detail = projectionFirstNonEmpty(terminal.Detail, "The provider returned an error.")
			severity = "error"
		}
		entry := map[string]any{
			"id":     "turn-terminal:" + terminal.SourceEventID,
			"kind":   "meta",
			"turnId": terminal.TurnID,
			"meta": map[string]any{
				"title":    title,
				"detail":   detail,
				"severity": severity,
			},
			"clientNonce":   terminal.ClientNonce,
			"time":          terminal.Time,
			"sourceEventId": terminal.SourceEventID,
			"orderKey":      terminal.OrderKey,
		}
		items = append(items, projectedEntryItem{
			entry:    entry,
			orderKey: terminal.OrderKey,
			index:    baseIndex + offset,
		})
		offset += 1
	}
	baseIndex += len(s.turnTerminals)
	for idx, awaiting := range s.awaitingInputs {
		card := projectAwaitingInputCard(awaiting, s.answeredQuestions[awaiting.TimelineID], s.dismissedQuestions[awaiting.QuestionTurnID])
		items = append(items, projectedEntryItem{
			entry:    card,
			orderKey: transcriptMapString(card, "orderKey"),
			index:    baseIndex + idx,
		})
	}
	baseIndex += len(s.awaitingInputs)
	for idx, tool := range s.awaitingInputTools {
		tool.index = baseIndex + idx
		items = append(items, tool)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].orderKey != "" && items[j].orderKey != "" && items[i].orderKey != items[j].orderKey {
			return items[i].orderKey < items[j].orderKey
		}
		if items[i].orderKey != "" && items[j].orderKey == "" {
			return true
		}
		if items[i].orderKey == "" && items[j].orderKey != "" {
			return false
		}
		return items[i].index < items[j].index
	})
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, annotateProjectionTerminal(item.entry, s.turnTerminals))
	}
	return out
}

func (s *projectionState) backgroundProviderItemIDs() map[string]bool {
	out := map[string]bool{}
	for _, task := range s.backgroundTasks {
		if task.ProviderItemID != "" {
			out[task.ProviderItemID] = true
		}
		if task.ToolUseID != "" {
			out[task.ToolUseID] = true
		}
	}
	return out
}

func (s *projectionState) backgroundTaskContinuationTurns() map[string]bool {
	out := map[string]bool{}
	for _, task := range s.backgroundTasks {
		if task == nil || task.TurnID == "" {
			continue
		}
		terminal, ok := s.turnTerminals[task.TurnID]
		if !ok || terminal.Status != "completed" {
			continue
		}
		if task.CompletedOrderKey == "" || task.CompletedOrderKey > terminal.OrderKey {
			out[task.TurnID] = true
		}
	}
	return out
}

func (s *projectionState) backgroundTaskWakeParentTurns() map[string]string {
	return backgroundTaskWakeParentTurnsFromTasks(s.backgroundTasks, s.backgroundWakeTurns, s.backgroundWakeTaskIDs)
}

func backgroundTaskWakeParentTurnsFromTasks(tasks []*projectionBackgroundTask, backgroundWakeTurns map[string]bool, backgroundWakeTaskIDs map[string]string) map[string]string {
	taskParents := map[string]string{}
	wakeTurnParents := map[string]string{}
	for _, task := range tasks {
		if task == nil || task.TaskID == "" || task.TurnID == "" {
			continue
		}
		taskParents[task.TaskID] = task.TurnID
		wakeTurnID := conversation.TurnIDForClientNonce(pgstore.BackgroundTaskWakeClientNonce(task.TaskID))
		if wakeTurnID != "" {
			wakeTurnParents[wakeTurnID] = task.TurnID
		}
	}
	// directParent resolves a single continuation hop: the turn that started the
	// background task whose terminal woke wakeTurnID.
	directParent := func(wakeTurnID string) string {
		if taskID := backgroundWakeTaskIDs[wakeTurnID]; taskID != "" {
			if parent := taskParents[taskID]; parent != "" {
				return parent
			}
		}
		if parent := wakeTurnParents[wakeTurnID]; parent != "" {
			return parent
		}
		return taskParents[backgroundWakeTaskIDFromTurnID(wakeTurnID)]
	}
	out := map[string]string{}
	for wakeTurnID := range backgroundWakeTurns {
		// Walk the continuation chain to the originating *real* (non-bgtask)
		// turn. A wake turn that itself launched a background task is the parent
		// of the next wake turn, so a wake-of-a-wake must collapse all the way
		// to the one user-visible turn that started the chain — never to an
		// intermediate wake turn. The contract is explicit: wake activity is
		// "part of the originating turn, not a second user-visible turn".
		parent := directParent(wakeTurnID)
		guard := map[string]bool{wakeTurnID: true}
		for parent != "" && isBackgroundWakeTurnID(parent) && !guard[parent] {
			guard[parent] = true
			parent = directParent(parent)
		}
		if parent == "" || parent == wakeTurnID || isBackgroundWakeTurnID(parent) {
			continue
		}
		out[wakeTurnID] = parent
	}
	return out
}

// isBackgroundWakeTurnID reports whether turnID is a synthetic background-task
// wake continuation turn (turn_bgtask-<task>) rather than a real user-visible
// turn. Wake turns are continuation mechanics: they fold into the originating
// turn and are never surfaced as standalone turns.
func isBackgroundWakeTurnID(turnID string) bool {
	return strings.HasPrefix(turnID, "turn_bgtask-")
}

func backgroundWakeParentTurnsFromEvents(events []map[string]any) map[string]string {
	state := newProjectionState()
	for _, event := range orderedTranscriptEvents(events) {
		state.apply(event)
	}
	return state.backgroundTaskWakeParentTurns()
}

func backgroundWakeTaskIDFromTurnID(turnID string) string {
	if !strings.HasPrefix(turnID, "turn_bgtask-") {
		return ""
	}
	return strings.TrimPrefix(turnID, "turn_bgtask-")
}

func projectProjectionItem(item *projectionItem) map[string]any {
	if item == nil {
		return nil
	}
	if item.Actor == "assistant" && (item.Kind == "message" || item.Kind == "agent_message") {
		if strings.TrimSpace(item.Text) == "" {
			return nil
		}
		entry := map[string]any{
			"id":             item.ID,
			"kind":           "message",
			"role":           "assistant",
			"text":           strings.TrimSpace(item.Text),
			"turnId":         item.TurnID,
			"providerItemId": item.ProviderItemID,
			"time":           item.CreatedAt,
			"sourceEventId":  item.SourceEventID,
			"orderKey":       item.OrderKey,
		}
		return entry
	}
	if item.Kind == "reasoning" {
		text := projectionFirstNonEmpty(strings.TrimSpace(item.Text), transcriptMapString(item.Payload, "text"))
		if text == "" {
			return nil
		}
		return map[string]any{
			"id":             item.ID,
			"kind":           "reasoning",
			"reasoning":      map[string]any{"text": text},
			"turnId":         item.TurnID,
			"providerItemId": item.ProviderItemID,
			"time":           item.CreatedAt,
			"sourceEventId":  item.SourceEventID,
			"orderKey":       item.OrderKey,
		}
	}
	if isProjectionToolLikeItem(item) {
		entry := map[string]any{
			"id":             item.ID,
			"kind":           "tool",
			"toolInput":      projectionToolInput(item),
			"toolOutput":     projectionToolOutput(item),
			"toolStatus":     item.Status,
			"turnId":         item.TurnID,
			"providerItemId": item.ProviderItemID,
			"time":           projectionFirstNonEmpty(item.StartedAt, item.CreatedAt),
			"startedAt":      projectionFirstNonEmpty(item.StartedAt, item.CreatedAt),
			"completedAt":    item.CompletedAt,
			"sourceEventId":  item.SourceEventID,
			"orderKey":       item.OrderKey,
		}
		for k, v := range projectionToolDisplay(item) {
			entry[k] = v
		}
		return entry
	}
	if strings.TrimSpace(item.Text) == "" {
		return nil
	}
	severity := "info"
	if item.Status == "failed" {
		severity = "error"
	}
	return map[string]any{
		"id":             item.ID,
		"kind":           "meta",
		"meta":           map[string]any{"title": projectionFirstNonEmpty(item.Title, item.Kind), "detail": strings.TrimSpace(item.Text), "severity": severity},
		"turnId":         item.TurnID,
		"providerItemId": item.ProviderItemID,
		"time":           item.CreatedAt,
		"sourceEventId":  item.SourceEventID,
		"orderKey":       item.OrderKey,
	}
}

// projectSessionBackgroundTasks projects a session's background
// (run_in_background) shell-task lifecycle events into their background_task
// entries, in start order. This is the session-level ledger the Background
// screen renders — the same durable shell_task.* events that fold into per-turn
// activity, surfaced as a first-class list (running and recently completed)
// instead of buried inside each turn's collapsed activity. Input is the
// shell_task.* event set (store.ShellTaskEvents); other event types are ignored.
func projectSessionBackgroundTasks(shellTaskEvents []map[string]any) []map[string]any {
	state := newProjectionState()
	for _, event := range orderedTranscriptEvents(shellTaskEvents) {
		state.apply(event)
	}
	out := make([]map[string]any, 0, len(state.backgroundTasks))
	for _, task := range state.backgroundTasks {
		if entry := projectProjectionBackgroundTask(task); entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

func projectProjectionBackgroundTask(task *projectionBackgroundTask) map[string]any {
	entry := map[string]any{
		"id":              task.ID,
		"kind":            "background_task",
		"taskId":          task.TaskID,
		"taskStatus":      task.Status,
		"taskSummary":     task.Summary,
		"taskDescription": task.Description,
		"taskError":       task.Error,
		"taskToolUseId":   task.ToolUseID,
		"taskCommand":     task.Command,
		"taskCwd":         task.CWD,
		"taskProcessId":   task.ProcessID,
		"taskOutput":      task.Output,
		"taskExitCode":    task.ExitCode,
		"taskDurationMs":  task.DurationMS,
		"taskRawItem":     task.RawItem,
		"lastToolName":    task.LastToolName,
		"turnId":          task.TurnID,
		"providerItemId":  task.ProviderItemID,
		"time":            projectionFirstNonEmpty(task.StartedAt, task.CreatedAt, task.UpdatedAt),
		"startedAt":       projectionFirstNonEmpty(task.StartedAt, task.CreatedAt),
		"updatedAt":       task.UpdatedAt,
		"completedAt":     task.CompletedAt,
		"sourceEventId":   task.SourceEventID,
		"orderKey":        task.OrderKey,
	}
	return entry
}

func projectProjectionScheduledWakeup(wakeup *projectionScheduledWakeup) map[string]any {
	if wakeup == nil {
		return nil
	}
	entry := map[string]any{
		"id":                 wakeup.ID,
		"kind":               "background_task",
		"backgroundOnly":     true,
		"taskKind":           "scheduled_wakeup",
		"taskId":             wakeup.WakeupID,
		"taskStatus":         scheduledWakeupTaskStatus(wakeup.Status),
		"taskSummary":        scheduledWakeupSummary(wakeup.Status),
		"taskDescription":    scheduledWakeupDescription(wakeup),
		"taskCommand":        wakeup.Prompt,
		"taskOutput":         scheduledWakeupOutput(wakeup),
		"taskError":          wakeup.LastError,
		"taskRawItem":        scheduledWakeupRawItem(wakeup),
		"turnId":             wakeup.TurnID,
		"clientNonce":        wakeup.ClientNonce,
		"providerItemId":     wakeup.ProviderItemID,
		"time":               projectionFirstNonEmpty(wakeup.ScheduledAt, wakeup.CreatedAt, wakeup.DueAt),
		"startedAt":          projectionFirstNonEmpty(wakeup.ScheduledAt, wakeup.CreatedAt),
		"updatedAt":          projectionFirstNonEmpty(wakeup.UpdatedAt, wakeup.DueAt),
		"completedAt":        wakeup.CompletedAt,
		"sourceEventId":      wakeup.SourceEventID,
		"orderKey":           wakeup.OrderKey,
		"wakeupStatus":       wakeup.Status,
		"wakeupDueAt":        wakeup.DueAt,
		"wakeupScheduledAt":  wakeup.ScheduledAt,
		"wakeupPrompt":       wakeup.Prompt,
		"wakeupFiredTurnId":  wakeup.FiredTurnID,
		"wakeupLastError":    wakeup.LastError,
		"wakeupAttemptCount": wakeup.AttemptCount,
	}
	return entry
}

func projectScheduledWakeupRows(rows []pgstore.ScheduledWakeup) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		wakeup := projectionScheduledWakeupFromRow(row)
		if entry := projectProjectionScheduledWakeup(wakeup); entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

func projectionScheduledWakeupFromRow(row pgstore.ScheduledWakeup) *projectionScheduledWakeup {
	status := normalizeProjectionScheduledWakeupStatus(string(row.Status))
	updated := ""
	completedAt := ""
	if isTerminalProjectionScheduledWakeupStatus(status) {
		completedAt = row.DueAt.UTC().Format(time.RFC3339Nano)
	}
	return &projectionScheduledWakeup{
		ID:             "scheduled-wakeup:" + row.WakeupID,
		WakeupID:       row.WakeupID,
		TurnID:         row.ScheduledTurnID,
		ClientNonce:    row.ClientNonce,
		ProviderItemID: row.ProviderItemID,
		Status:         status,
		Prompt:         row.Prompt,
		ScheduledAt:    row.ScheduledAt.UTC().Format(time.RFC3339Nano),
		DueAt:          row.DueAt.UTC().Format(time.RFC3339Nano),
		FiredTurnID:    row.FiredTurnID,
		LastError:      row.LastError,
		AttemptCount:   row.AttemptCount,
		CreatedAt:      row.ScheduledAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      updated,
		CompletedAt:    completedAt,
	}
}

func projectTurnUsage(usage turnUsageProjection) map[string]any {
	entry := map[string]any{
		"id":       "turn-usage:" + usage.TurnID,
		"kind":     "meta",
		"metaKind": "turn_usage",
		"meta": map[string]any{
			"title":    "Token usage updated",
			"severity": "info",
		},
		"turnId":        usage.TurnID,
		"time":          usage.Time,
		"updatedAt":     usage.UpdatedAt,
		"sourceEventId": usage.SourceEventID,
		"orderKey":      usage.OrderKey,
		"activityEndOrderKey": projectionFirstNonEmpty(
			usage.EndOrderKey,
			usage.OrderKey,
		),
		"turnUsage": usage.Usage,
	}
	if usage.UsageObservation != nil {
		entry["usageObservation"] = usage.UsageObservation
	}
	return entry
}

func annotateProjectionTerminal(entry map[string]any, terminals map[string]turnTerminalProjection) map[string]any {
	turnID := transcriptMapString(entry, "turnId")
	if turnID == "" {
		return entry
	}
	terminal, ok := terminals[turnID]
	if !ok {
		return entry
	}
	out := cloneAnyMap(entry)
	out["turnTerminalStatus"] = terminal.Status
	out["turnTerminalAt"] = terminal.Time
	out["turnTerminalEventId"] = terminal.SourceEventID
	out["turnTerminalOrderKey"] = terminal.OrderKey
	if transcriptMapString(entry, "metaKind") != "turn_usage" {
		if terminal.Usage != nil {
			out["turnUsage"] = terminal.Usage
		}
		if terminal.UsageObservation != nil {
			out["usageObservation"] = terminal.UsageObservation
		}
	}
	return out
}

func compactProjectedTranscript(entries []map[string]any, activeTurnID string, runStatus string, terminals map[string]turnTerminalProjection, backgroundWakeTurns map[string]bool, continuationTurns map[string]bool, wakeParents map[string]string) transcriptProjection {
	handoffActivities := awaitingInputHandoffProjectedActivities(entries, terminals)
	for _, activity := range handoffActivities {
		if activity.TurnID == activeTurnID {
			activeTurnID = ""
			break
		}
	}
	activities := append(terminalProjectedActivities(entries, terminals, backgroundWakeTurns, continuationTurns), handoffActivities...)
	activities = append(activities, activeProjectedActivities(entries, activeTurnID, runStatus)...)
	bodies := map[string]turnActivityBody{}
	for _, activity := range activities {
		bodies[activity.TurnID] = activity
	}
	if len(activities) == 0 {
		return transcriptProjection{Entries: entries, ActivityBodies: bodies}
	}
	activityByInsertIndex := map[int]turnActivityBody{}
	compactedIndexes := map[int]bool{}
	for _, activity := range activities {
		insertBefore := projectedActivityInsertIndex(entries, activity)
		activeProgressOnly := activity.Summary["active"] == true && len(activity.CompactedEntryIDs) == 0 && firstTurnProgressIndex(entries, activity.TurnID) >= 0
		activeNeedsInput := activity.Summary["active"] == true && activity.Status == "needs_input"
		awaitingInputHandoff := activity.Summary["awaitingInputHandoff"] == true
		if len(activity.CompactedEntryIDs) == 0 && !activeProgressOnly && !activeNeedsInput && !awaitingInputHandoff {
			continue
		}
		activityByInsertIndex[insertBefore] = activity
		for _, entryID := range activity.CompactedEntryIDs {
			for idx, entry := range entries {
				if transcriptMapString(entry, "id") == entryID {
					compactedIndexes[idx] = true
				}
			}
		}
	}
	for idx, entry := range entries {
		if isProjectionTurnProgress(entry) {
			compactedIndexes[idx] = true
		}
	}
	out := make([]map[string]any, 0, len(entries))
	for idx, entry := range entries {
		if activity, ok := activityByInsertIndex[idx]; ok {
			// A wake/continuation turn whose originating turn is known never
			// surfaces its own shell: its body folds into the originating
			// turn's shell (foldBackgroundWakeContinuationActivities), which is
			// that content's durable home. Every other turn — including a
			// parked origin turn whose background task outlived the terminal,
			// and a wake turn whose parent edge cannot be derived — emits its
			// shell. Suppressing a shell whose body has no surviving container
			// is how parked turns' content was annihilated from the durable
			// read model (the session-161 bug museum): "parked" is a state on
			// the shell, not grounds for suppression.
			if backgroundWakeTurns[activity.TurnID] && wakeParents[activity.TurnID] != "" {
				continue
			}
			if continuationTurns[activity.TurnID] {
				activity.Summary["continuation"] = true
			}
			out = append(out, buildTurnActivityShellRow(activity, entries))
		}
		if !compactedIndexes[idx] {
			out = append(out, entry)
		}
	}
	return transcriptProjection{Entries: out, ActivityBodies: bodies}
}

// buildTurnActivityShellRow builds the durable turn_activity row for one
// turn's body. turnEntries supplies the user-message anchor lookup — entries
// of other turns are ignored, so callers may pass the whole flat list (batch
// pipeline) or just this turn's entries (checkpointed fold).
func buildTurnActivityShellRow(activity turnActivityBody, turnEntries []map[string]any) map[string]any {
	shellOrderKey := transcriptMapString(activity.Summary, "startOrderKey")
	shellStartedAt := transcriptMapString(activity.Summary, "startedAt")
	if umKey := turnUserMessageOrderKey(turnEntries, activity.TurnID); umKey != "" && shellOrderKey <= umKey {
		// Folded session-startup lifecycle carries order keys that predate
		// the turn's message. The transcript row store positions a
		// turn_activity row by activity.startOrderKey (its row cursor is
		// startOrderKey+id), so anchor the shell's start to the turn's first
		// real event after the message (turn.submitted/started). The
		// lifecycle stays inside the body; only the shell's placement and
		// reported start move to the turn's own start — never above the
		// message it belongs to.
		if anchor := turnFirstEntryAfter(turnEntries, activity.TurnID, umKey); anchor != nil {
			shellOrderKey = transcriptMapString(anchor, "orderKey")
			activity.Summary["startOrderKey"] = shellOrderKey
			if t := transcriptMapString(anchor, "time"); t != "" {
				shellStartedAt = t
				activity.Summary["startedAt"] = t
			}
		}
	}
	shell := map[string]any{
		"id":            "turn-activity-" + activity.TurnID,
		"kind":          "turn_activity",
		"turnId":        activity.TurnID,
		"time":          shellStartedAt,
		"orderKey":      shellOrderKey,
		"activity":      activity.Summary,
		"activityIds":   activity.CompactedEntryIDs,
		"sourceEventId": transcriptMapString(activity.Summary, "sourceEventId"),
	}
	if turnUsage := activity.Summary["turnUsage"]; turnUsage != nil {
		shell["turnUsage"] = turnUsage
	}
	if usageObservation := activity.Summary["usageObservation"]; usageObservation != nil {
		shell["usageObservation"] = usageObservation
	}
	return shell
}

func foldBackgroundWakeContinuationActivities(projection transcriptProjection, wakeParents map[string]string) transcriptProjection {
	if len(wakeParents) == 0 {
		return projection
	}
	out := projection
	out.Entries = reassignBackgroundWakeProjectedEntries(projection.Entries, wakeParents)
	out.ActivityBodies = mergeBackgroundWakeActivityBodies(projection.ActivityBodies, wakeParents)
	out.Entries = refreshFoldedParentShells(out.Entries, out.ActivityBodies, wakeParents)
	return out
}

// refreshFoldedParentShells rewrites the emitted shell row of every turn that
// absorbed a wake/continuation body so the durable shell reflects the merged
// truth — counts, compacted child ids, and the end of the turn's activity span
// extend through the folded continuation. The shell's placement fields
// (orderKey/time) and its anchored start (which compactProjectedTranscript may
// have moved below the turn's user message) are deliberately preserved.
func refreshFoldedParentShells(entries []map[string]any, bodies map[string]turnActivityBody, wakeParents map[string]string) []map[string]any {
	parents := map[string]bool{}
	for _, parent := range wakeParents {
		if parent != "" {
			parents[parent] = true
		}
	}
	if len(parents) == 0 {
		return entries
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		turnID := transcriptMapString(entry, "turnId")
		if transcriptMapString(entry, "kind") != "turn_activity" || !parents[turnID] {
			out = append(out, entry)
			continue
		}
		body, ok := bodies[turnID]
		if !ok {
			out = append(out, entry)
			continue
		}
		next := cloneAnyMap(entry)
		summary := cloneAnyMap(body.Summary)
		if prior, ok := entry["activity"].(map[string]any); ok {
			for _, anchored := range []string{"startOrderKey", "startedAt", "sourceEventId"} {
				if v := transcriptMapString(prior, anchored); v != "" {
					summary[anchored] = v
				}
			}
			if prior["continuation"] == true {
				summary["continuation"] = true
			}
		}
		next["activity"] = summary
		next["activityIds"] = append([]string(nil), body.CompactedEntryIDs...)
		out = append(out, next)
	}
	return out
}

func reassignBackgroundWakeProjectedEntries(entries []map[string]any, wakeParents map[string]string) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		turnID := transcriptMapString(entry, "turnId")
		parent := wakeParents[turnID]
		if parent == "" {
			out = append(out, entry)
			continue
		}
		next := cloneAnyMap(entry)
		next["turnId"] = parent
		next["backendTurnId"] = turnID
		out = append(out, next)
	}
	return out
}

func mergeBackgroundWakeActivityBodies(bodies map[string]turnActivityBody, wakeParents map[string]string) map[string]turnActivityBody {
	if len(bodies) == 0 {
		return bodies
	}
	out := make(map[string]turnActivityBody, len(bodies))
	for turnID, body := range bodies {
		out[turnID] = body
	}
	for wakeTurnID, parentTurnID := range wakeParents {
		wakeBody, ok := out[wakeTurnID]
		if !ok || parentTurnID == "" {
			continue
		}
		parentBody, ok := out[parentTurnID]
		if !ok {
			wakeBody = reassignTurnActivityBody(wakeBody, parentTurnID, wakeTurnID)
			out[parentTurnID] = wakeBody
			delete(out, wakeTurnID)
			continue
		}
		mergedEntries := append([]map[string]any{}, parentBody.Entries...)
		mergedEntries = append(mergedEntries, reassignBackgroundWakeProjectedEntries(wakeBody.Entries, map[string]string{wakeTurnID: parentTurnID})...)
		// The folded body must read chronologically: a wake notice that fired
		// at T+90s renders after the ack the turn gave at T, never above it.
		// Append order is not a contract; the durable order keys are.
		sort.SliceStable(mergedEntries, func(i, j int) bool {
			return transcriptMapString(mergedEntries[i], "orderKey") < transcriptMapString(mergedEntries[j], "orderKey")
		})
		compactedEntries := compactedEntriesForIDs(mergedEntries, append(parentBody.CompactedEntryIDs, wakeBody.CompactedEntryIDs...))
		merged := makeTurnActivityBody(parentTurnID, parentBody.Status, mergedEntries, compactedEntries, transcriptMapBool(parentBody.Summary, "active"))
		if parentBody.Status == "needs_input" {
			merged.Status = parentBody.Status
			merged.Summary["status"] = parentBody.Status
		}
		out[parentTurnID] = merged
		delete(out, wakeTurnID)
	}
	return out
}

func reassignTurnActivityBody(body turnActivityBody, parentTurnID, wakeTurnID string) turnActivityBody {
	entries := reassignBackgroundWakeProjectedEntries(body.Entries, map[string]string{wakeTurnID: parentTurnID})
	compacted := compactedEntriesForIDs(entries, body.CompactedEntryIDs)
	return makeTurnActivityBody(parentTurnID, body.Status, entries, compacted, transcriptMapBool(body.Summary, "active"))
}

func compactedEntriesForIDs(entries []map[string]any, ids []string) []map[string]any {
	if len(ids) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			want[id] = true
		}
	}
	out := make([]map[string]any, 0, len(want))
	for _, entry := range entries {
		if want[transcriptMapString(entry, "id")] {
			out = append(out, entry)
		}
	}
	return out
}

func isBackgroundTaskWakeTurnEvent(event map[string]any) bool {
	if transcriptString(event, "type") != string(conversation.EventTurnSubmitted) {
		return false
	}
	switch transcriptPayloadString(event, "source") {
	case string(conversation.TurnSubmittedSourceBackgroundTask):
		// Claude background-task wake: Tank fired the durable wake row.
		return true
	case string(conversation.TurnSubmittedSourceAgentContinuation):
		// Antigravity self-continuation relay (tank-operator#1030): agy fired
		// its own timer/task and the runner relayed it. Same continuation
		// semantics — the turn folds into the one that started the task and
		// must never surface standalone. Missing from this predicate was half
		// of tank-operator#1035 (the other half: the antigravity runner never
		// published the durable shell_task parent edge).
		return true
	}
	return false
}

func terminalProjectedActivities(entries []map[string]any, terminals map[string]turnTerminalProjection, backgroundWakeTurns map[string]bool, continuationTurns map[string]bool) []turnActivityBody {
	var activities []turnActivityBody
	for _, fold := range groupTurnShellFolds(entries) {
		terminal, ok := terminals[fold.turnID]
		if !ok {
			continue
		}
		if body, ok := fold.finishTerminal(terminal, backgroundWakeTurns[fold.turnID], continuationTurns[fold.turnID]); ok {
			activities = append(activities, body)
		}
	}
	return activities
}

func awaitingInputHandoffProjectedActivities(entries []map[string]any, terminals map[string]turnTerminalProjection) []turnActivityBody {
	var activities []turnActivityBody
	for _, fold := range groupTurnShellFolds(entries) {
		if _, terminal := terminals[fold.turnID]; terminal {
			continue
		}
		if body, ok := fold.finishAwaitingInputHandoff(); ok {
			activities = append(activities, body)
		}
	}
	return activities
}

func activeProjectedActivities(entries []map[string]any, activeTurnID string, runStatus string) []turnActivityBody {
	if activeTurnID == "" {
		return nil
	}
	fold := newTurnShellFold(activeTurnID)
	for _, entry := range entries {
		if transcriptMapString(entry, "turnId") == activeTurnID {
			fold.upsertEntry(entry)
		}
	}
	if body, ok := fold.finishActive(runStatus); ok {
		return []turnActivityBody{body}
	}
	return nil
}

func projectedActivityInsertIndex(entries []map[string]any, activity turnActivityBody) int {
	base := -1
	if idx := firstTurnProgressIndex(entries, activity.TurnID); idx >= 0 {
		base = idx
	} else if len(activity.Entries) > 0 {
		base = projectedEntryIndex(entries, activity.Entries[0])
	}
	// A turn's activity body (its noise bin) must never render above the turn's
	// own user message. Folded session-lifecycle entries can carry order keys
	// that predate the message (the session reported ready after you pressed
	// enter), so clamp the shell to sit just after the user message.
	if um := turnUserMessageIndex(entries, activity.TurnID); um >= 0 && base <= um {
		base = um + 1
	}
	return base
}

func turnUserMessageIndex(entries []map[string]any, turnID string) int {
	for idx, entry := range entries {
		if transcriptMapString(entry, "turnId") == turnID && isProjectedUserMessage(entry) {
			return idx
		}
	}
	return -1
}

func turnUserMessageOrderKey(entries []map[string]any, turnID string) string {
	for _, entry := range entries {
		if transcriptMapString(entry, "turnId") == turnID && isProjectedUserMessage(entry) {
			return transcriptMapString(entry, "orderKey")
		}
	}
	return ""
}

// turnFirstEntryAfter returns the entry with the smallest order key strictly
// greater than afterKey among entries belonging to turnID. Used to anchor a
// turn's activity shell to the turn's first real event after its user message,
// so folded pre-message lifecycle can't drag the shell above the message.
func turnFirstEntryAfter(entries []map[string]any, turnID, afterKey string) map[string]any {
	var best map[string]any
	bestKey := ""
	for _, entry := range entries {
		if transcriptMapString(entry, "turnId") != turnID {
			continue
		}
		ok := transcriptMapString(entry, "orderKey")
		if ok == "" || ok <= afterKey {
			continue
		}
		if bestKey == "" || ok < bestKey {
			bestKey, best = ok, entry
		}
	}
	return best
}

func firstTurnProgressIndex(entries []map[string]any, turnID string) int {
	for idx, entry := range entries {
		if transcriptMapString(entry, "turnId") == turnID && isProjectionTurnProgress(entry) {
			return idx
		}
	}
	return -1
}

func applyActivityAnchorSummary(summary map[string]any, anchors []map[string]any, useAnchorEnd bool) {
	if len(anchors) == 0 {
		return
	}
	first := anchors[0]
	summary["startedAt"] = transcriptMapString(first, "time")
	summary["startOrderKey"] = transcriptMapString(first, "orderKey")
	summary["sourceEventId"] = transcriptMapString(first, "sourceEventId")
	if useAnchorEnd {
		last := anchors[len(anchors)-1]
		summary["lastActivityAt"] = transcriptMapString(last, "time")
		summary["endOrderKey"] = transcriptMapString(last, "orderKey")
	}
}

func makeTurnActivityBody(turnID, status string, activityEntries, compactedEntries []map[string]any, active bool) turnActivityBody {
	compactedIDs := make([]string, 0, len(compactedEntries))
	for _, entry := range compactedEntries {
		if id := transcriptMapString(entry, "id"); id != "" {
			compactedIDs = append(compactedIDs, id)
		}
	}
	summary := turnActivitySummaryMap(activityEntries, compactedEntries, active)
	summary["status"] = status
	summary["active"] = active
	summary["compactedEntryIds"] = compactedIDs
	summary["childCount"] = len(activityEntries)
	summary["turnId"] = turnID
	return turnActivityBody{
		TurnID:            turnID,
		Status:            status,
		Entries:           activityEntries,
		CompactedEntryIDs: compactedIDs,
		Summary:           summary,
	}
}

func turnActivitySummaryMap(activityEntries, compactedEntries []map[string]any, active bool) map[string]any {
	out := map[string]any{
		"toolCount":           0,
		"progressNoteCount":   0,
		"reasoningCount":      0,
		"backgroundTaskCount": 0,
		"questionCount":       0,
		"errorCount":          0,
		"active":              active,
	}
	var snapshotUsage any
	var snapshotObservation any
	for _, entry := range activityEntries {
		if turnUsage := entry["turnUsage"]; turnUsage != nil {
			out["turnUsage"] = turnUsage
		}
		if usageObservation := entry["usageObservation"]; usageObservation != nil {
			out["usageObservation"] = usageObservation
		}
		if transcriptMapString(entry, "metaKind") == "turn_usage" {
			if turnUsage := entry["turnUsage"]; turnUsage != nil {
				snapshotUsage = turnUsage
			}
			if usageObservation := entry["usageObservation"]; usageObservation != nil {
				snapshotObservation = usageObservation
			}
		}
		switch transcriptMapString(entry, "kind") {
		case "tool":
			out["toolCount"] = out["toolCount"].(int) + 1
			status := transcriptMapString(entry, "toolStatus")
			if status == "failed" || status == "error" {
				out["errorCount"] = out["errorCount"].(int) + 1
			}
		case "message":
			if transcriptMapString(entry, "role") == "assistant" {
				out["progressNoteCount"] = out["progressNoteCount"].(int) + 1
			}
		case "reasoning":
			out["reasoningCount"] = out["reasoningCount"].(int) + 1
		case "background_task":
			out["backgroundTaskCount"] = out["backgroundTaskCount"].(int) + 1
			if transcriptMapString(entry, "taskStatus") == "failed" {
				out["errorCount"] = out["errorCount"].(int) + 1
			}
		case "meta":
			if transcriptMapString(entry, "metaKind") == "awaiting_input" {
				out["questionCount"] = out["questionCount"].(int) + 1
			}
			if meta := transcriptMap(entry, "meta"); transcriptMapString(meta, "severity") == "error" {
				out["errorCount"] = out["errorCount"].(int) + 1
			}
		}
	}
	if snapshotUsage != nil {
		out["turnUsage"] = snapshotUsage
	}
	if snapshotObservation != nil {
		out["usageObservation"] = snapshotObservation
	}
	if len(activityEntries) > 0 {
		first := activityEntries[0]
		last := first
		lastOrderKey := projectionActivityEntryEndOrderKey(first)
		for _, entry := range activityEntries[1:] {
			candidate := projectionActivityEntryEndOrderKey(entry)
			if candidate != "" && (lastOrderKey == "" || candidate > lastOrderKey) {
				last = entry
				lastOrderKey = candidate
			}
		}
		out["startedAt"] = projectionFirstNonEmpty(transcriptMapString(first, "startedAt"), transcriptMapString(first, "time"))
		out["completedAt"] = projectionFirstNonEmpty(transcriptMapString(last, "completedAt"), transcriptMapString(last, "turnTerminalAt"), transcriptMapString(last, "time"))
		out["lastActivityAt"] = projectionFirstNonEmpty(
			transcriptMapString(last, "completedAt"),
			transcriptMapString(last, "updatedAt"),
			transcriptMapString(last, "turnTerminalAt"),
			transcriptMapString(last, "time"),
			transcriptMapString(last, "startedAt"),
		)
		out["startOrderKey"] = transcriptMapString(first, "orderKey")
		out["endOrderKey"] = projectionFirstNonEmpty(lastOrderKey, transcriptMapString(last, "orderKey"))
		out["sourceEventId"] = transcriptMapString(first, "sourceEventId")
	}
	out["compactedCount"] = len(compactedEntries)
	return out
}

func projectionActivityEntryEndOrderKey(entry map[string]any) string {
	return projectionFirstNonEmpty(
		transcriptMapString(entry, "turnTerminalOrderKey"),
		transcriptMapString(entry, "activityEndOrderKey"),
		transcriptMapString(entry, "orderKey"),
	)
}

func projectedEntryIndex(entries []map[string]any, target map[string]any) int {
	id := transcriptMapString(target, "id")
	for idx, entry := range entries {
		if transcriptMapString(entry, "id") == id {
			return idx
		}
	}
	return 0
}

func isProjectedUserMessage(entry map[string]any) bool {
	return transcriptMapString(entry, "kind") == "message" && transcriptMapString(entry, "role") == "user"
}

func isProjectionWakePrompt(entry map[string]any) bool {
	return transcriptMapBool(entry, "wakePrompt")
}

func isProjectionTurnProgress(entry map[string]any) bool {
	return transcriptMapString(entry, "kind") == "meta" &&
		transcriptMapString(entry, "metaKind") == "turn_progress"
}

func isProjectionSessionStatus(entry map[string]any) bool {
	_, ok := entry["sessionStatus"]
	return ok
}

// dropOrphanSessionLifecycle removes happy-path session lifecycle (loading/ready)
// that has no owning turn. Such an event is operational noise with nowhere to
// live — a session opened with no message yet, or the per-event materialization
// path projecting a lone session.status — so it produces no transcript row. It
// only surfaces by folding into the turn that adopts it (assignSessionStatusOwnership
// plus the leading-lifecycle adoption in readAllTurnEvents). A failed banner is
// never dropped: failures are surfaced as top-level rows.
func dropOrphanSessionLifecycle(entries []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if isProjectionSessionStatus(entry) &&
			transcriptMapString(entry, "sessionStatus") != "failed" &&
			transcriptMapString(entry, "turnId") == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// assignSessionStatusOwnership folds happy-path session lifecycle (session.status
// loading/ready) into the turn it belongs to, so "Session is loading./ready."
// lives inside that turn's activity body — the noise bin — instead of floating at
// conversation altitude as a top-level system message. The conversation transcript
// is for turns; operational lifecycle is turn-scoped activity.
//
// Ownership is the turn whose epoch contains the event by order key. Lifecycle
// that precedes the first turn's user message (the create-with-initial-turn race,
// where the session reports loading/ready around the same instant you press enter)
// is owned by that first turn, which is why the startup rows can no longer sort
// above your message. A session.status:failed event is left unattached so it stays
// promoted as a top-level banner — failures are exactly the case we surface.
func assignSessionStatusOwnership(entries []map[string]any) {
	type turnAnchor struct{ turnID, orderKey string }
	var anchors []turnAnchor
	for _, entry := range entries {
		if isProjectedUserMessage(entry) {
			anchors = append(anchors, turnAnchor{
				turnID:   transcriptMapString(entry, "turnId"),
				orderKey: transcriptMapString(entry, "orderKey"),
			})
		}
	}
	if len(anchors) == 0 {
		return
	}
	for _, entry := range entries {
		if !isProjectionSessionStatus(entry) ||
			transcriptMapString(entry, "sessionStatus") == "failed" ||
			transcriptMapString(entry, "turnId") != "" {
			continue
		}
		orderKey := transcriptMapString(entry, "orderKey")
		owner := anchors[0].turnID
		for _, a := range anchors {
			if a.orderKey == "" {
				continue
			}
			if orderKey >= a.orderKey {
				owner = a.turnID
			} else {
				break
			}
		}
		if owner != "" {
			entry["turnId"] = owner
		}
	}
}

func isProjectionAwaitingInputEntry(entry map[string]any) bool {
	return transcriptMapString(entry, "kind") == "meta" &&
		transcriptMapString(entry, "metaKind") == "awaiting_input"
}

// projectAwaitingInputCard projects a question turn's turn.awaiting_input pause
// into the Turn question page. The main transcript handoff is the separate
// derived assistant_message.created event. `answered` is derived from a later
// turn.input_answered event, not a browser-local flag, so a fresh tab opened
// after the user answered renders the resolved question set.
func projectAwaitingInputCard(awaiting projectionAwaitingInput, answer projectionAnsweredInput, dismissedOrderKey string) map[string]any {
	summary := awaitingInputSummary(awaiting.Questions)
	title := "I need your input"
	answered := answer.Answered
	dismissed := !answered && dismissedOrderKey != ""
	orderKey := awaiting.OrderKey
	if orderKey != "" {
		orderKey = orderKey + "~awaiting_input"
	}
	anchor := awaiting.TimelineID
	if anchor == "" {
		anchor = awaiting.QuestionTurnID
	}
	awaitingInput := projectionAwaitingInputPayloadFromMap(map[string]any{
		"asking_turn_id":       awaiting.AskingTurnID,
		"question_turn_id":     awaiting.QuestionTurnID,
		"provider_item_id":     awaiting.ProviderItemID,
		"timeline_id":          awaiting.TimelineID,
		"provider_timeline_id": awaiting.ProviderTimelineID,
		"questions":            awaiting.Questions,
		"plan":                 awaiting.Plan,
		"question_index":       awaiting.QuestionIndex,
		"question_set":         awaiting.QuestionSet,
	}, answered, answer)
	if dismissed {
		awaitingInput["dismissed"] = true
	}
	card := map[string]any{
		"id":             anchor + ":awaiting_input",
		"kind":           "meta",
		"metaKind":       "awaiting_input",
		"turnId":         awaiting.QuestionTurnID,
		"providerItemId": awaiting.ProviderItemID,
		"time":           awaiting.Time,
		"orderKey":       orderKey,
		"sourceEventId":  awaiting.SourceEventID,
		"meta": map[string]any{
			"title":    title,
			"detail":   summary,
			"severity": "info",
		},
		"awaitingInput": awaitingInput,
	}
	if dismissed {
		markEntryContentOrderKey(card, dismissedOrderKey)
	}
	if answered {
		markEntryContentOrderKey(card, answer.OrderKey)
	}
	return card
}

func projectionAwaitingInputPayloadFromMap(raw map[string]any, answered bool, answer projectionAnsweredInput) map[string]any {
	questions, _ := raw["questions"].([]any)
	out := map[string]any{
		"askingTurnId":       transcriptMapString(raw, "asking_turn_id"),
		"questionTurnId":     transcriptMapString(raw, "question_turn_id"),
		"providerItemId":     transcriptMapString(raw, "provider_item_id"),
		"timelineId":         transcriptMapString(raw, "timeline_id"),
		"providerTimelineId": transcriptMapString(raw, "provider_timeline_id"),
		"questionCount":      len(questions),
		"questions":          questions,
		"answered":           answered,
	}
	if idx, ok := transcriptNumeric(raw["question_index"]); ok {
		out["questionIndex"] = int(idx)
	}
	if set, ok := transcriptNumeric(raw["question_set"]); ok {
		out["questionSet"] = int(set)
	}
	if answer.Answers != nil {
		out["answers"] = answer.Answers
	}
	if answer.Annotations != nil {
		out["annotations"] = answer.Annotations
	}
	// ExitPlanMode plan-approval pauses carry the plan markdown; the Turns
	// question page renders it above the Approve/Request-changes question.
	if plan := transcriptMapString(raw, "plan"); plan != "" {
		out["plan"] = plan
	}
	return out
}

func awaitingInputSummary(questions []any) string {
	if len(questions) == 0 {
		return "Answer to continue."
	}
	first, _ := questions[0].(map[string]any)
	text := transcriptMapString(first, "question")
	if text == "" {
		text = transcriptMapString(first, "header")
	}
	if text == "" {
		text = "Answer to continue."
	}
	if len([]rune(text)) > 140 {
		runes := []rune(text)
		text = string(runes[:137]) + "…"
	}
	if len(questions) > 1 {
		return fmt.Sprintf("%s (+%d more)", text, len(questions)-1)
	}
	return text
}

func isProjectedAssistantMessage(entry map[string]any) bool {
	return transcriptMapString(entry, "kind") == "message" && transcriptMapString(entry, "role") == "assistant"
}

func filterMainTranscriptQuestionTurnRows(entries []map[string]any) []map[string]any {
	out := entries[:0]
	for _, entry := range entries {
		if isProjectionSessionStatus(entry) && transcriptMapString(entry, "sessionStatus") != "failed" {
			continue
		}
		if isProjectionAwaitingInputEntry(entry) {
			continue
		}
		if isProjectionAwaitingInputToolEntry(entry) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func isProjectionAwaitingInputToolEntry(entry map[string]any) bool {
	return transcriptMapString(entry, "kind") == "tool" &&
		transcriptMapString(entry, "toolName") == "AskUserQuestion" &&
		strings.HasSuffix(transcriptMapString(entry, "id"), ":ask_user_question_invocation")
}

func isQuestionOnlyTurnActivityShell(entry map[string]any) bool {
	if transcriptMapString(entry, "kind") != "turn_activity" {
		return false
	}
	activity, _ := entry["activity"].(map[string]any)
	if transcriptMapString(activity, "status") != "needs_input" {
		return false
	}
	ids, _ := entry["activityIds"].([]string)
	if len(ids) == 0 {
		if count, ok := transcriptNumeric(activity["questionCount"]); ok && count == 1 {
			return true
		}
		return false
	}
	return len(ids) == 1 && strings.Contains(ids[0], ":awaiting_input")
}

func isProjectionTerminalMetaEntry(entry map[string]any, terminal turnTerminalProjection) bool {
	return transcriptMapString(entry, "id") == "turn-terminal:"+terminal.SourceEventID
}

func isProjectionToolLikeItem(item *projectionItem) bool {
	if item == nil {
		return false
	}
	if item.Actor == "tool" {
		return true
	}
	switch item.Kind {
	case "tool", "tool_result", "approval", "needs_input", "command_execution", "file_change", "mcp_tool_call", "web_search":
		return true
	default:
		return false
	}
}

func projectionToolDisplay(item *projectionItem) map[string]any {
	raw := transcriptAnyMap(item.Payload["raw_item"])
	rawServer := transcriptMapString(raw, "server")
	rawTool := transcriptMapString(raw, "tool")
	server := projectionFirstNonEmpty(transcriptMapString(item.Payload, "server"), rawServer)
	action := projectionFirstNonEmpty(transcriptMapString(item.Payload, "tool"), rawTool)
	if item.Kind == "mcp_tool_call" || server != "" || action != "" {
		toolAction := projectionFirstNonEmpty(action, item.Title, "tool")
		toolServer := projectionFirstNonEmpty(server, "mcp")
		return map[string]any{
			"toolName":   toolServer + "." + toolAction,
			"toolKind":   "mcp",
			"toolServer": toolServer,
			"toolAction": toolAction,
		}
	}
	name := projectionFirstNonEmpty(
		transcriptMapString(item.Payload, "name"),
		item.Title,
		transcriptMapString(item.Payload, "title"),
		transcriptMapString(item.Payload, "command"),
		item.Kind,
	)
	out := map[string]any{"toolName": name}
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
		if len(parts) == 2 {
			out["toolKind"] = "mcp"
			out["toolServer"] = parts[0]
			out["toolAction"] = parts[1]
		}
	}
	if item.Kind == "command_execution" || name == "Bash" {
		out["toolKind"] = "shell"
	}
	return out
}

func projectionToolInput(item *projectionItem) string {
	raw := transcriptAnyMap(item.Payload["raw_item"])
	return projectionFormatValue(firstNonNil(
		item.Payload["input"],
		item.Payload["arguments"],
		item.Payload["command"],
		raw["arguments"],
		raw["changes"],
		raw["command"],
	))
}

func projectionToolOutput(item *projectionItem) string {
	raw := transcriptAnyMap(item.Payload["raw_item"])
	return projectionFormatValue(firstNonNil(
		item.Payload["output"],
		item.Payload["result"],
		item.Payload["error"],
		raw["aggregated_output"],
		raw["result"],
		raw["error"],
	))
}

func completedProjectionItemStatus(event map[string]any) string {
	outcome := transcriptAnyMap(transcriptPayloadValue(event, "outcome"))
	if len(outcome) > 0 {
		if transcriptMapString(outcome, "kind") == "result_failed" {
			return "failed"
		}
		return "completed"
	}
	if projectionNonzeroExitCode(transcriptPayloadValue(event, "exit_code")) || projectionNonzeroExitCode(transcriptAnyMap(transcriptPayloadValue(event, "raw_item"))["exit_code"]) {
		return "failed"
	}
	return "completed"
}

func defaultProjectionItemKind(event map[string]any) string {
	if strings.HasPrefix(transcriptString(event, "type"), "tool.") {
		return "approval"
	}
	if transcriptString(event, "actor") == "assistant" {
		return "message"
	}
	return transcriptString(event, "actor")
}

func projectionIsCodexUserMessageEcho(event map[string]any) bool {
	if transcriptString(event, "source") != "codex" {
		return false
	}
	eventType := transcriptString(event, "type")
	if eventType != "item.started" && eventType != "item.completed" && eventType != "item.failed" {
		return false
	}
	raw := transcriptPayloadMap(event, "raw_item")
	return projectionIsUserEchoKind(transcriptPayloadValue(event, "kind")) ||
		projectionIsUserEchoKind(transcriptPayloadValue(event, "title")) ||
		projectionIsUserEchoKind(raw["type"])
}

func projectionIsUserEchoKind(value any) bool {
	return value == "userMessage" || value == "user_message"
}

func projectionNonzeroExitCode(value any) bool {
	switch v := value.(type) {
	case float64:
		return int64(v) != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case string:
		n, err := strconv.Atoi(v)
		return err == nil && n != 0
	default:
		return false
	}
}

func normalizeProjectionBackgroundStatus(status string) string {
	switch strings.ToLower(status) {
	case "running", "completed", "failed", "stopped":
		return strings.ToLower(status)
	default:
		return "unknown"
	}
}

func isTerminalProjectionItemStatus(status string) bool {
	return status == "completed" || status == "failed"
}

func isTerminalProjectionBackgroundStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "stopped"
}

func projectionErrorText(event map[string]any) string {
	if errorValue := transcriptPayloadValue(event, "error"); errorValue != nil {
		if text, ok := errorValue.(string); ok {
			return text
		}
		if record := transcriptAnyMap(errorValue); record != nil {
			return transcriptMapString(record, "message")
		}
	}
	return transcriptPayloadString(event, "reason")
}

func projectionFinalAnswerIDs(event map[string]any) map[string]bool {
	finalAnswer := transcriptPayloadMap(event, "final_answer")
	if len(finalAnswer) == 0 {
		return nil
	}
	ids := map[string]bool{}
	for _, id := range projectionStringArray(finalAnswer["timeline_ids"]) {
		ids[id] = true
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func projectionStringArray(value any) []string {
	var raw []string
	switch items := value.(type) {
	case []string:
		raw = items
	case []any:
		for _, item := range items {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	default:
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := strings.TrimSpace(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func projectionFormatValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func transcriptPayload(event map[string]any) map[string]any {
	return transcriptAnyMap(event["payload"])
}

func transcriptPayloadValue(event map[string]any, key string) any {
	payload := transcriptPayload(event)
	return payload[key]
}

func transcriptPayloadString(event map[string]any, key string) string {
	return transcriptMapString(transcriptPayload(event), key)
}

func transcriptPayloadMap(event map[string]any, key string) map[string]any {
	return transcriptAnyMap(transcriptPayloadValue(event, key))
}

func transcriptPayloadAttachments(event map[string]any) []map[string]any {
	var raw []any
	switch value := transcriptPayloadValue(event, "attachments").(type) {
	case []any:
		raw = value
	case []map[string]any:
		raw = make([]any, 0, len(value))
		for _, item := range value {
			raw = append(raw, item)
		}
	default:
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		record := transcriptAnyMap(item)
		if record == nil {
			continue
		}
		label := projectionFirstNonEmpty(transcriptMapString(record, "label"), transcriptMapString(record, "name"))
		name := projectionFirstNonEmpty(transcriptMapString(record, "name"), label)
		if label == "" || name == "" {
			continue
		}
		kind := transcriptMapString(record, "kind")
		if kind != "image" {
			kind = "file"
		}
		attachment := map[string]any{
			"label": label,
			"name":  name,
			"kind":  kind,
		}
		if path := transcriptMapString(record, "path"); path != "" {
			attachment["path"] = path
		}
		if absPath := projectionFirstNonEmpty(transcriptMapString(record, "absPath"), transcriptMapString(record, "abs_path")); absPath != "" {
			attachment["absPath"] = absPath
		}
		if size, ok := transcriptNumeric(record["size"]); ok && size >= 0 {
			attachment["size"] = size
		}
		out = append(out, attachment)
	}
	return out
}

func transcriptMap(entry map[string]any, key string) map[string]any {
	return transcriptAnyMap(entry[key])
}

func transcriptString(event map[string]any, key string) string {
	return transcriptMapString(event, key)
}

func transcriptMapString(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	value, ok := record[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func transcriptMapBool(record map[string]any, key string) bool {
	if record == nil {
		return false
	}
	value, _ := record[key].(bool)
	return value
}

func transcriptAnyMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if record, ok := value.(map[string]any); ok {
		return record
	}
	if record, ok := value.(map[string]interface{}); ok {
		out := make(map[string]any, len(record))
		for k, v := range record {
			out[k] = v
		}
		return out
	}
	return nil
}

func transcriptNumeric(value any) (float64, bool) {
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

func cloneAnyMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func projectionFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizeProjectionScheduledWakeupStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "claiming":
		return "claiming"
	case "fired":
		return "fired"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	default:
		return "scheduled"
	}
}

func scheduledWakeupTaskStatus(status string) string {
	switch normalizeProjectionScheduledWakeupStatus(status) {
	case "fired":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled":
		return "stopped"
	default:
		return "running"
	}
}

func isTerminalProjectionScheduledWakeupStatus(status string) bool {
	switch normalizeProjectionScheduledWakeupStatus(status) {
	case "fired", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func scheduledWakeupSummary(status string) string {
	switch normalizeProjectionScheduledWakeupStatus(status) {
	case "claiming":
		return "Timer is firing"
	case "fired":
		return "Timer fired"
	case "failed":
		return "Timer failed"
	case "cancelled":
		return "Timer cancelled"
	default:
		return "Timer scheduled"
	}
}

func scheduledWakeupDescription(wakeup *projectionScheduledWakeup) string {
	if wakeup == nil {
		return ""
	}
	parts := []string{}
	if wakeup.DueAt != "" {
		parts = append(parts, "Due "+wakeup.DueAt)
	}
	if wakeup.FiredTurnID != "" {
		parts = append(parts, "Fired turn "+wakeup.FiredTurnID)
	}
	if wakeup.LastError != "" {
		parts = append(parts, "Error "+wakeup.LastError)
	}
	return strings.Join(parts, " · ")
}

func scheduledWakeupOutput(wakeup *projectionScheduledWakeup) string {
	if wakeup == nil {
		return ""
	}
	if wakeup.FiredTurnID != "" {
		return "Fired turn " + wakeup.FiredTurnID
	}
	return ""
}

func scheduledWakeupRawItem(wakeup *projectionScheduledWakeup) map[string]any {
	if wakeup == nil {
		return nil
	}
	return map[string]any{
		"wakeup_id":         wakeup.WakeupID,
		"status":            wakeup.Status,
		"prompt":            wakeup.Prompt,
		"client_nonce":      wakeup.ClientNonce,
		"scheduled_turn_id": wakeup.TurnID,
		"provider_item_id":  wakeup.ProviderItemID,
		"scheduled_at":      wakeup.ScheduledAt,
		"due_at":            wakeup.DueAt,
		"attempt_count":     wakeup.AttemptCount,
		"fired_turn_id":     wakeup.FiredTurnID,
		"last_error":        wakeup.LastError,
	}
}

func existingString(item *projectionItem, field string) string {
	if item == nil {
		return ""
	}
	switch field {
	case "ParentID":
		return item.ParentID
	case "ProviderItemID":
		return item.ProviderItemID
	case "Kind":
		return item.Kind
	case "Title":
		return item.Title
	case "Text":
		return item.Text
	case "OrderKey":
		return item.OrderKey
	case "CreatedAt":
		return item.CreatedAt
	case "StartedAt":
		return item.StartedAt
	case "CompletedAt":
		return item.CompletedAt
	default:
		return ""
	}
}

func existingBackgroundString(item *projectionBackgroundTask, field string) string {
	if item == nil {
		return ""
	}
	switch field {
	case "ProviderItemID":
		return item.ProviderItemID
	case "ToolUseID":
		return item.ToolUseID
	case "Summary":
		return item.Summary
	case "Description":
		return item.Description
	case "LastToolName":
		return item.LastToolName
	case "Command":
		return item.Command
	case "CWD":
		return item.CWD
	case "ProcessID":
		return item.ProcessID
	case "Output":
		return item.Output
	case "OrderKey":
		return item.OrderKey
	case "CreatedAt":
		return item.CreatedAt
	case "StartedAt":
		return item.StartedAt
	case "UpdatedAt":
		return item.UpdatedAt
	case "CompletedAt":
		return item.CompletedAt
	default:
		return ""
	}
}

func existingBackgroundAny(item *projectionBackgroundTask, field string) any {
	if item == nil {
		return nil
	}
	switch field {
	case "ExitCode":
		return item.ExitCode
	case "DurationMS":
		return item.DurationMS
	case "RawItem":
		return item.RawItem
	case "Error":
		return item.Error
	default:
		return nil
	}
}

func existingScheduledWakeupString(item *projectionScheduledWakeup, field string) string {
	if item == nil {
		return ""
	}
	switch field {
	case "TurnID":
		return item.TurnID
	case "ClientNonce":
		return item.ClientNonce
	case "ProviderItemID":
		return item.ProviderItemID
	case "Prompt":
		return item.Prompt
	case "ScheduledAt":
		return item.ScheduledAt
	case "DueAt":
		return item.DueAt
	case "FiredTurnID":
		return item.FiredTurnID
	case "LastError":
		return item.LastError
	case "OrderKey":
		return item.OrderKey
	case "CreatedAt":
		return item.CreatedAt
	case "UpdatedAt":
		return item.UpdatedAt
	case "CompletedAt":
		return item.CompletedAt
	case "CompletedOrderKey":
		return item.CompletedOrderKey
	default:
		return ""
	}
}

func existingScheduledWakeupAny(item *projectionScheduledWakeup, field string) any {
	if item == nil {
		return nil
	}
	switch field {
	case "AttemptCount":
		return item.AttemptCount
	default:
		return nil
	}
}

// backgroundWakeLostObservabilityPromptPrefix is the opening sentence of the
// unknown-status wake prompt (buildBackgroundTaskWakePromptForProvider). The
// projection keys the wake chip's title off it so a lost-observability wake
// never renders as "task finished". Composer and projection are pinned
// together by TestBackgroundWakeChipTitleTracksLostObservability.
const backgroundWakeLostObservabilityPromptPrefix = "Tank lost the ability to observe"
