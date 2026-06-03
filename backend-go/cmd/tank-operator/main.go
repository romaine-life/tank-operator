package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
	"github.com/romaine-life/tank-operator/backend-go/internal/avatarassets"
	"github.com/romaine-life/tank-operator/backend-go/internal/avataruploads"
	"github.com/romaine-life/tank-operator/backend-go/internal/conversationreadstate"
	"github.com/romaine-life/tank-operator/backend-go/internal/mcpgithub"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstats"
	"github.com/romaine-life/tank-operator/backend-go/internal/pgstore"
	"github.com/romaine-life/tank-operator/backend-go/internal/profiles"
	"github.com/romaine-life/tank-operator/backend-go/internal/providerhealth"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessioncontroller"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionregistry"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessions"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/store"
)

// buildMCPGitHubClient wires up the mcpgithub client when the
// orchestrator pod has the auth.romaine.life-audience projected SA
// token mounted. Returns nil (and logs) when the token isn't mounted;
// the /api/github/repos handler then 503s loudly rather than failing
// open. Endpoint overrides (MCP_GITHUB_URL, MCP_GITHUB_EXCHANGE_URL)
// let tests + local dev point at fakes.
func buildMCPGitHubClient() *mcpgithub.Client {
	saPath := strings.TrimSpace(os.Getenv("MCP_GITHUB_SA_TOKEN_PATH"))
	if saPath == "" {
		saPath = mcpgithub.DefaultSATokenPath
	}
	if _, err := os.Stat(saPath); err != nil {
		slog.Warn("mcp-github client disabled (auth-romaine projected SA token volume not mounted); /api/github/repos will 503",
			"path", saPath, "error", err)
		return nil
	}
	return mcpgithub.NewClient(mcpgithub.Options{
		ExchangeURL:  envDefault("MCP_GITHUB_EXCHANGE_URL", mcpgithub.DefaultExchangeURL),
		MCPGitHubURL: envDefault("MCP_GITHUB_URL", mcpgithub.DefaultMCPGitHubURL),
		SATokenPath:  saPath,
	})
}

// providerHealthEmitter adapts the session-events store + bus to the
// narrow EventEmitter interface providerhealth.Manager calls when
// fanning out session.status:failed banner events. Upsert lands the
// durable row; Wake nudges every open SSE stream on the session.
type providerHealthEmitter struct {
	events       store.SessionEventStore
	materializer transcriptRowsMaterializer
	bus          *sessionbus.Bus
}

func (e providerHealthEmitter) Upsert(ctx context.Context, event map[string]any) error {
	if e.events == nil {
		return nil
	}
	if err := e.events.Upsert(ctx, event); err != nil {
		return err
	}
	return e.materializer.RefreshEvent(ctx, event)
}

func (e providerHealthEmitter) Wake(ctx context.Context, storageKey string) {
	if e.bus == nil || storageKey == "" {
		return
	}
	if err := e.bus.PublishSessionEventWake(ctx, storageKey); err != nil {
		slog.Warn("providerhealth wake publish failed",
			"storage_key", storageKey, "error", err)
	}
}

