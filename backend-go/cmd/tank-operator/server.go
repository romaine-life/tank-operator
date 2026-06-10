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

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/avatarassets"
	"github.com/romaine-life/tank-operator/backend-go/internal/avataruploads"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/providerhealth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
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
	turns               store.SessionTurnStore
	avatars             avatarassets.Store
	avatarImages        avatarassets.ImageStore
	avatarUploads       avataruploads.Store
	pgPool              *pgxpool.Pool
	sessionBus          sessionCommandBus
	// rowWriter is the shared session-row transition writer (same instance
	// the K8s watch and chat-activity emitter use). The internal
	// provider-fatal endpoint routes runner-reported agent-process death
	// through it so the session moves to Failed exactly like pod death.
	rowWriter           *sessioncontroller.RowWriter
	readStates          store.ConversationReadStateStore
	activityRefresher   sessionActivityRefresher
	verifier            *auth.Verifier
	gitHubInstallStates gitHubInstallStateStore
	streamAuthTickets   streamAuthTicketStore
	messageLinkShares   messageLinkShareStore
	staticPages         staticPageSnapshotStore
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

	// scheduledWakeups is the durable backend-owned provider wakeup store.
	// Runners register self-resume schedule tool_use items here; the
	// orchestrator claims due rows and feeds them through the normal SDK
	// turn boundary instead of holding process-local timers in a session pod.
	scheduledWakeups scheduledWakeupStore

	// backgroundTaskWakes is the durable backend-owned store for "a Claude
	// background task finished while the session was idle" wakes. The runner
	// registers the natural terminal; the orchestrator claims due rows and
	// feeds them through the normal SDK turn boundary (source=background-task)
	// instead of relying on a task-lifecycle frame that never starts a turn.
	backgroundTaskWakes backgroundTaskWakeStore

	// controlActions is the durable audit ledger for privileged cross-system
	// effects initiated by session pods through MCP servers. It backs the
	// user-facing "what changed main, from which session?" trace.
	controlActions controlActionStore

	// pendingLaunch is the durable store for attachment-backed deferred
	// launches (#865): the create boundary registers the launch, the
	// launch-attachments upload endpoint stages bytes, and the dispatch
	// reconciler claims ready rows, materializes the bytes into the pod, and
	// publishes submit_turn — so the launch survives a browser that goes away
	// after create. nil in stub mode / when pgPool is unset.
	pendingLaunch pendingLaunchStore

	// imageOverrides backs the test-slot session-image repoint flow
	// (docs/testing.md): the internal /session-scopes/{scope}/image-override
	// endpoints read/write it, and the Manager resolves it at session-create.
	// nil in stub mode / when pgPool is unset.
	imageOverrides sessionImageOverrideStore
	// sessionImageOverridesEnabled gates the override write path on the
	// test-env signal (SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED). false in
	// production, where the Manager resolver is also left nil.
	sessionImageOverridesEnabled bool
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
	PublishPinnedReposUpdate(ctx context.Context, email string) error
	SubscribePinnedReposUpdates(ctx context.Context, email string) (<-chan struct{}, func(), error)
}

type streamAuthTicketStore interface {
	Create(context.Context, pgstore.StreamAuthTicket) error
	Validate(ctx context.Context, token, streamKind, sessionScope, sessionID string) (pgstore.StreamAuthTicket, error)
}

type messageLinkShareStore interface {
	Create(context.Context, pgstore.MessageLinkShare) error
	Get(context.Context, string) (pgstore.MessageLinkShare, error)
}

type staticPageSnapshotStore interface {
	Upsert(context.Context, pgstore.StaticPageSnapshot) error
	Get(ctx context.Context, scope, sessionID, relPath string) (pgstore.StaticPageSnapshot, error)
}

type scheduledWakeupStore interface {
	Register(context.Context, pgstore.RegisterScheduledWakeupRequest) (pgstore.ScheduledWakeup, error)
	ClaimDue(context.Context, time.Time, int, time.Duration) ([]pgstore.ScheduledWakeup, error)
	ListBySession(context.Context, string, string) ([]pgstore.ScheduledWakeup, error)
	MarkFired(context.Context, string, string) (pgstore.ScheduledWakeup, error)
	MarkFailed(context.Context, string, string) (pgstore.ScheduledWakeup, error)
	ScheduledDueCount(context.Context, time.Time) (int, error)
	CancelPendingForSession(context.Context, string, string) ([]pgstore.ScheduledWakeup, error)
}

