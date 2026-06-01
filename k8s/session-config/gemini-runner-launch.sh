#!/bin/sh
# Pod-side launch shim for the gemini-runner Node service. Sibling of
# agent-runner-launch.sh and codex-runner-launch.sh.
#
# Gemini auth path: write settings.json for oauth-personal auth. gemini_test
# mounts real test credentials and runs without the proxy; normal Gemini modes
# fall back to managed-by-tank-operator placeholders that the in-cluster
# gemini-api-proxy swaps for current Google OAuth tokens.

set -eu

configure_gemini() {
  mkdir -p "$HOME/.gemini"

  cat > "$HOME/.gemini/settings.json" <<EOF
{
  "security": {
    "auth": {
      "selectedType": "oauth-personal"
    }
  }
}
EOF
  chmod 600 "$HOME/.gemini/settings.json"

  if [ -f /etc/gemini-credentials/oauth_creds.json ]; then
    cp /etc/gemini-credentials/oauth_creds.json "$HOME/.gemini/oauth_creds.json"
    chmod 600 "$HOME/.gemini/oauth_creds.json"
  elif [ -f "$HOME/.gemini/oauth_creds.json" ]; then
    echo "oauth_creds.json already exists (possibly mounted). Skipping generation."
  else
    cat > "$HOME/.gemini/oauth_creds.json" <<EOF
{
  "access_token": "managed-by-tank-operator",
  "refresh_token": "managed-by-tank-operator",
  "scope": "https://www.googleapis.com/auth/cloud-platform openid https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/userinfo.email",
  "token_type": "Bearer",
  "id_token": "eyJhbGciOiJSUzI1NiIsImtpZCI6IjA2YzdjNDc2NzliODA4ZmNlZGY3MzkxZDdiMWUzNjU3YmNhMzBkYmIiLCJ0eXAiOiJKV1QifQ.eyJpc3MiOiJodHRwczovL2FjY291bnRzLmdvb2dsZS5jb20iLCJhenAiOiI2ODEyNTU4MDkzOTUtb284ZnQyb3ByZHJucDllM2FxZjZhdjNobWRpYjEzNWouYXBwcy5nb29nbGV1c2VyY29udGVudC5jb20iLCJhdWQiOiI2ODEyNTU4MDkzOTUtb284ZnQyb3ByZHJucDllM2FxZjZhdjNobWRpYjEzNWouYXBwcy5nb29nbGV1c2VyY29udGVudC5jb20iLCJzdWIiOiIxMTM0ODIwNTYxMTIzMTA2Mzc5NjIiLCJlbWFpbCI6ImZ1bGxuZWxzb25ncmlwQGdtYWlsLmNvbSIsImVtYWlsX3ZlcmlmaWVkIjp0cnVlLCJpYXQiOjE3ODAyMTEwMzQsImV4cCI6OTk5OTk5OTk5OX0.dummy-signature",
  "expiry_date": 9999999999000
}
EOF
    chmod 600 "$HOME/.gemini/oauth_creds.json"
  fi
}

# Git identity for any commits the agent makes from /workspace.
git config --global user.name "tank-operator-gemini[bot]"
git config --global user.email "tank-operator-gemini@romaine.life"

configure_gemini

# Materialize Tank-provided policy docs into /workspace.
if [ -f /opt/tank/session-config/install-tank-docs.sh ]; then
  sh /opt/tank/session-config/install-tank-docs.sh || true
fi

# Optional: install tank-flavored skills.
if [ -f /opt/tank/session-config/install-tank-skills.sh ]; then
  sh /opt/tank/session-config/install-tank-skills.sh || true
fi

# Hand off to the runner. In test-slot mode (orchestrator sets
# GLIMMUNG_SUPERVISOR_CHILD on the container), exec tank-supervisor as
# PID 1 so the gemini-runner code can be hot-swapped via SIGHUP re-exec.
# In production, GLIMMUNG_SUPERVISOR_CHILD is unset and node runs as PID 1.
if [ -n "${GLIMMUNG_SUPERVISOR_CHILD:-}" ] && [ -x /app/tank-supervisor ]; then
  exec /app/tank-supervisor
fi
exec node /opt/gemini-runner/dist/index.js
