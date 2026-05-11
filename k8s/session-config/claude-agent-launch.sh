#!/bin/bash
# Launch the persistent Claude agent for a GUI session.
#
# Called once per session (not once per turn). Sets up Claude credentials
# the same way headless-run.sh does, then execs persistent-agent.py which
# owns the session for its lifetime.
#
# Args:
#   $1  session_id     — used to name the turns dir and agent pid file
#   $2  model          — passed through to ClaudeCodeOptions (may be empty)
#   $3  permission_mode — bypassPermissions|acceptEdits|auto|plan (may be empty)
set -euo pipefail

session_id="${1:-}"
model="${2:-}"
permission_mode="${3:-}"

if [ -z "$session_id" ]; then
  echo "usage: claude-agent-launch.sh <session_id> [model] [permission_mode]" >&2
  exit 64
fi

configure_git_identity() {
  git config --global user.name "tank-operator-claude[bot]"
  git config --global user.email "tank-operator-claude@romaine.life"
}

configure_claude() {
  mkdir -p "$HOME/.claude"
  cat > "$HOME/.claude/settings.json" <<'EOF'
{"theme":"dark","permissions":{"defaultMode":"bypassPermissions"},"skipDangerousModePermissionPrompt":true}
EOF

  local mcp_enabled='[]'
  if [ -f /workspace/.mcp.json ]; then
    mcp_enabled="$(jq -c '.mcpServers | keys' /workspace/.mcp.json)"
  fi

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

bash /opt/tank/write-glimmung-context.sh
source /opt/tank/session-config/install-tank-skills.sh
install_tank_skills
configure_git_identity
configure_claude

turns_dir="/tmp/tank-turns-${session_id}"
agent_pid="/tmp/tank-agent-${session_id}.pid"
mkdir -p "$turns_dir"

# Export model/permission_mode so persistent-agent.py can read them from env
# if it needs them for ClaudeCodeOptions construction.
export TANK_AGENT_MODEL="$model"
export TANK_AGENT_PERMISSION_MODE="$permission_mode"

exec python3 /opt/tank/session-config/persistent-agent.py \
    "$session_id" \
    "$turns_dir" \
    "$agent_pid"
