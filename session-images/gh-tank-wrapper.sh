#!/bin/sh
# Durable `gh` for non-restricted Tank sessions.
#
# Installed at /usr/local/bin/gh — ahead of the real /usr/bin/gh on PATH — so it
# shadows gh. On each invocation it mints a fresh GitHub App token (scoped to the
# session's /workspace repos, plus any --repo/-R on the command line) via the
# in-pod mcp-github MCP and runs the real gh with it, so the agent never has to
# re-auth. Restricted sessions pass straight through: the governed git flow owns
# credentials there, so gh stays unauthenticated.
set -u
REAL_GH="${TANK_REAL_GH:-/usr/bin/gh}"

# Restricted sessions: do not auto-mint.
case "$(printf '%s' "${TANK_RESTRICTED_GIT:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on) exec "$REAL_GH" "$@" ;;
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
for g in /workspace/*/.git; do
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

req="$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mint_clone_token","arguments":{"repos":%s,"full":true,"write":true,"workflows":true}}}' "$repos")"
resp="$(curl -sS -m 25 \
  -H "Authorization: Bearer ${auth_tok}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -X POST "$mcp_url" -d "$req" 2>/dev/null || true)"
token="$(printf '%s\n' "$resp" | sed -n 's/^data: //p' | jq -r '.result.structuredContent.token // empty' 2>/dev/null | head -n1)"
[ -n "$token" ] || token="$(printf '%s' "$resp" | jq -r '.result.structuredContent.token // empty' 2>/dev/null || true)"
[ -n "$token" ] && export GH_TOKEN="$token"

exec "$REAL_GH" "$@"
