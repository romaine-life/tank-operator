#!/usr/bin/env bash

# tank-api.sh — thin curl wrapper that signs requests with a tank
# session JWT minted from the local `az` session.
#
# Usage:
#   scripts/tank-api.sh GET /api/sessions/8/events
#   scripts/tank-api.sh GET '/api/sessions?owner=other@example.com'
#   scripts/tank-api.sh POST /api/sessions -d '{"mode":"claude-gui"}'
#
# Extra args after the path are passed verbatim to curl. The path is
# joined to $TANK_BASE_URL (default https://tank.romaine.life) and the
# Authorization header is set automatically. Other curl options like
# -H, --data, --data-binary, etc. work as expected.
#
# Token cache + mint chain lives in tank-jwt-from-az.sh — see that
# script for one-time setup (Entra app, helm values, interactive
# sign-in to seed the Better Auth user row).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TANK_BASE_URL="${TANK_BASE_URL:-https://tank.romaine.life}"

if [[ $# -lt 2 ]]; then
  cat >&2 <<'EOF'
usage: tank-api.sh METHOD PATH [curl-args...]
example: tank-api.sh GET /api/sessions/8/events
EOF
  exit 64
fi

method="$1"
path="$2"
shift 2

jwt="$("$HERE/tank-jwt-from-az.sh")"

curl --silent --show-error \
  -X "$method" \
  -H "Authorization: Bearer $jwt" \
  "$@" \
  "${TANK_BASE_URL}${path}"