// buildProviderHealthConfigs reads CODEX_PROXY_HEALTH_URL (and, when it
// lands, CLAUDE_PROXY_HEALTH_URL) and returns one ProviderConfig per
// configured provider. An empty env var disables that provider's
// banner — useful in tests and local dev where the proxy isn't
// running. The Action target URL is the canonical re-sign-in flow on
// auth.romaine.life; the SPA opens it as a top-level navigation.
func buildProviderHealthConfigs() []providerhealth.ProviderConfig {
	var configs []providerhealth.ProviderConfig
	if url := strings.TrimSpace(os.Getenv("CODEX_PROXY_HEALTH_URL")); url != "" {
		configs = append(configs, providerhealth.ProviderConfig{
			Provider: "codex",
			Source:   providerhealth.NewHTTPSource("codex", url, nil),
			Action: providerhealth.Action{
				Label: "Re-sign-in to Codex",
				Href:  envDefault("CODEX_REAUTH_URL", "https://auth.romaine.life/codex"),
			},
		})
	}
	return configs
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
		defer pgPool.Close()
		// Test slots set RUN_MIGRATIONS=false in deployment.yaml. They share
		// the production database for realistic read validation, but schema
		// migrations are owned by the production deployment and validated in CI
		// or against a restored production copy for heavy backfills. Defaults
		// on so an unset or misspelled value cannot silently skip production
		// migrations.
		if envBoolDefault("RUN_MIGRATIONS", true) {
			// The ledger-backed engine applies only un-recorded migrations, so a
			// steady-state boot is a single SELECT — not the every-boot re-run of
			// all statements (incl. full-table backfills) that crashlooped under
			// the old engine. The generous overall budget covers the rare boot that
			// introduces a new migration (each migration is independently bounded
			// by pgstore.perMigrationTimeout); it stays bounded so an
			// unreachable/stuck database crashloops visibly instead of hanging
			// startup forever.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := pgstore.RunMigrationsWithMetrics(ctx, pgPool, promMigrationMetrics{}); err != nil {
				cancel()
				slog.Error("postgres schema migration failed", "error", err)
				os.Exit(1)
			}
			cancel()
		} else {
			slog.Warn("schema migrations disabled by RUN_MIGRATIONS=false; using existing shared schema unchanged")
		}
	}

	// 4. Init profile store.
	profileStore := buildProfileStore(pgPool)
	avatarStore := buildAvatarAssetStore(pgPool)
	avatarImageStore := buildAvatarImageStore(azCred, pgPool)
	avatarUploadAttemptStore := buildAvatarUploadAttemptStore(pgPool)
	if pgAvatarStore, ok := avatarStore.(*pgstore.AvatarAssetStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := migrateLegacyAvatarAssetImages(ctx, pgAvatarStore, avatarImageStore); err != nil {
			cancel()
			slog.Error("avatar image blob migration failed", "error", err)
			os.Exit(1)
		}
		if err := pgAvatarStore.EnsureBlobConstraints(ctx); err != nil {
			cancel()
			slog.Error("avatar asset blob constraint migration failed", "error", err)
			os.Exit(1)
		}
		cancel()
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		seedDefaultAvatarAssets(ctx, avatarStore, avatarImageStore, tankStaticRoots())
		cancel()
	}

	sessionScope := envDefault("SESSION_REGISTRY_SCOPE", "default")

	// 5. Init session registry. We also retain the concrete
	// *sessionregistry.Store (when Postgres is wired) because the
	// orphan-consumer sweep needs a scope-wide session_id query that
	// isn't on the sessions.SessionRegistry interface.
	var sessionRegStore *sessionregistry.Store
	if pgPool != nil {
		sessionRegStore = sessionregistry.NewPostgresStore(pgPool, sessionScope)
	}
	sessionReg := buildSessionRegistry(pgPool, sessionScope)

	// 6. Init session events store for the SDK runners' canonical stream.
	sessionEventsStore := buildSessionEventStore(pgPool, sessionScope)
	transcriptRowsStore := buildSessionTranscriptRowStore(pgPool, sessionScope)
	turnsStore := buildSessionTurnStore(pgPool, sessionScope)
	transcriptMaterializer := transcriptRowsMaterializer{
		events: sessionEventsStore,
		rows:   transcriptRowsStore,
		turns:  turnsStore,
	}

	// 7. Init NATS JetStream session bus for SDK commands/events.
	sessionBus := buildSessionBus(sessionScope)

	// 8. Init per-user SDK conversation read-state store.
	readStateStore := buildConversationReadStateStore(pgPool, sessionScope)
	var scheduledWakeupStore *pgstore.ScheduledWakeupStore
	if pgPool != nil {
		scheduledWakeupStore = pgstore.NewScheduledWakeupStore(pgPool, sessionScope)
	}

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
	if sessionImage == "" || codexSessionImage == "" {
		slog.Error("session image env vars missing — chart must set SESSION_IMAGE / CODEX_SESSION_IMAGE to fingerprinted tags",
			"SESSION_IMAGE", sessionImage,
			"CODEX_SESSION_IMAGE", codexSessionImage,
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

	// Test-slot session-image override (docs/testing.md): when the test-env
	// gate is on, the orchestrator can repoint NEW sessions in its scope at a
	// branch-built session image. The resolver is left nil in production, so
	// prod always stamps the chart-pinned SESSION_IMAGE / CODEX_SESSION_IMAGE.
	var imageOverrideStore *pgstore.SessionImageOverrideStore
	if pgPool != nil {
		imageOverrideStore = pgstore.NewSessionImageOverrideStore(pgPool)
	}
	sessionImageOverridesEnabled := envBool("SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED")
	var imageOverrideResolver sessions.SessionImageOverrides
	if sessionImageOverridesEnabled && imageOverrideStore != nil {
		imageOverrideResolver = imageOverrideAdapter{store: imageOverrideStore}
	}

	mgr := sessions.NewManager(k8sClient, restCfg, namespace, sessionReg, rowPublisher, sessions.ManagerOptions{
		ManifestOpts: sessionmodel.ManifestOptions{
			SessionsNamespace:              namespace,
			SessionServiceAccount:          sessionServiceAccount,
			SessionConfigMap:               envDefault("SESSION_CONFIGMAP", sessionmodel.SessionConfigMap),
			ArgoCDTrackingApp:              envDefault("ARGOCD_TRACKING_APP", "tank-operator-sessions"),
			SessionImage:                   sessionImage,
			CodexSessionImage:              codexSessionImage,
			SessionScope:                   sessionScope,
			TankOperatorInternalURL:        tankOperatorInternalURL,
			GitHubAppSecret:                envDefault("GITHUB_APP_SECRET", sessionmodel.DefaultGitHubAppSecret),
			NATSURL:                        envDefault("NATS_URL", ""),
			NATSStream:                     envDefault("NATS_STREAM", "TANK_SESSION_BUS"),
			NATSAuthSecret:                 envDefault("NATS_AUTH_SECRET", "tank-nats-auth"),
			SpireLensTailscaleOIDCClientID: envDefault("SESSION_SPIRELENS_TAILSCALE_OIDC_CLIENT_ID", ""),
			SpireLensTailscaleTailnet:      envDefault("SESSION_SPIRELENS_TAILSCALE_TAILNET", ""),
			SpireLensTailscaleAuthTag:      envDefault("SESSION_SPIRELENS_TAILSCALE_AUTH_TAG", sessionmodel.DefaultSpireLensTailscaleTag),
			SpireLensHost:                  envDefault("SESSION_SPIRELENS_HOST", ""),
			SpireLensMCPPort:               envInt("SESSION_SPIRELENS_MCP_PORT", sessionmodel.DefaultSpireLensMCPPort),
			// Test-slot SDK-runner hot-swap. Off by default; the chart
			// turns this on only when the chart runs in hot test-slot mode.
			// See scripts/check-session-pod-hot-swap-migration.mjs and
			// docs in sessionmodel.ManifestOptions.HotSwapAgentRunner.
			HotSwapAgentRunner: envBool("SESSION_AGENT_RUNNER_HOT_SWAP_ENABLED"),
		},
		OAuthGatewayHost:  os.Getenv("CLAUDE_OAUTH_GATEWAY_HOST"),
		APIProxyHost:      os.Getenv("CLAUDE_API_PROXY_HOST"),
		CodexAPIProxyHost: os.Getenv("CODEX_API_PROXY_HOST"),
		ImageOverrides:    imageOverrideResolver,
		OnImageOverrideApplied: func(scope, mode, kind string) {
			recordSessionImageOverrideApplied(scope, kind)
		},
	})

	// 10. Init auth. Tank verifies the upstream auth.romaine.life JWT
	// directly; it does not mint a service-local session token.
	verifier := auth.NewVerifier(auth.NewRomaineLifeKeyResolver())
	gitHubInstallStates := buildGitHubInstallStateStore(pgPool)
	streamAuthTickets := buildStreamAuthTicketStore(pgPool)
	messageLinkShares := buildMessageLinkShareStore(pgPool)

	// 11. Start background workers under a process signal context so rolling
	// updates can drain HTTP cleanly.
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	mgr.StartReaper(ctx)
	// Build the shared RowWriter that the K8s watch and chat-activity
	// emitter call through. Per docs/session-list-redesign.md Phase 4
	// the durable sessions row is the only persistent state â€” the prior
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
	// Wire the chat-event â†’ activity-summary delta hook so the
	// persister emits session.activity_changed rows + sessions
	// activity_summary updates on each indicator-affecting chat event.
	// Done after the session bus + lifecycle store + RowWriter are
	// built, before the persister goroutine starts.
	var activityEmitter *sessioncontroller.ChatActivityEmitter
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
		activityEmitter = emitter
		sessionBus.SetLifecycleEmitter(emitter)
		persisterStore := transcriptMaterializingEventStore{
			SessionEventStore: sessionEventsStore,
			materializer:      transcriptMaterializer,
		}
		go func() {
			if err := sessionBus.RunEventPersister(ctx, persisterStore, promPersisterMetrics{}); err != nil {
				slog.Error("session bus event persister stopped", "error", err)
			}
		}()
	}
	// Start the K8s watch producer (leader-elected; the follower keeps
	// a warm k8s client + the SSE handlers stay up). Skipped when the
	// session bus or RowWriter is unwired â€” the stub paths are
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

	// Postgres connection-saturation poller. Reuses the orchestrator's
	// AAD-aware pool (no sidecar — the upstream pg_exporter image has
	// no Entra ID workload-identity path) to read pg_stat_database and
	// current_setting('max_connections') every 30s. Drives the
	// TankPgConnectionSaturation alert that wasn't there to catch the
	// 2026-05-25 SQLSTATE 53300 orchestrator crash-loop. Skipped when
	// pgPool is nil — stub mode has no real server to poll.
	if pgPool != nil {
		poller, err := pgstats.New(pgstats.Config{
			Pool:    pgPool,
			Metrics: promPGStatsMetrics{},
		})
		if err != nil {
			slog.Error("pgstats poller init failed", "error", err)
		} else {
			go func() {
				if err := poller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("pgstats poller stopped", "error", err)
				}
			}()
		}
	}

	// Provider-credential health poller. Reads /health/<provider> on
	// the in-cluster api-proxy services, debounces sustained failures,
	// writes provider_credential_health rows, and fans session.status
	// banner events into every active session whose mode requires the
	// affected provider. The poller is the durable source of the
	// transcript-surfaced "Codex sign-in expired" banner (the
	// designed-and-visible failure surface replacing the deleted SPA
	// pill). Skipped when pgPool is nil — stub mode has no place to
	// write the Layer 1 row.
	var providerHealthManager *providerhealth.Manager
	if pgPool != nil && sessionEventsStore != nil {
		providerHealthStore := pgstore.NewProviderCredentialHealthStore(pgPool)
		providerConfigs := buildProviderHealthConfigs()
		providerHealthManager = providerhealth.NewManager(providerhealth.ManagerConfig{
			Store:     providerHealthStore,
			Pool:      pgPool,
			Emitter:   providerHealthEmitter{events: sessionEventsStore, materializer: transcriptMaterializer, bus: sessionBus},
			Providers: providerConfigs,
			Scope:     sessionScope,
			Metrics:   promProviderHealthMetrics{},
		})
		if len(providerConfigs) > 0 {
			go func() {
				if err := providerHealthManager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("providerhealth manager stopped", "error", err)
				}
			}()
		}
	}

	// Orphan-consumer sweep. Periodically deletes stranded JetStream
	// consumers (per-session data/control consumers that nothing
	// cleans up when a session goes away). Without this loop, deleted
	// sessions leak consumers until the JetStream RAM budget is
	// saturated — observed at 725 consumers / 6 live sessions on
	// 2026-05-25. Skipped in stub mode where there's no real
	// Postgres or NATS to operate against.
	startOrphanConsumerSweeps(ctx, sessionBus, sessionRegStore, sessionScope)

	// 12. Register all routes. Internal session handlers authenticate via
	// the auth.romaine.life service-principal JWT path (#486 Stage 4); the
	// pre-migration (ns, sa) allowlist env was retired with that change.
	mux := http.NewServeMux()
	srv := &appServer{
		k8s:                          k8sClient,
		restCfg:                      restCfg,
		mgr:                          mgr,
		profiles:                     profileStore,
		sessionEvents:                sessionEventsStore,
		transcriptRows:               transcriptRowsStore,
		turns:                        turnsStore,
		avatars:                      avatarStore,
		avatarImages:                 avatarImageStore,
		avatarUploads:                avatarUploadAttemptStore,
		pgPool:                       pgPool,
		sessionImageOverridesEnabled: sessionImageOverridesEnabled,
		sessionBus:                   sessionBus,
		readStates:                   readStateStore,
		activityRefresher: &scopedSessionActivityRefresher{
			pool:       pgPool,
			publisher:  sessionBus,
			localScope: sessionScope,
			local:      activityEmitter,
		},
		verifier:                 verifier,
		gitHubInstallStates:      gitHubInstallStates,
		streamAuthTickets:        streamAuthTickets,
		messageLinkShares:        messageLinkShares,
		streamRegistry:           sessionstream.NewRegistry(),
		namespace:                namespace,
		sessionScope:             sessionScope,
		sessionServiceAccount:    sessionServiceAccount,
		designSelectionNamespace: designSelectionNamespace,
		spawnQuota:               NewSpawnQuotaTracker(),
		mcpGitHub:                buildMCPGitHubClient(),
		providerHealth:           providerHealthManager,
		scheduledWakeups:         scheduledWakeupStore,
	}
	// Assign the override store only when non-nil so the appServer field stays
	// a true nil interface in stub mode (avoids the typed-nil-pointer trap that
	// would make `s.imageOverrides == nil` false and panic on first use).
	if imageOverrideStore != nil {
		srv.imageOverrides = imageOverrideStore
	}
	if scheduledWakeupStore != nil && sessionBus != nil {
		go func() {
			if err := runScheduledWakeupLoop(ctx, srv, scheduledWakeupDefaultInterval); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("scheduled wakeup loop stopped", "error", err)
			}
		}()
	}
	srv.registerRoutes(mux)

	// 13.5. Start the conversation read-cursor stagnation sampler.
	// It snapshots the open-SSE-stream registry every 60s, joins each
	// stream against the durable sessions row + conversation_read_state
	// cursor, and increments tank_conversation_read_cursor_stagnant_total
	// when an idle session's cursor lags the durable tail. Pairs with
	// the client-side navigation-mode telemetry as the
	// `TankConversationReadCursorStagnant` alert's load-bearing input.
	// Disabled when pgPool is nil (stub mode).
	if pgPool != nil {
		sampler := conversationreadstate.NewSampler(conversationreadstate.SamplerConfig{
			Registry: srv.streamRegistry,
			Lookup: conversationreadstate.SessionLookupFromQuery{
				Pool: pgPool,
			},
			ReadStates: func(scope string) conversationreadstate.ReadStateLookup {
				return srv.readStateStoreForScope(scope)
			},
			Counter:    conversationReadCursorCounterAdapter{},
			LocalScope: sessionScope,
		})
		if sampler != nil {
			go sampler.Run(ctx)
		}
	}

	// 14. Listen and serve. Every request flows through
	// httpInstrumentationMiddleware so 5xx errors carry method, route,
	// email, and the underlying detail field to slog â€” the missing
	// context that made the retired activity-polling endpoint's 500s
	// undebuggable from logs.
	server := &http.Server{
		Addr:              addr,
		Handler:           httpInstrumentationMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("starting tank-operator go server", "addr", addr)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("shutdown requested; draining tank-operator go server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown failed", "error", err)
		}
		cancel()
		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed during shutdown", "error", err)
			os.Exit(1)
		}
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

