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

	// 7. Init event bus.
	eventBus := sessions.NewEventBus()

	// 8. Init Manager.
	namespace := envDefault("SESSIONS_NAMESPACE", compat.SessionsNamespace)
	mgr := sessions.NewManager(k8sClient, restCfg, namespace, sessionReg, eventBus, sessions.ManagerOptions{
		ManifestOpts: compat.ManifestOptions{
			ArgoCDTrackingApp: envDefault("ARGOCD_TRACKING_APP", "tank-operator-sessions"),
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
func (r *stubSessionRegistry) Upsert(_ context.Context, _ compat.SessionRecord) error { return nil }
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
