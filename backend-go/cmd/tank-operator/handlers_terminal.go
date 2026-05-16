package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/coder/websocket"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// handleCLIProcess creates a process through the pod-local sandbox-agent API.
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

	agentURL := fmt.Sprintf("http://%s:%d/v1/processes", podIP, sessionmodel.SandboxAgentPort)
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

// handleSandboxTerminalProxy proxies a WebSocket terminal connection to sandbox-agent.
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

	agentURL := fmt.Sprintf("ws://%s:%d/v1/processes/%s/terminal/ws", podIP, sessionmodel.SandboxAgentPort, processID)
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
