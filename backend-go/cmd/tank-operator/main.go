package main

import (
	"context"
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
	"github.com/nelsong6/tank-operator/backend-go/internal/pgstore"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionbus"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionmodel"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionregistry"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

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

	mgr := sessions.NewManager(k8sClient, restCfg, namespace, sessionReg, sessionBus, sessions.ManagerOptions{
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
		},
		OAuthGatewayHost:  os.Getenv("CLAUDE_OAUTH_GATEWAY_HOST"),
		APIProxyHost:      os.Getenv("CLAUDE_API_PROXY_HOST"),
		CodexAPIProxyHost: os.Getenv("CODEX_API_PROXY_HOST"),
	})

	// 10. Init auth signer + verifier (RS256, signing key in KV).
	jwtKey, err := buildJWTSigner(azCred)
	if err != nil {
		slog.Error("JWT signing key failed", "error", err)
		os.Exit(1)
	}
	// Access gate is the role claim on the auth.romaine.life JWT (verified at
	// exchange time and stamped onto the tank-operator session JWT). The
	// Verifier just checks role ∈ {admin, user}; no per-tank email allowlist.
	verifier := auth.NewVerifier(jwtKey)
	minter := auth.NewMinter(jwtKey, jwtKey)

	// 11. Start reaper.
	ctx := context.Background()
	mgr.StartReaper(ctx)
	if sessionBus != nil {
		go func() {
			if err := sessionBus.RunEventPersister(ctx, sessionEventsStore, promPersisterMetrics{}); err != nil {
				slog.Error("session bus event persister stopped", "error", err)
			}
		}()
	}

	// 12. Parse internal allowed subjects.
	// Accepts both "ns/name=email" and plain "ns/name" entries.
	internalSubjects := parseInternalSubjects(
		envDefault("INTERNAL_API_ALLOWED_SUBJECTS", "mcp-tank-operator/mcp-tank-operator,mcp-glimmung/mcp-glimmung"),
	)

	// 13. Register all routes.
	mux := http.NewServeMux()
	srv := &appServer{
		k8s:                      k8sClient,
		restCfg:                  restCfg,
		mgr:                      mgr,
		profiles:                 profileStore,
		sessionEvents:            sessionEventsStore,
		sessionBus:               sessionBus,
		readStates:               readStateStore,
		verifier:                 verifier,
		minter:                   minter,
		namespace:                namespace,
		sessionScope:             sessionScope,
		sessionServiceAccount:    sessionServiceAccount,
		designSelectionNamespace: designSelectionNamespace,
		internalAllowedSubjects:  internalSubjects,
	}
	srv.registerRoutes(mux)

	// 14. Listen and serve. Every request flows through
	// httpInstrumentationMiddleware so 5xx errors carry method, route,
	// email, and the underlying detail field to slog — the missing
	// context that made "/api/sessions/activity returned 500"
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
// in-memory stub below) is just a constructor change.
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
func (r *stubSessionRegistry) MarkDeleted(_ context.Context, _, _ string) error        { return nil }

func envDefault(name, fallback string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v
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

// parseInternalSubjects parses a comma-separated list of "ns/name" or "ns/name=email" entries
// into a map[qualified]email. Plain entries without "=" are mapped to "".
func parseInternalSubjects(raw string) map[string]string {
	m := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.IndexByte(entry, '=')
		if idx > 0 {
			subj := strings.TrimSpace(entry[:idx])
			email := strings.ToLower(strings.TrimSpace(entry[idx+1:]))
			if subj != "" {
				m[subj] = email
			}
		} else {
			// Plain "ns/name" without email.
			m[entry] = ""
		}
	}
	return m
}
