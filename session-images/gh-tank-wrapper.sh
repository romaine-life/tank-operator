#!/bin/sh
# Durable `gh` for Tank sessions.
#
# Installed at /usr/local/bin/gh — ahead of the real /usr/bin/gh on PATH — so it
# shadows gh. On each invocation it mints a fresh GitHub App token (scoped to the
# session's /workspace repos, plus any --repo/-R on the command line) via the
# in-pod mcp-github MCP and runs the real gh with it, so the agent never has to
# re-auth. Non-restricted sessions mint a full read/write token. Restricted
# sessions mint a READ-ONLY token (contents:read) so `gh` reads (pr view, run
# view, api, …) work without handing the shell a write credential — writes still
# go through the governed Tank MCP path and fail loudly via the real gh's 403.
set -u
REAL_GH="${TANK_REAL_GH:-/usr/bin/gh}"

# Restricted sessions mint read-only; non-restricted mint full read/write.
restricted=false
case "$(printf '%s' "${TANK_RESTRICTED_GIT:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on) restricted=true ;;
esac

# Honor an explicitly-provided token.
if [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
  exec "$REAL_GH" "$@"
fi

mcp_url="${TANK_GIT_CRED_MCP_URL:-http://127.0.0.1:9992/}"
auth_tok="$(cat "${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}" 2>/dev/null || true)"
[ -n "$auth_tok" ] || exec "$REAL_GH" "$@"

# Token repo scope: the session's cloned repos under /workspace, plus an explicit
# `--repo owner/name` / `-R owner/name` if present on the command line.
repos=""
seen=" "
add_repo() {
  case "$1" in */*) : ;; *) return ;; esac
  case "$seen" in *" $1 "*) return ;; esac
  seen="$seen$1 "
  repos="$repos\"$1\","
}
for g in "${TANK_WORKSPACE_DIR:-/workspace}"/*/.git; do
  [ -e "$g" ] || continue
  d=${g%/.git}
  url="$(git -C "$d" remote get-url origin 2>/dev/null || true)"
  slug="$(printf '%s' "$url" | sed -E 's#^https://github\.com/##; s#^git@github\.com:##; s#\.git$##')"
  add_repo "$slug"
done
prev=""
for a in "$@"; do
  case "$prev" in --repo|-R) add_repo "$a" ;; esac
  prev="$a"
done
[ -n "$repos" ] || exec "$REAL_GH" "$@"
repos="[${repos%,}]"

# Break-glass elevation (restricted only): before the read-only mint, ask the
# in-pod break-glass server (:9999, the grant source of truth) whether an
# active, repo-covering, UNLIMITED-branch grant exists. If so it mints the App's
# FULL permission set and audits the use, so `gh pr edit`/`ready`/merge, issues,
# etc. work automatically while the grant is live. No qualifying grant ->
# {"active":false} and we keep the read-only default.
#
# FAIL LOUD, never silent: the mint endpoint answers `{"active":true,"token":…}`
# (elevate) or `{"active":false}` (genuine no-grant, stay read-only). Anything
# else — a JSON-RPC error like `{"error":{"code":-32600,...}}` (the symptom when
# the mcp-auth-proxy sidecar predates the /mint-git-token route and the POST
# falls through to the MCP catch-all), an HTTP error, a timeout, or any
# unrecognized shape — is reported to stderr instead of being silently
# collapsed to read-only. A silent downgrade here is exactly what made the
# :9999 sidecar-skew regression nearly impossible to diagnose, so it is part of
# the bug, not an acceptable fallback.
if [ "$restricted" = "true" ]; then
  bg_url="${TANK_BREAK_GLASS_MINT_URL:-http://127.0.0.1:9999/mint-git-token}"
  # Capture body and HTTP status together; the cold full-mint path does a Tank
  # grant lookup + GitHub App mint, so allow headroom rather than a tight
  # timeout that would masquerade as "no grant".
  bg_raw="$(curl -sS -m 8 -w 'HTTPSTATUS:%{http_code}' \
    -H "Authorization: Bearer ${auth_tok}" \
    -H "Content-Type: application/json" \
    -X POST "$bg_url" \
    -d "$(printf '{"repos":%s}' "$repos")" 2>/dev/null || printf 'HTTPSTATUS:000')"
  bg_code="${bg_raw##*HTTPSTATUS:}"
  bg_body="${bg_raw%HTTPSTATUS:*}"
  bg_token="$(printf '%s' "$bg_body" | jq -r 'select(.active==true) | .token // empty' 2>/dev/null | head -n1 || true)"
  if [ -n "$bg_token" ]; then
    export GH_TOKEN="$bg_token"
    exec "$REAL_GH" "$@"
  fi
  # No elevation token. Only a clean `{"active":false}` over HTTP 200 is the
  # expected, quiet no-grant case; everything else is surfaced loudly.
  if [ "$bg_code" != "200" ] || ! printf '%s' "$bg_body" | jq -e '.active == false' >/dev/null 2>&1; then
    printf 'tank(gh): break-glass elevation FAILED — POST %s returned an unexpected response (HTTP %s); falling back to a READ-ONLY token. If an active break-glass grant exists, gh/git writes WILL fail. Most likely the in-pod mcp-auth-proxy sidecar predates the /mint-git-token route (image/version skew) or the break-glass server errored. Response: %.300s\n' \
      "$bg_url" "$bg_code" "$bg_body" >&2
  fi
  mint_args="$(printf '{"repos":%s,"write":false,"workflows":false,"full":false}' "$repos")"
else
  mint_args="$(printf '{"repos":%s,"full":true,"write":true,"workflows":true}' "$repos")"
fi
req="$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mint_clone_token","arguments":%s}}' "$mint_args")"
resp="$(curl -sS -m 25 \
  -H "Authorization: Bearer ${auth_tok}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -X POST "$mcp_url" -d "$req" 2>/dev/null || true)"
token="$(printf '%s\n' "$resp" | sed -n 's/^data: //p' | jq -r '.result.structuredContent.token // empty' 2>/dev/null | head -n1)"
[ -n "$token" ] || token="$(printf '%s' "$resp" | jq -r '.result.structuredContent.token // empty' 2>/dev/null || true)"
[ -n "$token" ] && export GH_TOKEN="$token"

exec "$REAL_GH" "$@"
