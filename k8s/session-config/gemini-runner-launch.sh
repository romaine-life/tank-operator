#!/bin/sh
# Pod-side launch shim for the gemini-runner Node service. Sibling of
# agent-runner-launch.sh and codex-runner-launch.sh.
#
# Gemini auth path: write a synthetic settings.json whose access and refresh
# tokens are the managed-by-tank-operator placeholders. The in-cluster
# gemini-api-proxy hostAlias swaps that placeholder for the current real
# Google OAuth token and centrally owns refresh/write-back.

set -eu

configure_gemini() {
  mkdir -p "$HOME/.gemini"

  cat > "$HOME/.gemini/settings.json" <<EOF
{
  "access_token": "managed-by-tank-operator",
  "refresh_token": "managed-by-tank-operator",
  "expiry_date": 9999999999000
}
EOF
  chmod 600 "$HOME/.gemini/settings.json"
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

# Hand off to the runner.
exec node /opt/gemini-runner/dist/index.js