type pendingLaunchStore interface {
	Register(context.Context, pgstore.RegisterPendingLaunchRequest) (pgstore.PendingLaunchTurn, error)
	StageAttachment(context.Context, string, string, pgstore.LaunchAttachmentBlob) (pgstore.PendingLaunchStatus, error)
	ClaimReady(context.Context, time.Time, int, time.Duration) ([]pgstore.PendingLaunchTurn, error)
	FindStale(context.Context, time.Time, int) ([]pgstore.PendingLaunchTurn, error)
	LoadAttachments(context.Context, string, string) ([]pgstore.LaunchAttachmentBlob, error)
	MarkDispatched(context.Context, string, string, string) error
	MarkFailed(context.Context, string, string, string) error
	Get(context.Context, string, string) (pgstore.PendingLaunchTurn, error)
}

type backgroundTaskWakeStore interface {
	Register(context.Context, pgstore.RegisterBackgroundTaskWakeRequest) (pgstore.BackgroundTaskWake, error)
	ClaimDue(context.Context, time.Time, int, time.Duration) ([]pgstore.BackgroundTaskWake, error)
	MarkFired(context.Context, string, string) error
	MarkFailed(context.Context, string, string) error
	Release(context.Context, string) error
	DueCount(context.Context, time.Time) (int, error)
	CancelPendingForSession(context.Context, string, string) (int64, error)
}

type controlActionStore interface {
	Append(context.Context, pgstore.ControlActionEvent) (pgstore.ControlActionEvent, error)
	ListBySession(context.Context, string, string, string, int) ([]pgstore.ControlActionEvent, error)
}

// sessionImageOverrideStore is the durable per-scope session-image override
// surface backing the test-slot repoint flow. Satisfied by
// *pgstore.SessionImageOverrideStore; an interface so handler tests can fake it
// without Postgres.
type sessionImageOverrideStore interface {
	Get(ctx context.Context, scope string) (pgstore.SessionImageOverride, error)
	Upsert(ctx context.Context, ov pgstore.SessionImageOverride) error
	Delete(ctx context.Context, scope string) (bool, error)
}

