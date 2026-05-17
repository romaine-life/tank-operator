package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maxSDKTurnPromptBytes = 256 * 1024
	maxSDKInputReplyBytes = 64 * 1024
)

// persistBackendEvent writes a backend-owned Tank conversation event
// directly to the Postgres session_events ledger and wakes any active SSE
// subscribers on the per-session events subject. Backend-owned events
// (user_message.created, turn.submitted, turn.command_failed) are the
// backend's authority: it writes durably itself and signals the live
// path explicitly so SSE clients don't wait up to one heartbeat for
// boundary events.
//
// SchemaError responses increment the same producer-regression counter
// the persister uses, so the backend-as-producer path is visible to the
// alert that the runner-as-producer path already feeds.
func (s *appServer) persistBackendEvent(ctx context.Context, storageKey string, event map[string]any) error {
	if err := s.sessionEvents.Upsert(ctx, event); err != nil {
		var schemaErr *conversation.SchemaError
		if errors.As(err, &schemaErr) {
			recordSessionEventPersistSchemaRejected()
			slog.Warn("backend-event schema rejected",
				"storage_key", storageKey,
				"event_type", event["type"],
				"event_id", event["event_id"],
				"error", schemaErr.Error(),
			)
		}
		return err
	}
	if s.sessionBus != nil && storageKey != "" {
		if err := s.sessionBus.PublishSessionEventWake(ctx, storageKey); err != nil {
			slog.Warn("session event wake publish failed",
				"storage_key", storageKey, "event_type", event["type"], "error", err)
		}
	}
	return nil
}

// handleEnqueueSessionTurn is the durable submit boundary for SDK runtime
// sessions. The browser writes work here and reads transcript events from the
// durable SSE stream; runner-local transports are not part of the UI contract.
func (s *appServer) handleEnqueueSessionTurn(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session_id")
		return
	}

	var body struct {
		ClientNonce    string `json:"client_nonce"`
		Prompt         string `json:"prompt"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		SkillName      string `json:"skill_name"`
		FollowUp       bool   `json:"follow_up"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	resp, status, detail := s.enqueueSDKTurn(r.Context(), user.Email, sessionID, sdkTurnRequest{
		ClientNonce:    body.ClientNonce,
		RequireNonce:   true,
		Prompt:         body.Prompt,
		Model:          body.Model,
		PermissionMode: body.PermissionMode,
		SkillName:      body.SkillName,
		FollowUp:       body.FollowUp,
	})
	if detail != "" {
		writeError(w, status, detail)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *appServer) handleInterruptSessionTurn(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	targetTurnID := strings.TrimSpace(r.PathValue("turn_id"))
	if sessionID == "" || targetTurnID == "" || !turnIDPattern.MatchString(targetTurnID) {
		writeError(w, http.StatusBadRequest, "turn_id is required and must match turn id syntax")
		return
	}

	info, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider, ok := sdkProviderForMode(info.Mode)
	if !ok {
		writeError(w, http.StatusBadRequest, "session mode does not support app chat turns")
		return
	}
	if s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "session bus unavailable")
		return
	}

	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	interruptTurnID := "interrupt_" + auth.RandomHex(12)

	// Durable-first: persist turn.interrupt_requested before publishing the
	// JetStream command, so a refresh-after-stop replays the stopping
	// projection state from the ledger instead of relying on a UI-local
	// flag. Event_id is deterministic in target turn id, so a double-click
	// POST collapses to one durable row at the Postgres UNIQUE constraint.
	requestedEvent := conversation.TurnInterruptRequestedEventMap(conversation.TurnInterruptRequestedArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             user.Email,
		TurnID:            targetTurnID,
		ClientNonce:       targetTurnID,
		Runtime:           provider,
		Now:               time.Now().UTC(),
	})
	if err := s.persistBackendEvent(r.Context(), storageKey, requestedEvent); err != nil {
		turnInterruptRequestTotal.WithLabelValues("persist_failed").Inc()
		writeError(w, http.StatusInternalServerError, "persist interrupt request: "+err.Error())
		return
	}

	if err := s.sessionBus.PublishCommand(r.Context(), sessionbus.Command{
		CommandID:         "interrupt:" + targetTurnID + ":" + auth.RandomHex(12),
		Type:              sessionbus.CommandInterrupt,
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             user.Email,
		Provider:          provider,
		Source:            "interrupt",
		TurnID:            interruptTurnID,
		ClientNonce:       targetTurnID,
		TargetTurnID:      targetTurnID,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		turnInterruptRequestTotal.WithLabelValues("publish_failed").Inc()
		failedEvent := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storageKey,
			Email:             user.Email,
			TurnID:            interruptTurnID,
			ClientNonce:       targetTurnID,
			Runtime:           provider,
			Reason:            "publish_interrupt_failed: " + err.Error(),
			Now:               time.Now().UTC(),
		})
		if writeErr := s.persistBackendEvent(r.Context(), storageKey, failedEvent); writeErr != nil {
			slog.Warn("persist turn.command_failed for interrupt",
				"session_id", sessionID, "target_turn_id", targetTurnID, "error", writeErr)
		}
		writeError(w, http.StatusInternalServerError, "publish interrupt: "+err.Error())
		return
	}
	turnInterruptRequestTotal.WithLabelValues("persisted").Inc()
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":         "accepted",
		"target_turn_id": targetTurnID,
	})
}

