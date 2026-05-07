#!/bin/bash
set -euo pipefail

provider="${1:-}"
prompt_file="${2:-}"

if [ -z "$provider" ] || [ -z "$prompt_file" ] || [ ! -f "$prompt_file" ]; then
  echo "usage: headless-run.sh <claude|codex> <prompt-file>" >&2
  exit 64
fi

configure_git_identity() {
  case "$provider" in
    codex)
      git config --global user.name "tank-operator-codex[bot]"
      git config --global user.email "tank-operator-codex@romaine.life"
      ;;
    *)
      git config --global user.name "tank-operator-claude[bot]"
      git config --global user.email "tank-operator-claude@romaine.life"
      ;;
  esac
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

configure_codex() {
  mkdir -p "$HOME/.codex"
  local mcp_blocks=""
  if [ -f /workspace/.mcp.json ]; then
    mcp_blocks=$(jq -r '.mcpServers | to_entries[] |
      "\n[mcp_servers.\(.key)]" +
      (if .value.type == "http" then
         "\nurl = \"\(.value.url)\""
       elif .value.command then
         "\ncommand = \"\(.value.command)\"" +
         (if .value.args then "\nargs = " + (.value.args | tojson) else "" end)
       else "" end) +
      (if .value.env then
         "\n\n[mcp_servers.\(.key).env]" +
         (.value.env | to_entries | map("\n\(.key) = " + (.value | tojson)) | join(""))
       else "" end)
    ' /workspace/.mcp.json)
  fi

  cat > "$HOME/.codex/config.toml" <<EOF
cli_auth_credentials_store = "file"
approval_policy = "never"
sandbox_mode = "danger-full-access"

[projects."/workspace"]
trust_level = "trusted"

[tui]
notifications = true
notification_condition = "always"
notification_method = "bel"
${mcp_blocks}
EOF

  if [ ! -f /etc/codex-creds/auth.json ]; then
    echo "no codex credentials found in /etc/codex-creds/auth.json" >&2
    echo "spawn a 'Codex config' session and save credentials first." >&2
    exit 78
  fi
  cp /etc/codex-creds/auth.json "$HOME/.codex/auth.json"
  chmod 600 "$HOME/.codex/auth.json"
}

bash /opt/tank/write-glimmung-context.sh
configure_git_identity

case "$provider" in
  claude)
    configure_claude
    exec claude -p --output-format stream-json "$(cat "$prompt_file")"
    ;;
  codex)
    configure_codex
    exec codex exec --json "$(cat "$prompt_file")"
    ;;
  *)
    echo "unknown provider: $provider" >&2
    exit 64
    ;;
esac
