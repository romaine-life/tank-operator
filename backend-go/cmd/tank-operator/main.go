package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/hermes"
	"github.com/nelsong6/tank-operator/backend-go/internal/mcpgithub"
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionregistry"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// buildHermesBridge constructs the hermes bridge from env config. Returns
// nil when HERMES_API_URL is unset OR the audience-pinned projected SA
// token volume is not mounted (which would mean the orchestrator can't
// mint a role=service JWT to call Hermes); handlers branch on nil and
// return 503 so a missing-config surfaces as a visible error rather
// than a silent NPE. See nelsong6/tank-operator#540 + nelsong6/auth#42.
//
// Token shape: a fresh role=service JWT minted per cache-miss by
// exchanging the projected SA token at auth.romaine.life/api/auth/exchange/k8s.
// The orchestrator is admitted as a pod-stable consumer in nelsong6/auth#42
// (slug=tank-operator, stableId=orchestrator). Cache TTL = (token exp -
// 30s) so a steady-state load issues ~one exchange call per 15min.
//
// The bridge's row-publish hook is unset for now: hermes_gui sessions
// don't yet wire activity-summary updates onto the SPA sidebar's row
// stream. Tracked as a follow-up.
func buildHermesBridge(eventStore store.SessionEventStore, scope string) *hermes.Bridge {
	baseURL := strings.TrimSpace(os.Getenv("HERMES_API_URL"))
	if baseURL == "" {
		slog.Warn("hermes bridge disabled (missing HERMES_API_URL); hermes_gui sessions will return 503")
		return nil
	}
	tokenSource := hermes.NewAuthRomaineServiceProvider(hermes.AuthRomaineOptions{
		ExchangeURL: os.Getenv("HERMES_AUTH_ROMAINE_EXCHANGE_URL"),
		SATokenPath: os.Getenv("HERMES_AUTH_ROMAINE_SA_TOKEN_PATH"),
	})
	if tokenSource == nil {
		slog.Warn("hermes bridge disabled (auth-romaine projected SA token volume not mounted); hermes_gui sessions will return 503")
		return nil
	}
	client := hermes.NewClient(hermes.Options{BaseURL: baseURL, Tokens: tokenSource})
	return hermes.NewBridge(hermes.BridgeOptions{
		Client:   client,
		Store:    eventStore,
		Scope:    scope,
		Recorder: promHermesRecorder{},
	})
}

// promHermesRecorder bridges the hermes.Recorder interface to the
// tank_hermes_* prometheus counters defined in observability.go.
type promHermesRecorder struct{}

func (promHermesRecorder) RunCreated() {
	hermesRunTotal.WithLabelValues("created").Inc()
}
func (promHermesRecorder) RunCreateFailed() {
	hermesRunTotal.WithLabelValues("failed_to_create").Inc()
}
func (promHermesRecorder) RunTerminal(terminal string) {
	hermesRunTerminalTotal.WithLabelValues(terminal).Inc()
}
func (promHermesRecorder) TranslatorError(reason string) {
	hermesTranslatorErrorTotal.WithLabelValues(reason).Inc()
}

// buildMCPGitHubClient wires up the mcpgithub client when the
// orchestrator pod has the auth.romaine.life-audience projected SA
// token mounted. The same token Hermes already uses today — stage 2
// reuses the path rather than minting a parallel projected volume.
// Returns nil (and logs) when the token isn't mounted; the
// /api/github/repos handler then 503s loudly rather than failing
// open. Endpoint overrides (MCP_GITHUB_URL,
// MCP_GITHUB_EXCHANGE_URL) let tests + local dev point at fakes.
func buildMCPGitHubClient() *mcpgithub.Client {
	saPath := strings.TrimSpace(os.Getenv("MCP_GITHUB_SA_TOKEN_PATH"))
	if saPath == "" {
		saPath = strings.TrimSpace(os.Getenv("HERMES_AUTH_ROMAINE_SA_TOKEN_PATH"))
	}
	if saPath == "" {
		saPath = mcpgithub.DefaultSATokenPath
	}
	if _, err := os.Stat(saPath); err != nil {
		slog.Warn("mcp-github client disabled (auth-romaine projected SA token volume not mounted); /api/github/repos will 503",
			"path", saPath, "error", err)
		return nil
	}
	return mcpgithub.NewClient(mcpgithub.Options{
		ExchangeURL:  envDefault("MCP_GITHUB_EXCHANGE_URL", os.Getenv("HERMES_AUTH_ROMAINE_EXCHANGE_URL")),
		MCPGitHubURL: envDefault("MCP_GITHUB_URL", mcpgithub.DefaultMCPGitHubURL),
		SATokenPath:  saPath,
	})
}

