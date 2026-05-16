#!/bin/sh
# Pod-side launch shim for the agent-runner Node service.
# Performs credential setup once per pod lifetime, then execs the
# long-lived runner that drives the SDK.
#
# Why bash + exec node, not a Node entrypoint that does cred setup
# itself: the credential blob shape and configure_claude logic are
# small but very platform-specific (file modes, claude-code's
# expected JSON shape), and keeping them in shell matches the
# existing shell setup style used by the session image.

set -eu

configure_claude() {
  mkdir -p "$HOME/.claude"
  cat > "$HOME/.claude/settings.json" <<'EOF'
{"theme":"dark","permissions":{"defaultMode":"bypassPermissions"},"skipDangerousModePermissionPrompt":true}
EOF

  mcp_enabled='[]'
  if [ -f /workspace/.mcp.json ]; then
    mcp_enabled="$(jq -c '.mcpServers | keys' /workspace/.mcp.json)"
  fi

  # Placeholder OAuth bearer. The in-cluster api-proxy hostAlias-rewrites
  # api.anthropic.com to the proxy's ClusterIP, sees this placeholder, and
  # swaps in the real OAuth token from KV. The agent-runner / SDK never
  # holds the real token directly. See api-proxy/src/tank_api_proxy/server.py.
  cat > "$HOME/.claude/.credentials.json" <<'EOF'
{
  "claudeAiOauth": {
    "accessToken": "managed-by-tank-operator",
    "refreshToken": "managed-by-tank-operator",
    "expiresAt": 9999999999000,
    "scopes": ["user:inference", "user:profile"],
    "subscriptionType": "max",
    "rateLimitTier": "max"
  }
}
EOF
  chmod 600 "$HOME/.claude/.credentials.json"
  unset ANTHROPIC_API_KEY

  cat > "$HOME/.claude.json" <<EOF
{
  "hasCompletedOnboarding": true,
  "remoteDialogSeen": true,
  "officialMarketplaceAutoInstallAttempted": true,
  "officialMarketplaceAutoInstalled": true,
  "projects": {
    "/workspace": {
      "allowedTools": [],
      "mcpContextUris": [],
      "mcpServers": {},
      "enabledMcpjsonServers": ${mcp_enabled},
      "disabledMcpjsonServers": [],
      "hasTrustDialogAccepted": true,
      "projectOnboardingSeenCount": 1,
      "hasClaudeMdExternalIncludesApproved": false,
      "hasClaudeMdExternalIncludesWarningShown": false,
      "lastGracefulShutdown": false
    }
  }
}
EOF
}

# Git identity for any commits the agent makes from /workspace.
git config --global user.name "tank-operator-claude[bot]"
git config --global user.email "tank-operator-claude@romaine.life"

configure_claude

# Optional: install tank-flavored skills into the agent's home dir.
# Same script the per-turn path uses; idempotent.
if [ -f /opt/tank/session-config/install-tank-skills.sh ]; then
  sh /opt/tank/session-config/install-tank-skills.sh || true
fi

# Hand off to the runner. In test-slot mode (orchestrator sets
# GLIMMUNG_SUPERVISOR_CHILD on the container), exec tank-supervisor as
# PID 1 so the agent-runner code can be hot-swapped via SIGHUP re-exec.
# In production, GLIMMUNG_SUPERVISOR_CHILD is unset — the supervisor
# binary is dormant and node runs as PID 1 directly, exactly as before.
# See scripts/check-session-pod-hot-swap-migration.mjs for the
# completion contract; checkbox 2 of that manifest pins this fallback
# behavior. SIGTERM goes straight to whichever process is PID 1, which
# handles graceful shutdown of the SDK subprocess.
if [ -n "${GLIMMUNG_SUPERVISOR_CHILD:-}" ] && [ -x /app/tank-supervisor ]; then
  exec /app/tank-supervisor
fi
exec node /opt/agent-runner/dist/index.js