func (s *appServer) registerRoutes(mux *http.ServeMux) {
	// Health / config / metrics.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/provider-quotas", s.handleProviderQuotas)
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
	// Browser-side main-thread long-task probe. Surfaces input-
	// blocking ≥50 ms blocks (the failure mode behind "clicks aren't
	// responding") with a correlation label tying each block to the
	// most-recent tank-event / session-switch / scroll the SPA saw.
	mux.HandleFunc("POST /api/client-metrics/long-tasks", s.handleLongTaskMetrics)

	// Avatar assets. Reads are authenticated so uploaded backing photos
	// are not exposed as static public files; writes are admin-only.
	mux.HandleFunc("GET /api/avatars", s.handleListAvatars)
	mux.HandleFunc("GET /api/avatars/{avatar_id}/image", s.handleGetAvatarImage)
	mux.HandleFunc("GET /api/avatars/{avatar_id}/backing", s.handleGetAvatarBacking)
	mux.HandleFunc("GET /api/admin/avatar-decks", s.handleGetAvatarDecks)
	mux.HandleFunc("POST /api/admin/avatars", s.handleCreateAvatar)
	mux.HandleFunc("PATCH /api/admin/avatars/{avatar_id}", s.handleUpdateAvatar)
	mux.HandleFunc("PATCH /api/admin/avatars/{avatar_id}/kind", s.handleUpdateAvatarKind)
	mux.HandleFunc("DELETE /api/admin/avatars/{avatar_id}", s.handleDeleteAvatar)
	mux.HandleFunc("GET /api/admin/session-report", s.handleAdminSessionReport)
	mux.HandleFunc("POST /api/admin/session-report-shares", s.handleCreateSessionReportShare)
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
	mux.HandleFunc("GET /api/github/pinned-repos", s.handleGitHubPinnedRepos)
	mux.HandleFunc("PUT /api/github/pinned-repos", s.handleGitHubPinnedRepos)
	mux.HandleFunc("GET /api/github/pinned-repos/events", s.handleGitHubPinnedReposEvents)
	// /api/github/repos enumerates the caller's GitHub App installation
	// repos via mcp-github. Pairs with the auth.romaine.life on-behalf-of
	// exchange so the orchestrator can mint a service JWT acting for the
	// SPA user.
	mux.HandleFunc("GET /api/github/repos", s.handleGitHubRepos)
	mux.HandleFunc("GET /api/bug-labels", s.handleListBugLabels)
	mux.HandleFunc("GET /api/session-run-options", s.handleSessionRunOptions)

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
	// Admin-only operator inbox backed by Prometheus/Alertmanager:
	// firing Tank alerts, recent orchestrator 5xx routes, and links
	// into the scoped /api/debug/* surfaces for detail.
	mux.HandleFunc("GET /api/debug/observability-summary", s.handleDebugObservabilitySummary)
	// Admin-only audit surface for the durable session_events ledger.
	// The projected transcript/message-link paths are the normal
	// owner-readable pickup flow, including visible=false sidebar
	// tombstones; this raw-ledger surface is for audit/debug detail
	// when the projection is not enough.
	mux.HandleFunc("GET /api/debug/session-event-ledger", s.handleDebugSessionEventLedger)
	// Admin-only debug surface for the durable conversation_read_state
	// cursor + sessions.activity_summary view. Pairs with the
	// TankChatScrollUserAtBottomLatched alert: when the alert fires,
	// the runbook directs the operator here for a per-session lag
	// computation against the durable ledger.
	mux.HandleFunc("GET /api/debug/conversation-read-state", s.handleDebugConversationReadState)
	// Admin-only debug surface for orchestrator-detected stuck turns
	// (durably accepted submitted/claimed with no provider progress past
	// the stall threshold). Pairs with the TankSessionStuckInProgress
	// alert: when the gauge is nonzero, the runbook directs the operator
	// here for the session_ids + stuck_seconds + provider rate-limit
	// state of the wedged turns.
	mux.HandleFunc("GET /api/debug/stuck-turns", s.handleDebugStuckTurns)
	mux.HandleFunc("PUT /api/sessions/order", s.handleReorderSessions)
	mux.HandleFunc("DELETE /api/sessions/{session_id}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{session_id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{session_id}/touch", s.handleTouchSession)
	mux.HandleFunc("PATCH /api/sessions/{session_id}", s.handlePatchSession)
	mux.HandleFunc("PUT /api/sessions/{session_id}/open-target", s.handleSetOpenTarget)
	mux.HandleFunc("PUT /api/sessions/{session_id}/bug-label", s.handleSetSessionBugLabel)
	mux.HandleFunc("POST /api/sessions/{session_id}/test-state", s.handleSetTestState)
	mux.HandleFunc("POST /api/sessions/{session_id}/rollout-state", s.handleSetRolloutState)
	mux.HandleFunc("POST /api/sessions/{session_id}/save-credentials", s.handleSaveCredentials)
	mux.HandleFunc("POST /api/sessions/{session_id}/paste-image", s.handlePasteImage)
	mux.HandleFunc("POST /api/sessions/{session_id}/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/{session_id}/message-links", s.handleCreateMessageLinkShare)
	mux.HandleFunc("POST /api/sessions/with-context", s.handleCreateSessionWithContext)

	// File endpoints.
	mux.HandleFunc("GET /api/sessions/{session_id}/files", s.handleListFiles)
	mux.HandleFunc("GET /api/sessions/{session_id}/files/content", s.handleGetFileContent)
	mux.HandleFunc("GET /api/sessions/{session_id}/files/raw", s.handleGetFileRaw)
	mux.HandleFunc("GET /api/sessions/{session_id}/files/walk", s.handleWalkFiles)
	mux.HandleFunc("POST /api/sessions/{session_id}/files/upload", s.handleUploadFile)
	mux.HandleFunc("PUT /api/sessions/{session_id}/files/content", s.handleWriteFile)
	mux.HandleFunc("POST /api/sessions/{session_id}/static-pages", s.handleCaptureStaticPage)
	mux.HandleFunc("GET /api/sessions/{session_id}/static-pages", s.handleGetStaticPage)
	// Durable staging for an attachment-backed deferred launch (#865): the
	// bytes land in Postgres keyed by the launch turn id + ordinal, and the
	// dispatch reconciler writes them into the pod. Unlike files/upload (which
	// writes straight into the live pod), this survives a browser that goes
	// away before the pod is ready.
	mux.HandleFunc("PUT /api/sessions/{session_id}/launch-attachments/{ordinal}", s.handleStageLaunchAttachment)
	mux.HandleFunc("GET /api/sessions/{session_id}/skills", s.handleListSkills)
	mux.HandleFunc("GET /api/sessions/{session_id}/mcp-servers", s.handleListMCPServers)
	mux.HandleFunc("GET /api/sessions/{session_id}/mcp-tools", s.handleListMCPTools)

	// App-managed chat surface.
	mux.HandleFunc("POST /api/sessions/{session_id}/turns", s.handleEnqueueSessionTurn)
	mux.HandleFunc("POST /api/sessions/{session_id}/turns/{turn_id}/interrupt", s.handleInterruptSessionTurn)
	mux.HandleFunc("POST /api/sessions/{session_id}/turns/{turn_id}/answer", s.handleAnswerSessionTurn)
	mux.HandleFunc("GET /api/sessions/{session_id}/background-tasks", s.handleListSessionBackgroundTasks)
	mux.HandleFunc("POST /api/sessions/{session_id}/background-tasks/{task_id}/stop", s.handleStopBackgroundTask)
	mux.HandleFunc("GET /api/sessions/{session_id}/events", s.handleSessionEventStream)
	mux.HandleFunc("GET /api/sessions/{session_id}/timeline", s.handleSessionTimeline)
	mux.HandleFunc("GET /api/sessions/{session_id}/scheduled-wakeups", s.handleListScheduledWakeups)
	mux.HandleFunc("POST /api/sessions/{session_id}/scheduled-wakeups/cancel", s.handleCancelScheduledWakeups)
	mux.HandleFunc("GET /api/sessions/{session_id}/control-actions", s.handleListControlActions)
	mux.HandleFunc("GET /api/sessions/{session_id}/turns/{turn_id}/activity", s.handleSessionTurnActivity)
	// Durable resolver for the public per-session turn number: the canonical
	// route is /sessions/{id}/turns/{n}; this maps n -> turn_id + anchor cursor
	// server-side so a cold deep link resolves from session_turns, not from the
	// browser's loaded transcript window.
	mux.HandleFunc("GET /api/sessions/{session_id}/turns/{number}", s.handleResolveSessionTurnNumber)
	mux.HandleFunc("PUT /api/sessions/{session_id}/read-state", s.handleUpdateSessionReadState)

	// Public read-only transcript shares. These are intentionally not
	// general unauthenticated session routes: every read validates an
	// opaque token minted by the authenticated copy-message-link action.
	mux.HandleFunc("GET /api/public/message-links/{share_token}", s.handleGetPublicMessageLink)
	mux.HandleFunc("GET /api/public/message-links/{share_token}/avatars", s.handlePublicMessageLinkAvatars)
	mux.HandleFunc("GET /api/public/message-links/{share_token}/avatars/{avatar_id}/image", s.handlePublicMessageLinkAvatarImage)
	mux.HandleFunc("GET /api/public/message-links/{share_token}/avatars/{avatar_id}/backing", s.handlePublicMessageLinkAvatarBacking)
	mux.HandleFunc("GET /api/public/message-links/{share_token}/timeline", s.handlePublicMessageLinkTimeline)
	mux.HandleFunc("GET /api/public/message-links/{share_token}/turns/{turn_id}/activity", s.handlePublicMessageLinkTurnActivity)
	mux.HandleFunc("GET /api/public/session-report-shares/{share_token}", s.handleGetPublicSessionReportShare)

	// CLI / sandbox agent.
	mux.HandleFunc("POST /api/sessions/{session_id}/cli-process", s.handleCLIProcess)
	mux.HandleFunc("GET /api/sessions/{session_id}/sandbox-agent/v1/processes/{process_id}/terminal/ws", s.handleSandboxTerminalProxy)

	// Internal API.
	mux.HandleFunc("GET /api/internal/github/installation", s.handleInternalGitHubInstallation)
	mux.HandleFunc("GET /api/internal/session-run-options", s.handleInternalSessionRunOptions)
	mux.HandleFunc("GET /api/internal/sessions", s.handleInternalListSessions)
	mux.HandleFunc("POST /api/internal/sessions", s.handleInternalCreateSession)
	mux.HandleFunc("POST /api/internal/session-scopes/{session_scope}/retire", s.handleInternalRetireSessionScope)
	mux.HandleFunc("GET /api/internal/session-scopes/{session_scope}/image-override", s.handleInternalGetSessionImageOverride)
	mux.HandleFunc("PUT /api/internal/session-scopes/{session_scope}/image-override", s.handleInternalSetSessionImageOverride)
	mux.HandleFunc("DELETE /api/internal/session-scopes/{session_scope}/image-override", s.handleInternalDeleteSessionImageOverride)
	mux.HandleFunc("DELETE /api/internal/sessions/{session_id}", s.handleInternalDeleteSession)
	mux.HandleFunc("PATCH /api/internal/sessions/{session_id}", s.handleInternalPatchSession)
	mux.HandleFunc("GET /api/internal/sessions/{session_id}/capabilities", s.handleInternalSessionCapabilities)
	mux.HandleFunc("GET /api/internal/sessions/{session_id}/timeline", s.handleInternalSessionTimeline)
	mux.HandleFunc("GET /api/internal/sessions/{session_id}/turns/{turn_id}/terminal", s.handleInternalSessionTurnTerminal)
	mux.HandleFunc("PUT /api/internal/sessions/{session_id}/runtime-config", s.handleInternalSessionRuntimeConfig)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/scheduled-wakeups", s.handleInternalRegisterScheduledWakeup)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/background-task-wakes", s.handleInternalRegisterBackgroundTaskWake)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/provider-fatal", s.handleInternalProviderFatal)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/agent-continuation", s.handleInternalAgentContinuation)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/control-actions", s.handleInternalAppendControlAction)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/test-state", s.handleInternalSetTestState)
	mux.HandleFunc("POST /api/internal/sessions/{session_id}/pull-request-link", s.handleInternalSetPullRequestLink)
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
		"spirelens_mcp_available": boolConfigString(
			envDefault("SESSION_SPIRELENS_TAILSCALE_OIDC_CLIENT_ID", "") != "" &&
				envDefault("SESSION_SPIRELENS_TAILSCALE_TAILNET", "") != "" &&
				envDefault("SESSION_SPIRELENS_HOST", "") != "",
		),
		"fork_session_prompt_template": readOptionalFile(
			os.Getenv("TANK_FORK_SESSION_PROMPT_FILE"),
			defaultForkSessionPromptTemplate,
		),
		// Splash-page initial-message mode directives. Source of truth is the
		// markdown under k8s/app-config/, rendered into the app-config
		// ConfigMap and mounted into this pod; editing those files on main
		// (ArgoCD sync) changes the directive with no image rebuild. The
		// const fallbacks below only apply during first-install ordering
		// before the ConfigMap is mounted (mirrors fork_session_prompt_template).
		"initial_mode_diagnose_directive": readOptionalFile(
			os.Getenv("TANK_INITIAL_MODE_DIAGNOSE_FILE"),
			defaultInitialModeDiagnoseDirective,
		),
		"initial_mode_bug_report_directive": readOptionalFile(
			os.Getenv("TANK_INITIAL_MODE_BUG_REPORT_FILE"),
			defaultInitialModeBugReportDirective,
		),
		"initial_mode_quality_gaps_directive": readOptionalFile(
			os.Getenv("TANK_INITIAL_MODE_QUALITY_GAPS_FILE"),
			defaultInitialModeQualityGapsDirective,
		),
		"initial_mode_go_long_directive": readOptionalFile(
			os.Getenv("TANK_INITIAL_MODE_GO_LONG_FILE"),
			defaultInitialModeGoLongDirective,
		),
		"initial_mode_test_directive": readOptionalFile(
			os.Getenv("TANK_INITIAL_MODE_TEST_FILE"),
			defaultInitialModeTestDirective,
		),
	}
}

