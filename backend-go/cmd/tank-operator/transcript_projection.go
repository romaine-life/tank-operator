package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const turnActivityEventLimit = 1000

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
	messages          []projectedEntryItem
	items             []*projectionItem
	itemIndex         map[string]int
	backgroundTasks   []*projectionBackgroundTask
	backgroundIndex   map[string]int
	interruptRequests []projectedEntryItem
	turnUsages        map[string]turnUsageProjection
	turnTerminals     map[string]turnTerminalProjection
	runStatus         string
	activeTurnID      string
	activeItemID      string
	needsInput        bool
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
	ID             string
	TaskID         string
	TurnID         string
	ProviderItemID string
	ToolUseID      string
	Status         string
	Summary        string
	Description    string
	LastToolName   string
	Command        string
	CWD            string
	ProcessID      string
	Output         string
	ExitCode       any
	DurationMS     any
	RawItem        any
	Error          any
	OrderKey       string
	SourceEventID  string
	CreatedAt      string
	StartedAt      string
	UpdatedAt      string
	CompletedAt    string
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
	Time             string
	SourceEventID    string
	Usage            any
	UsageObservation any
}

func projectTranscriptEvents(events []map[string]any) transcriptProjection {
	state := projectionState{
		itemIndex:       map[string]int{},
		backgroundIndex: map[string]int{},
		turnUsages:      map[string]turnUsageProjection{},
		turnTerminals:   map[string]turnTerminalProjection{},
		runStatus:       "ready",
	}
	for _, event := range orderedTranscriptEvents(events) {
		state.apply(event)
	}
	flat := state.projectFlatEntries()
	return compactProjectedTranscript(flat, state.activeTurnID, state.turnTerminals)
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
	case "turn.submitted":
		s.runStatus = "submitted"
		s.activeTurnID = transcriptString(event, "turn_id")
		s.needsInput = false
	case "turn.started":
		s.runStatus = "streaming"
		s.activeTurnID = transcriptString(event, "turn_id")
		s.needsInput = false
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
		s.runStatus = "error"
		s.activeTurnID = ""
		s.activeItemID = ""
		s.needsInput = false
	case "turn.interrupt_requested":
		s.applyInterruptRequested(event)
	case "turn.interrupted":
		s.applyTurnTerminal(event, "interrupted")
		s.runStatus = "stopped"
		s.activeTurnID = ""
		s.activeItemID = ""
		s.needsInput = false
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
	case "tool.approval_requested":
		s.runStatus = "needs_input"
		s.needsInput = true
		s.activeTurnID = transcriptString(event, "turn_id")
		s.upsertItem(event, "started")
	case "tool.approval_resolved":
		s.upsertItem(event, completedProjectionItemStatus(event))
		if s.activeItemID == transcriptString(event, "timeline_id") {
			s.activeItemID = ""
		}
		s.needsInput = false
		if s.activeTurnID != "" {
			s.runStatus = "streaming"
		} else {
			s.runStatus = "ready"
		}
	}
}

