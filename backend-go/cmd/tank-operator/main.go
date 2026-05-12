package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
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

	// 3. Init profile store.
	profileStore := buildProfileStore(azCred)

	// 4. Init session registry.
	sessionReg := buildSessionRegistry(azCred)

	// 5. Init active runs store.
	activeRunsStore := buildActiveRunStore(azCred)

	// 6. Init run events store.
	runEventsStore := buildRunEventStore(azCred)

	// 6b. Init turn queue store for durable SDK submissions.
	turnQueueStore := buildTurnQueueStore(azCred)

	// 6c. Init session events store for the SDK runners' canonical stream.
	sessionEventsStore := buildSessionEventStore(azCred)

	// 6d. Init per-user SDK conversation read-state store.
	readStateStore := buildConversationReadStateStore(azCred)

	// 7. Init event bus.
	eventBus := sessions.NewEventBus()

	// 8. Init Manager.
	namespace := envDefault("SESSIONS_NAMESPACE", compat.SessionsNamespace)

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

	mgr := sessions.NewManager(k8sClient, restCfg, namespace, sessionReg, eventBus, sessions.ManagerOptions{
		ManifestOpts: compat.ManifestOptions{
			ArgoCDTrackingApp: envDefault("ARGOCD_TRACKING_APP", "tank-operator-sessions"),
			SessionImage:      sessionImage,
			CodexSessionImage: codexSessionImage,
			PiSessionImage:    piSessionImage,
			// Pass the orchestrator's Cosmos config through to the pod's
			// agent-runner via env vars — same endpoint, same database,
			// the runner authenticates with its own UAMI (federated to
			// claude-session SA, see infra/tank_session_identity.tf).
			CosmosEndpoint:               envDefault("COSMOS_ENDPOINT", ""),
			CosmosDatabase:               envDefault("COSMOS_DATABASE", "tank-operator"),
			CosmosSessionEventsContainer: envDefault("COSMOS_SESSION_EVENTS_CONTAINER", "session-events"),
			CosmosTurnQueueContainer:     envDefault("COSMOS_TURN_QUEUE_CONTAINER", "turn-queue"),
		},
		OAuthGatewayHost: os.Getenv("CLAUDE_OAUTH_GATEWAY_HOST"),
		APIProxyHost:     os.Getenv("CLAUDE_API_PROXY_HOST"),
	})

	// 9. Init auth signer + verifier (RS256, signing key in KV).
	jwtKey, err := buildJWTSigner(azCred)
	if err != nil {
		slog.Error("JWT signing key failed", "error", err)
		os.Exit(1)
	}
	allowedEmails := os.Getenv("ALLOWED_EMAILS")
	verifier := auth.NewVerifier(jwtKey, allowedEmails)
	minter := auth.NewMinter(jwtKey, jwtKey, allowedEmails)

	// 10. Start reaper.
	ctx := context.Background()
	mgr.StartReaper(ctx)

	// 11. Parse internal allowed subjects.
	// Accepts both "ns/name=email" and plain "ns/name" entries.
	internalSubjects := parseInternalSubjects(
		envDefault("INTERNAL_API_ALLOWED_SUBJECTS", "mcp-github/mcp-github,mcp-tank-operator/mcp-tank-operator,mcp-glimmung/mcp-glimmung"),
	)

	// 12. Register all routes.
	mux := http.NewServeMux()
	srv := &appServer{
		k8s:                     k8sClient,
		restCfg:                 restCfg,
		mgr:                     mgr,
		profiles:                profileStore,
		activeRuns:              activeRunsStore,
		runEvents:               runEventsStore,
		turnQueue:               turnQueueStore,
		sessionEvents:           sessionEventsStore,
		readStates:              readStateStore,
		eventBus:                eventBus,
		verifier:                verifier,
		minter:                  minter,
		namespace:               namespace,
		internalAllowedSubjects: internalSubjects,
	}
	srv.registerRoutes(mux)

	// 12. Listen and serve.
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
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

func buildProfileStore(azCred *azidentity.DefaultAzureCredential) profilesStore {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return profiles.StubStore{}
	}
	store, err := profiles.NewCosmosStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_PROFILES_CONTAINER", "profiles"),
		azCred,
	)
	if err != nil {
		slog.Warn("profile store disabled", "error", err)
		return profiles.StubStore{}
	}
	return store
}