func boolConfigString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

const defaultForkSessionPromptTemplate = `The user forked this session from an assistant message in another Tank Operator session to deal with a divergent issue.

Use the forked assistant message as the immediate starting point. The previous session data is identified below; read that session's transcript from Tank Operator data if it would help, but do not assume you need the entire prior conversation before making progress.

Forked assistant message:
{{forked_message}}

Source session pointer:
` + "```json" + `
{{source_session_json}}
` + "```"

// Initial-message mode directive fallbacks. These mirror the canonical text in
// k8s/app-config/initial-mode-*.md and are served only when the ConfigMap file
// is not mounted (first-install ordering / local dev). The mounted file is the
// live source of truth in-cluster, so these consts are allowed to lag a live
// edit — the SPA carries the same fallback for the offline path.
const defaultInitialModeDiagnoseDirective = `Initial message type: diagnose — first message only.

When you respond to this first message, investigate the issue, gather evidence, and report findings only; do not edit files or make code changes in this turn.

The no-code stance applies to this first turn only — once I reply, treat the session normally and make code changes when the work calls for it.`

const defaultInitialModeBugReportDirective = `Initial message type: bug report — first response only.

This is a serious bug-investigation and design session. Do not edit files or make code changes in the first response.

Before forming a fix, read /workspace/.tank/docs/quality-timeframes.md, /workspace/.tank/docs/migration-policy.md, and /workspace/.tank/docs/product-inspirations.md.

If any of those docs is missing, report it as a session setup gap before proceeding.

Once the in-scope repo is cloned, also read whichever of its own diagnostic, design, and quality docs exist (docs/diagnostic-discipline*.md, docs/quality-timeframes*.md, docs/migration-policy*.md, docs/design-system*.md, docs/product-inspirations*.md, docs/architecture*.md, any design-system/SKILL.md, plus AGENTS.md and CLAUDE.md). The repo's own docs win where they are more specific; the global invariants set the floor.

In the first response:

1. Restate the reported bug as a falsifiable behavior claim.
2. Gather evidence before proposing a cause. Use durable sources before logs or live symptoms when the repo guidance says they are the source of truth.
3. Identify the architectural miss: what invariant, ownership boundary, durable state, observability, or migration guard should have prevented or exposed this bug?
4. Propose the code-change shape that fixes the class of bug, not only the observed symptom.
5. Explain how the proposal conforms to the north-star docs, including tests, observability, migration cleanup, and any deploy/runtime risks.
6. Stop and wait for permission before making code changes.

After I approve the proposal, treat the session normally and make code changes when the work calls for it.`

