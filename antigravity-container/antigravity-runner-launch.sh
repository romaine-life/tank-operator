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

exec node /opt/antigravity-runner/dist/index.js