func (s *appServer) handleInputReplySessionTurn(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	targetTurnID := strings.TrimSpace(r.PathValue("turn_id"))
	if sessionID == "" || targetTurnID == "" || !turnIDPattern.MatchString(targetTurnID) {
		writeError(w, http.StatusBadRequest, "turn_id is required and must match turn id syntax")
		return
	}

	var body struct {
		ProviderItemID string `json:"provider_item_id"`
		TimelineID     string `json:"timeline_id"`
		Text           string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	providerItemID := strings.TrimSpace(body.ProviderItemID)
	timelineID := strings.TrimSpace(body.TimelineID)
	text := strings.TrimSpace(body.Text)
	if providerItemID == "" || timelineID == "" {
		writeError(w, http.StatusBadRequest, "provider_item_id and timeline_id are required")
		return
	}
	if text == "" {
		writeError(w, http.StatusBadRequest, "missing input reply text")
		return
	}
	if len([]byte(text)) > maxSDKInputReplyBytes {
		writeError(w, http.StatusBadRequest, "input reply too large")
		return
	}

	info, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sessionmodel.NormalizeSessionMode(info.Mode) != sessionmodel.ClaudeGUIMode {
		writeError(w, http.StatusBadRequest, "input replies are only supported for Claude GUI sessions")
		return
	}
	if s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "session bus unavailable")
		return
	}

	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	inputReplyTurnID := "input_reply_" + auth.RandomHex(12)
	if err := s.sessionBus.PublishCommand(r.Context(), sessionbus.Command{
		CommandID:            "input-reply:" + targetTurnID + ":" + auth.RandomHex(12),
		Type:                 sessionbus.CommandInputReply,
		SessionID:            sessionID,
		SessionStorageKey:    storageKey,
		Email:                user.Email,
		Provider:             "claude",
		Source:               "input-reply",
		TurnID:               inputReplyTurnID,
		ClientNonce:          targetTurnID,
		TargetTurnID:         targetTurnID,
		TargetTimelineID:     timelineID,
		TargetProviderItemID: providerItemID,
		InputReply:           text,
		Prompt:               text,
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		failedEvent := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storageKey,
			Email:             user.Email,
			TurnID:            inputReplyTurnID,
			ClientNonce:       targetTurnID,
			Runtime:           "claude",
			Reason:            "publish_input_reply_failed: " + err.Error(),
			Now:               time.Now().UTC(),
		})
		if writeErr := s.persistBackendEvent(r.Context(), storageKey, failedEvent); writeErr != nil {
			slog.Warn("persist turn.command_failed for input reply",
				"session_id", sessionID, "target_turn_id", targetTurnID, "error", writeErr)
		}
		writeError(w, http.StatusInternalServerError, "publish input reply: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":                  "accepted",
		"target_turn_id":          targetTurnID,
		"target_timeline_id":      timelineID,
		"target_provider_item_id": providerItemID,
	})
}

type sdkTurnRequest struct {
	ClientNonce    string
	RequireNonce   bool
	Prompt         string
	Model          string
	PermissionMode string
	SkillName      string
	FollowUp       bool
	// OriginSessionID identifies the sibling tank-operator session that
	// authored this turn via an MCP handoff. Set only on the
	// service-principal path (handleInternalSendMessage); the human-typed
	// browser path leaves it empty. Threaded into UserSubmissionArgs so
	// the persisted user_message.created event carries it for the
	// frontend's avatar selection.
	OriginSessionID string
}

