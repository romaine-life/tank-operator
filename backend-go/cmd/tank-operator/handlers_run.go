package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// handleRunWebSocket handles the interactive run WebSocket connection.
func (s *appServer) handleRunWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()

	// Authenticate via cookie.
	user, authErr := s.verifier.CurrentUserFromWebSocket(r)
	if authErr != nil {
		_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": authErr.Error()}))
		conn.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}

	// Read first frame: run parameters.
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}

	var params struct {
		Prompt         string `json:"prompt"`
		Resume         bool   `json:"resume"`
		RunID          string `json:"run_id"`
		FollowUp       bool   `json:"follow_up"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		Offset         int    `json:"offset"`
		SkillName      string `json:"skill_name"`
	}
	if err := json.Unmarshal(data, &params); err != nil {
		_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": "invalid params"}))
		return
	}

	if !params.Resume {
		prompt := strings.TrimSpace(params.Prompt)
		if prompt == "" {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": "prompt is required"}))
			return
		}
		if len(prompt) > 256*1024 {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": "prompt too large"}))
			return
		}
		params.Prompt = prompt
	}

	runID := validateRunID(params.RunID)
	model := validateHeadlessArg(params.Model)
	permMode := validateHeadlessArg(params.PermissionMode)
	skillName := validateSkillName(params.SkillName)

	// Track WS for reaper.
	untrack := s.mgr.TrackWS(sessionID)
	defer untrack()

	// Wait for pod to be ready, sending keepalives.
	podName, err := waitForPod(ctx, conn, s.mgr, user.Email, sessionID)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": err.Error()}))
		return
	}

	// Get session info for mode.
	info, err := s.mgr.GetByOwner(ctx, user.Email, sessionID)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": "session not found"}))
		return
	}

	mode := compat.NormalizeSessionMode(info.Mode)
	provider := "claude"
	if mode == compat.CodexGUIMode {
		provider = "codex"
	}

	streamPath := compat.RunStreamPath(runID)
	pidPath := compat.RunPIDPath(runID)

	if !params.Resume {
		// Write prompt file and launch live run script.
		promptPath := "/tmp/tank-prompt-" + auth.RandomHex(8)
		if writeErr := kubeexec.WriteFile(ctx, s.k8s, s.restCfg, s.namespace, podName, promptPath, []byte(params.Prompt)); writeErr != nil {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": "write prompt: " + writeErr.Error()}))
			return
		}

		followUpStr := "false"
		if params.FollowUp {
			followUpStr = "true"
		}
		headlessCmd := fmt.Sprintf(
			"bash /opt/tank/headless-run.sh %s %s %s %s %s %s",
			shellQ(provider), shellQ(promptPath), followUpStr,
			shellQ(model), shellQ(permMode), shellQ(skillName),
		)
		liveScript := sessions.BuildLiveRunScript(headlessCmd, pidPath)
		if launchErr := kubeexec.LaunchDetached(ctx, s.k8s, s.restCfg, s.namespace, podName, liveScript, streamPath); launchErr != nil {
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"error": "launch: " + launchErr.Error()}))
			return
		}

		if s.activeRuns != nil {
			if _, arErr := s.activeRuns.Start(ctx, user.Email, sessionID, runID, podName, provider, streamPath, pidPath); arErr != nil {
				slog.Warn("persist active run failed", "run_id", runID, "err", arErr)
			}
		}
	}

	// Notify client we're attached.
	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
		"status": "attached",
		"run_id": runID,
	}))

	// Build tail command and stream to WebSocket.
	tailScript := sessions.BuildTailRunScript(streamPath, params.Offset)
	tailCmd := []string{"bash", "-lc", tailScript}
	cancelCmd := sessions.BuildCancelRunCommand(pidPath)

	// Observer parses stdout for semantic run events.
	email := user.Email
	observer := buildStdoutObserver(ctx, s.runEvents, email, sessionID, runID, provider)

	if streamErr := kubeexec.StreamToWebSocket(ctx, s.k8s, s.restCfg, s.namespace, podName, tailCmd, nil, cancelCmd, conn, observer); streamErr != nil {
		slog.Warn("run stream ended", "session", sessionID, "run_id", runID, "err", streamErr)
	}

	// Only mark the run completed if the detached agent process is actually
	// gone. The stream WebSocket can return on transport-level events that
	// leave the agent inside the pod running — the AKS apiserver kills exec
	// connections every few hours, and a tab close drops the WebSocket on
	// purpose without cancelling the pod. In both cases, marking the run
	// completed would hide it from /run/active so the next reconnect would
	// see no run and lose the live stream. Use a fresh context: r.Context()
	// is typically cancelled by the time we get here.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	if !s.agentRunDone(cleanupCtx, podName, pidPath) {
		slog.Info("run stream ended with agent still running; leaving active row",
			"session", sessionID, "run_id", runID)
		return
	}
	if s.activeRuns != nil {
		_ = s.activeRuns.MarkCompleted(cleanupCtx, sessionID, runID)
	}
	if s.runEvents != nil {
		_, _ = s.runEvents.Append(cleanupCtx, email, sessionID, runID, "run.completed", map[string]any{})
	}
}

// agentRunDone reports whether the detached agent process for a run has
// exited inside the pod. Used to decide whether MarkCompleted is safe: a
// premature MarkCompleted hides the run from /run/active and breaks any
// future reconnect. False on exec errors — conservative bias is to leave
// the row alone; handleGetActiveRun's own liveness probe will reconcile.
func (s *appServer) agentRunDone(ctx context.Context, podName, pidPath string) bool {
	script := fmt.Sprintf(
		`pid=$(cat %s 2>/dev/null); if [ -z "$pid" ]; then echo D; exit 0; fi; if kill -0 "$pid" 2>/dev/null; then echo A; else echo D; fi; exit 0`,
		shellQ(pidPath),
	)
	out, err := kubeexec.Capture(ctx, s.k8s, s.restCfg, s.namespace, podName, []string{"bash", "-lc", script})
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "D"
}

// handleGetActiveRun returns the currently active run for a session.
func (s *appServer) handleGetActiveRun(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")

	_, podName, herr := s.resolveSessionPod(r.Context(), user.Email, sessionID)
	if herr != nil {
		// Session not ready: return null active run.
		writeJSON(w, http.StatusOK, nil)
		return
	}

	activeRun, err := s.activeRuns.GetActive(r.Context(), sessionID)
	if err != nil || activeRun == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}

	// Check if the process is still alive.
	checkScript := fmt.Sprintf(
		`pid=$(cat '%s' 2>/dev/null || true); [ -z "$pid" ] && exit 0; kill -0 "$pid" 2>/dev/null || { rm -f '%s'; exit 0; }; bytes=$(wc -c < '%s' 2>/dev/null || echo 0); echo %s $bytes`,
		activeRun.PIDPath, activeRun.PIDPath, activeRun.StreamPath, activeRun.RunID,
	)
	out, _ := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName,
		[]string{"bash", "-lc", checkScript})

	line := strings.TrimSpace(string(out))
	if line == "" {
		// Process is dead.
		_ = s.activeRuns.MarkStale(r.Context(), sessionID, activeRun.RunID)
		writeJSON(w, http.StatusOK, nil)
		return
	}

	parts := strings.Fields(line)
	var streamOffset int
	if len(parts) >= 2 {
		streamOffset, _ = strconv.Atoi(parts[1])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":        activeRun.RunID,
		"session_id":    sessionID,
		"stream_path":   activeRun.StreamPath,
		"stream_offset": streamOffset,
		"provider":      activeRun.Provider,
	})
}

// handleRunHistory returns run history as newline-delimited JSON.
func (s *appServer) handleRunHistory(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")

	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	latest, err := s.activeRuns.GetLatest(r.Context(), sessionID)
	if err != nil || latest == nil {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	_ = enc.Encode(latest)
}

// handleLatestRunEvents streams the latest run events as SSE.
func (s *appServer) handleLatestRunEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")

	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// Get the latest run.
	latest, err := s.activeRuns.GetLatest(r.Context(), sessionID)
	if err != nil || latest == nil {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	s.streamRunEvents(w, r, sessionID, latest.RunID)
}

// handleLatestRunEventsJSON returns the latest run events as a JSON array.
func (s *appServer) handleLatestRunEventsJSON(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")

	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	latest, err := s.activeRuns.GetLatest(r.Context(), sessionID)
	if err != nil || latest == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	events, err := s.runEvents.ListAfter(r.Context(), latest.RunID, sessionID, 0, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []store.RunEventRecord{}
	}
	writeJSON(w, http.StatusOK, events)
}

// handleRunEvents streams run events for a specific run as SSE.
func (s *appServer) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")
	runID := r.PathValue("run_id")

	if _, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	s.streamRunEvents(w, r, sessionID, runID)
}

// streamRunEvents streams run events as SSE for a given sessionID+runID.
func (s *appServer) streamRunEvents(w http.ResponseWriter, r *http.Request, sessionID, runID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	var lastEventID int64
	pollTicker := time.NewTicker(2 * time.Second)
	keepaliveTicker := time.NewTicker(15 * time.Second)
	defer pollTicker.Stop()
	defer keepaliveTicker.Stop()

	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepaliveTicker.C:
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-pollTicker.C:
			events, err := s.runEvents.ListAfter(ctx, runID, sessionID, lastEventID, 100)
			if err != nil {
				continue
			}
			for _, ev := range events {
				payload, _ := json.Marshal(ev.Payload)
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.EventID, ev.Type, string(payload))
				lastEventID = ev.EventID
			}
			if len(events) > 0 {
				flusher.Flush()
			}

			// Check if run is complete.
			rec, recErr := s.activeRuns.GetLatest(ctx, sessionID)
			if recErr == nil && rec != nil && rec.RunID == runID {
				if rec.Status == "completed" || rec.Status == "stale" {
					fmt.Fprintf(w, "event: done\ndata: {}\n\n")
					flusher.Flush()
					return
				}
			}
		}
	}
}

// handleCreateCLIProcess creates a CLI process via the sandbox agent HTTP API.
func (s *appServer) handleCLIProcess(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session_id")

	podIP, _, err := s.mgr.GetTerminalEndpoint(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session not ready: "+err.Error())
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}

	agentURL := fmt.Sprintf("http://%s:%d/v1/processes", podIP, compat.SandboxAgentPort)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, agentURL, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "sandbox agent: "+err.Error())
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// handleSandboxTerminalProxy proxies a WebSocket terminal connection to the sandbox agent.
func (s *appServer) handleSandboxTerminalProxy(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	processID := r.PathValue("process_id")

	user, ok := s.requireWSAuth(w, r)
	if !ok {
		return
	}

	podIP, _, err := s.mgr.GetTerminalEndpoint(r.Context(), user.Email, sessionID)
	if err != nil {
		http.Error(w, "session not ready", http.StatusServiceUnavailable)
		return
	}

	browser, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if acceptErr != nil {
		return
	}
	defer browser.Close(websocket.StatusNormalClosure, "")

	agentURL := fmt.Sprintf("ws://%s:%d/v1/processes/%s/terminal/ws", podIP, compat.SandboxAgentPort, processID)
	agentCtx := r.Context()

	agent, _, dialErr := websocket.Dial(agentCtx, agentURL, nil)
	if dialErr != nil {
		_ = browser.Write(agentCtx, websocket.MessageText, mustJSON(map[string]any{"error": "sandbox agent unavailable: " + dialErr.Error()}))
		browser.Close(websocket.StatusInternalError, "")
		return
	}
	defer agent.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(agentCtx)
	defer cancel()

	// Browser → Agent.
	go func() {
		defer cancel()
		for {
			mt, data, err := browser.Read(ctx)
			if err != nil {
				return
			}
			if err := agent.Write(ctx, mt, data); err != nil {
				return
			}
		}
	}()

	// Agent → Browser.
	for {
		mt, data, err := agent.Read(ctx)
		if err != nil {
			return
		}
		if err := browser.Write(ctx, mt, data); err != nil {
			return
		}
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// waitForPod polls until the session pod is ready, sending WS keepalives every 10s.
func waitForPod(ctx context.Context, conn *websocket.Conn, mgr *sessions.Manager, email, sessionID string) (string, error) {
	keepalive := time.NewTicker(10 * time.Second)
	defer keepalive.Stop()

	for {
		podName, err := mgr.GetPodName(ctx, email, sessionID)
		if err == nil {
			return podName, nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-keepalive.C:
			_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
				"keepalive": true,
				"phase":     "waiting_for_pod",
			}))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// buildStdoutObserver builds a StdoutObserver that parses JSON run events.
func buildStdoutObserver(ctx context.Context, runEventStore store.RunEventStore, email, sessionID, runID, provider string) kubeexec.StdoutObserver {
	if runEventStore == nil || provider != "claude" {
		return nil
	}

	var (
		outputStarted bool
		lineBuf       strings.Builder
	)

	return func(text string) {
		if !outputStarted {
			outputStarted = true
			_, _ = runEventStore.Append(ctx, email, sessionID, runID, "run.output.started", map[string]any{})
		}

		lineBuf.WriteString(text)
		buf := lineBuf.String()
		scanner := bufio.NewScanner(strings.NewReader(buf))
		remaining := buf

		for scanner.Scan() {
			line := scanner.Text()
			remaining = strings.TrimPrefix(remaining, line+"\n")

			line = strings.TrimSpace(line)
			if line == "" || line[0] != '{' {
				continue
			}
			var msg map[string]any
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			msgType, _ := msg["type"].(string)
			switch msgType {
			case "assistant":
				content, _ := msg["message"].(map[string]any)
				if content == nil {
					content, _ = msg["content"].(map[string]any)
				}
				if content != nil {
					contentArr, _ := content["content"].([]any)
					for _, item := range contentArr {
						itemMap, _ := item.(map[string]any)
						if itemMap == nil {
							continue
						}
						if itemMap["type"] == "tool_use" {
							_, _ = runEventStore.Append(ctx, email, sessionID, runID, "run.tool.started", map[string]any{
								"tool_name": itemMap["name"],
								"tool_id":   itemMap["id"],
							})
						}
					}
				}
			case "result":
				_, _ = runEventStore.Append(ctx, email, sessionID, runID, "run.message.created", map[string]any{
					"content": msg["result"],
				})
			}
		}

		// Keep only un-processed portion.
		lineBuf.Reset()
		lineBuf.WriteString(remaining)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// shellQ is a local shell quote helper for use in run handler.
func shellQ(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
