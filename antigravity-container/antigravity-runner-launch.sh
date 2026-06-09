#!/usr/bin/env bash
# Launch the antigravity-runner sidecar for an antigravity_gui session.
#
# Credential boundary (mirrors the Claude/Codex proxy shape): agy NEVER holds
# the real Google OAuth blob. We write a PLACEHOLDER token into agy's data dir
# and route agy's Code Assist traffic through the antigravity-api-proxy (via pod
# hostAliases). The proxy owns the refresh token, injects the real access token
# on every request, and refreshes against Google on upstream 401. The placeholder
# carries a far-future expiry and no
# refresh token, so agy never attempts an in-place refresh — the same way the
# Claude/Codex session CLIs are seeded.
#
# agy is a Go binary, so it trusts the proxy's leaf via the system trust store.
# We concatenate the mounted oauth-gateway CA with the base bundle and export
# SSL_CERT_FILE (honored by Go's crypto/x509) before exec'ing the runner, which
# spawns agy with the inherited environment.
set -euo pipefail

AGY_HOME="${HOME}/.gemini/antigravity-cli"
TANK_SESSION_CONFIG_DIR="${TANK_SESSION_CONFIG_DIR:-/opt/tank/session-config}"
mkdir -p "${AGY_HOME}"

# Placeholder OAuth token. access_token == the proxy's injection discriminator
# ("Bearer managed-by-tank-operator"); the far-future expiry stops agy from
# refreshing; no refresh_token is present so a compromised agy has nothing
# durable to exfiltrate. Shape matches the real harvest blob
# ({token:{...}, auth_method}) so agy parses it.
cat > "${AGY_HOME}/antigravity-oauth-token" <<'EOF'
{"token":{"access_token":"managed-by-tank-operator","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"},"auth_method":"consumer"}
EOF
chmod 600 "${AGY_HOME}/antigravity-oauth-token"

# Antigravity's current CLI config root is ~/.gemini/config. On first run agy
# migrates from ~/.gemini/antigravity-cli into that root, but a prewritten
# mcp_config.json there is preserved and loaded before tool discovery. Tank's
# canonical MCP surface is the chart-managed mcp.json, so fail the runner if it
# is absent instead of starting an agent with silently missing MCP tools.
MCP_SOURCE="${TANK_SESSION_CONFIG_DIR}/mcp.json"
AGY_MCP_CONFIG_DIR="${HOME}/.gemini/config"
AGY_MCP_CONFIG="${AGY_MCP_CONFIG_DIR}/mcp_config.json"
if [ ! -s "${MCP_SOURCE}" ]; then
  echo "antigravity-runner-launch: required MCP config missing or empty at '${MCP_SOURCE}'" >&2
  exit 1
fi
if ! jq -e 'has("mcpServers") and (.mcpServers | type == "object")' "${MCP_SOURCE}" >/dev/null; then
  echo "antigravity-runner-launch: MCP config at '${MCP_SOURCE}' is not a valid mcpServers document" >&2
  exit 1
fi
mkdir -p "${AGY_MCP_CONFIG_DIR}"
tmp_mcp="${AGY_MCP_CONFIG}.tmp.$$"
jq '.' "${MCP_SOURCE}" > "${tmp_mcp}"
chmod 600 "${tmp_mcp}"
mv "${tmp_mcp}" "${AGY_MCP_CONFIG}"

# Trust the antigravity-api-proxy leaf. SSL_CERT_FILE replaces Go's default
# bundle, so we must concatenate the system roots (for any genuine TLS agy does
# directly, e.g. telemetry) with our internal CA.
CA="${ANTIGRAVITY_OAUTH_GATEWAY_CA:-}"
if [ -n "${CA}" ] && [ -f "${CA}" ]; then
  BUNDLE=/tmp/agy-ca-bundle.crt
  SYS_BUNDLE=/etc/ssl/certs/ca-certificates.crt
  if [ -f "${SYS_BUNDLE}" ]; then
    cat "${SYS_BUNDLE}" "${CA}" > "${BUNDLE}"
  else
    cp "${CA}" "${BUNDLE}"
  fi
  export SSL_CERT_FILE="${BUNDLE}"
else
  echo "antigravity-runner-launch: no oauth-gateway CA at '${CA}'; agy will not trust the proxy leaf" >&2
fi

if [ -f "${TANK_SESSION_CONFIG_DIR}/install-tank-skills.sh" ]; then
  sh "${TANK_SESSION_CONFIG_DIR}/install-tank-skills.sh" || true
fi

# In test-slot mode the pod spec sets GLIMMUNG_SUPERVISOR_CHILD, and the
# supervisor becomes PID 1 so Glimmung can restart the runner after copying a
# hot artifact. Production leaves the env var unset and keeps the direct exec.
if [ -n "${GLIMMUNG_SUPERVISOR_CHILD:-}" ] && [ -x /app/tank-supervisor ]; then
  exec /app/tank-supervisor
fi
exec /opt/tank/antigravity-cli-runner