func main() {
	addr := envDefault("TANK_OPERATOR_ADDR", ":8000")

	// 1. Load K8s client.
	restCfg, err := loadKubeConfig()
	if err != nil {
		slog.Error("k8s config failed", "error", err)
		os.Exit(1)
	}
	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		slog.Error("k8s client failed", "error", err)
		os.Exit(1)
	}

	// 2. Init Azure credential.
	azCred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		slog.Warn("azure credential failed, some features may be degraded", "error", err)
	}

	// 3. Init Postgres pool + schema. Replaces the prior per-store Cosmos
	// client construction. When POSTGRES_HOST is unset (local dev), pool is
	// nil and the build* helpers below fall back to in-memory stubs.
	pgPool := buildPostgresPool(azCred)
	if pgPool != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := pgstore.RunMigrations(ctx, pgPool); err != nil {
			cancel()
			slog.Error("postgres schema migration failed", "error", err)
			os.Exit(1)
		}
		cancel()
		defer pgPool.Close()
	}

	// 4. Init profile store.
	profileStore := buildProfileStore(pgPool)

	sessionScope := envDefault("SESSION_REGISTRY_SCOPE", "default")

	// 5. Init session registry.
	sessionReg := buildSessionRegistry(pgPool, sessionScope)

	// 6. Init session events store for the SDK runners' canonical stream.
	sessionEventsStore := buildSessionEventStore(pgPool, sessionScope)

	// 7. Init NATS JetStream session bus for SDK commands/events.
	sessionBus := buildSessionBus(sessionScope)

	// 8. Init per-user SDK conversation read-state store.
	readStateStore := buildConversationReadStateStore(pgPool, sessionScope)

	// 8. Init Manager. SessionListWaker wakes are routed through the
	// NATS session bus (per-email subject), replacing the prior
	// in-process EventBus.
	namespace := envDefault("SESSIONS_NAMESPACE", sessionmodel.SessionsNamespace)
	sessionServiceAccount := envDefault("SESSION_SERVICE_ACCOUNT", sessionmodel.SessionServiceAccount)
	tankOperatorInternalURL := envDefault("TANK_OPERATOR_INTERNAL_URL", "http://tank-operator.tank-operator.svc.cluster.local")
	designSelectionNamespace := envDefault("DESIGN_SELECTION_NAMESPACE", currentPodNamespace())

	// Session image tags come from the chart's values.yaml session.*
	// keys, bumped per-commit to fingerprinted tags by the
	// claude-container-build workflow. Fail loudly at startup if any
	// are missing — a silent `:latest` fallback hid this exact bug for
	// 15 hours after the Go cutover (the Python orchestrator read these
	// env vars; the Go port forgot, every new session pod fell back to
	// an April-25 `:latest` that didn't have mcp-auth-proxy installed,
	// every claude_gui session crashlooped).
	sessionImage := os.Getenv("SESSION_IMAGE")
	codexSessionImage := os.Getenv("CODEX_SESSION_IMAGE")
	piSessionImage := os.Getenv("PI_SESSION_IMAGE")
	if sessionImage == "" || codexSessionImage == "" || piSessionImage == "" {
		slog.Error("session image env vars missing — chart must set SESSION_IMAGE / CODEX_SESSION_IMAGE / PI_SESSION_IMAGE to fingerprinted tags",
			"SESSION_IMAGE", sessionImage,
			"CODEX_SESSION_IMAGE", codexSessionImage,
			"PI_SESSION_IMAGE", piSessionImage,
		)
		os.Exit(1)
	}

	// Build the RowPublisher every lifecycle producer fans row updates
	// through (Manager user-actions, sessioncontroller K8s watch,
	// chat-activity emitter). The Fetcher reads post-write row state
	// from the registry; the Publisher hands the marshaled payload to
	// NATS. Per docs/session-list-redesign.md Phase 3 this is the
	// single wire path the SPA's SessionStore consumes — no typed-
	// event reducer, no event-type switch.
	rowPublisher := &sessioncontroller.RowPublisher{
		Fetcher:   rowFetcherFor(sessionReg),
		Publisher: sessionBus,
		Scope:     sessionScope,
	}

	mgr := sessions.NewManager(k8sClient, restCfg, namespace, sessionReg, rowPublisher, sessions.ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionsNamespace:       namespace,
			SessionServiceAccount:   sessionServiceAccount,
			SessionConfigMap:        envDefault("SESSION_CONFIGMAP", sessionmodel.SessionConfigMap),
			ArgoCDTrackingApp:       envDefault("ARGOCD_TRACKING_APP", "tank-operator-sessions"),
			SessionImage:            sessionImage,
			CodexSessionImage:       codexSessionImage,
			PiSessionImage:          piSessionImage,
			SessionScope:            sessionScope,
			TankOperatorInternalURL: tankOperatorInternalURL,
			GitHubAppSecret:         envDefault("GITHUB_APP_SECRET", sessionmodel.DefaultGitHubAppSecret),
			NATSURL:                 envDefault("NATS_URL", ""),
			NATSStream:              envDefault("NATS_STREAM", "TANK_SESSION_BUS"),
			NATSAuthSecret:          envDefault("NATS_AUTH_SECRET", "tank-nats-auth"),
			// Test-slot agent-runner hot-swap. Off by default; the chart
			// turns this on only when the chart runs in hot test-slot mode.
			// See scripts/check-session-pod-hot-swap-migration.mjs and
			// docs in sessionmodel.ManifestOptions.HotSwapAgentRunner.
			HotSwapAgentRunner: envBool("SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED"),
		},
		OAuthGatewayHost:  os.Getenv("CLAUDE_OAUTH_GATEWAY_HOST"),
		APIProxyHost:      os.Getenv("CLAUDE_API_PROXY_HOST"),
		CodexAPIProxyHost: os.Getenv("CODEX_API_PROXY_HOST"),
	})

	// 10. Init auth signer + verifier (RS256). Two key sources:
	//   - KV-backed `tank-operator-jwt-signing` for legacy tank-operator-
	//     minted JWTs (browser cookies issued by /api/auth/exchange).
	//   - auth.romaine.life's published JWKS for JWTs the platform IdP
	//     mints directly (admin bot tokens from /admin/bot-tokens,
	//     future service-role tokens, anything else issued by
	//     auth.romaine.life itself).
	// The chained resolver tries both and short-circuits on the first
	// success. Key namespaces are disjoint in production — auth.romaine
	// .life signs with auth-jwt-signing, tank-operator signs with
	// tank-operator-jwt-signing — so the chain produces a deterministic
	// answer per kid.
	jwtKey, err := buildJWTSigner(azCred)
	if err != nil {
		slog.Error("JWT signing key failed", "error", err)
		os.Exit(1)
	}
	keyResolver := auth.NewChainedKeyResolver(
		auth.NewRomaineLifeKeyResolver(),
		jwtKey,
	)
	verifier := auth.NewVerifier(keyResolver)
	minter := auth.NewMinter(jwtKey, jwtKey)

	// 11. Start reaper.
	ctx := context.Background()
	mgr.StartReaper(ctx)
	// Build the shared RowWriter that the K8s watch and chat-activity
	// emitter call through. Per docs/session-list-redesign.md Phase 4
	// the durable sessions row is the only persistent state — the prior
	// session_lifecycle_events ledger is gone. RowWriter updates the
	// row columns (status / ready_at / terminating_at / activity_summary
	// + row_version bump) and fans the post-write row state out on the
	// NATS row-update subject.
	var rowWriter *sessioncontroller.RowWriter
	if sessionBus != nil {
		rw, err := sessioncontroller.NewRowWriter(
			rowPublisher,
			pgPool,
			promRowWriterMetrics{},
		)
		if err != nil {
			slog.Error("session controller row writer init failed", "error", err)
			os.Exit(1)
		}
		rowWriter = rw
	}
	// Wire the chat-event → activity-summary delta hook so the
	// persister emits session.activity_changed rows + sessions
	// activity_summary updates on each indicator-affecting chat event.
	// Done after the session bus + lifecycle store + RowWriter are
	// built, before the persister goroutine starts.
	if sessionBus != nil && rowWriter != nil {
		emitter := &sessioncontroller.ChatActivityEmitter{
			Writer:     rowWriter,
			ChatEvents: sessionEventsStore,
			ReadStates: readStateStore,
			Registry:   buildSessionRegistryOwnerResolver(sessionReg),
			Rows:       rowFetcherFor(sessionReg),
			Metrics:    promLifecycleEmitterMetrics{},
			Scope:      sessionScope,
		}
		sessionBus.SetLifecycleEmitter(emitter)
		go func() {
			if err := sessionBus.RunEventPersister(ctx, sessionEventsStore, promPersisterMetrics{}); err != nil {
				slog.Error("session bus event persister stopped", "error", err)
			}
		}()
	}
	// Start the K8s watch producer (leader-elected; the follower keeps
	// a warm k8s client + the SSE handlers stay up). Skipped when the
	// session bus or RowWriter is unwired — the stub paths are
	// local-dev only and have no consumers.
	if rowWriter != nil && pgPool != nil {
		orchestratorNamespace := currentPodNamespace()
		if orchestratorNamespace == "" {
			orchestratorNamespace = "tank-operator"
		}
		go func() {
			cfg := sessioncontroller.K8sWatchConfig{
				K8s:            k8sClient,
				Writer:         rowWriter,
				Metrics:        promK8sWatchMetrics{},
				Scope:          sessionScope,
				Namespace:      namespace,
				LeaseNamespace: orchestratorNamespace,
				Identity:       strings.TrimSpace(os.Getenv("HOSTNAME")),
			}
			if err := sessioncontroller.RunK8sWatch(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("session controller k8s watch stopped", "error", err)
			}
		}()
	}

	// 12. Register all routes. Internal session handlers authenticate via
	// the auth.romaine.life service-principal JWT path (#486 Stage 4); the
	// pre-migration (ns, sa) allowlist env was retired with that change.
	mux := http.NewServeMux()
	srv := &appServer{
		k8s:                      k8sClient,
		restCfg:                  restCfg,
		mgr:                      mgr,
		profiles:                 profileStore,
		sessionEvents:            sessionEventsStore,
		pgPool:                   pgPool,
		sessionBus:               sessionBus,
		readStates:               readStateStore,
		verifier:                 verifier,
		minter:                   minter,
		namespace:                namespace,
		sessionScope:             sessionScope,
		sessionServiceAccount:    sessionServiceAccount,
		designSelectionNamespace: designSelectionNamespace,
		spawnQuota:               NewSpawnQuotaTracker(),
		hermesBridge:             buildHermesBridge(sessionEventsStore, sessionScope),
		mcpGitHub:                buildMCPGitHubClient(),
	}
	srv.registerRoutes(mux)

	// 14. Listen and serve. Every request flows through
	// httpInstrumentationMiddleware so 5xx errors carry method, route,
	// email, and the underlying detail field to slog — the missing
	// context that made the retired activity-polling endpoint's 500s
	// undebuggable from logs.
	server := &http.Server{
		Addr:              addr,
		Handler:           httpInstrumentationMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("starting tank-operator go server", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func loadKubeConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// buildJWTSigner constructs the Key Vault-backed JWT signer/verifier the
// orchestrator uses for its session and install-state tokens. Required:
// JWT_KV_VAULT (vault DNS URL) and JWT_KV_KEY_NAME (key name within the
// vault). Returns an error if either is unset or KV is unreachable —
// the orchestrator must not silently fall back to an unsigned/HS256 path.
func buildJWTSigner(azCred *azidentity.DefaultAzureCredential) (*auth.KeyVaultJWT, error) {
	vaultURL := strings.TrimSpace(os.Getenv("JWT_KV_VAULT"))
	keyName := strings.TrimSpace(os.Getenv("JWT_KV_KEY_NAME"))
	if vaultURL == "" || keyName == "" {
		return nil, fmt.Errorf("JWT_KV_VAULT and JWT_KV_KEY_NAME must be set")
	}
	if azCred == nil {
		return nil, fmt.Errorf("azure credential not available")
	}
	return auth.NewKeyVaultJWT(vaultURL, keyName, azCred)
}

// buildPostgresPool constructs the shared Postgres connection pool the
// orchestrator's durable stores all share. Returns nil when POSTGRES_HOST is
// unset (local-dev paths fall back to in-memory stubs in the build* helpers
// below). Fails loud on any other configuration error — silently degrading
// hides the bug where the orchestrator runs against stubs in production.
func buildPostgresPool(azCred *azidentity.DefaultAzureCredential) *pgxpool.Pool {
	host := strings.TrimSpace(os.Getenv("POSTGRES_HOST"))
	if host == "" {
		slog.Warn("POSTGRES_HOST unset; durable stores will use in-memory stubs")
		return nil
	}
	if azCred == nil {
		slog.Error("POSTGRES_HOST set but Azure credential unavailable")
		os.Exit(1)
	}
	username := envDefault("POSTGRES_USER", "claude-credentials-refresher-identity")
	database := envDefault("POSTGRES_DATABASE", "tank-operator")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgstore.NewPool(ctx, pgstore.Config{
		Host:         host,
		Database:     database,
		Username:     username,
		Credential:   azCred,
		QueryMetrics: promPGMetrics{},
	})
	if err != nil {
		slog.Error("postgres pool init failed", "error", err)
		os.Exit(1)
	}
	return pool
}

func buildProfileStore(pool *pgxpool.Pool) profilesStore {
	if pool == nil {
		return profiles.StubStore{}
	}
	return profiles.NewPostgresStore(pool)
}

func buildSessionRegistry(pool *pgxpool.Pool, scope string) sessions.SessionRegistry {
	if pool == nil {
		return &stubSessionRegistry{}
	}
	return &sessionRegistryAdapter{sessionregistry.NewPostgresStore(pool, scope)}
}

func buildSessionEventStore(pool *pgxpool.Pool, scope string) store.SessionEventStore {
	if pool == nil {
		return store.StubSessionEventStore{}
	}
	return store.NewPostgresSessionEventStore(pool, scope)
}

// rowFetcherFor adapts the SessionRegistry interface to the narrower
// sessioncontroller.RowFetcher shape. The Postgres-backed Store has a
// Get method; the in-memory stub falls back to a not-found result so
// row-update publishes silently no-op in local-dev mode.
func rowFetcherFor(reg sessions.SessionRegistry) sessioncontroller.RowFetcher {
	if fetcher, ok := reg.(sessioncontroller.RowFetcher); ok {
		return fetcher
	}
	return stubRowFetcher{}
}

type stubRowFetcher struct{}

func (stubRowFetcher) Get(_ context.Context, _, _ string) (sessionmodel.SessionRecord, bool, error) {
	return sessionmodel.SessionRecord{}, false, nil
}

// buildSessionRegistryOwnerResolver wraps the SessionRegistry interface
// so the chat-activity emitter can call OwnerForSession via the narrow
// sessioncontroller.SessionToOwnerResolver interface. The Postgres-backed
// Store satisfies this directly; the in-memory stub returns "" for every
// session, which the emitter treats as "no owner, no emit" — fine for
// local dev.
func buildSessionRegistryOwnerResolver(reg sessions.SessionRegistry) sessioncontroller.SessionToOwnerResolver {
	if resolver, ok := reg.(sessioncontroller.SessionToOwnerResolver); ok {
		return resolver
	}
	return stubOwnerResolver{}
}

type stubOwnerResolver struct{}

func (stubOwnerResolver) OwnerForSession(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func buildConversationReadStateStore(pool *pgxpool.Pool, scope string) store.ConversationReadStateStore {
	if pool == nil {
		return store.NewStubConversationReadStateStore()
	}
	return store.NewPostgresConversationReadStateStore(pool, scope)
}

func buildSessionBus(scope string) *sessionbus.Bus {
	url := strings.TrimSpace(os.Getenv("NATS_URL"))
	if url == "" {
		slog.Warn("session bus disabled; NATS_URL is unset")
		return nil
	}
	replicas := 2
	if raw := strings.TrimSpace(os.Getenv("NATS_STREAM_REPLICAS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			replicas = parsed
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bus, err := sessionbus.Connect(ctx, sessionbus.Config{
		URL:               url,
		Token:             os.Getenv("NATS_TOKEN"),
		Stream:            envDefault("NATS_STREAM", "TANK_SESSION_BUS"),
		Scope:             scope,
		Replicas:          replicas,
		WakeMetrics:       promWakeMetrics{},
		ConnectionMetrics: promNATSConnectionMetrics{},
	})
	if err != nil {
		slog.Error("session bus unavailable", "error", err)
		os.Exit(1)
	}
	return bus
}

// profilesStore is the interface satisfied by both the Postgres-backed
// profiles.Store and the in-memory StubStore (for local dev without
// POSTGRES_HOST set).
type profilesStore interface {
	GetOrCreate(ctx context.Context, email string) (profiles.Profile, error)
}

// profilesUpdateStore is an optional interface for updating installation ID.
type profilesUpdateStore interface {
	profilesStore
	UpdateInstallation(ctx context.Context, email string, installationID int64, githubLogin *string) (profiles.Profile, error)
}

// profilesPrefsStore is an optional interface for the SPA's run-pref
// sync (Phase E). Implemented by the Postgres-backed profiles.Store and
// StubStore. The handler surfaces a 503 when the backing store doesn't
// satisfy it.
type profilesPrefsStore interface {
	profilesStore
	UpdatePrefs(ctx context.Context, email string, prefs map[string]any) (profiles.Profile, error)
}

// sessionRegistryAdapter wraps the Postgres-backed sessionregistry.Store so it
// satisfies sessions.SessionRegistry. The interface methods live on the embedded
// store; this adapter exists so swapping in a different backing impl (e.g. the
// in-memory stub below) is just a constructor change. The embedded Store also
// satisfies sessionToOwnerResolver via its OwnerForSession method, so the
// ChatActivityEmitter can resolve owner emails through the same adapter.
type sessionRegistryAdapter struct {
	*sessionregistry.Store
}

// stubSessionRegistry is an in-memory stub satisfying sessions.SessionRegistry.
type stubSessionRegistry struct {
	mu      sync.Mutex
	counter int64
}

func (r *stubSessionRegistry) List(_ context.Context, _ string) ([]sessionmodel.SessionRecord, error) {
	return nil, nil
}
func (r *stubSessionRegistry) NextSessionID(_ context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counter++
	return fmt.Sprintf("%d", r.counter), nil
}
func (r *stubSessionRegistry) Upsert(_ context.Context, _ sessionmodel.SessionRecord) error {
	return nil
}
func (r *stubSessionRegistry) SetName(_ context.Context, _, _ string, _ *string) error { return nil }
func (r *stubSessionRegistry) SetTestState(_ context.Context, _, _ string, _ map[string]any) error {
	return nil
}
func (r *stubSessionRegistry) SetRolloutState(_ context.Context, _, _ string, _ map[string]any) error {
	return nil
}
func (r *stubSessionRegistry) SetCloneState(_ context.Context, _, _ string, _ map[string]any) error {
	return nil
}
func (r *stubSessionRegistry) Reorder(_ context.Context, _ string, orderedIDs []string) ([]string, error) {
	return orderedIDs, nil
}
func (r *stubSessionRegistry) MarkDeleted(_ context.Context, _, _ string) error { return nil }

func envDefault(name, fallback string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v
}

// envBool reads a boolean env var. Accepts "1", "true", "yes" (case-insensitive)
// as true; anything else (including unset and empty) as false. Used for
// development-mode toggles like SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED.
func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes"
}

func currentPodNamespace() string {
	raw, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func parseEmailSet(raw string) map[string]bool {
	m := map[string]bool{}
	for _, entry := range strings.Split(raw, ",") {
		email := strings.ToLower(strings.TrimSpace(entry))
		if email != "" {
			m[email] = true
		}
	}
	return m
}
