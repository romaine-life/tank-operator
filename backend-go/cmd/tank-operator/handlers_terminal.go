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

// cliProcessLaunchForMode picks the sandbox-agent process shape for the
// in-browser CLI shell. The orchestrator owns this — not the SPA — so the
// browser can't dictate what binary runs inside the pod.
//
// The "config" modes are credential-refresh wizards: the shell boots already
// running the right `login` invocation, then drops to interactive bash so the
// user can poke around or rerun the login if it fails. Any other mode gets a
// plain login bash. See session-pod-bootstrap.sh for the matching per-mode
// on-disk seeding (~/.codex/config.toml, ~/.claude/settings.json, etc.) that
// these login commands depend on.
func cliProcessLaunchForMode(mode string) sandboxProcessCreate {
	base := sandboxProcessCreate{
		Command:     "bash",
		Cwd:         "/workspace",
		Interactive: true,
		TTY:         true,
	}
	switch mode {
	case sessionmodel.CodexConfigMode:
		// codex login --device-auth is the headless-friendly OAuth
		// flow — prints a URL + one-time code instead of trying to
		// open a localhost callback unreachable from a pod. The auth
		// blob lands at $HOME/.codex/auth.json once the user completes
		// the device flow; the save-credentials button harvests it.
		base.Args = []string{"-lc", "codex login --device-auth; exec bash"}
	case sessionmodel.ConfigMode:
		// claude /login walks through OAuth; the resulting blob lands
		// at $HOME/.claude/.credentials.json for the save-credentials
		// button.
		base.Args = []string{"-lc", "claude /login; exec bash"}
	case sessionmodel.GeminiConfigMode:
		// gemini login runs the Google OAuth flow; credentials land
		// at $HOME/.gemini/settings.json for the save-credentials button.
		base.Args = []string{"-lc", "gemini login; exec bash"}
	case sessionmodel.PiConfigMode:
		// Pi's /login is a slash command inside the interactive `pi`
		// REPL, not a CLI subcommand. Drop into pi, let the user
		// /login, then back to bash on exit.
		base.Args = []string{"-lc", `printf "Run /login in Pi to authenticate.\n\n"; pi; exec bash`}
	default:
		base.Args = []string{"-l"}
	}
	return base
}

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

	info, err := s.mgr.GetByOwner(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session not ready: "+err.Error())
		return
	}

	podIP, _, err := s.mgr.GetTerminalEndpoint(r.Context(), user.Email, sessionID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session not ready: "+err.Error())
		return
	}

	baseURL := fmt.Sprintf("http://%s:%d", podIP, sessionmodel.SandboxAgentPort)
	launch := cliProcessLaunchForMode(info.Mode)
	processID, err := findOrCreateSandboxCLIProcess(r.Context(), http.DefaultClient, baseURL, launch)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"process_id": processID})
}

// findOrCreateSandboxCLIProcess looks for an already-running shell matching
// the requested launch shape and returns its id; otherwise it asks
// sandbox-agent to spawn one. The GET is retried on transport errors during
// sandbox-agent's boot window — the readiness probe sits on the container,
// not the HTTP server, so podReady can race the listener.
//
// The matching predicate uses the same (command, args) tuple the launch
// carries, so opening the codex_config panel a second time after a fresh
// reload reattaches to the same codex-login shell instead of spawning a
// second device-auth flow against the same auth.json.
func findOrCreateSandboxCLIProcess(ctx context.Context, client *http.Client, baseURL string, launch sandboxProcessCreate) (string, error) {
	processesURL := baseURL + "/v1/processes"

	deadline := time.Now().Add(sandboxAgentBootWait)
	var (
		list    sandboxProcessList
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
		if p.Command == launch.Command &&
			slices.Equal(p.Args, launch.Args) &&
			p.Status == "running" {
			return p.ID, nil
		}
	}

	body, err := json.Marshal(launch)
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
