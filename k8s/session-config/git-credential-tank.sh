#!/bin/sh
# Tank git credential helper (mode-aware).
#
# On every git network operation, mints a short-lived (~1h) GitHub App
# installation token via the in-pod mcp-github MCP server, scoped to exactly
# the repo being accessed, and hands it to git. Authentication is the pod's
# projected auth.romaine.life service-account token — the same identity the
# agent already uses to mint tokens through the MCP tool surface, so this
# helper grants no capability the session does not already have.
#
# Installed in BOTH modes by install-agent-git-template.sh; the scope is
# mode-aware:
#   - non-restricted (TANK_RESTRICTED_GIT false/unset): mints the App's full
#     permission set so clone/fetch/pull/push all work with no manual token.
#   - restricted (TANK_RESTRICTED_GIT truthy): mints a READ-ONLY token
#     (contents:read) so clone/fetch/pull work for reads while writes stay on
#     the governed publish_current_head / break-glass path. The pre-push hook
#     still blocks pushes and a read-only token cannot push anyway, so this
#     grants nothing the session can't already do via the GitHub read MCP tools.
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

# Scope is mode-aware. Restricted sessions mint a read-only token so reads work
# without a push-capable credential in the shell; non-restricted sessions mint
# the App's full permission set (write+workflows belt-and-suspenders) so
# `git push` and workflow-file pushes always work regardless of how the server
# composes the flags.
restricted=false
case "$(printf '%s' "${TANK_RESTRICTED_GIT:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on) restricted=true ;;
esac
if [ "$restricted" = "true" ]; then
  # Break-glass elevation. Before falling back to the read-only mint, ask the
  # in-pod break-glass server (:9999) — the single source of truth for grants —
  # whether an active, repo-covering, UNLIMITED-branch grant exists. If so it
  # mints the App's FULL permission set (full=true) and audits the use, so
  # `git push`/PR writes work automatically while the grant is live. No
  # qualifying grant -> {"active":false} and we keep the read-only default.
  #
  # FAIL LOUD, never silent: the mint endpoint answers `{"active":true,"token":…}`
  # (elevate) or `{"active":false}` (genuine no-grant). Anything else — a JSON-RPC
  # error like `{"error":{"code":-32600,...}}` (the symptom when the mcp-auth-proxy
  # sidecar predates the /mint-git-token route and the POST falls through to the
  # MCP catch-all), an HTTP error, a timeout, or any unrecognized shape — is
  # reported to stderr rather than silently collapsed to read-only. The silent
  # downgrade is what made the :9999 sidecar-skew regression undiagnosable, so it
  # is part of the bug, not an acceptable fallback.
  bg_url="${TANK_BREAK_GLASS_MINT_URL:-http://127.0.0.1:9999/mint-git-token}"
  # Capture body and HTTP status together; the cold full-mint path does a Tank
  # grant lookup + GitHub App mint, so allow headroom rather than a tight timeout
  # that would masquerade as "no grant".
  bg_raw="$(curl -sS -m 8 -w 'HTTPSTATUS:%{http_code}' \
    -H "Authorization: Bearer ${auth_tok}" \
    -H "Content-Type: application/json" \
    -X POST "$bg_url" \
    -d "$(printf '{"repos":["%s"]}' "$repo")" 2>/dev/null || printf 'HTTPSTATUS:000')"
  bg_code="${bg_raw##*HTTPSTATUS:}"
  bg_body="${bg_raw%HTTPSTATUS:*}"
  bg_token="$(printf '%s' "$bg_body" | jq -r 'select(.active==true) | .token // empty' 2>/dev/null | head -n1 || true)"
  if [ -n "$bg_token" ]; then
    printf 'username=x-access-token\n'
    printf 'password=%s\n' "$bg_token"
    exit 0
  fi
  # No elevation token. Only a clean `{"active":false}` over HTTP 200 is the
  # expected, quiet no-grant case; everything else is surfaced loudly.
  if [ "$bg_code" != "200" ] || ! printf '%s' "$bg_body" | jq -e '.active == false' >/dev/null 2>&1; then
    printf 'tank(git-credential): break-glass elevation FAILED — POST %s returned an unexpected response (HTTP %s); falling back to a READ-ONLY token. If an active break-glass grant exists, git pushes/PR writes WILL fail. Most likely the in-pod mcp-auth-proxy sidecar predates the /mint-git-token route (image/version skew) or the break-glass server errored. Response: %.300s\n' \
      "$bg_url" "$bg_code" "$bg_body" >&2
  fi
  args="$(printf '{"repos":["%s"],"write":false,"workflows":false,"full":false}' "$repo")"
else
  args="$(printf '{"repos":["%s"],"full":true,"write":true,"workflows":true}' "$repo")"
fi
req="$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mint_clone_token","arguments":%s}}' "$args")"

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
