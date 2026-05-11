package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/sessions"
)

func main() {
	addr := os.Getenv("TANK_OPERATOR_ADDR")
	if addr == "" {
		addr = ":8000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	if shadowSessionsEnabled() {
		if reader, err := sessionReader(); err != nil {
			slog.Warn("session shadow endpoints disabled", "error", err)
		} else {
			mux.HandleFunc("GET /api/shadow/sessions", listSessions(reader))
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
	return sessions.NewReader(client, namespace), nil
}

func shadowSessionsEnabled() bool {
	return os.Getenv("TANK_GO_SHADOW_SESSIONS") == "1"
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"detail": message})
}
