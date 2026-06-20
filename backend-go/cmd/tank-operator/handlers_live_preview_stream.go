package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Live-preview control channel — the event-driven half that takes the agent out
// of the loop for the live frontend preview feature. The in-pod live-preview
// daemon (k8s/session-config/live-preview-daemon.sh) opens this single-session
// SSE stream and converges its build+push loop on the emitted control state:
// the owner's test_state.live_preview.enabled toggle and the slot URL to stream
// against. No agent ever invokes the push; the daemon holds the credential and
// reacts to this stream.
//
// This is deliberately event-driven, never a poll loop. test_state changes
// dual-write the pod annotation and the registry row (Manager.UpdateLivePreviewState),
// and the sessioncontroller row publisher fires a per-session event wake on the
// session bus on every row publish. We subscribe to that same per-session wake
// (the mechanism the browser transcript stream at /api/sessions/{id}/events
// rides) and recompute-and-compare the control state on each wake, emitting only
// when it actually changes. The heartbeat re-fetch is a backstop for a dropped
// NATS wake (the wake channel is drop-on-full), keeping a long-lived daemon from
// stranding on a missed toggle without ever polling in steady state.

const (
	livePreviewStreamHeartbeat = 15 * time.Second
)

// livePreviewControl is the wire shape the daemon consumes. Only comparable
// fields so a recompute-and-compare can suppress no-op emits with ==.
type livePreviewControl struct {
	// Enabled mirrors test_state.live_preview.enabled — the owner's toggle.
	// On true the daemon builds + pushes; on false it stops and reverts the
	// slot override.
	Enabled bool `json:"enabled"`
	// SlotURL is test_state.url (trailing slash trimmed): the base URL of the
	// session's provisioned test slot the daemon PUTs the built dist/ to. Empty
	// when no slot is active — the daemon must not push without it.
	SlotURL string `json:"slot_url"`
	// BuildHint is the last build the daemon reported pushing
	// (test_state.live_preview.pushed_build); a daemon that restarts can use it
	// to avoid re-pushing an identical build. Omitted when empty.
	BuildHint string `json:"build_hint,omitempty"`
}

// livePreviewControlFromState projects the live-preview control state out of a
// session's test_state. Source of truth: test_state.url (slot) and
// test_state.live_preview.{enabled,pushed_build}.
func livePreviewControlFromState(testState map[string]any) livePreviewControl {
	ctrl := livePreviewControl{
		SlotURL: strings.TrimRight(stringFromState(testState, "url"), "/"),
	}
	if lp, ok := testState["live_preview"].(map[string]any); ok {
		ctrl.Enabled = boolFromState(lp, "enabled")
		ctrl.BuildHint = stringFromState(lp, "pushed_build")
	}
	return ctrl
}

// handleInternalLivePreviewStream streams a single session's live-preview
// control state to that session's in-pod daemon over SSE. Gated by
// requireServicePrincipal + internalCallerMatchesSession: a pod streams ONLY
// its own session (the #1207 invariant — the verified per-session service
// subject is the sole authorization factor, never a caller-asserted header).
func (s *appServer) handleInternalLivePreviewStream(w http.ResponseWriter, r *http.Request) {
	user := s.requireServicePrincipal(w, r, "GET /api/internal/sessions/{session_id}/live-preview/stream")
	if user == nil {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if !s.internalCallerMatchesSession(user, sessionID) {
		writeError(w, http.StatusForbidden, "live-preview stream requires a session pod streaming its own session")
		return
	}

	// Resolve the initial control state before switching to SSE framing so a
	// missing session is a clean JSON 404 rather than a stream-error event.
	info, err := s.mgr.GetByID(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	if s.sessionBus == nil {
		writeError(w, http.StatusServiceUnavailable, "session bus unavailable")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// Per-write deadlines (issue #1077): every write/flush below rides the
	// deadline-arming wrapper so a hung or slow client errors the stream
	// instead of pinning this goroutine forever.
	deadlineW := newSSEDeadlineWriter(w, flusher)
	w = deadlineW
	flusher = deadlineW

	// Subscribe BEFORE the initial emit so a toggle that lands between the
	// GetByID above and the subscribe is not lost: the wake re-fetches and the
	// recompute-and-compare emits it. SubscribeWakes keys on this backend's own
	// scope + sessionID — the same per-session wake the transcript stream uses.
	notify, unsubscribe, err := s.sessionBus.SubscribeWakes(r.Context(), sessionID)
	if err != nil {
		writeSSEJSONEvent(w, "stream-error", "", map[string]any{
			"reason": "event_wake_subscribe_failed",
			"detail": err.Error(),
		})
		flusher.Flush()
		return
	}
	defer unsubscribe()

	last := livePreviewControlFromState(info.TestState)
	writeSSEJSONEvent(w, "live-preview", "", last)
	flusher.Flush()

	slog.Info("live-preview control stream open",
		"session_id", sessionID,
		"actor_email", user.ActorEmail,
		"enabled", last.Enabled,
		"has_slot", last.SlotURL != "",
	)
	defer slog.Info("live-preview control stream close",
		"session_id", sessionID,
		"actor_email", user.ActorEmail,
	)

	heartbeat := time.NewTicker(livePreviewStreamHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-notify:
			if s.emitLivePreviewIfChanged(r.Context(), w, flusher, sessionID, &last) {
				continue
			}
		case <-heartbeat.C:
			// Re-fetch on heartbeat too: a dropped NATS wake (drop-on-full
			// channel) must not strand the daemon on a stale toggle. If nothing
			// changed, fall through to a keep-alive comment.
			if s.emitLivePreviewIfChanged(r.Context(), w, flusher, sessionID, &last) {
				continue
			}
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// emitLivePreviewIfChanged re-reads the session's control state and emits a
// `live-preview` event only when it differs from last (recompute-and-compare,
// so a redelivered wake is a no-op). Returns true when it emitted. A transient
// fetch error is swallowed (the next wake or heartbeat retries) so a blip never
// tears down the daemon's stream.
func (s *appServer) emitLivePreviewIfChanged(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sessionID string, last *livePreviewControl) bool {
	info, err := s.mgr.GetByID(ctx, sessionID)
	if err != nil {
		slog.Warn("live-preview control stream refetch failed",
			"session_id", sessionID, "error", err)
		return false
	}
	cur := livePreviewControlFromState(info.TestState)
	if cur == *last {
		return false
	}
	*last = cur
	writeSSEJSONEvent(w, "live-preview", "", cur)
	flusher.Flush()
	slog.Info("live-preview control changed",
		"session_id", sessionID,
		"enabled", cur.Enabled,
		"has_slot", cur.SlotURL != "",
	)
	return true
}