// buildPostgresPool constructs the shared Postgres connection pool the
// orchestrator's durable stores all share. Returns nil when POSTGRES_HOST is
// unset (local-dev paths fall back to in-memory stubs in the build* helpers
// below). Fails loud on any other configuration error â€” silently degrading
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

func buildAvatarAssetStore(pool *pgxpool.Pool) avatarassets.Store {
	if pool == nil {
		slog.Warn("avatar asset store using in-memory stub; POSTGRES_HOST is unset")
		return avatarassets.NewMemoryStore()
	}
	return pgstore.NewAvatarAssetStore(pool)
}

func buildAvatarImageStore(azCred *azidentity.DefaultAzureCredential, pool *pgxpool.Pool) avatarassets.ImageStore {
	if dir := strings.TrimSpace(os.Getenv("AVATAR_FILE_STORE_DIR")); dir != "" {
		store, err := avatarassets.NewFileImageStore(dir)
		if err != nil {
			slog.Error("avatar file image store init failed", "dir", dir, "error", err)
			os.Exit(1)
		}
		slog.Warn("avatar image store using filesystem backend", "dir", dir)
		return store
	}
	accountURL := strings.TrimSpace(os.Getenv("AVATAR_BLOB_ACCOUNT_URL"))
	container := strings.TrimSpace(os.Getenv("AVATAR_BLOB_CONTAINER"))
	if accountURL == "" || container == "" {
		if pool == nil {
			slog.Warn("avatar image store using in-memory stub; AVATAR_BLOB_ACCOUNT_URL/AVATAR_BLOB_CONTAINER and POSTGRES_HOST are unset")
			return avatarassets.NewMemoryImageStore()
		}
		slog.Error("avatar blob storage env vars missing while Postgres is enabled",
			"AVATAR_BLOB_ACCOUNT_URL_set", accountURL != "",
			"AVATAR_BLOB_CONTAINER_set", container != "",
		)
		os.Exit(1)
	}
	if azCred == nil {
		slog.Error("avatar blob storage configured but Azure credential unavailable")
		os.Exit(1)
	}
	store, err := avatarassets.NewAzureImageStore(accountURL, container, azCred)
	if err != nil {
		slog.Error("avatar blob image store init failed", "error", err)
		os.Exit(1)
	}
	return store
}

