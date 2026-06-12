#!/bin/sh
# Materialize Claude user settings for Tank session pods.
#
# Tank sessions are the trust boundary: a Claude parent agent and Claude
# subagents running inside the same pod get the same local-tool authority and
# the same configured MCP server authority. Claude Code's subagents do not
# inherit the parent's bypass permission mode, so the explicit allow surface
# below is generated from the pod's mounted MCP config instead of hand-maintained
# per-tool entries.

set -eu

settings_path="${1:-$HOME/.claude/settings.json}"
mcp_config="${MCP_CONFIG:-/workspace/.mcp.json}"

mkdir -p "$(dirname "$settings_path")"

if [ -f "$mcp_config" ]; then
  mcp_allow="$(jq -c '[.mcpServers // {} | keys[] | "mcp__" + .]' "$mcp_config")"
else
  mcp_allow='[]'
fi

jq -n --argjson mcpAllow "$mcp_allow" '{
  theme: "dark",
  permissions: {
    defaultMode: "bypassPermissions",
    allow: ([
      "Read",
      "LS",
      "Grep",
      "Glob",
      "Edit",
      "Write",
      "MultiEdit",
      "NotebookEdit",
      "Bash",
      "WebFetch",
      "WebSearch",
      "TodoWrite"
    ] + $mcpAllow | unique)
  },
  skipDangerousModePermissionPrompt: true
}' > "$settings_path"
