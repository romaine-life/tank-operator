package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/conversation"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

type compositeLifecycleEmitter struct {
	emitters []sessionbus.LifecycleEmitter
}

func (e compositeLifecycleEmitter) EmitChatActivityDelta(ctx context.Context, event map[string]any) error {
	var out error
	for _, emitter := range e.emitters {
		if emitter == nil {
			continue
		}
		if err := emitter.EmitChatActivityDelta(ctx, event); err != nil {
			out = errors.Join(out, err)
		}
	}
	return out
}

type backgroundTaskContinuationEmitter struct {
	server *appServer
}

func (e backgroundTaskContinuationEmitter) EmitChatActivityDelta(ctx context.Context, event map[string]any) error {
	if e.server == nil {
		return nil
	}
	return e.server.handleBackgroundTaskExitContinuation(ctx, event)
}

func (s *appServer) handleBackgroundTaskExitContinuation(ctx context.Context, event map[string]any) error {
	if s == nil || event == nil || s.sessionEvents == nil || s.sessionBus == nil {
		return nil
	}
	if stringMapField(event, "type") != string(conversation.EventShellTaskExited) {
		return nil
	}
	if stringMapField(event, "source") != string(conversation.SourceClaude) {
		return nil
	}
	status := backgroundTaskExitStatus(event)
	if status == "stopped" || status == "cancelled" || status == "canceled" {
		return nil
	}
	sessionID := stringMapField(event, "session_id")
	storageKey := stringMapField(event, "tank_session_id")
	if sessionID == "" && storageKey != "" {
		_, sessionID = sessionbus.StorageScopeAndSessionID(storageKey)
	}
	turnID := stringMapField(event, "turn_id")
	if sessionID == "" || turnID == "" {
		return nil
	}
	terminal, err := s.sessionEvents.FindTurnTerminal(ctx, sessionID, turnID)
	if err != nil {
		return fmt.Errorf("background task continuation terminal lookup: %w", err)
	}
	if stringMapField(terminal, "type") != string(conversation.EventTurnCompleted) {
		return nil
	}
	owner := strings.ToLower(strings.TrimSpace(stringMapField(event, "email")))
	if owner == "" {
		return nil
	}
	clientNonce := backgroundTaskContinuationNonce(event)
	prompt := backgroundTaskContinuationPrompt(event)
	resp, statusCode, detail := s.enqueueSDKTurn(ctx, owner, sessionID, sdkTurnRequest{
		ClientNonce:  clientNonce,
		RequireNonce: true,
		Prompt:       prompt,
		Source:       "background-task",
		FollowUp:     true,
		AuthorKind:   string(conversation.AuthorKindSystem),
		CreatedAt:    time.Now().UTC(),
	})
	if statusCode != 0 {
		return fmt.Errorf("background task continuation enqueue failed: %d:%s", statusCode, strings.TrimSpace(detail))
	}
	slog.Info("background task continuation enqueued",
		"session_id", sessionID,
		"turn_id", turnID,
		"task_id", stringMapField(event, "task_id"),
		"follow_up_turn_id", strings.TrimSpace(resp["turn_id"]),
	)
	return nil
}

func backgroundTaskContinuationNonce(event map[string]any) string {
	seed := strings.Join([]string{
		stringMapField(event, "event_id"),
		stringMapField(event, "id"),
		stringMapField(event, "session_id"),
		stringMapField(event, "turn_id"),
		stringMapField(event, "task_id"),
	}, "|")
	sum := sha256.Sum256([]byte(seed))
	return "background_task-" + hex.EncodeToString(sum[:])[:32]
}

func backgroundTaskContinuationPrompt(event map[string]any) string {
	taskID := stringMapField(event, "task_id")
	if taskID == "" {
		taskID = "unknown"
	}
	status := backgroundTaskExitStatus(event)
	if status == "" {
		status = "completed"
	}
	lines := []string{
		fmt.Sprintf("Background task %s from your previous turn finished with status %s.", taskID, status),
		"Continue from the point where you were waiting on this background task. If no further action is needed, briefly report that and stop.",
	}
	payload, _ := event["payload"].(map[string]any)
	for _, key := range []string{"summary", "description", "last_tool_name", "error"} {
		value := strings.TrimSpace(stringFromEventPayload(event, payload, key))
		if value != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", key, value))
		}
	}
	return strings.Join(lines, "\n")
}

func backgroundTaskExitStatus(event map[string]any) string {
	payload, _ := event["payload"].(map[string]any)
	status := stringFromEventPayload(event, payload, "status")
	return strings.ToLower(strings.TrimSpace(status))
}

func stringFromEventPayload(event, payload map[string]any, key string) string {
	if value, _ := payload[key].(string); strings.TrimSpace(value) != "" {
		return value
	}
	return stringMapField(event, key)
}