func (s *appServer) enqueueSDKTurn(ctx context.Context, email, sessionID string, req sdkTurnRequest) (map[string]string, int, string) {
	clientNonce := strings.TrimSpace(req.ClientNonce)
	if clientNonce == "" {
		if req.RequireNonce {
			return nil, http.StatusBadRequest, "client_nonce is required and must match turn id syntax"
		}
		clientNonce = auth.RandomHex(12)
	}
	if !turnIDPattern.MatchString(clientNonce) {
		return nil, http.StatusBadRequest, "client_nonce is required and must match turn id syntax"
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, http.StatusBadRequest, "missing prompt"
	}
	if len([]byte(prompt)) > maxSDKTurnPromptBytes {
		return nil, http.StatusBadRequest, "prompt too large"
	}

	info, err := s.mgr.GetByOwner(ctx, email, sessionID)
	if err != nil {
		return nil, http.StatusNotFound, "session not found"
	}

	provider, ok := sdkProviderForMode(info.Mode)
	if !ok {
		return nil, http.StatusBadRequest, "session mode does not support app chat turns"
	}
	skillName := validateSkillName(req.SkillName)
	if strings.TrimSpace(req.SkillName) != "" && skillName == "" {
		return nil, http.StatusBadRequest, "skill_name is invalid"
	}
	if skillName != "" && !promptMatchesSkillTrigger(provider, skillName, prompt) {
		return nil, http.StatusBadRequest, "skill_name does not match prompt trigger"
	}
	if info.PodName == nil {
		return nil, http.StatusServiceUnavailable, "session pod not ready"
	}
	pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(ctx, *info.PodName, metav1.GetOptions{})
	if err != nil {
		return nil, http.StatusServiceUnavailable, "pod fetch failed: " + err.Error()
	}
	if !podHasSDKRunner(pod) {
		return nil, http.StatusBadRequest, "session pod has no SDK runner container"
	}

	if s.sessionBus == nil {
		return nil, http.StatusServiceUnavailable, "session bus unavailable"
	}
	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	turnID, events, err := conversation.UserSubmissionEventMaps(conversation.UserSubmissionArgs{
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             email,
		ClientNonce:       clientNonce,
		Text:              prompt,
		Message:           map[string]any{"role": "user", "content": prompt},
		Runtime:           provider,
		SkillName:         skillName,
		OriginSessionID:   strings.TrimSpace(req.OriginSessionID),
		Now:               time.Now().UTC(),
	})
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	command := sessionbus.Command{
		CommandID:         "turn:" + clientNonce,
		Type:              sessionbus.CommandSubmitTurn,
		SessionID:         sessionID,
		SessionStorageKey: storageKey,
		Email:             email,
		Provider:          provider,
		Source:            "sdk",
		TurnID:            turnID,
		ClientNonce:       clientNonce,
		Prompt:            prompt,
		Model:             validateTurnArg(req.Model),
		PermissionMode:    validateTurnArg(req.PermissionMode),
		SkillName:         skillName,
		FollowUp:          req.FollowUp,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339Nano),
	}
	// Boundary events are backend-owned: the orchestrator accepted the turn,
	// the orchestrator persists user_message.created + turn.submitted to
	// the durable ledger before any runner involvement. Writing directly
	// to Postgres here (instead of publishing onto the bus and waiting for
	// the persister) keeps a single source of truth for these events and
	// guarantees they exist before this handler returns success. The
	// runner-side dispatchCreate of these events was removed during the
	// SDK migration cutover; the backend is now the sole producer.
	for _, event := range events {
		if writeErr := s.persistBackendEvent(ctx, storageKey, event); writeErr != nil {
			return nil, http.StatusInternalServerError, "persist boundary event: " + writeErr.Error()
		}
	}
	if err := s.sessionBus.PublishCommand(ctx, command); err != nil {
		// Command publish failed — NATS is likely broken. Boundary events
		// are already durable above; emit a turn.command_failed marker
		// keyed to the same turn_id so the SPA renders the stranded
		// submission as failed instead of perpetually "submitted."
		failedEvent := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storageKey,
			Email:             email,
			TurnID:            turnID,
			ClientNonce:       clientNonce,
			Runtime:           provider,
			Reason:            "publish_submit_turn_failed: " + err.Error(),
			Now:               time.Now().UTC(),
		})
		if writeErr := s.persistBackendEvent(ctx, storageKey, failedEvent); writeErr != nil {
			slog.Warn("persist turn.command_failed",
				"session_id", sessionID, "turn_id", turnID, "error", writeErr)
		}
		return nil, http.StatusInternalServerError, "publish turn: " + err.Error()
	}

	return map[string]string{
		"status":       "accepted",
		"turn_id":      turnID,
		"client_nonce": clientNonce,
		"provider":     provider,
	}, 0, ""
}

func promptMatchesSkillTrigger(provider, skillName, prompt string) bool {
	trigger := skillPromptTrigger(provider, skillName)
	return prompt == trigger || strings.HasPrefix(prompt, trigger+" ") || strings.HasPrefix(prompt, trigger+"\n")
}

func skillPromptTrigger(provider, skillName string) string {
	if provider == "codex" {
		return "$" + skillName
	}
	return "/" + skillName
}

func sdkProviderForMode(mode string) (string, bool) {
	switch sessionmodel.NormalizeSessionMode(mode) {
	case sessionmodel.ClaudeGUIMode:
		return "claude", true
	case sessionmodel.CodexGUIMode:
		return "codex", true
	default:
		return "", false
	}
}
