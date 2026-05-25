package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/avatarassets"
	"github.com/nelsong6/tank-operator/backend-go/internal/avataruploads"
	"github.com/nelsong6/tank-operator/backend-go/internal/hermes"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/providerhealth"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionstream"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

const designSelectionConfigMapName = "tank-design-selection"

// appServer holds shared application state for all handlers.
type appServer struct {
	k8s                 kubernetes.Interface
	restCfg             *rest.Config
	mgr                 *sessions.Manager
	profiles            profilesStore
	sessionEvents       store.SessionEventStore
	transcriptRows      store.SessionTranscriptRowStore
	avatars             avatarassets.Store
	avatarImages        avatarassets.ImageStore
	avatarUploads       avataruploads.Store
	pgPool              *pgxpool.Pool
	sessionBus          sessionCommandBus
	readStates          store.ConversationReadStateStore
	activityRefresher   sessionActivityRefresher
	verifier            *auth.Verifier
	gitHubInstallStates gitHubInstallStateStore
	streamAuthTickets   streamAuthTicketStore
	// streamRegistry tracks every open /api/sessions/{id}/events SSE
	// handler so the /api/debug/session-event-streams admin endpoint
	// can surface per-stream wake/page/emit state for diagnosis.
	// Wired at boot in main.go.
	streamRegistry           *sessionstream.Registry
	namespace                string
	sessionScope             string
	sessionServiceAccount    string
	designSelectionNamespace string

	designSelectionMu     sync.Mutex
	latestDesignSelection map[string]any

	// spawnQuota enforces per-`sub` rate limits on the service-principal
	// spawn surface — the runaway-loop guard inside a single session
	// pod. The per-`actor_email` concurrent-active-session cap that
	// previously sat alongside it was removed; see quota.go for the
	// rationale and what to design back in next time.
	spawnQuota *SpawnQuotaTracker

	// hermes bridge drives hermes_gui session turns (no pod, external
	// /v1/runs API in nelsong6/hermes). nil when HERMES_API_URL /
	// HERMES_API_BEARER aren't set in env — the bridge is constructed
	// best-effort in main.go so a missing config fails loud at the
	// hermes_gui branch in handleEnqueueSessionTurn rather than at boot.
	// See nelsong6/tank-operator#540.
	hermesBridge *hermes.Bridge

	// mcpGitHub drives GET /api/github/repos — the picker's "All repos"
	// section. Mints an on-behalf-of service JWT for the SPA caller
	// (auth.romaine.life #43) and proxies the call to mcp-github.
	// nil when the orchestrator deployment hasn't mounted the
	// auth.romaine.life-audience projected SA token — the endpoint
	// then 503s loudly rather than mis-routing the request.
	mcpGitHub AppServerMCPGitHub

	// providerHealth drives the transcript-surfaced "<provider>
	// sign-in expired" banner. The poll loop owns Layer 1 and the
	// post-transition fan-out; this handle is used by handleCreateSession
	// to backfill a session.status:failed banner on a freshly-created
	// session whose mode's provider is currently in a failed state.
	// nil when pgPool is unset (stub mode).
	providerHealth *providerhealth.Manager
}

type sessionCommandBus interface {
	PublishCommand(context.Context, sessionbus.Command) error
	PublishSessionEventWake(context.Context, string) error
	SubscribeWakes(context.Context, string) (<-chan struct{}, func(), error)
	// SubscribeWakesWithRecorder is the per-stream-aware variant of
	// SubscribeWakes used by the session event SSE handler so the
	// admin endpoint can answer "did a wake arrive on the subject I
	// expected, for this specific browser?" without devtools.
	SubscribeWakesWithRecorder(ctx context.Context, sessionID string, recorder sessionbus.WakeRecorder) (<-chan struct{}, func(), error)
	SubscribeWakesForStorageKey(ctx context.Context, sessionStorageKey string, recorder sessionbus.WakeRecorder) (<-chan struct{}, func(), error)
	PublishSessionRowUpdate(ctx context.Context, email, scope string, payload []byte) error
	SubscribeSessionRowUpdates(ctx context.Context, email, scope string) (<-chan []byte, func(), error)
}

type streamAuthTicketStore interface {
	Create(context.Context, pgstore.StreamAuthTicket) error
	Validate(ctx context.Context, token, streamKind, sessionScope, sessionID string) (pgstore.StreamAuthTicket, error)
}

