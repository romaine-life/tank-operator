#!/bin/sh
# Pod-side launch shim for the gemini-runner Node service. Sibling of
# agent-runner-launch.sh and codex-runner-launch.sh.
#
# Gemini auth is proxyless: the real Google OAuth (Code Assist) credential is
# mounted at /etc/gemini-credentials/oauth_creds.json. We copy it to
# ~/.gemini/oauth_creds.json and select oauth-personal in settings.json. The
# gemini CLI refreshes the token itself against Google — there is no in-cluster
# proxy. This is the "gemini test" shape that stayed healthy under load, now the
# only Gemini path.

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
    echo "oauth_creds.json already present; leaving as-is."
  else
    echo "WARNING: no Gemini OAuth credential mounted at /etc/gemini-credentials/oauth_creds.json; gemini turns will fail to authenticate." >&2
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
# GLIMMUNG_SUPERVISOR_CHILD on the container), exec tank-supervisor as PID 1 so
# the gemini-runner code can be hot-swapped via SIGHUP re-exec. In production,
# GLIMMUNG_SUPERVISOR_CHILD is unset and node runs as PID 1.
if [ -n "${GLIMMUNG_SUPERVISOR_CHILD:-}" ] && [ -x /app/tank-supervisor ]; then
  exec /app/tank-supervisor
fi
exec node /opt/gemini-runner/dist/index.js