const defaultInitialModeQualityGapsDirective = `Initial message type: address this issue and inspect the quality/migration gaps it exposes.

Read /workspace/.tank/docs/quality-timeframes.md and /workspace/.tank/docs/migration-policy.md before planning.

If either policy doc is missing, report that as a session setup gap before proceeding.

Make the required code changes and call out any gaps against those docs.`

const defaultInitialModeGoLongDirective = `Initial message type: go long. This is the long-horizon, heavy-solution bar — the durable solution is the only acceptable outcome, and the docs named below are binding invariants, not suggestions.

Before planning, read /workspace/.tank/docs/quality-timeframes.md, /workspace/.tank/docs/migration-policy.md, and /workspace/.tank/docs/product-inspirations.md.

If any of those docs is missing, report it as a session setup gap before proceeding.

Once the in-scope repo is cloned, also read whichever of its own design/quality docs exist (docs/quality-timeframes*.md, docs/migration-policy*.md, docs/design-system*.md, docs/product-inspirations*.md, docs/architecture*.md, any design-system/SKILL.md, plus AGENTS.md and CLAUDE.md). The repo's own docs win where they are more specific; the global invariants set the floor.

Heavy is the default: do not present a minimal fix as the option and do not ask me to choose quick-vs-thorough. If the full solution is too large for one PR, write the full plan first and stage it so each step leaves the system coherent.

Settled decisions stay settled: do not reintroduce a route, flag, type, test, doc, or UI path that a prior change deliberately removed. Treat legacy, compatibility, fallback, and temporary as deletion targets, not design options.

Definition of done is quality-timeframes.md — check the work against it before calling it complete, and name any remaining hardening as unfinished scope rather than optional.`

const defaultInitialModeTestDirective = `Initial message type: make code changes and immediately run the test skill for this.

Use the test skill workflow as part of implementation and keep the test environment updated while validating.`

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