func buildSessionRegistry(azCred *azidentity.DefaultAzureCredential) sessions.SessionRegistry {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return &stubSessionRegistry{}
	}
	scope := envDefault("SESSION_REGISTRY_SCOPE", "default")
	s, err := sessionregistry.NewCosmosStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_PROFILES_CONTAINER", "profiles"),
		scope,
		azCred,
	)
	if err != nil {
		slog.Warn("session registry disabled, using stub", "error", err)
		return &stubSessionRegistry{}
	}
	return &cosmosSessionRegistryAdapter{s}
}

func buildActiveRunStore(azCred *azidentity.DefaultAzureCredential) store.ActiveRunStore {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return store.StubActiveRunStore{}
	}
	s, err := store.NewCosmosActiveRunStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_ACTIVE_RUNS_CONTAINER", "active-runs"),
		azCred,
	)
	if err != nil {
		slog.Warn("active run store disabled", "error", err)
		return store.StubActiveRunStore{}
	}
	return s
}

func buildSessionEventStore(azCred *azidentity.DefaultAzureCredential) store.SessionEventStore {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return store.StubSessionEventStore{}
	}
	s, err := store.NewCosmosSessionEventStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_SESSION_EVENTS_CONTAINER", "session-events"),
		azCred,
	)
	if err != nil {
		slog.Warn("session events store disabled", "error", err)
		return store.StubSessionEventStore{}
	}
	return s
}

func buildConversationReadStateStore(azCred *azidentity.DefaultAzureCredential) store.ConversationReadStateStore {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return store.NewStubConversationReadStateStore()
	}
	s, err := store.NewCosmosConversationReadStateStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_PROFILES_CONTAINER", "profiles"),
		azCred,
	)
	if err != nil {
		slog.Warn("conversation read-state store disabled", "error", err)
		return store.NewStubConversationReadStateStore()
	}
	return s
}

func buildTurnQueueStore(azCred *azidentity.DefaultAzureCredential) store.TurnQueueStore {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return store.StubTurnQueueStore{}
	}
	s, err := store.NewCosmosTurnQueueStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_TURN_QUEUE_CONTAINER", "turn-queue"),
		azCred,
	)
	if err != nil {
		slog.Warn("turn queue store disabled", "error", err)
		return store.StubTurnQueueStore{}
	}
	return s
}

func buildRunEventStore(azCred *azidentity.DefaultAzureCredential) store.RunEventStore {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" || azCred == nil {
		return store.StubRunEventStore{}
	}
	s, err := store.NewCosmosRunEventStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_RUN_EVENTS_CONTAINER", "run-events"),
		azCred,
	)
	if err != nil {
		slog.Warn("run event store disabled", "error", err)
		return store.StubRunEventStore{}
	}
	return s
}

// profilesStore is the interface satisfied by both CosmosStore and StubStore.
type profilesStore interface {
	GetOrCreate(ctx context.Context, email string) (profiles.Profile, error)
}

// profilesUpdateStore is an optional interface for updating installation ID.
type profilesUpdateStore interface {
	profilesStore
	UpdateInstallation(ctx context.Context, email string, installationID int64, githubLogin *string) (profiles.Profile, error)
}

// profilesPrefsStore is an optional interface for the SPA's run-pref
// sync (Phase E). Implemented by CosmosStore and StubStore. The handler
// surfaces a 503 when the backing store doesn't satisfy it.
type profilesPrefsStore interface {
	profilesStore
	UpdatePrefs(ctx context.Context, email string, prefs map[string]any) (profiles.Profile, error)
}

// cosmosSessionRegistryAdapter wraps CosmosStore to satisfy sessions.SessionRegistry.
type cosmosSessionRegistryAdapter struct {
	*sessionregistry.CosmosStore
}

// stubSessionRegistry is an in-memory stub satisfying sessions.SessionRegistry.
type stubSessionRegistry struct {
	mu      sync.Mutex
	counter int64
}

func (r *stubSessionRegistry) List(_ context.Context, _ string) ([]compat.SessionRecord, error) {
	return nil, nil
}
func (r *stubSessionRegistry) NextSessionID(_ context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counter++
	return fmt.Sprintf("%d", r.counter), nil
}
func (r *stubSessionRegistry) Upsert(_ context.Context, _ compat.SessionRecord) error  { return nil }
func (r *stubSessionRegistry) SetName(_ context.Context, _, _ string, _ *string) error { return nil }
func (r *stubSessionRegistry) MarkDeleted(_ context.Context, _, _ string) error        { return nil }

func envDefault(name, fallback string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	return v
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
