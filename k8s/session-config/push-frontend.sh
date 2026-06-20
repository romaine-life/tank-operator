#!/usr/bin/env bash
# push-frontend.sh — stream a built frontend dist/ to the session's test slot.
#
# This is the sender half of the live frontend preview feature. It mints a
# role=service JWT from the pod's projected auth.romaine.life SA token (the same
# exchange repo-cloner.sh uses), tars the built frontend dist/, and PUTs it to
# the slot's static-override receiver. The next request to the slot serves the
# streamed bundle — no kubectl, no pods/exec, no new k8s RBAC.
#
# It is a dev-only "for seeing" lane: what it pushes is un-gated scratch on top
# of the slot's image-baked baseline, never merge evidence. Promotion stays on
# the durable image-deploy gate.
#
# Usage:
#   push-frontend.sh [--build] [--minify] [--revert] [--slot-url URL]
#
#   --build        run `vite build` first (npm ci if needed); default assumes
#                  dist/ is already fresh (e.g. a `vite build --watch` is running)
#   --minify       keep production minify on the build (default: off, for speed)
#   --revert       DELETE the override (revert the slot to its image-baked assets)
#   --slot-url URL slot base URL; default: discover from this session's test_state
#
# Env overrides: FRONTEND_DIR, SESSION_ID, AUTH_ROMAINE_TOKEN_PATH,
#   AUTH_ROMAINE_EXCHANGE_URL, TANK_OPERATOR_INTERNAL_URL, TANK_SLOT_BASE_URL.
set -euo pipefail

FRONTEND_DIR="${FRONTEND_DIR:-/workspace/tank-operator/frontend}"
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
AUTH_EXCHANGE_URL="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
TANK_OPERATOR_INTERNAL_URL="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
SLOT_URL="${TANK_SLOT_BASE_URL:-}"

DO_BUILD=false
DO_REVERT=false
MINIFY=false
while [ $# -gt 0 ]; do
  case "$1" in
    --build) DO_BUILD=true ;;
    --minify) MINIFY=true ;;
    --revert) DO_REVERT=true ;;
    --slot-url) shift; SLOT_URL="${1:-}" ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "push-frontend: unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

log() { printf 'push-frontend: %s\n' "$*" >&2; }

exchange_auth_token() {
  local attempt token
  for attempt in 1 2 3 4 5; do
    token="$(
      curl -fsS -X POST "$AUTH_EXCHANGE_URL" \
        -H "Authorization: Bearer $(cat "$AUTH_TOKEN_PATH")" \
        -H "Content-Type: application/json" \
        -d '{}' | jq -r '.token // empty'
    )" && {
      if [ -n "$token" ]; then printf '%s\n' "$token"; return 0; fi
    }
    log "auth exchange attempt ${attempt}/5 failed"
    sleep "$attempt"
  done
  return 1
}

# discover_slot_url reads this session's test_state.url from the orchestrator's
# internal sessions list (scoped to the caller's actor_email). The slot must
# already be provisioned (the test-slot page Create flow) — this lane streams on
# top of that baseline, it does not stand a slot up.
discover_slot_url() {
  local token="$1"
  if [ -z "${SESSION_ID:-}" ]; then
    log "SESSION_ID unset; pass --slot-url or set TANK_SLOT_BASE_URL" >&2
    return 1
  fi
  curl -fsS -H "Authorization: Bearer $token" \
    "$TANK_OPERATOR_INTERNAL_URL/api/internal/sessions" \
    | jq -r --arg id "$SESSION_ID" '.[] | select(.id == $id) | .test_state.url // empty'
}

log "exchanging pod identity"
TOKEN="$(exchange_auth_token || true)"
if [ -z "$TOKEN" ]; then
  log "auth.romaine.life exchange returned no token"; exit 1
fi

if [ -z "$SLOT_URL" ]; then
  SLOT_URL="$(discover_slot_url "$TOKEN" || true)"
fi
if [ -z "$SLOT_URL" ]; then
  log "no slot URL — is a test slot provisioned for this session?"; exit 1
fi
SLOT_URL="${SLOT_URL%/}"

if [ "$DO_REVERT" = true ]; then
  log "reverting slot override -> image-baked assets ($SLOT_URL)"
  curl --fail -sS -X DELETE -H "Authorization: Bearer $TOKEN" \
    "$SLOT_URL/api/internal/static-override"
  echo
  log "reverted"
  exit 0
fi

if [ "$DO_BUILD" = true ]; then
  log "building frontend in $FRONTEND_DIR"
  if [ ! -d "$FRONTEND_DIR/node_modules" ]; then
    ( cd "$FRONTEND_DIR" && npm ci )
  fi
  if [ "$MINIFY" = true ]; then
    ( cd "$FRONTEND_DIR" && npx vite build )
  else
    ( cd "$FRONTEND_DIR" && npx vite build --minify false )
  fi
fi

DIST_DIR="$FRONTEND_DIR/dist"
if [ ! -f "$DIST_DIR/index.html" ]; then
  log "no built bundle at $DIST_DIR (index.html missing) — run with --build first"; exit 1
fi

log "streaming $DIST_DIR -> $SLOT_URL/api/internal/static-override"
tar czf - -C "$DIST_DIR" . \
  | curl --fail -sS -X PUT \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/gzip" \
      --data-binary @- \
      "$SLOT_URL/api/internal/static-override"
echo
log "pushed"
