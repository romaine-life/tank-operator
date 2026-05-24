package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"

	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
)

// cliProcess* describe the long-lived in-pod shell the
// /api/sessions/{id}/cli-process endpoint hands the SPA. Bash login shell in
// /workspace, TTY+interactive so the sandbox-agent terminal WebSocket carries
// keystrokes (codex login --device-auth lives behind this). The orchestrator
// owns the command — not the SPA — so a browser can't pick what binary runs
// inside the pod.
var (
	cliProcessCommand = "bash"
	cliProcessArgs    = []string{"-l"}
	cliProcessCwd     = "/workspace"
)

// sandboxAgentBootWait bounds the wait for the in-pod sandbox-agent HTTP
// server to start accepting connections after the pod is Ready. The kubelet
// readiness probe isn't wired on the claude container, so podReady fires the
// moment the container is started — sandbox-agent may still be running the
// install-tank-docs.sh prelude or downloading via `npx -y` on fresh images.
const sandboxAgentBootWait = 30 * time.Second

type sandboxProcessInfo struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Status  string   `json:"status"`
}

type sandboxProcessList struct {
	Processes []sandboxProcessInfo `json:"processes"`
}

type sandboxProcessCreate struct {
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Cwd         string   `json:"cwd,omitempty"`
	Interactive bool     `json:"interactive"`
	TTY         bool     `json:"tty"`
}

// handleCLIProcess returns a long-lived bash process id for the in-browser
// terminal. Reuses an already-running shell if one matches the canonical
// shape; otherwise creates one through the pod-local sandbox-agent API.
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

	baseURL := fmt.Sprintf("http://%s:%d", podIP, sessionmodel.SandboxAgentPort)
	processID, err := findOrCreateSandboxCLIProcess(r.Context(), http.DefaultClient, baseURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"process_id": processID})
}

// findOrCreateSandboxCLIProcess looks for an already-running shell matching
// the canonical CLI-process shape and returns its id; otherwise it asks
// sandbox-agent to spawn one. The GET is retried on transport errors during
// sandbox-agent's boot window — the readiness probe sits on the container,
// not the HTTP server, so podReady can race the listener.
func findOrCreateSandboxCLIProcess(ctx context.Context, client *http.Client, baseURL string) (string, error) {
	processesURL := baseURL + "/v1/processes"

	deadline := time.Now().Add(sandboxAgentBootWait)
	var (
		list   sandboxProcessList
		lastErr error
	)
	for {
		list, lastErr = sandboxListProcesses(ctx, client, processesURL)
		if lastErr == nil {
			break
		}
		// Only retry transport-level failures (the listener isn't up
		// yet). A successful HTTP response with a 4xx/5xx body is a
		// real protocol error and shouldn't be papered over.
		var protoErr *sandboxAgentProtocolError
		if errors.As(lastErr, &protoErr) {
			return "", lastErr
		}
		if time.Now().After(deadline) {
			return "", lastErr
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}

	for _, p := range list.Processes {
		if p.Command == cliProcessCommand &&
			slices.Equal(p.Args, cliProcessArgs) &&
			p.Status == "running" {
			return p.ID, nil
		}
	}

	body, err := json.Marshal(sandboxProcessCreate{
		Command:     cliProcessCommand,
		Args:        cliProcessArgs,
		Cwd:         cliProcessCwd,
		Interactive: true,
		TTY:         true,
	})
	if err != nil {
		return "", fmt.Errorf("marshal sandbox-agent request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, processesURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build sandbox-agent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sandbox-agent create: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 400 {
		return "", &sandboxAgentProtocolError{
			op:     "POST /v1/processes",
			status: resp.StatusCode,
			body:   string(respBody),
		}
	}
	var info sandboxProcessInfo
	if err := json.Unmarshal(respBody, &info); err != nil {
		return "", fmt.Errorf("sandbox-agent create response not JSON: %w (body=%q)", err, string(respBody))
	}
	if info.ID == "" {
		return "", fmt.Errorf("sandbox-agent create response missing id (body=%q)", string(respBody))
	}
	return info.ID, nil
}

func sandboxListProcesses(ctx context.Context, client *http.Client, processesURL string) (sandboxProcessList, error) {
	var out sandboxProcessList
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, processesURL, nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 400 {
		return out, &sandboxAgentProtocolError{
			op:     "GET /v1/processes",
			status: resp.StatusCode,
			body:   string(body),
		}
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("sandbox-agent list response not JSON: %w (body=%q)", err, string(body))
	}
	return out, nil
}

type sandboxAgentProtocolError struct {
	op     string
	status int
	body   string
}

func (e *sandboxAgentProtocolError) Error() string {
	return fmt.Sprintf("sandbox-agent %s returned %d: %s", e.op, e.status, e.body)
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
