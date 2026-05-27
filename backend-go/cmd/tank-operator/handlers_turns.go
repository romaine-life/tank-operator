package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/conversation"
	"github.com/nelsong6/tank-operator/backend-go/internal/hermes"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionactivity"
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
	if err := s.refreshTranscriptRowsForEvent(ctx, event); err != nil {
		return err
	}
	// Silent-stranding observability: bump the lifecycle counter for the
	// five bounding types after the durable write commits. The
	// backend-direct path writes user_message.created + turn.submitted
	// here at submit time, and the various turn.command_failed events on
	// the interrupt / input-reply / stop-background paths. Pairs with
	// the same call inside sessionbus.persistOneEvent so the signal
	// works regardless of which path wrote the row. Filter on
	// IsTurnLifecycleEvent at the call boundary so the helper just
	// records. See docs/features/agent-runners/contract.md → Observability.
	if eventType := stringMapField(event, "type"); conversation.IsTurnLifecycleEvent(conversation.EventType(eventType)) {
		recordTurnLifecyclePersisted(eventType)
	}
	if eventType := stringMapField(event, "type"); conversation.IsTurnTerminalEvent(conversation.EventType(eventType)) && stringMapField(event, "client_nonce") == "" {
		source := stringMapField(event, "source")
		if source == "" {
			source = "unknown"
		}
		recordTurnTerminalMissingClientNonce(source, eventType)
	}
	if s.sessionBus != nil && storageKey != "" {
		if err := s.sessionBus.PublishSessionEventWake(ctx, storageKey); err != nil {
			slog.Warn("session event wake publish failed",
				"storage_key", storageKey, "event_type", event["type"], "error", err)
		}
	}
	s.refreshActivityForBackendEvent(ctx, storageKey, event)
	return nil
}

func (s *appServer) refreshTranscriptRowsForEvent(ctx context.Context, event map[string]any) error {
	if s == nil || s.sessionEvents == nil || s.transcriptRows == nil {
		return nil
	}
	return (transcriptRowsMaterializer{
		events: s.sessionEvents,
		rows:   s.transcriptRows,
	}).RefreshEvent(ctx, event)
}

func (s *appServer) refreshActivityForBackendEvent(ctx context.Context, storageKey string, event map[string]any) {
	if s == nil || s.activityRefresher == nil || event == nil {
		return
	}
	eventType := strings.TrimSpace(stringMapField(event, "type"))
	if !sessionactivity.IsLifecycleChatEventType(eventType) {
		return
	}
	owner := strings.ToLower(strings.TrimSpace(stringMapField(event, "email")))
	if owner == "" {
		return
	}
	if eventStorageKey := stringMapField(event, "tank_session_id"); eventStorageKey != "" {
		storageKey = eventStorageKey
	}
	scope := s.sessionScope
	sessionID := ""
	if strings.TrimSpace(storageKey) != "" {
		scope, sessionID = sessionbus.StorageScopeAndSessionID(storageKey)
	}
	if publicID := stringMapField(event, "session_id"); publicID != "" {
		sessionID = publicID
	}
	if strings.TrimSpace(scope) == "" {
		scope = s.sessionScope
	}
	if sessionID == "" {
		return
	}
	if err := s.activityRefresher.RefreshSessionActivity(ctx, owner, scope, sessionID); err != nil {
		slog.Warn("backend-event activity refresh failed",
			"storage_key", storageKey,
			"event_type", eventType,
			"session_id", sessionID,
			"scope", scope,
			"error", err,
		)
	}
}

func stringMapField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