func (s *appServer) registerRoutes(mux *http.ServeMux) {
	// Health / config / metrics.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/cluster-health", s.handleClusterHealth)
	mux.HandleFunc("GET /api/design/selection/latest", s.handleGetLatestDesignSelection)
	mux.HandleFunc("POST /api/design/selection", s.handlePostDesignSelection)
	mux.HandleFunc("POST /api/client-metrics/chat-scroll", s.handleChatScrollMetrics)
	// Browser-side SSE event stream telemetry — the candidate-B
	// (zombie SSE) and candidate-C (reducer drop) stethoscope on the
	// client side. Pairs with server-side counters in observability.go.
	mux.HandleFunc("POST /api/client-metrics/session-events-stream", s.handleSessionEventStreamMetrics)
	// Browser-side session-list debug capture. The SPA posts its bounded
	// /_debug/session-list ring when the debug page explicitly captures
	// the current browser state or records a diagnostic window.
	mux.HandleFunc("POST /api/client-metrics/session-list-debug-capture", s.handleSessionListDebugCapture)

	// Avatar assets. Reads are authenticated so uploaded backing photos
	// are not exposed as static public files; writes are admin-only.
	mux.HandleFunc("GET /api/avatars", s.handleListAvatars)
	mux.HandleFunc("GET /api/avatars/{avatar_id}/image", s.handleGetAvatarImage)
	mux.HandleFunc("GET /api/avatars/{avatar_id}/backing", s.handleGetAvatarBacking)
	mux.HandleFunc("GET /api/admin/avatar-decks", s.handleGetAvatarDecks)
	mux.HandleFunc("POST /api/admin/avatars", s.handleCreateAvatar)
	mux.HandleFunc("PATCH /api/admin/avatars/{avatar_id}/kind", s.handleUpdateAvatarKind)
	mux.HandleFunc("DELETE /api/admin/avatars/{avatar_id}", s.handleDeleteAvatar)
	// Admin-only durable support surface for avatar upload failures. The
	// form error returns attempt_id; this endpoint turns that reference into
	// a curl-able diagnosis without browser devtools.
	mux.HandleFunc("GET /api/debug/avatar-upload-attempts", s.handleDebugAvatarUploadAttempts)

	// Auth.
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("PUT /api/auth/prefs", s.handleUpdatePrefs)
	mux.HandleFunc("POST /api/auth/stream-ticket", s.handleCreateStreamTicket)

	// GitHub install.
	mux.HandleFunc("GET /api/github/install/url", s.handleGitHubInstallURL)
	mux.HandleFunc("GET /api/github/install/callback", s.handleGitHubInstallCallback)
	mux.HandleFunc("POST /api/github/install/complete", s.handleGitHubInstallComplete)
	// /api/github/recent-repos surfaces the caller's recently-selected
	// repo slugs to the splash-page picker. It reads sessions.repos
	// directly with no mcp-github hop. See handlers_repos.go for the SQL.
	mux.HandleFunc("GET /api/github/recent-repos", s.handleGitHubRecentRepos)
	// /api/github/repos enumerates the caller's GitHub App installation
	// repos via mcp-github. Pairs with the auth.romaine.life on-behalf-of
	// exchange so the orchestrator can mint a service JWT acting for the
	// SPA user.
	mux.HandleFunc("GET /api/github/repos", s.handleGitHubRepos)

	// Sessions CRUD.
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	// /api/sessions/events streams per-row UPDATEs to the SPA sidebar
	// (per-owner row-version-cursor-resumable SSE). After Phase 4 of
	// docs/session-list-redesign.md the wire is post-write sessions row
	// state, not the retired typed session_lifecycle_events ledger; the
	// SPA's SessionStore reconciles by primary key.
	mux.HandleFunc("GET /api/sessions/events", s.handleSessionsEvents)
	// Admin-only debug surface for sidebar diagnosis. Returns the
	// server's view of (owner, scope) — every registry row including
	// visible=false, plus the current row-update cursor. Per
	// memory/feedback_no_devtools_build_surfaces_instead.md the SPA
	// user can't open browser devtools; this endpoint is the curl-
	// able server-side observability that replaces "share a Network
	// tab screenshot."
	mux.HandleFunc("GET /api/debug/session-list-state", s.handleDebugSessionListState)
	// Admin-only durable client-side captures posted by
	// /api/client-metrics/session-list-debug-capture.
	mux.HandleFunc("GET /api/debug/session-list-captures", s.handleDebugSessionListCaptures)
	// Admin-only debug surface for the chat-side SSE stream registry.
	// Returns per-open-stream state (wakes/pages/emits/cursor) so an
	// operator can distinguish wake-key-mismatch from zombie-SSE from
	// reducer-drop without browser devtools. Per
	// memory/feedback_no_devtools_build_surfaces_instead.md.
	mux.HandleFunc("GET /api/debug/session-event-streams", s.handleDebugSessionEventStreams)
	// Admin-only audit surface for the durable session_events ledger.
	// Bypasses the registry visibility gate so a deleted session's
	// chat is reachable via curl + bot token — closes the gap that
	// would otherwise force an un-soft-delete write or a one-off psql
	// pod just to pick up a codex agent's prior conversation. Pairs
	// with /api/debug/session-list-state for invisible-session lookup
	// and with the avatar-upload-attempts surface as the existing
	// "admin debug counterpart to a user-facing read" template.
	mux.HandleFunc("GET /api/debug/session-event-ledger", s.handleDebugSessionEventLedger)
	mux.HandleFunc("PUT /api/sessions/order", s.handleReorderSessions)
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
	mux.HandleFunc("POST /api/sessions/{session_id}/background-tasks/{task_id}/stop", s.handleStopBackgroundTask)
	mux.HandleFunc("GET /api/sessions/{session_id}/events", s.handleSessionEventStream)
	mux.HandleFunc("GET /api/sessions/{session_id}/timeline", s.handleSessionTimeline)
	mux.HandleFunc("GET /api/sessions/{session_id}/turns/{turn_id}/activity", s.handleSessionTurnActivity)
	mux.HandleFunc("PUT /api/sessions/{session_id}/read-state", s.handleUpdateSessionReadState)

	// CLI / sandbox agent.
	mux.HandleFunc("POST /api/sessions/{session_id}/cli-process", s.handleCLIProcess)
	mux.HandleFunc("GET /api/sessions/{session_id}/sandbox-agent/v1/processes/{process_id}/terminal/ws", s.handleSandboxTerminalProxy)

	// Internal API.
	mux.HandleFunc("GET /api/internal/github/installation", s.handleInternalGitHubInstallation)
	mux.HandleFunc("GET /api/internal/sessions", s.handleInternalListSessions)
	mux.HandleFunc("POST /api/internal/sessions", s.handleInternalCreateSession)
	mux.HandleFunc("POST /api/internal/session-scopes/{session_scope}/retire", s.handleInternalRetireSessionScope)
	mux.HandleFunc("DELETE /api/internal/sessions/{session_id}", s.handleInternalDeleteSession)
	mux.HandleFunc("PATCH /api/internal/sessions/{session_id}", s.handleInternalPatchSession)
	mux.HandleFunc("GET /api/internal/sessions/{session_id}/capabilities", s.handleInternalSessionCapabilities)
	mux.HandleFunc("GET /api/internal/sessions/{session_id}/turns/{turn_id}/terminal", s.handleInternalSessionTurnTerminal)
	mux.HandleFunc("PUT /api/internal/sessions/{session_id}/runtime-config", s.handleInternalSessionRuntimeConfig)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/test-state", s.handleInternalSetTestState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/rollout-state", s.handleInternalSetRolloutState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/clone-state", s.handleInternalSetCloneState)
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
			if isTankMessageLinkRequest(r) && wantsTankMessageLinkJSON(r) {
				s.handleTankMessageLink(w, r)
				return
			}
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
		// Where the SPA redirects users for sign-in. Microsoft auth happens
		// at auth.romaine.life; tank-operator verifies that JWT directly.
		"auth_url":      envDefault("AUTH_URL", "https://auth.romaine.life"),
		"session_scope": envDefault("SESSION_REGISTRY_SCOPE", "default"),
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

func (s *appServer) handlePostDesignSelection(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var payload map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid selection payload"})
		return
	}
	payload["received_at"] = time.Now().UTC().Format(time.RFC3339Nano)

	if err := s.saveLatestDesignSelection(r, payload); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to store selection"})
		return
	}

	writeJSON(w, http.StatusOK, payload)
}