func buildAvatarUploadAttemptStore(pool *pgxpool.Pool) avataruploads.Store {
	if pool == nil {
		slog.Warn("avatar upload attempt store using in-memory stub; POSTGRES_HOST is unset")
		return avataruploads.NewMemoryStore()
	}
	return pgstore.NewAvatarUploadAttemptStore(pool)
}

func buildGitHubInstallStateStore(pool *pgxpool.Pool) gitHubInstallStateStore {
	if pool == nil {
		slog.Warn("github install state store disabled; POSTGRES_HOST is unset")
		return nil
	}
	return pgstore.NewGitHubInstallStateStore(pool)
}

func buildStreamAuthTicketStore(pool *pgxpool.Pool) streamAuthTicketStore {
	if pool == nil {
		slog.Warn("stream auth ticket store disabled; POSTGRES_HOST is unset")
		return nil
	}
	return pgstore.NewStreamAuthTicketStore(pool)
}

func buildMessageLinkShareStore(pool *pgxpool.Pool) messageLinkShareStore {
	if pool == nil {
		slog.Warn("message link share store disabled; POSTGRES_HOST is unset")
		return nil
	}
	return pgstore.NewMessageLinkShareStore(pool)
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

func buildSessionTranscriptRowStore(pool *pgxpool.Pool, scope string) store.SessionTranscriptRowStore {
	if pool == nil {
		return store.StubSessionTranscriptRowStore{}
	}
	return store.NewPostgresSessionTranscriptRowStore(pool, scope)
}

func buildSessionTurnStore(pool *pgxpool.Pool, scope string) store.SessionTurnStore {
	if pool == nil {
		return store.StubSessionTurnStore{}
	}
	return store.NewPostgresSessionTurnStore(pool, scope)
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
// session, which the emitter treats as "no owner, no emit" â€” fine for
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
	replicas := 3
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

// profilesPinnedReposStore persists the splash repo picker's per-user pin list.
type profilesPinnedReposStore interface {
	profilesStore
	UpdatePinnedRepos(ctx context.Context, email string, repos []string) (profiles.Profile, error)
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

// envBoolDefault reads a boolean env var, returning fallback when the value is
// unset, empty, or unrecognized. Accepts "1"/"true"/"yes" and
// "0"/"false"/"no" case-insensitively.
func envBoolDefault(name string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return fallback
	}
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
