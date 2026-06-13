#!/bin/sh

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
mcp_url="${TANK_MCP_LOCAL_URL:-http://127.0.0.1:9996/}"

cat <<'EOF'

[tank-agent] Local commit created. Tank is publishing this commit to the
session PR and starting CI/mergeability watching. Direct git push is disabled
in normal mode; use the Tank MCP publish_current_head tool to retry manually.

EOF

if ! command -v curl >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
  echo "[tank-agent] warning: curl+jq are required to auto-publish commits" >&2
  exit 0
fi

payload="$(
  jq -nc \
    --arg repo_path "$repo_root" \
    '{
      jsonrpc: "2.0",
      id: "tank-post-commit-publish",
      method: "tools/call",
      params: {
        name: "publish_current_head",
        arguments: {repo_path: $repo_path, source: "post-commit"}
      }
    }'
)"
response="$(curl -fsS -X POST "$mcp_url" -H "Content-Type: application/json" -d "$payload" 2>/tmp/tank-post-commit-publish.err || true)"
if [ -z "$response" ]; then
  echo "[tank-agent] warning: Tank auto-publish failed: $(cat /tmp/tank-post-commit-publish.err 2>/dev/null || true)" >&2
  exit 0
fi
if [ "$(printf '%s' "$response" | jq -r 'has("error")' 2>/dev/null || printf false)" = "true" ]; then
  echo "[tank-agent] warning: Tank auto-publish rejected this commit:" >&2
  printf '%s' "$response" | jq -r '.error.message // "unknown error"' >&2
  exit 0
fi
printf '%s' "$response" | jq -r '.result.content[]? | select(.type == "text") | .text' 2>/dev/null || true
