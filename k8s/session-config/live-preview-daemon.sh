#!/usr/bin/env bash
# live-preview-daemon.sh — the in-pod daemon that takes the agent OUT of the
# loop for the live frontend preview feature.
#
# It runs as the `live-preview` sidecar in a session pod (see
# backend-go/internal/sessionmodel/sessionmodel.go). It HOLDS the projected
# auth.romaine.life service-account token so the agent never invokes the push.
# Its control input is the orchestrator's per-session SSE control channel:
#
#     GET /api/internal/sessions/$SESSION_ID/live-preview/stream
#
# which emits {enabled, slot_url, build_hint?} and is event-driven on the
# session row wake (no polling). The daemon converges on that state:
#
#   enabled=true  → npm ci (if needed), then `vite build --watch --minify false`
#                   in frontend/, and on each successful rebuild (debounced) push
#                   the built dist/ to the streamed slot_url by reusing
#                   push-frontend.sh (token exchange + tar + PUT to the slot's
#                   /api/internal/static-override), then POST a push receipt.
#   enabled=false → stop the watch and DELETE the slot override (revert the slot
#                   to its image-baked baseline) via push-frontend.sh --revert.
#
# Fail-safe by construction: a build is pushed ONLY after vite prints its
# success marker, so a broken build never streams a dead bundle; errors go to
# stderr/logs. No agent involvement, ever.
#
# Env (defaults match the sidecar wiring):
#   SESSION_ID                  this pod's session id (required)
#   TANK_OPERATOR_INTERNAL_URL  orchestrator internal base URL
#   AUTH_ROMAINE_TOKEN_PATH     projected SA token path
#   AUTH_ROMAINE_EXCHANGE_URL   auth.romaine.life k8s exchange URL
#   FRONTEND_DIR                frontend project dir (has package.json)
#   PUSH_FRONTEND_SCRIPT        path to the sibling push-frontend.sh
#   LIVE_PREVIEW_DEBOUNCE_SECONDS  settle window before a push (default 2)
#   LIVE_PREVIEW_RECONNECT_SECONDS backoff before re-opening the SSE (default 3)
set -uo pipefail

SESSION_ID="${SESSION_ID:-}"
TANK_OPERATOR_INTERNAL_URL="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
AUTH_EXCHANGE_URL="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
FRONTEND_DIR="${FRONTEND_DIR:-/workspace/tank-operator/frontend}"
PUSH_FRONTEND_SCRIPT="${PUSH_FRONTEND_SCRIPT:-/workspace/tank-operator/k8s/session-config/push-frontend.sh}"
DEBOUNCE_SECONDS="${LIVE_PREVIEW_DEBOUNCE_SECONDS:-2}"
RECONNECT_SECONDS="${LIVE_PREVIEW_RECONNECT_SECONDS:-3}"

log() { printf 'live-preview: %s\n' "$*" >&2; }

if [ -z "$SESSION_ID" ]; then
  log "SESSION_ID unset — cannot open control stream; idling"
  exec sleep infinity
fi

INTERNAL_URL="${TANK_OPERATOR_INTERNAL_URL%/}"
STREAM_URL="$INTERNAL_URL/api/internal/sessions/$SESSION_ID/live-preview/stream"
RECEIPT_URL="$INTERNAL_URL/api/internal/sessions/$SESSION_ID/live-preview/push"

# exchange_auth_token mints a role=service JWT from the pod's projected SA token.
# Same exchange push-frontend.sh / repo-cloner.sh use. Echoes the token on
# success; returns non-zero after retries so callers can degrade rather than die.
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

# current_build_id is a short, stable-ish identifier for the bundle just built,
# reported in the push receipt (surfaces back as build_hint). Prefer the repo
# HEAD short sha; fall back to a timestamp when git is unavailable.
current_build_id() {
  local sha
  sha="$(git -C "$(dirname "$FRONTEND_DIR")" rev-parse --short HEAD 2>/dev/null || true)"
  if [ -n "$sha" ]; then printf '%s\n' "$sha"; else date -u +%Y%m%dT%H%M%SZ; fi
}

# post_receipt records a successful push so the test-slot page can show
# "streaming · last pushed Ns ago". Never changes the owner toggle. Best-effort.
post_receipt() {
  local slot="$1" build token
  token="$(exchange_auth_token || true)"
  if [ -z "$token" ]; then
    log "receipt skipped: no token"
    return 0
  fi
  build="$(current_build_id)"
  if curl -fsS -X POST \
      -H "Authorization: Bearer $token" \
      -H "Content-Type: application/json" \
      -d "$(jq -nc --arg b "$build" '{build:$b}')" \
      "$RECEIPT_URL" >/dev/null; then
    log "receipt posted (build=$build, slot=$slot)"
  else
    log "receipt post failed (build=$build)"
  fi
}

# push_dist streams the current dist/ to the streamed slot_url by reusing
# push-frontend.sh (token exchange + tar + PUT). vite --watch keeps dist/ fresh,
# so this runs WITHOUT --build. A failed push is logged and does not post a
# receipt (so the page never shows a push that didn't land).
push_dist() {
  local slot="$1"
  # Debounce: let a burst of rapid rebuilds settle so we push once for the
  # latest state instead of once per intermediate write.
  sleep "$DEBOUNCE_SECONDS"
  log "pushing dist -> $slot"
  if bash "$PUSH_FRONTEND_SCRIPT" --slot-url "$slot"; then
    post_receipt "$slot"
  else
    log "push failed (build NOT streamed); leaving prior bundle live"
  fi
}

