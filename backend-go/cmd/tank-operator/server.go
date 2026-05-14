package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	turnQueue     store.TurnQueueStore
	sessionEvents store.SessionEventStore
	eventBroker   *sessionEventBroker
	readStates    store.ConversationReadStateStore
	eventBus      *sessions.EventBus
	verifier      *auth.Verifier
	minter        *auth.Minter
	namespace     string
	sessionScope  string

	sessionServiceAccount string

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
	mux.HandleFunc("GET /.well-known/jwks.json", s.handleInternalJWKS)

	// Sessions CRUD.
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
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
	mux.HandleFunc("GET /api/sessions/{session_id}/mcp-tools", s.handleListMCPTools)

	// App-managed chat surface.
	mux.HandleFunc("POST /api/sessions/{session_id}/turns", s.handleEnqueueSessionTurn)
	mux.HandleFunc("POST /api/sessions/{session_id}/turns/{turn_id}/interrupt", s.handleInterruptSessionTurn)
	mux.HandleFunc("POST /api/sessions/{session_id}/turns/{turn_id}/input-reply", s.handleInputReplySessionTurn)
	mux.HandleFunc("GET /api/sessions/{session_id}/events", s.handleSessionEventStream)
	mux.HandleFunc("GET /api/sessions/{session_id}/timeline", s.handleSessionTimeline)
	mux.HandleFunc("PUT /api/sessions/{session_id}/read-state", s.handleUpdateSessionReadState)

	// CLI / sandbox agent.
	mux.HandleFunc("POST /api/sessions/{session_id}/cli-process", s.handleCLIProcess)
	mux.HandleFunc("GET /api/sessions/{session_id}/sandbox-agent/v1/processes/{process_id}/terminal/ws", s.handleSandboxTerminalProxy)

	// Internal API.
	mux.HandleFunc("GET /api/internal/jwks", s.handleInternalJWKS)
	mux.HandleFunc("POST /api/internal/github/attestation", s.handleInternalGitHubAttestation)
	mux.HandleFunc("GET /api/internal/sessions", s.handleInternalListSessions)
	mux.HandleFunc("POST /api/internal/sessions", s.handleInternalCreateSession)
	mux.HandleFunc("DELETE /api/internal/sessions/{session_id}", s.handleInternalDeleteSession)
	mux.HandleFunc("PATCH /api/internal/sessions/{session_id}", s.handleInternalPatchSession)
	mux.HandleFunc("GET /api/internal/sessions/{session_id}/capabilities", s.handleInternalSessionCapabilities)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/events/notify", s.handleInternalSessionEventsNotify)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/test-state", s.handleInternalSetTestState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/rollout-state", s.handleInternalSetRolloutState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/messages", s.handleInternalSendMessage)

	// Static files.
	staticRoots := tankStaticRoots()
	if staticRoots.enabled() {
		mux.HandleFunc("GET /assets/", s.serveStaticAsset(staticRoots, "assets"))
		mux.HandleFunc("GET /fonts/", s.serveStaticAsset(staticRoots, "fonts"))
		mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
			serveTankStaticFile(w, r, staticRoots, "manifest.webmanifest")
		})
		mux.HandleFunc("GET /_styleguide", func(w http.ResponseWriter, r *http.Request) {
			serveTankStaticFile(w, r, staticRoots, "index.html")
		})
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			serveTankStaticFile(w, r, staticRoots, "index.html")
		})
	}
}

func (s *appServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *appServer) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, publicConfig())
}

func publicConfig() map[string]string {
	return map[string]string{
		"entra_client_id": os.Getenv("ENTRA_CLIENT_ID"),
		"entra_authority": "https://login.microsoftonline.com/common",
		"fork_session_prompt_template": readOptionalFile(
			os.Getenv("TANK_FORK_SESSION_PROMPT_FILE"),
			defaultForkSessionPromptTemplate,
		),
	}
}

const defaultForkSessionPromptTemplate = `The user forked this session from an assistant message in another Tank Operator session to deal with a divergent issue.

Use the forked assistant message as the immediate starting point. The previous session data is identified below; read that session's transcript from Tank Operator data if it would help, but do not assume you need the entire prior conversation before making progress.

Forked assistant message:
{{forked_message}}

Source session pointer:
` + "```json" + `
{{source_session_json}}
` + "```"

func readOptionalFile(path string, fallback string) string {
	if strings.TrimSpace(path) == "" {
		return fallback
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return string(body)
}

type tankStaticRootSet struct {
	override string
	base     string
}

func tankStaticRoots() tankStaticRootSet {
	return tankStaticRootSet{
		override: os.Getenv("TANK_OPERATOR_STATIC_OVERRIDE_DIR"),
		base:     os.Getenv("TANK_OPERATOR_STATIC_DIR"),
	}
}

func (r tankStaticRootSet) enabled() bool {
	for _, root := range []string{r.override, r.base} {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func (s *appServer) serveStaticAsset(roots tankStaticRootSet, prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/"+prefix+"/")
		serveTankStaticFile(w, r, roots, prefix, filepath.FromSlash(rel))
	}
}

func serveTankStaticFile(w http.ResponseWriter, r *http.Request, roots tankStaticRootSet, parts ...string) {
	found, ok := tankStaticFile(roots, parts...)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, found)
}

func tankStaticFile(roots tankStaticRootSet, parts ...string) (string, bool) {
	for _, root := range []string{roots.override, roots.base} {
		if root == "" {
			continue
		}
		if found, ok := tankStaticFileInRoot(root, parts...); ok {
			return found, true
		}
	}
	return "", false
}

func tankStaticFileInRoot(root string, parts ...string) (string, bool) {
	for _, part := range parts {
		if part == "" || filepath.IsAbs(part) {
			return "", false
		}
		for _, segment := range strings.Split(filepath.Clean(part), string(filepath.Separator)) {
			if segment == ".." {
				return "", false
			}
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidate := filepath.Join(append([]string{rootAbs}, parts...)...)
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	info, err := os.Stat(candidateAbs)
	if err != nil || info.IsDir() {
		return "", false
	}
	return candidateAbs, true
}
