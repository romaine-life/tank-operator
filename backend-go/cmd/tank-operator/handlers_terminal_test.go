package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// defaultLaunch mirrors the cli launch shape for non-config modes (plain bash
// login shell). Used as a stand-in by the generic helper tests so they don't
// have to thread a mode through every call.
var defaultLaunch = cliProcessLaunchForMode("")

// TestCliProcessLaunchForMode_ConfigWizards pins the credential-refresh
// wizard contracts. Each "*_config" mode boots straight into the
// provider-specific login command (the user shouldn't have to know what to
// type), then drops to interactive bash so the shell stays usable after the
// login completes. Regression target: 650c282 deleted these auto-launches.
func TestCliProcessLaunchForMode_ConfigWizards(t *testing.T) {
	cases := []struct {
		mode     string
		wantArgs []string
	}{
		{sessionmodel.CodexConfigMode, []string{"-lc", "codex login --device-auth; exec bash"}},
		{sessionmodel.ConfigMode, []string{"-lc", "claude /login; exec bash"}},
		{sessionmodel.ClaudeSecondaryConfigMode, []string{"-lc", "claude /login; exec bash"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			launch := cliProcessLaunchForMode(tc.mode)
			if launch.Command != "bash" {
				t.Errorf("command = %q, want bash", launch.Command)
			}
			if !reflect.DeepEqual(launch.Args, tc.wantArgs) {
				t.Errorf("args = %v, want %v", launch.Args, tc.wantArgs)
			}
			if launch.Cwd != "/workspace" {
				t.Errorf("cwd = %q, want /workspace", launch.Cwd)
			}
			if !launch.Interactive || !launch.TTY {
				t.Errorf("interactive=%v tty=%v, both want true", launch.Interactive, launch.TTY)
			}
		})
	}
}

// TestCliProcessLaunchForMode_NonConfigDefault covers cli/gui/exec modes and
// the unknown-mode fallback. These don't have a wizard to auto-run, so they
// drop to a plain login shell.
func TestCliProcessLaunchForMode_NonConfigDefault(t *testing.T) {
	for _, mode := range []string{
		sessionmodel.ClaudeCLIMode,
		sessionmodel.CodexCLIMode,
		sessionmodel.CodexGUIMode,
		"", "unknown_mode_4242",
	} {
		t.Run(mode, func(t *testing.T) {
			launch := cliProcessLaunchForMode(mode)
			if !reflect.DeepEqual(launch.Args, []string{"-l"}) {
				t.Errorf("args = %v, want [-l] for non-wizard mode", launch.Args)
			}
		})
	}
}

// TestFindOrCreateSandboxCLIProcess_CreatesFreshShell locks in the recovery
// from PR #437 — when no matching shell exists, the helper must POST a
// concrete payload (the SPA does not send one). Asserts the wire body matches
// the launch shape the caller passed in.
func TestFindOrCreateSandboxCLIProcess_CreatesFreshShell(t *testing.T) {
	var createBody sandboxProcessCreate
	var creates atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/processes":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessList{Processes: nil})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/processes":
			creates.Add(1)
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Errorf("decode body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessInfo{
				ID:      "proc-new",
				Command: createBody.Command,
				Args:    createBody.Args,
				Status:  "running",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	id, err := findOrCreateSandboxCLIProcess(context.Background(), srv.Client(), srv.URL, defaultLaunch)
	if err != nil {
		t.Fatalf("findOrCreateSandboxCLIProcess: %v", err)
	}
	if id != "proc-new" {
		t.Errorf("process id = %q, want %q", id, "proc-new")
	}
	if got := creates.Load(); got != 1 {
		t.Errorf("create calls = %d, want 1", got)
	}
	if createBody.Command != "bash" {
		t.Errorf("create command = %q, want bash", createBody.Command)
	}
	if !reflect.DeepEqual(createBody.Args, []string{"-l"}) {
		t.Errorf("create args = %v, want [-l]", createBody.Args)
	}
	if createBody.Cwd != "/workspace" {
		t.Errorf("create cwd = %q, want /workspace", createBody.Cwd)
	}
	if !createBody.Interactive {
		t.Errorf("create interactive = false, want true")
	}
	if !createBody.TTY {
		t.Errorf("create tty = false, want true")
	}
}

// TestFindOrCreateSandboxCLIProcess_CreateMatchesLaunch threads the per-mode
// launch all the way through and asserts the on-wire body carries it.
// Regression target: codex_config dropping to plain bash instead of running
// codex login.
func TestFindOrCreateSandboxCLIProcess_CreateMatchesLaunch(t *testing.T) {
	var createBody sandboxProcessCreate

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/processes":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessList{Processes: nil})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/processes":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Errorf("decode body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessInfo{
				ID: "proc-codex", Command: createBody.Command, Args: createBody.Args, Status: "running",
			})
		}
	}))
	defer srv.Close()

	launch := cliProcessLaunchForMode(sessionmodel.CodexConfigMode)
	if _, err := findOrCreateSandboxCLIProcess(context.Background(), srv.Client(), srv.URL, launch); err != nil {
		t.Fatalf("findOrCreateSandboxCLIProcess: %v", err)
	}
	if !reflect.DeepEqual(createBody.Args, launch.Args) {
		t.Errorf("create args = %v, want %v (per-mode launch must reach sandbox-agent)", createBody.Args, launch.Args)
	}
}