# revert_slot DELETEs the slot override so the slot serves its image-baked
# assets again. Best-effort; reused from push-frontend.sh.
revert_slot() {
  local slot="$1"
  log "reverting slot override -> image-baked ($slot)"
  bash "$PUSH_FRONTEND_SCRIPT" --revert --slot-url "$slot" || log "revert failed"
}

# build_and_push_loop runs vite build --watch and pushes on each successful
# rebuild. It is started under setsid (its own process group) so stop_builder
# can take down vite and this loop together. Exported so the setsid subshell
# sees it.
build_and_push_loop() {
  local slot="$1"
  cd "$FRONTEND_DIR" || { log "frontend dir $FRONTEND_DIR missing; cannot build"; exit 1; }
  if [ ! -d node_modules ]; then
    log "installing frontend deps (npm ci)"
    npm ci || log "npm ci failed (will let vite surface the error)"
  fi
  log "starting vite watch build (minify off) for slot $slot"
  # Stream vite output; push only after the success marker. On a build error
  # vite prints the error and emits NO success marker, so nothing is pushed.
  npx vite build --watch --minify false 2>&1 | while IFS= read -r line; do
    printf 'live-preview[vite]: %s\n' "$line" >&2
    case "$line" in
      *"built in "*|*"build completed"*)
        push_dist "$slot"
        ;;
    esac
  done
}
export -f build_and_push_loop push_dist post_receipt revert_slot exchange_auth_token current_build_id log
export SESSION_ID TANK_OPERATOR_INTERNAL_URL AUTH_TOKEN_PATH AUTH_EXCHANGE_URL
export FRONTEND_DIR PUSH_FRONTEND_SCRIPT DEBOUNCE_SECONDS RECEIPT_URL

BUILDER_PGID=""
BUILDER_SLOT=""

stop_builder() {
  if [ -n "$BUILDER_PGID" ]; then
    log "stopping vite watch (pgid $BUILDER_PGID)"
    kill -TERM "-$BUILDER_PGID" 2>/dev/null || true
    wait "$BUILDER_PGID" 2>/dev/null || true
    BUILDER_PGID=""
  fi
}

start_builder() {
  local slot="$1"
  setsid bash -c 'build_and_push_loop "$1"' _ "$slot" &
  BUILDER_PGID=$!
  BUILDER_SLOT="$slot"
}

# converge drives the daemon toward the desired control state. desired = run a
# build+push loop iff enabled AND a slot URL exists; otherwise idle. A slot
# change while enabled restarts the loop against the new slot.
converge() {
  local enabled="$1" slot="$2"
  if [ "$enabled" = "true" ] && [ -n "$slot" ]; then
    if [ -n "$BUILDER_PGID" ] && [ "$slot" != "$BUILDER_SLOT" ]; then
      stop_builder
      revert_slot "$BUILDER_SLOT"
    fi
    if [ -z "$BUILDER_PGID" ]; then
      log "live preview ENABLED for slot $slot"
      start_builder "$slot"
    fi
  else
    if [ -n "$BUILDER_PGID" ]; then
      log "live preview DISABLED"
      local prev="$BUILDER_SLOT"
      stop_builder
      revert_slot "$prev"
    fi
  fi
}

cleanup() {
  if [ -n "$BUILDER_PGID" ]; then
    local prev="$BUILDER_SLOT"
    stop_builder
    revert_slot "$prev"
  fi
}
trap cleanup EXIT INT TERM

# stream_control opens the SSE control channel once and applies each emitted
# control state. Returns when the stream ends (so the outer loop reconnects).
stream_control() {
  local token="$1"
  local ev="" data enabled slot
  # Process substitution (not `curl | while`) so the read loop runs in THIS
  # shell: converge mutates the BUILDER_PGID/BUILDER_SLOT globals the EXIT trap
  # and the next event both depend on. A pipe would fork the loop into a
  # subshell and orphan the builder across reconnects. curl --no-buffer streams
  # each SSE event as it arrives.
  while IFS= read -r line; do
    case "$line" in
      "event: "*)
        ev="${line#event: }"
        ;;
      "data: "*)
        data="${line#data: }"
        if [ "$ev" = "live-preview" ]; then
          enabled="$(printf '%s' "$data" | jq -r '.enabled // false')"
          slot="$(printf '%s' "$data" | jq -r '.slot_url // empty')"
          log "control: enabled=$enabled slot=${slot:-<none>}"
          converge "$enabled" "$slot"
        elif [ "$ev" = "stream-error" ]; then
          log "control stream error: $data"
        fi
        ;;
      "")
        ev=""
        ;;
      *)
        : # comments / keep-alives
        ;;
    esac
  done < <(curl -sS -N --no-buffer \
    -H "Authorization: Bearer $token" \
    -H "Accept: text/event-stream" \
    "$STREAM_URL")
}

log "live-preview daemon starting for session $SESSION_ID (stream $STREAM_URL)"
while true; do
  TOKEN="$(exchange_auth_token || true)"
  if [ -z "$TOKEN" ]; then
    log "no service token; retrying in ${RECONNECT_SECONDS}s"
    sleep "$RECONNECT_SECONDS"
    continue
  fi
  log "control stream connecting"
  stream_control "$TOKEN"
  log "control stream ended; reconnecting in ${RECONNECT_SECONDS}s"
  sleep "$RECONNECT_SECONDS"
done
