#!/bin/sh
# Pod-side launch shim for the codex-runner Node service. Sibling of
# claude-runner-launch.sh: same shape, codex-specific credential setup
# instead of claude-specific.
#
# Codex auth path: write a synthetic chatgptAuthTokens auth.json whose
# access token is the managed-by-tank-operator placeholder. The in-cluster
# codex-api-proxy hostAlias for chatgpt.com swaps that placeholder for the
# current real ChatGPT access token and centrally owns refresh/write-back.

set -eu

CODEX_PLACEHOLDER_ID_TOKEN="eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJlbWFpbCI6InRhbmstb3BlcmF0b3JAbG9jYWwiLCJleHAiOjQxMDI0NDQ4MDAsImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6eyJjaGF0Z3B0X3BsYW5fdHlwZSI6InBybyIsImNoYXRncHRfdXNlcl9pZCI6Im1hbmFnZWQtYnktdGFuay1vcGVyYXRvciIsImNoYXRncHRfYWNjb3VudF9pZCI6Im1hbmFnZWQtYnktdGFuay1vcGVyYXRvciJ9fQ.signature"

configure_codex() {
  mkdir -p "$HOME/.codex"

  cat > "$HOME/.codex/auth.json" <<EOF
{
  "auth_mode": "chatgptAuthTokens",
  "tokens": {
    "id_token": "${CODEX_PLACEHOLDER_ID_TOKEN}",
    "access_token": "managed-by-tank-operator",
    "refresh_token": "",
    "account_id": "managed-by-tank-operator"
  },
  "last_refresh": "2099-01-01T00:00:00Z"
}
EOF
  chmod 600 "$HOME/.codex/auth.json"

  # config.toml: codex CLI reads this for non-credential settings
  # (model, sandbox mode, mcp servers). Keep it minimal; the SPA's
  # per-turn TurnOptions can override most things via SDK turn config.
  mcp_block=""
  if [ -f /workspace/.mcp.json ]; then
    # codex's mcp config lives in config.toml under [mcp_servers.<name>]
    # blocks. Generate them from the same .mcp.json the claude path uses
    # so both runtimes see the same MCP surface.
    mcp_block="$(jq -r '
      .mcpServers | to_entries[] |
      "[mcp_servers." + .key + "]\nurl = \"" + (.value.url // "") + "\""
    ' /workspace/.mcp.json 2>/dev/null || true)"
  fi

  cat > "$HOME/.codex/config.toml" <<EOF
# Generated at pod start by codex-runner-launch.sh. Do not hand-edit;
# changes will be overwritten on the next pod boot.
sandbox_mode = "danger-full-access"
approval_policy = "never"
cli_auth_credentials_store = "file"

# Tank GUI sessions run Codex through codex exec, which uses Codex's
# default mode. Keep the ask-user tool available there so Codex GUI can
# surface request_user_input instead of falling back to a text-only prompt.
[features]
default_mode_request_user_input = true

${mcp_block}
EOF

  unset OPENAI_API_KEY
  unset CODEX_API_KEY
}

# Git identity for any commits the agent makes from /workspace.
git config --global user.name "tank-operator-codex[bot]"
git config --global user.email "tank-operator-codex@romaine.life"
if [ -f /opt/tank/session-config/install-agent-git-template.sh ]; then
  sh /opt/tank/session-config/install-agent-git-template.sh || true
fi

configure_codex

# Materialize Tank-provided policy docs into /workspace so first-turn
# directives can reference stable paths independent of cloned repos.
if [ -f /opt/tank/session-config/install-tank-docs.sh ]; then
  sh /opt/tank/session-config/install-tank-docs.sh
fi

# Optional: install tank-flavored skills into the agent's home dir.
# Same script the per-turn path uses; idempotent.
if [ -f /opt/tank/session-config/install-tank-skills.sh ]; then
  sh /opt/tank/session-config/install-tank-skills.sh || true
fi

# Hand off to the runner. In test-slot mode (orchestrator sets
# GLIMMUNG_SUPERVISOR_CHILD on the container), exec tank-supervisor as
# PID 1 so the codex-runner code can be hot-swapped via SIGHUP re-exec.
# In production, GLIMMUNG_SUPERVISOR_CHILD is unset and node runs as PID 1.
if [ -n "${GLIMMUNG_SUPERVISOR_CHILD:-}" ] && [ -x /app/tank-supervisor ]; then
  exec /app/tank-supervisor
fi
exec node /opt/codex-runner/dist/index.js
