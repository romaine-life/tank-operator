#!/bin/sh
# Tank git credential helper for non-restricted sessions.
#
# On every git network operation, mints a short-lived (~1h) GitHub App
# installation token via the in-pod mcp-github MCP server, scoped to exactly
# the repo being accessed, and hands it to git. Authentication is the pod's
# projected auth.romaine.life service-account token — the same identity the
# agent already uses to mint tokens through the MCP tool surface, so this
# helper grants no capability the session does not already have.
#
# This is installed ONLY in non-restricted sessions (TANK_RESTRICTED_GIT
# false/unset) by install-agent-git-template.sh. Restricted sessions keep the
# governed publish path (publish_current_head / break-glass) and never get
# this helper.
#
# git invokes it as: git-credential-tank <get|store|erase>
# POSIX sh (no bashisms) so it runs under dash as well as bash.
set -eu

op="${1:-get}"
# Only `get` mints; nothing to persist or forget for store/erase.
[ "$op" = "get" ] || exit 0

# Endpoint + identity are overridable for tests; defaults match the session pod.
MCP_GITHUB_URL="${TANK_GIT_CRED_MCP_URL:-http://127.0.0.1:9992/}"
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"

# Read git's request: key=value lines terminated by a blank line.
host=""
path=""
while IFS='=' read -r key val; do
  [ -z "$key" ] && break
  case "$key" in
    host) host="$val" ;;
    path) path="$val" ;;
  esac
done

# Only handle github.com; let any other helper or prompt handle the rest.
[ "$host" = "github.com" ] || exit 0

# Derive owner/repo from the request path (needs credential.useHttpPath=true).
repo="${path#/}"
repo="${repo%.git}"
case "$repo" in
  */*) : ;;     # looks like owner/repo
  *) exit 0 ;;  # cannot scope a token without a repo -> bail quietly
esac

auth_tok="$(cat "$AUTH_TOKEN_PATH" 2>/dev/null || true)"
[ -n "$auth_tok" ] || exit 0

# Full-access token (the App's full permission set) for the trusted,
# non-restricted path; write+workflows are included belt-and-suspenders so
# `git push` and workflow-file pushes always work regardless of how the
# server composes the flags.
req="$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mint_clone_token","arguments":{"repos":["%s"],"full":true,"write":true,"workflows":true}}}' "$repo")"

resp="$(curl -sS -m 25 \
  -H "Authorization: Bearer ${auth_tok}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -X POST "$MCP_GITHUB_URL" \
  -d "$req" 2>/dev/null || true)"

# The MCP HTTP transport frames the reply as Server-Sent Events: one or more
# `data: {json}` lines. Pull the token out of the structured result.
token="$(printf '%s\n' "$resp" \
  | sed -n 's/^data: //p' \
  | jq -r '.result.structuredContent.token // empty' 2>/dev/null \
  | head -n1)"

# Fall back to a plain (non-SSE) JSON body if that is what we got.
if [ -z "$token" ]; then
  token="$(printf '%s' "$resp" | jq -r '.result.structuredContent.token // empty' 2>/dev/null || true)"
fi

[ -n "$token" ] || exit 0

printf 'username=x-access-token\n'
printf 'password=%s\n' "$token"
