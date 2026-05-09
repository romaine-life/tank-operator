#!/bin/bash
set -euo pipefail

provider="${1:-}"
prompt_file="${2:-}"
follow_up="${3:-false}"
# 4th + 5th positional args added when the frontend ships them. Both are
# pre-validated against [A-Za-z0-9._-]{1,64} in api.py before getting here.
model="${4:-}"
permission_mode="${5:-}"

if [ -z "$provider" ] || [ -z "$prompt_file" ] || [ ! -f "$prompt_file" ]; then
  echo "usage: headless-run.sh <claude|codex> <prompt-file> [follow_up] [model] [permission_mode]" >&2
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
    claude_args=(-p --verbose --output-format stream-json)
    if [ "$follow_up" = "true" ]; then
      claude_args=(--continue "${claude_args[@]}")
    fi
    if [ -n "$model" ]; then
      claude_args+=(--model "$model")
    fi
    # acceptEdits / auto / bypassPermissions all map to claude's
    # --dangerously-skip-permissions in headless mode (the CLI doesn't
    # have finer-grained per-mode flags). plan mode prefixes the prompt
    # with a planning instruction since claude -p is non-interactive.
    prompt_text="$(cat "$prompt_file")"
    case "$permission_mode" in
      acceptEdits|auto|bypassPermissions)
        claude_args+=(--dangerously-skip-permissions)
        ;;
      plan)
        prompt_text="[Plan mode: produce a step-by-step plan first; do not execute tool calls until the plan is confirmed in a follow-up message.]\n\n${prompt_text}"
        ;;
    esac
    exec claude "${claude_args[@]}" "$prompt_text" < /dev/null
    ;;
  codex)
    configure_codex
    exec python3 - "$prompt_file" "$follow_up" "$model" <<'PY'
import os
import pty
import sys

prompt_path = sys.argv[1]
follow_up = sys.argv[2] == "true"
model = sys.argv[3]
with open(prompt_path, encoding="utf-8") as f:
    prompt = f.read()

os.chdir("/workspace")
args = ["codex", "exec"]
if follow_up:
    args.extend(["resume", "--last"])
args.extend(["--json", "--skip-git-repo-check"])
if model:
    args.extend(["--model", model])
args.append(prompt)
status = pty.spawn(args)
raise SystemExit(os.waitstatus_to_exitcode(status))
PY
    ;;
  *)
    echo "unknown provider: $provider" >&2
    exit 64
    ;;
esac