// handleEnqueueSessionTurn is the durable submit boundary for SDK runtime
// sessions. The browser writes work here and reads projected transcript rows
// from the durable SSE stream; runner-local transports are not part of the UI
// contract.
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
		ClientNonce         string `json:"client_nonce"`
		Prompt              string `json:"prompt"`
		DisplayText         string `json:"display_text"`
		Model               string `json:"model"`
		Effort              string `json:"effort"`
		PermissionMode      string `json:"permission_mode"`
		SkillName           string `json:"skill_name"`
		FollowUp            bool   `json:"follow_up"`
		OriginSessionID     string `json:"origin_session_id"`
		ExistingUserMessage bool   `json:"existing_user_message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	owner := user.OwnerEmail()
	resp, status, detail := s.enqueueSDKTurn(r.Context(), owner, sessionID, sdkTurnRequest{
		ClientNonce:     body.ClientNonce,
		RequireNonce:    true,
		Prompt:          body.Prompt,
		DisplayText:     body.DisplayText,
		Model:           body.Model,
		Effort:          body.Effort,
		PermissionMode:  body.PermissionMode,
		SkillName:       body.SkillName,
		FollowUp:        body.FollowUp,
		OmitUserMessage: body.ExistingUserMessage,
		OriginSessionID: body.OriginSessionID,
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

	owner := user.OwnerEmail()
	info, err := s.mgr.GetByOwner(r.Context(), owner, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// hermes_gui interrupts go through the bridge's StopRun → emits a
	// turn.interrupt_requested marker; the durable terminal event lands
	// on the bridge's SSE-tailing goroutine when Hermes acks the stop.
	// Same "Stop is only complete when the durable terminal arrives"
	// contract Tank's pod-side runners follow per #532.
	if sessionmodel.IsNoPodMode(info.Mode) {
		if s.hermesBridge == nil {
			writeError(w, http.StatusServiceUnavailable, "hermes bridge not configured")
			return
		}
		if err := s.hermesBridge.StopTurn(r.Context(), sessionID, owner, targetTurnID, targetTurnID); err != nil {
			writeError(w, http.StatusBadGateway, "hermes stop: "+err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
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
		Email:             owner,
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
		Email:             owner,
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
			Email:             owner,
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

// inputReplyRequest is the JSON body shape accepted by
// `POST /api/sessions/{session_id}/turns/{turn_id}/input-reply`.
//
// `answers` is `{questionText: answerLabel[]}` — always a slice so
// single-select and multi-select questions share one shape. The runner
// joins multi-element slices with ", " at the SDK boundary to match the
// Claude Agent SDK's AskUserQuestion zod preprocess. `annotations` is
// optional `{questionText: {preview?, notes?}}` from the SDK schema.
type inputReplyRequest struct {
	ProviderItemID string                                     `json:"provider_item_id"`
	TimelineID     string                                     `json:"timeline_id"`
	Answers        map[string][]string                        `json:"answers"`
	Annotations    map[string]sessionbus.InputReplyAnnotation `json:"annotations,omitempty"`
}

type stopBackgroundTaskRequest struct {
	TurnID         string `json:"turn_id"`
	TimelineID     string `json:"timeline_id"`
	ProviderItemID string `json:"provider_item_id"`
	ProcessID      string `json:"process_id"`
}

func (s *appServer) handleStopBackgroundTask(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	taskID := strings.TrimSpace(r.PathValue("task_id"))
	if sessionID == "" || taskID == "" || !backgroundTaskIDPattern.MatchString(taskID) {
		writeError(w, http.StatusBadRequest, "task_id is required and must match task id syntax")
		return
	}

	var body stopBackgroundTaskRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	targetTurnID := strings.TrimSpace(body.TurnID)
	if targetTurnID == "" || !turnIDPattern.MatchString(targetTurnID) {
		writeError(w, http.StatusBadRequest, "turn_id is required and must match turn id syntax")
		return
	}

	owner := user.OwnerEmail()
	info, err := s.mgr.GetByOwner(r.Context(), owner, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	normalizedMode := sessionmodel.NormalizeSessionMode(info.Mode)
	if normalizedMode != sessionmodel.CodexGUIMode && normalizedMode != sessionmodel.CodexAppServerMode {
		writeError(w, http.StatusBadRequest, "background task stop is only supported for Codex app-server transport sessions")
		return
	}
	if s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "session bus unavailable")
		return
	}

	storageKey := sessionmodel.SessionStorageKey(s.sessionScope, sessionID)
	stopTurnID := "stop_background_" + auth.RandomHex(12)
	if err := s.sessionBus.PublishCommand(r.Context(), sessionbus.Command{
		CommandID:            "stop-background:" + taskID + ":" + auth.RandomHex(12),
		Type:                 sessionbus.CommandStopBackgroundTask,
		SessionID:            sessionID,
		SessionStorageKey:    storageKey,
		Email:                owner,
		Provider:             "codex",
		Source:               "background-stop",
		TurnID:               stopTurnID,
		ClientNonce:          targetTurnID,
		TargetTurnID:         targetTurnID,
		TargetTimelineID:     strings.TrimSpace(body.TimelineID),
		TargetProviderItemID: strings.TrimSpace(body.ProviderItemID),
		TargetTaskID:         taskID,
		TargetProcessID:      strings.TrimSpace(body.ProcessID),
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		failedEvent := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storageKey,
			Email:             owner,
			TurnID:            stopTurnID,
			ClientNonce:       targetTurnID,
			Runtime:           "codex",
			Reason:            "publish_background_stop_failed: " + err.Error(),
			Now:               time.Now().UTC(),
		})
		if writeErr := s.persistBackendEvent(r.Context(), storageKey, failedEvent); writeErr != nil {
			slog.Warn("persist turn.command_failed for background stop",
				"session_id", sessionID, "target_turn_id", targetTurnID, "task_id", taskID, "error", writeErr)
		}
		writeError(w, http.StatusInternalServerError, "publish background stop: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":         "accepted",
		"target_turn_id": targetTurnID,
		"target_task_id": taskID,
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

	var body inputReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	providerItemID := strings.TrimSpace(body.ProviderItemID)
	timelineID := strings.TrimSpace(body.TimelineID)
	if providerItemID == "" || timelineID == "" {
		writeError(w, http.StatusBadRequest, "provider_item_id and timeline_id are required")
		return
	}

	// Validate the answers payload up front: at least one question with
	// at least one non-empty label. This catches empty submits before
	// they hit JetStream and matches the SDK's zod schema rejecting
	// empty answer maps.
	answers := normalizeInputReplyAnswers(body.Answers)
	if len(answers) == 0 {
		writeError(w, http.StatusBadRequest, "answers must contain at least one non-empty selection")
		return
	}
	annotations := normalizeInputReplyAnnotations(body.Annotations)

	if size := inputReplyPayloadSize(answers, annotations); size > maxSDKInputReplyBytes {
		writeError(w, http.StatusBadRequest, "input reply too large")
		return
	}

	owner := user.OwnerEmail()
	info, err := s.mgr.GetByOwner(r.Context(), owner, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	normalizedMode := sessionmodel.NormalizeSessionMode(info.Mode)
	if normalizedMode != sessionmodel.ClaudeGUIMode && normalizedMode != sessionmodel.CodexGUIMode && normalizedMode != sessionmodel.CodexAppServerMode {
		writeError(w, http.StatusBadRequest, "input replies are only supported for Claude GUI and Codex app-server transport sessions")
		return
	}
	provider := "claude"
	if normalizedMode == sessionmodel.CodexGUIMode || normalizedMode == sessionmodel.CodexAppServerMode {
		provider = "codex"
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
		Email:                owner,
		Provider:             provider,
		Source:               "input-reply",
		TurnID:               inputReplyTurnID,
		ClientNonce:          targetTurnID,
		TargetTurnID:         targetTurnID,
		TargetTimelineID:     timelineID,
		TargetProviderItemID: providerItemID,
		Answers:              answers,
		Annotations:          annotations,
		CreatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		failedEvent := conversation.TurnCommandFailedEventMap(conversation.TurnCommandFailedArgs{
			SessionID:         sessionID,
			SessionStorageKey: storageKey,
			Email:             owner,
			TurnID:            inputReplyTurnID,
			ClientNonce:       targetTurnID,
			Runtime:           provider,
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

// normalizeInputReplyAnswers trims each question/label pair and drops
// empties. Returns nil for "no usable answers" so the caller's `len() == 0`
// check handles the rejection in one place.
func normalizeInputReplyAnswers(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for question, labels := range in {
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
			out[trimmedQuestion] = cleaned
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeInputReplyAnnotations(in map[string]sessionbus.InputReplyAnnotation) map[string]sessionbus.InputReplyAnnotation {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]sessionbus.InputReplyAnnotation, len(in))
	for question, ann := range in {
		trimmedQuestion := strings.TrimSpace(question)
		if trimmedQuestion == "" {
			continue
		}
		cleaned := sessionbus.InputReplyAnnotation{
			Preview: strings.TrimSpace(ann.Preview),
			Notes:   strings.TrimSpace(ann.Notes),
		}
		if cleaned.Preview != "" || cleaned.Notes != "" {
			out[trimmedQuestion] = cleaned
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// inputReplyPayloadSize sums the answers + annotations bytes against
// maxSDKInputReplyBytes. The cap is intentionally generous (64 KiB)
// because previews can carry HTML fragments per the SDK schema.
func inputReplyPayloadSize(answers map[string][]string, annotations map[string]sessionbus.InputReplyAnnotation) int {
	total := 0
	for question, labels := range answers {
		total += len(question)
		for _, label := range labels {
			total += len(label)
		}
	}
	for question, ann := range annotations {
		total += len(question) + len(ann.Preview) + len(ann.Notes)
	}
	return total
}

type sdkTurnRequest struct {
	ClientNonce      string
	RequireNonce     bool
	Prompt           string
	DisplayText      string
	Model            string
	Effort           string
	PermissionMode   string
	SkillName        string
	FollowUp         bool
	AllowBeforeReady bool
	OmitUserMessage  bool
	SessionMode      string
	CreatedAt        time.Time
	OrderBase        time.Time
	// OriginSessionID identifies the sibling tank-operator session that
	// authored this turn via an MCP handoff, or the source session for a
	// browser-created fork. Human-typed browser turns leave it empty.
	// Threaded into UserSubmissionArgs so the persisted user_message.created
	// event carries it for the frontend's avatar selection.
	OriginSessionID string
}

type sessionRunConfig struct {
	Model  string
	Effort string
}

func validateCreateRunConfig(mode, rawModel, rawEffort string) (sessionRunConfig, int, string) {
	modelInput := strings.TrimSpace(rawModel)
	effortInput := strings.TrimSpace(rawEffort)
	if modelInput == "" && effortInput == "" {
		return sessionRunConfig{}, 0, ""
	}
	provider, ok := sdkProviderForMode(mode)
	if !ok {
		return sessionRunConfig{}, http.StatusBadRequest, "model and effort are only supported for SDK chat sessions"
	}
	model := validateTurnArg(modelInput)
	if modelInput != "" && model == "" {
		return sessionRunConfig{}, http.StatusBadRequest, "model is invalid"
	}
	effort := validateEffort(provider, effortInput)
	if effortInput != "" && effort == "" {
		if provider == "codex" {
			return sessionRunConfig{}, http.StatusBadRequest, "effort is invalid; want one of low|medium|high|xhigh"
		}
		return sessionRunConfig{}, http.StatusBadRequest, "effort is invalid; want one of low|medium|high|xhigh|max"
	}
	return sessionRunConfig{Model: model, Effort: effort}, 0, ""
}

func (s *appServer) enqueueSDKTurn(ctx context.Context, email, sessionID string, req sdkTurnRequest) (map[string]string, int, string) {
	createdAt := req.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}
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
	displayText := strings.TrimSpace(req.DisplayText)
	if displayText == "" {
		displayText = prompt
	}
	if len([]byte(displayText)) > maxSDKTurnPromptBytes {
		return nil, http.StatusBadRequest, "display_text too large"
	}

	sessionMode := strings.TrimSpace(req.SessionMode)
	var podName *string
	if sessionMode == "" {
		info, err := s.mgr.GetByOwner(ctx, email, sessionID)
		if err != nil {
			return nil, http.StatusNotFound, "session not found"
		}
		sessionMode = info.Mode
		podName = info.PodName
	} else {
		sessionMode = sessionmodel.NormalizeSessionMode(sessionMode)
	}

	// hermes_gui short-circuits the NATS / pod path entirely. The bridge
	// owns the durable boundary events, the /v1/runs POST, and the
	// SSE-tailing goroutine that writes translated events into
	// session_events. See nelsong6/tank-operator#540.
	if sessionmodel.IsNoPodMode(sessionMode) {
		if s.hermesBridge == nil {
			return nil, http.StatusServiceUnavailable, "hermes bridge not configured (HERMES_API_URL / HERMES_API_BEARER missing on the orchestrator)"
		}
		result, err := s.hermesBridge.SubmitTurn(ctx, hermes.SubmitArgs{
			SessionID:       sessionID,
			Email:           email,
			ClientNonce:     clientNonce,
			Text:            prompt,
			DisplayText:     displayText,
			SkillName:       validateSkillName(req.SkillName),
			OmitUserMessage: req.OmitUserMessage,
			Now:             createdAt,
			OrderBase:       req.OrderBase,
		})
		if err != nil {
			return nil, http.StatusBadGateway, "hermes submit: " + err.Error()
		}
		return map[string]string{"turn_id": result.TurnID, "run_id": result.RunID}, 0, ""
	}

	provider, ok := sdkProviderForMode(sessionMode)
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
	// Effort allowlist enforcement is loud on purpose: silent drop would
	// hide a frontend regression that ships a stale effort string. The
	// runner trusts whatever lands on the wire and has no rejection path
	// of its own, so the choke point is here. Empty is allowed and means
	// "use the runner's baked-in default" — that mapping is preserved.
	model := validateTurnArg(req.Model)
	effort := validateEffort(provider, strings.TrimSpace(req.Effort))
	registered, regErr := s.mgr.GetRegisteredByOwner(ctx, email, sessionID)
	hasSessionRunConfig := regErr == nil && (registered.Model != "" || registered.Effort != "")
	if hasSessionRunConfig {
		model = registered.Model
		effort = registered.Effort
	} else if strings.TrimSpace(req.Effort) != "" && effort == "" {
		if provider == "codex" {
			return nil, http.StatusBadRequest, "effort is invalid; want one of low|medium|high|xhigh"
		}
		return nil, http.StatusBadRequest, "effort is invalid; want one of low|medium|high|xhigh|max"
	}
	if !req.AllowBeforeReady {
		if podName == nil {
			return nil, http.StatusServiceUnavailable, "session pod not ready"
		}
		pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(ctx, *podName, metav1.GetOptions{})
		if err != nil {
			return nil, http.StatusServiceUnavailable, "pod fetch failed: " + err.Error()
		}
		if !podHasSDKRunner(pod) {
			return nil, http.StatusBadRequest, "session pod has no SDK runner container"
		}
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
		Text:              displayText,
		Message:           map[string]any{"role": "user", "content": displayText},
		Runtime:           provider,
		SkillName:         skillName,
		OriginSessionID:   strings.TrimSpace(req.OriginSessionID),
		Now:               createdAt,
	})
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	if req.OmitUserMessage {
		if status, detail := s.requireExistingUserMessage(ctx, sessionID, turnID); status != 0 {
			return nil, status, detail
		}
	}
	retimeTurnBoundaryEvents(events, req.OrderBase)
	if req.OmitUserMessage {
		events = omitUserMessageEvents(events)
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
		Model:             model,
		Effort:            effort,
		PermissionMode:    validateTurnArg(req.PermissionMode),
		SkillName:         skillName,
		FollowUp:          req.FollowUp,
		CreatedAt:         createdAt.Format(time.RFC3339Nano),
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

func (s *appServer) requireExistingUserMessage(ctx context.Context, sessionID, turnID string) (int, string) {
	if s.sessionEvents == nil {
		return http.StatusServiceUnavailable, "session event store unavailable"
	}
	orderKey, err := s.sessionEvents.OrderKeyForTimelineID(ctx, sessionID, turnID+":user")
	if err != nil {
		return http.StatusInternalServerError, "lookup existing user message: " + err.Error()
	}
	if strings.TrimSpace(orderKey) == "" {
		return http.StatusBadRequest, "existing_user_message requires a durable launch user message"
	}
	return 0, ""
}

func omitUserMessageEvents(events []map[string]any) []map[string]any {
	out := events[:0]
	for _, event := range events {
		if event["type"] == string(conversation.EventUserMessageCreated) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func retimeTurnBoundaryEvents(events []map[string]any, base time.Time) {
	if base.IsZero() {
		return
	}
	base = base.UTC()
	for i, event := range events {
		eventTime := base.Add(time.Duration(i) * time.Millisecond)
		event["created_at"] = eventTime.Format(time.RFC3339Nano)
		event["written_at"] = eventTime.Format(time.RFC3339Nano)
		event["order_key"] = orderKeyForEventTime(eventTime, i, eventIDForOrderKey(event))
	}
}

func orderKeyForEventTime(eventTime time.Time, sequence int, eventID string) string {
	return fmt.Sprintf("%013d-%08d-%s", eventTime.UTC().UnixMilli(), sequence, eventID)
}

func eventIDForOrderKey(event map[string]any) string {
	for _, key := range []string{"event_id", "id", "uuid"} {
		if value, ok := event[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return auth.RandomHex(12)
}

func promptMatchesSkillTrigger(provider, skillName, prompt string) bool {
	trigger := skillPromptTrigger(provider, skillName)
	return prompt == trigger || strings.HasPrefix(prompt, trigger+" ") || strings.HasPrefix(prompt, trigger+"\n")
}

func skillPromptTrigger(provider, skillName string) string {
	if provider == "codex" || provider == "hermes" {
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
	case sessionmodel.CodexExecGUIMode:
		return "codex", true
	case sessionmodel.CodexAppServerMode:
		return "codex", true
	default:
		return "", false
	}
}
