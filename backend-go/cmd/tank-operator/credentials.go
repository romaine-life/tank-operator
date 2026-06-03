package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/romaine-life/tank-operator/backend-go/internal/keyvault"
	"github.com/romaine-life/tank-operator/backend-go/internal/kubeexec"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionmodel"
)

// doSaveCredentials harvests credentials from the pod and stores them in Key Vault.
func doSaveCredentials(w http.ResponseWriter, r *http.Request, s *appServer, email, mode, podName string) {
	vaultURL := os.Getenv("AZURE_KEYVAULT_URL")
	if vaultURL == "" {
		writeError(w, http.StatusServiceUnavailable, "AZURE_KEYVAULT_URL not configured")
		return
	}

	var (
		execCmd   []string
		kvKeyEnv  string
		kvDefault string
	)

	switch mode {
	case sessionmodel.CodexConfigMode:
		execCmd = []string{"sh", "-c", "cat $HOME/.codex/auth.json"}
		kvKeyEnv = "CODEX_CREDENTIALS_KV_KEY"
		kvDefault = "codex-credentials"
	default:
		// claude / config modes
		execCmd = []string{"sh", "-c", "cat $HOME/.claude/.credentials.json"}
		kvKeyEnv = "CLAUDE_CREDENTIALS_KV_KEY"
		kvDefault = "claude-code-credentials"
	}

	kvSecretName := envDefault(kvKeyEnv, kvDefault)

	out, err := kubeexec.Capture(r.Context(), s.k8s, s.restCfg, s.namespace, podName, execCmd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read credentials from pod: "+err.Error())
		return
	}

	// Validate the JSON.
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		writeError(w, http.StatusBadRequest, "credentials not valid JSON: "+err.Error())
		return
	}
	if len(parsed) == 0 {
		writeError(w, http.StatusBadRequest, "credentials JSON is empty")
		return
	}

	cred, azErr := azidentity.NewDefaultAzureCredential(nil)
	if azErr != nil {
		writeError(w, http.StatusInternalServerError, "azure credential: "+azErr.Error())
		return
	}

	if err := keyvault.SetSecret(r.Context(), vaultURL, kvSecretName, string(out), cred); err != nil {
		writeError(w, http.StatusInternalServerError, "keyvault set: "+err.Error())
		return
	}

	slog.Info("credentials saved to keyvault", "email", email, "key", kvSecretName)
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"key":    fmt.Sprintf("%s/secrets/%s", vaultURL, kvSecretName),
	})
}
