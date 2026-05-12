package main

import (
	"net/http"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// appServer holds shared application state for all handlers.
type appServer struct {
	k8s           kubernetes.Interface
	restCfg       *rest.Config
	mgr           *sessions.Manager
	profiles      profilesStore
	activeRuns    store.ActiveRunStore
	runEvents     store.RunEventStore
	turnQueue     store.TurnQueueStore
	sessionEvents store.SessionEventStore
	readStates    store.ConversationReadStateStore
	eventBus      *sessions.EventBus
	verifier      *auth.Verifier
	minter        *auth.Minter
	namespace     string

	// internalAllowedSubjects maps "namespace/serviceaccount" → email for internal SA auth.
	internalAllowedSubjects map[string]string
}

func (s *appServer) registerRoutes(mux *http.ServeMux) {
	// Health / config.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/config", s.handleConfig)

	// Auth.
	mux.HandleFunc("POST /api/auth/microsoft/login", s.handleMicrosoftLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("PUT /api/auth/prefs", s.handleUpdatePrefs)
	mux.HandleFunc("POST /api/internal/auth/k8s", s.handleK8sAuth)

	// GitHub install.
	mux.HandleFunc("GET /api/github/install/url", s.handleGitHubInstallURL)
	mux.HandleFunc("GET /api/github/install/callback", s.handleGitHubInstallCallback)

	// Sessions CRUD.
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("POST /api/sessions/run", s.handleCreateAndRunSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/activity", s.handleSessionActivity)
	mux.HandleFunc("GET /api/sessions/events", s.handleSessionsEvents)
	mux.HandleFunc("DELETE /api/sessions/{session_id}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{session_id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/touch", s.handleTouchSession)
	mux.HandleFunc("PATCH /api/sessions/{session_id}", s.handlePatchSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/test-state", s.handleSetTestState)
	mux.HandleFunc("POST /api/sessions/{session_id}/rollout-state", s.handleSetRolloutState)
	mux.HandleFunc("POST /api/sessions/{session_id}/save-credentials", s.handleSaveCredentials)
	mux.HandleFunc("POST /api/sessions/{session_id}/paste-image", s.handlePasteImage)
	mux.HandleFunc("POST /api/sessions/{session_id}/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/with-context", s.handleCreateSessionWithContext)

	// File endpoints.
	mux.HandleFunc("GET /api/sessions/{session_id}/files", s.handleListFiles)
	mux.HandleFunc("GET /api/sessions/{session_id}/files/content", s.handleGetFileContent)
	mux.HandleFunc("GET /api/sessions/{session_id}/files/raw", s.handleGetFileRaw)
	mux.HandleFunc("GET /api/sessions/{session_id}/files/walk", s.handleWalkFiles)
	mux.HandleFunc("POST /api/sessions/{session_id}/files/upload", s.handleUploadFile)
	mux.HandleFunc("PUT /api/sessions/{session_id}/files/content", s.handleWriteFile)
	mux.HandleFunc("GET /api/sessions/{session_id}/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/sessions/{session_id}/mcp-servers", s.handleListMCPServers)

	// Legacy run endpoints - used by sessions whose pod has no SDK runner
	// sidecar (claude_cli, codex_cli, pi, and older GUI pods). The SPA's
	// chat pane uses these for the legacy data-ingestion path. They are not
	// being deleted: CLI/config modes still dispatch short-lived runs through
	// this stream-json surface.
	mux.HandleFunc("GET /api/sessions/{session_id}/run/active", s.handleGetActiveRun)
	mux.HandleFunc("GET /api/sessions/{session_id}/run/history", s.handleRunHistory)
	mux.HandleFunc("GET /api/sessions/{session_id}/runs/latest/events", s.handleLatestRunEvents)
	mux.HandleFunc("GET /api/sessions/{session_id}/runs/latest/events.json", s.handleLatestRunEventsJSON)
	mux.HandleFunc("GET /api/sessions/{session_id}/runs/{run_id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /api/sessions/{session_id}/run", s.handleRunWebSocket)

	// SDK runtime surface. The same chat pane consumes /agent-ws (live)
	// and /timeline (history) when session.runtime is "sdk" — the data
	// source differs from the legacy path, but the renderer is the same.
	mux.HandleFunc("GET /api/sessions/{session_id}/agent-ws", s.handleAgentWebSocket)
	mux.HandleFunc("POST /api/sessions/{session_id}/turns", s.handleEnqueueSessionTurn)
	mux.HandleFunc("GET /api/sessions/{session_id}/events", s.handleListSessionEvents)
	mux.HandleFunc("GET /api/sessions/{session_id}/timeline", s.handleSessionTimeline)
	mux.HandleFunc("PUT /api/sessions/{session_id}/read-state", s.handleUpdateSessionReadState)

	// CLI / sandbox agent.
	mux.HandleFunc("POST /api/sessions/{session_id}/cli-process", s.handleCLIProcess)
	mux.HandleFunc("GET /api/sessions/{session_id}/sandbox-agent/v1/processes/{process_id}/terminal/ws", s.handleSandboxTerminalProxy)

	// Internal API (SA token auth).
	mux.HandleFunc("GET /api/internal/resolve-caller", s.handleInternalResolveCaller)
	mux.HandleFunc("GET /api/internal/sessions", s.handleInternalListSessions)
	mux.HandleFunc("POST /api/internal/sessions", s.handleInternalCreateSession)
	mux.HandleFunc("DELETE /api/internal/sessions/{session_id}", s.handleInternalDeleteSession)
	mux.HandleFunc("PATCH /api/internal/sessions/{session_id}", s.handleInternalPatchSession)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/test-state", s.handleInternalSetTestState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/rollout-state", s.handleInternalSetRolloutState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/messages", s.handleInternalSendMessage)
	mux.HandleFunc("POST /api/internal/sessions/run", s.handleInternalRunSession)

	// Static files.
	staticDir := os.Getenv("TANK_OPERATOR_STATIC_DIR")
	if staticDir != "" {
		if _, err := os.Stat(staticDir); err == nil {
			fs := http.FileServer(http.Dir(staticDir))
			mux.Handle("GET /assets/", fs)
			mux.Handle("GET /fonts/", fs)
			mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
				http.ServeFile(w, r, staticDir+"/manifest.webmanifest")
			})
			mux.HandleFunc("GET /_styleguide", func(w http.ResponseWriter, r *http.Request) {
				http.ServeFile(w, r, staticDir+"/index.html")
			})
			mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
				http.ServeFile(w, r, staticDir+"/index.html")
			})
		}
	}
}

func (s *appServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *appServer) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"entra_client_id": os.Getenv("ENTRA_CLIENT_ID"),
		"entra_authority": "https://login.microsoftonline.com/common",
	})
}
