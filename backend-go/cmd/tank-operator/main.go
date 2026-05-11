package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/profiles"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessioncompare"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessionregistry"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

func main() {
	addr := os.Getenv("TANK_OPERATOR_ADDR")
	if addr == "" {
		addr = ":8000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /api/config", config)
	authVerifier := auth.NewVerifier(os.Getenv("JWT_SECRET"), os.Getenv("ALLOWED_EMAILS"))
	mux.HandleFunc("GET /api/auth/me", me(authVerifier, profileStore()))
	if shadowSessionsEnabled() {
		if reader, err := sessionReader(); err != nil {
			slog.Warn("session shadow endpoints disabled", "error", err)
		} else {
			mux.HandleFunc("GET /api/shadow/sessions", listSessions(reader))
			if pythonBase := pythonBaseURL(); pythonBase != "" {
				mux.HandleFunc("GET /api/shadow/sessions/compare", compareSessions(reader, pythonBase))
			}
			mux.HandleFunc("GET /api/shadow/sessions/{session_id}", getSession(reader))
		}
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("starting tank-operator go shadow server", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"entra_client_id": os.Getenv("ENTRA_CLIENT_ID"),
		"entra_authority": "https://login.microsoftonline.com/common",
	})
}

func sessionReader() (*sessions.Reader, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			if home, homeErr := os.UserHomeDir(); homeErr == nil {
				kubeconfig = filepath.Join(home, ".kube", "config")
			}
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	namespace := os.Getenv("SESSIONS_NAMESPACE")
	if namespace == "" {
		namespace = compat.SessionsNamespace
	}
	registry, err := sessionRegistry()
	if err != nil {
		return nil, err
	}
	return sessions.NewReaderWithRegistry(client, namespace, registry), nil
}

func shadowSessionsEnabled() bool {
	return os.Getenv("TANK_GO_SHADOW_SESSIONS") == "1"
}

func pythonBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("TANK_PYTHON_BASE_URL")), "/")
}

func profileStore() profiles.Store {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" {
		return profiles.StubStore{}
	}
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		slog.Warn("profile store disabled", "error", err)
		return profiles.StubStore{}
	}
	store, err := profiles.NewCosmosStore(
		endpoint,
		envDefault("COSMOS_DATABASE", "tank-operator"),
		envDefault("COSMOS_PROFILES_CONTAINER", "profiles"),
		credential,
	)
	if err != nil {
		slog.Warn("profile store disabled", "error", err)
		return profiles.StubStore{}
	}
	return store
}

func sessionRegistry() (sessions.Registry, error) {
	endpoint := strings.TrimSpace(os.Getenv("COSMOS_ENDPOINT"))
	if endpoint == "" {
		return nil, nil
	}
	database := envDefault("COSMOS_DATABASE", "tank-operator")
	container := envDefault("COSMOS_PROFILES_CONTAINER", "profiles")
	scope := strings.TrimSpace(os.Getenv("SESSION_REGISTRY_SCOPE"))
	if scope == "" {
		scope = "default"
	}
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}
	return sessionregistry.NewCosmosStore(endpoint, database, container, scope, credential)
}

func envDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func me(verifier *auth.Verifier, store profiles.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := verifier.CurrentUser(r)
		if err != nil {
			writeError(w, auth.ErrorStatus(err), err.Error())
			return
		}
		profile, err := store.GetOrCreate(r.Context(), user.Email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sub":             user.Sub,
			"email":           user.Email,
			"name":            user.Name,
			"avatar_url":      auth.GravatarURL(user.Email, 64),
			"github_login":    profile.GitHubLogin,
			"installation_id": profile.InstallationID,
		})
	}
}

func listSessions(reader *sessions.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		if owner == "" {
			writeError(w, http.StatusBadRequest, "missing owner query parameter")
			return
		}
		result, err := reader.List(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func compareSessions(reader *sessions.Reader, pythonBase string) http.HandlerFunc {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		if owner == "" {
			writeError(w, http.StatusBadRequest, "missing owner query parameter")
			return
		}
		goSessions, err := reader.List(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		pythonSessions, err := fetchPythonSessions(r.Context(), client, pythonBase, r.Header)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sessioncompare.Compare(pythonSessions, goSessions))
	}
}

func getSession(reader *sessions.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		if owner == "" {
			writeError(w, http.StatusBadRequest, "missing owner query parameter")
			return
		}
		sessionID := strings.TrimSpace(r.PathValue("session_id"))
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "missing session id")
			return
		}
		result, err := reader.Get(r.Context(), owner, sessionID)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, result)
		case errors.Is(err, sessions.ErrNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, sessions.ErrNotOwned):
			writeError(w, http.StatusForbidden, "session not owned")
		case errors.Is(err, context.Canceled):
			return
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
	}
}

func fetchPythonSessions(ctx context.Context, client *http.Client, pythonBase string, headers http.Header) ([]sessions.Info, error) {
	endpoint, err := url.JoinPath(pythonBase, "/api/sessions")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"Authorization", "Cookie"} {
		for _, value := range headers.Values(name) {
			req.Header.Add(name, value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("python /api/sessions returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out []sessions.Info
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"detail": message})
}