func (s *appServer) handleGetLatestDesignSelection(w http.ResponseWriter, r *http.Request) {
	selection, ok, err := s.loadLatestDesignSelection(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load selection"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"selection": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"selection": selection})
}

func (s *appServer) saveLatestDesignSelection(r *http.Request, payload map[string]any) error {
	s.designSelectionMu.Lock()
	s.latestDesignSelection = payload
	s.designSelectionMu.Unlock()

	if s.k8s == nil || s.designSelectionNamespace == "" {
		return nil
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	cms := s.k8s.CoreV1().ConfigMaps(s.designSelectionNamespace)
	cm, err := cms.Get(r.Context(), designSelectionConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cms.Create(r.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: designSelectionConfigMapName},
			Data:       map[string]string{"selection.json": string(encoded)},
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["selection.json"] = string(encoded)
	_, err = cms.Update(r.Context(), cm, metav1.UpdateOptions{})
	return err
}

func (s *appServer) loadLatestDesignSelection(r *http.Request) (map[string]any, bool, error) {
	if s.k8s != nil && s.designSelectionNamespace != "" {
		cm, err := s.k8s.CoreV1().ConfigMaps(s.designSelectionNamespace).Get(r.Context(), designSelectionConfigMapName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		raw := cm.Data["selection.json"]
		if raw == "" {
			return nil, false, nil
		}
		var selection map[string]any
		if err := json.Unmarshal([]byte(raw), &selection); err != nil {
			return nil, false, err
		}
		return selection, true, nil
	}

	s.designSelectionMu.Lock()
	selection := s.latestDesignSelection
	s.designSelectionMu.Unlock()
	return selection, selection != nil, nil
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
	if len(parts) == 1 && parts[0] == "index.html" && isTankMessageLinkRequest(r) {
		serveTankStaticIndexWithMessageLink(w, r, found)
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