// TestFindOrCreateSandboxCLIProcess_ReusesRunningShell guards the idempotence
// the Python original had — opening the run-shell panel a second time must
// not fan out a second sandbox-agent process. The matching tuple is
// (command, args, status=running), so a stale exited bash with the same args
// doesn't count and an unrelated running process doesn't count.
func TestFindOrCreateSandboxCLIProcess_ReusesRunningShell(t *testing.T) {
	var creates atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/processes":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessList{Processes: []sandboxProcessInfo{
				{ID: "proc-stale", Command: "bash", Args: []string{"-l"}, Status: "exited"},
				{ID: "proc-other", Command: "vim", Args: []string{"foo.txt"}, Status: "running"},
				{ID: "proc-live", Command: "bash", Args: []string{"-l"}, Status: "running"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/processes":
			creates.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	id, err := findOrCreateSandboxCLIProcess(context.Background(), srv.Client(), srv.URL, defaultLaunch)
	if err != nil {
		t.Fatalf("findOrCreateSandboxCLIProcess: %v", err)
	}
	if id != "proc-live" {
		t.Errorf("process id = %q, want proc-live", id)
	}
	if got := creates.Load(); got != 0 {
		t.Errorf("create calls = %d, want 0 (helper should reuse)", got)
	}
}

// TestFindOrCreateSandboxCLIProcess_ReuseMatchesPerModeLaunch confirms the
// reuse predicate uses the *requested* args, not a hardcoded shape. A panel
// reopen on a codex_config session should reattach to the codex-login shell,
// not the plain-bash shell from some other tab.
func TestFindOrCreateSandboxCLIProcess_ReuseMatchesPerModeLaunch(t *testing.T) {
	codexLaunch := cliProcessLaunchForMode(sessionmodel.CodexConfigMode)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/processes" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessList{Processes: []sandboxProcessInfo{
				{ID: "proc-plain", Command: "bash", Args: []string{"-l"}, Status: "running"},
				{ID: "proc-codex", Command: codexLaunch.Command, Args: codexLaunch.Args, Status: "running"},
			}})
			return
		}
		t.Errorf("unexpected request: %s %s — reuse should have hit", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	id, err := findOrCreateSandboxCLIProcess(context.Background(), srv.Client(), srv.URL, codexLaunch)
	if err != nil {
		t.Fatalf("findOrCreateSandboxCLIProcess: %v", err)
	}
	if id != "proc-codex" {
		t.Errorf("process id = %q, want proc-codex (per-mode launch must match the right process)", id)
	}
}

// TestFindOrCreateSandboxCLIProcess_RetriesTransportErrors covers the
// sandbox-agent boot window. The kubelet probe is wired on the container,
// not the in-container HTTP listener — so podReady can race the listener for
// a few seconds on a cold pod (especially with `npx -y` fetching the package
// on a fresh node).
func TestFindOrCreateSandboxCLIProcess_RetriesTransportErrors(t *testing.T) {
	var listCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/processes":
			if listCalls.Add(1) == 1 {
				// Simulate listener-not-up by hijacking and
				// closing without a response — httptest can't
				// `refuse connection` mid-test, but a
				// closed connection produces the same
				// transport-level error class on the client.
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("response writer is not a hijacker")
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatalf("hijack: %v", err)
				}
				_ = conn.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessList{Processes: nil})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/processes":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sandboxProcessInfo{
				ID: "proc-after-retry", Command: "bash", Args: []string{"-l"}, Status: "running",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := findOrCreateSandboxCLIProcess(ctx, srv.Client(), srv.URL, defaultLaunch)
	if err != nil {
		t.Fatalf("findOrCreateSandboxCLIProcess: %v", err)
	}
	if id != "proc-after-retry" {
		t.Errorf("process id = %q, want proc-after-retry", id)
	}
	if listCalls.Load() < 2 {
		t.Errorf("list calls = %d, want >= 2 (one failure + one retry)", listCalls.Load())
	}
}

// TestFindOrCreateSandboxCLIProcess_PropagatesProtocolError ensures we don't
// retry on a real 4xx/5xx HTTP response — the listener IS up, it's saying no.
// Looping on that would hide real bugs.
func TestFindOrCreateSandboxCLIProcess_PropagatesProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/processes" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"type":"about:blank","title":"Internal Server Error"}`)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	start := time.Now()
	_, err := findOrCreateSandboxCLIProcess(context.Background(), srv.Client(), srv.URL, defaultLaunch)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var protoErr *sandboxAgentProtocolError
	if !errors.As(err, &protoErr) {
		t.Errorf("error type = %T, want *sandboxAgentProtocolError; err=%v", err, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("returned after %v — protocol error should be fast-fail, not retried", elapsed)
	}
}
