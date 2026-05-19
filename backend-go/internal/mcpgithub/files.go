package mcpgithub

import (
	"os"
	"strings"
)

// readFileTrim reads the SA-token file every call. kubelet rotates
// projected SA tokens in-place around the 50-min mark of their TTL —
// caching the byte string in memory would mean the orchestrator
// starts presenting an expired SA token to auth.romaine.life ~10
// minutes before pod-lifetime token rotation. Read-on-each-call
// keeps the exchange honest and matches the session-pod
// mcp-auth-proxy's ServiceAccountTokenProvider shape.
func readFileTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