func (s *projectionState) applyUserMessage(event map[string]any) {
	text := transcriptPayloadString(event, "text")
	if text == "" {
		text = transcriptPayloadString(event, "message")
	}
	if transcriptString(event, "timeline_id") == "" || transcriptString(event, "turn_id") == "" || transcriptString(event, "client_nonce") == "" || strings.TrimSpace(text) == "" {
		return
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
	// author_kind marks a turn authored by a non-interactive principal (a
	// bot token). The renderer maps it to the session's system identity so
	// the user bubble does not borrow the human owner's Gravatar.
	if authorKind := transcriptString(event, "author_kind"); authorKind != "" {
		entry["authorKind"] = authorKind
	}
	s.messages = append(s.messages, projectedEntryItem{
		entry:    entry,
		orderKey: transcriptString(event, "order_key"),
		index:    len(s.messages),
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
	if transcriptPayloadString(event, "status") == "failed" {
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
	s.turnUsages[turnID] = turnUsageProjection{
		TurnID:           turnID,
		OrderKey:         transcriptString(event, "order_key"),
		Time:             transcriptString(event, "created_at"),
		SourceEventID:    transcriptString(event, "event_id"),
		Usage:            usage,
		UsageObservation: transcriptPayloadValue(event, "usage_observation"),
	}
	if _, terminal := s.turnTerminals[turnID]; terminal {
		return
	}
	if s.activeTurnID == "" {
		s.activeTurnID = turnID
	}
	if s.runStatus == "ready" || s.runStatus == "submitted" {
		s.runStatus = "streaming"
	}
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
	if s.runStatus == "submitted" || s.runStatus == "streaming" || s.runStatus == "needs_input" {
		s.runStatus = "stopping"
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
		ID:             id,
		TaskID:         taskID,
		TurnID:         turnID,
		ProviderItemID: projectionFirstNonEmpty(transcriptString(event, "provider_item_id"), existingBackgroundString(existing, "ProviderItemID")),
		ToolUseID:      projectionFirstNonEmpty(transcriptPayloadString(event, "tool_use_id"), existingBackgroundString(existing, "ToolUseID")),
		Status:         nextStatus,
		Summary:        projectionFirstNonEmpty(transcriptPayloadString(event, "summary"), command, existingBackgroundString(existing, "Summary")),
		Description:    projectionFirstNonEmpty(transcriptPayloadString(event, "description"), existingBackgroundString(existing, "Description")),
		LastToolName:   projectionFirstNonEmpty(transcriptPayloadString(event, "last_tool_name"), existingBackgroundString(existing, "LastToolName")),
		Command:        projectionFirstNonEmpty(command, existingBackgroundString(existing, "Command")),
		CWD:            projectionFirstNonEmpty(transcriptPayloadString(event, "cwd"), existingBackgroundString(existing, "CWD")),
		ProcessID:      projectionFirstNonEmpty(transcriptPayloadString(event, "process_id"), transcriptPayloadString(event, "processId"), existingBackgroundString(existing, "ProcessID")),
		Output:         projectionFirstNonEmpty(transcriptPayloadString(event, "output"), existingBackgroundString(existing, "Output")),
		ExitCode:       firstNonNil(transcriptPayloadValue(event, "exit_code"), transcriptPayloadValue(event, "exitCode"), existingBackgroundAny(existing, "ExitCode")),
		DurationMS:     firstNonNil(transcriptPayloadValue(event, "duration_ms"), transcriptPayloadValue(event, "durationMs"), existingBackgroundAny(existing, "DurationMS")),
		RawItem:        firstNonNil(transcriptPayloadValue(event, "raw_item"), existingBackgroundAny(existing, "RawItem")),
		Error:          firstNonNil(transcriptPayloadValue(event, "error"), existingBackgroundAny(existing, "Error")),
		OrderKey:       projectionFirstNonEmpty(existingBackgroundString(existing, "OrderKey"), transcriptString(event, "order_key")),
		SourceEventID:  transcriptString(event, "event_id"),
		CreatedAt:      projectionFirstNonEmpty(existingBackgroundString(existing, "CreatedAt"), transcriptString(event, "created_at")),
		StartedAt:      projectionFirstNonEmpty(existingBackgroundString(existing, "StartedAt"), existingBackgroundString(existing, "CreatedAt"), transcriptString(event, "created_at")),
		UpdatedAt:      projectionFirstNonEmpty(transcriptString(event, "created_at"), existingBackgroundString(existing, "UpdatedAt")),
		CompletedAt:    existingBackgroundString(existing, "CompletedAt"),
	}
	if transcriptString(event, "type") == "shell_task.started" {
		task.StartedAt = transcriptString(event, "created_at")
	}
	if isTerminalProjectionBackgroundStatus(nextStatus) {
		task.CompletedAt = projectionFirstNonEmpty(existingBackgroundString(existing, "CompletedAt"), transcriptString(event, "created_at"))
	}
	if exists {
		s.backgroundTasks[existingIdx] = task
	} else {
		s.backgroundIndex[id] = len(s.backgroundTasks)
		s.backgroundTasks = append(s.backgroundTasks, task)
	}
}

func (s *projectionState) projectFlatEntries() []map[string]any {
	items := make([]projectedEntryItem, 0, len(s.messages)+len(s.items)+len(s.backgroundTasks)+len(s.interruptRequests)+len(s.turnUsages)+len(s.turnTerminals))
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
		// Per the transcript contract, AskUserQuestion is a handoff back
		// to the user — the agent stops until the user answers. Emit a
		// companion meta-kind entry so the main transcript surface
		// carries the "Claude is waiting on you" signal alongside the
		// durable tool item that owns the full question UI in Turn
		// activity. Without this, the question lives only inside the
		// collapsible activity group, and the only chat-level attention
		// signal is the session-row status dot.
		//
		// The announcement sorts immediately after the tool item via a
		// derived orderKey suffix so historical replay and live streaming
		// agree on placement. It is excluded from the Turn-activity
		// compact (see isProjectionNeedsInputAnnouncement) so it stays
		// in the main transcript stream regardless of whether the
		// activity group is open or closed.
		if announcement := projectNeedsInputAnnouncement(item); announcement != nil {
			items = append(items, projectedEntryItem{
				entry:    announcement,
				orderKey: transcriptMapString(announcement, "orderKey"),
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
	for idx, request := range s.interruptRequests {
		request.index = baseIndex + idx
		items = append(items, request)
	}
	baseIndex += len(s.interruptRequests)
	offset := 0
	for _, usage := range s.turnUsages {
		if terminal, ok := s.turnTerminals[usage.TurnID]; ok && terminal.Usage != nil {
			continue
		}
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
		if terminal.Status == "completed" {
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
		if answers := projectionAskUserAnswers(item); len(answers) > 0 {
			entry["askUserAnswers"] = answers
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
		"sourceEventId": usage.SourceEventID,
		"orderKey":      usage.OrderKey,
		"turnUsage":     usage.Usage,
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
	if terminal.Usage != nil {
		out["turnUsage"] = terminal.Usage
	}
	if terminal.UsageObservation != nil {
		out["usageObservation"] = terminal.UsageObservation
	}
	return out
}

func compactProjectedTranscript(entries []map[string]any, activeTurnID string, terminals map[string]turnTerminalProjection) transcriptProjection {
	activities := append(terminalProjectedActivities(entries, terminals), activeProjectedActivities(entries, activeTurnID)...)
	if len(activities) == 0 {
		return transcriptProjection{Entries: entries, ActivityBodies: map[string]turnActivityBody{}}
	}
	activityByInsertIndex := map[int]turnActivityBody{}
	compactedIndexes := map[int]bool{}
	for _, activity := range activities {
		insertBefore := 0
		if len(activity.Entries) > 0 {
			insertBefore = projectedEntryIndex(entries, activity.Entries[0])
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
	out := make([]map[string]any, 0, len(entries))
	bodies := map[string]turnActivityBody{}
	for idx, entry := range entries {
		if activity, ok := activityByInsertIndex[idx]; ok {
			shell := map[string]any{
				"id":            "turn-activity-" + activity.TurnID,
				"kind":          "turn_activity",
				"turnId":        activity.TurnID,
				"time":          transcriptMapString(activity.Summary, "startedAt"),
				"orderKey":      transcriptMapString(activity.Summary, "startOrderKey"),
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
			out = append(out, shell)
			bodies[activity.TurnID] = activity
		}
		if !compactedIndexes[idx] {
			out = append(out, entry)
		}
	}
	return transcriptProjection{Entries: out, ActivityBodies: bodies}
}

func terminalProjectedActivities(entries []map[string]any, terminals map[string]turnTerminalProjection) []turnActivityBody {
	turnIndexes := map[string][]int{}
	turnOrder := []string{}
	for idx, entry := range entries {
		turnID := transcriptMapString(entry, "turnId")
		if turnID == "" {
			continue
		}
		if _, exists := turnIndexes[turnID]; !exists {
			turnOrder = append(turnOrder, turnID)
		}
		turnIndexes[turnID] = append(turnIndexes[turnID], idx)
	}
	var activities []turnActivityBody
	for _, turnID := range turnOrder {
		indexes := turnIndexes[turnID]
		terminal, ok := terminals[turnID]
		if !ok {
			continue
		}
		if terminal.Status == "completed" && len(terminal.FinalAnswerIDs) == 0 && turnHasAssistantMessage(entries, indexes) {
			recordTranscriptMaterializationInvariantViolation("completed_turn_missing_final_answer", "completed")
		}
		finalIndexes := map[int]bool{}
		if terminal.Status == "completed" {
			finalIndexes = finalAnswerProjectedIndexes(entries, indexes, terminal.FinalAnswerIDs)
			if len(terminal.FinalAnswerIDs) > 0 && len(finalIndexes) == 0 {
				recordTranscriptMaterializationInvariantViolation("completed_turn_final_answer_missing_entry", "completed")
			}
		}
		var compacted []map[string]any
		var activityEntries []map[string]any
		for _, idx := range indexes {
			entry := entries[idx]
			if isProjectedUserMessage(entry) || isProjectionTerminalMetaEntry(entry, terminal) ||
				isProjectionNeedsInputAnnouncement(entry) {
				continue
			}
			activityEntries = append(activityEntries, entry)
			if !finalIndexes[idx] {
				compacted = append(compacted, entry)
			}
		}
		if len(compacted) == 0 {
			continue
		}
		activities = append(activities, makeTurnActivityBody(turnID, terminal.Status, activityEntries, compacted, false))
	}
	return activities
}

func activeProjectedActivities(entries []map[string]any, activeTurnID string) []turnActivityBody {
	if activeTurnID == "" {
		return nil
	}
	var activityEntries []map[string]any
	for _, entry := range entries {
		if transcriptMapString(entry, "turnId") == activeTurnID &&
			transcriptMapString(entry, "turnTerminalStatus") == "" &&
			!isProjectedUserMessage(entry) &&
			!isProjectionNeedsInputAnnouncement(entry) {
			activityEntries = append(activityEntries, entry)
		}
	}
	if len(activityEntries) == 0 {
		return nil
	}
	return []turnActivityBody{makeTurnActivityBody(activeTurnID, "active", activityEntries, activityEntries, true)}
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
		"errorCount":          0,
		"active":              active,
	}
	for _, entry := range activityEntries {
		if turnUsage := entry["turnUsage"]; turnUsage != nil {
			out["turnUsage"] = turnUsage
		}
		if usageObservation := entry["usageObservation"]; usageObservation != nil {
			out["usageObservation"] = usageObservation
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
			if meta := transcriptMap(entry, "meta"); transcriptMapString(meta, "severity") == "error" {
				out["errorCount"] = out["errorCount"].(int) + 1
			}
		}
	}
	if len(activityEntries) > 0 {
		first := activityEntries[0]
		last := activityEntries[len(activityEntries)-1]
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
		out["endOrderKey"] = projectionFirstNonEmpty(transcriptMapString(last, "turnTerminalOrderKey"), transcriptMapString(last, "orderKey"))
		out["sourceEventId"] = transcriptMapString(first, "sourceEventId")
	}
	out["compactedCount"] = len(compactedEntries)
	return out
}

func finalAnswerProjectedIndexes(entries []map[string]any, indexes []int, finalAnswerIDs map[string]bool) map[int]bool {
	out := map[int]bool{}
	if len(finalAnswerIDs) == 0 {
		return out
	}
	for _, idx := range indexes {
		entry := entries[idx]
		if finalAnswerIDs[transcriptMapString(entry, "id")] && isProjectedAssistantMessage(entry) {
			out[idx] = true
		}
	}
	return out
}

func turnHasAssistantMessage(entries []map[string]any, indexes []int) bool {
	for _, idx := range indexes {
		if isProjectedAssistantMessage(entries[idx]) {
			return true
		}
	}
	return false
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

// isProjectionNeedsInputAnnouncement is the activity-compact opt-out for
// the AskUserQuestion handoff row projected by projectNeedsInputAnnouncement.
// The announcement is a transcript-level handoff, not Turn-activity noise,
// so terminalProjectedActivities and activeProjectedActivities must not
// fold it into the activity compact. Same shape as the existing
// isProjectedUserMessage opt-out — user messages and handoff
// announcements both anchor the main transcript surface.
func isProjectionNeedsInputAnnouncement(entry map[string]any) bool {
	return transcriptMapString(entry, "kind") == "meta" &&
		transcriptMapString(entry, "metaKind") == "needs_input_announcement"
}

// projectNeedsInputAnnouncement returns a meta-kind row that promotes an
// AskUserQuestion item into the settled main transcript as a handoff
// announcement, or nil if the item is not an AskUserQuestion tool call.
//
// Anchoring on the underlying item's orderKey (with a `~ann` suffix) keeps
// historical replay and live streaming aligned on the same insertion point
// — the announcement always renders immediately after the tool item it
// references. `answered` is sourced from the durable item status, not from
// any local "I submitted" flag, so a fresh tab opened after the user
// answered shows the resolved announcement.
func projectNeedsInputAnnouncement(item *projectionItem) map[string]any {
	if !isProjectionAskUserQuestion(item) {
		return nil
	}
	questions := projectionAskUserQuestionList(item)
	if len(questions) == 0 {
		return nil
	}
	summary := projectionAskUserQuestionSummary(questions)
	answered := item.Status == "completed"
	title := "Claude is waiting on you"
	if answered {
		title = "Answered"
	}
	orderKey := item.OrderKey
	if orderKey != "" {
		orderKey = orderKey + "~needs_input_announcement"
	}
	// The full canonical question payload rides on the streamed handoff row
	// so an already-open client renders the interactive answer form live,
	// off the same durable cursor stream as every other transcript row —
	// not from a one-shot `/turns/{id}/activity` fetch that never refreshes
	// after the question arrives. See docs/features/transcript/contract.md
	// → "An already-open transcript client must receive and render
	// post-cursor durable events without reload." The `~needs_input_announcement`
	// order key keeps this row beyond the live cursor at first appearance,
	// and the durable `tool.approval_resolved` re-projection advances the
	// underlying item so the answered row streams without a reload.
	questionPayload := make([]any, 0, len(questions))
	for _, q := range questions {
		questionPayload = append(questionPayload, q)
	}
	announcement := map[string]any{
		"targetTurnId":         item.TurnID,
		"targetProviderItemId": item.ProviderItemID,
		"targetTimelineId":     item.ID,
		"questionSummary":      summary,
		"questionCount":        len(questions),
		"answered":             answered,
		"questions":            questionPayload,
	}
	// Durable answers (with annotations) are mirrored onto the handoff once
	// `tool.approval_resolved` lands, so the live answered card and any
	// fresh tab render identical selections without the activity fetch.
	if answers := projectionAskUserAnswers(item); len(answers) > 0 {
		announcement["answers"] = answers
	}
	entry := map[string]any{
		"id":             item.ID + ":needs_input_announcement",
		"kind":           "meta",
		"metaKind":       "needs_input_announcement",
		"turnId":         item.TurnID,
		"providerItemId": item.ProviderItemID,
		"time":           projectionFirstNonEmpty(item.StartedAt, item.CreatedAt),
		"orderKey":       orderKey,
		"sourceEventId":  item.SourceEventID,
		"meta": map[string]any{
			"title":    title,
			"detail":   summary,
			"severity": "info",
		},
		"announcement": announcement,
	}
	return entry
}

func isProjectionAskUserQuestion(item *projectionItem) bool {
	if item == nil {
		return false
	}
	if transcriptMapString(item.Payload, "name") == "AskUserQuestion" {
		return true
	}
	if item.Title == "AskUserQuestion" {
		return true
	}
	// Some adapter paths set payload.kind = needs_input without a name;
	// fall back to detecting the questions[] payload that only
	// AskUserQuestion produces.
	if input := transcriptAnyMap(item.Payload["input"]); input != nil {
		if questions, ok := input["questions"].([]any); ok && len(questions) > 0 {
			return true
		}
	}
	return false
}

func projectionAskUserQuestionList(item *projectionItem) []map[string]any {
	if item == nil {
		return nil
	}
	input := transcriptAnyMap(item.Payload["input"])
	if input == nil {
		return nil
	}
	raw, ok := input["questions"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, q := range raw {
		if record, ok := q.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

func projectionAskUserQuestionSummary(questions []map[string]any) string {
	if len(questions) == 0 {
		return "Open the Turns tab to answer."
	}
	first := questions[0]
	text := transcriptMapString(first, "question")
	if text == "" {
		text = transcriptMapString(first, "header")
	}
	if text == "" {
		text = "Open the Turns tab to answer."
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

func projectionAskUserAnswers(item *projectionItem) map[string]any {
	rawAnswers := transcriptAnyMap(item.Payload["answers"])
	if len(rawAnswers) == 0 {
		return nil
	}
	annotations := transcriptAnyMap(item.Payload["annotations"])
	out := map[string]any{}
	for question, value := range rawAnswers {
		rawLabels, ok := value.([]any)
		if !ok {
			continue
		}
		labels := []string{}
		for _, raw := range rawLabels {
			if label, ok := raw.(string); ok && label != "" {
				labels = append(labels, label)
			}
		}
		if len(labels) == 0 {
			continue
		}
		answer := map[string]any{"labels": labels}
		if ann := transcriptAnyMap(annotations[question]); ann != nil {
			if preview := transcriptMapString(ann, "preview"); preview != "" {
				answer["preview"] = preview
			}
			if notes := transcriptMapString(ann, "notes"); notes != "" {
				answer["notes"] = notes
			}
		}
		out[question] = answer
	}
	return out
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
