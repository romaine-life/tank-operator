#!/usr/bin/env bash
# live-preview-push.sh — the generic, repo-agnostic live-frontend-preview sender.
#
# This is the Stage 3 sender of Glimmung's live frontend preview lane: a dev
# session BUILDS its own repo's frontend, gzips the built dist/, PUTs it to its
# preview environment's edge, and POSTs a push receipt to Glimmung. Glimmung's
# observed verifier then reads the edge back and marks the build live (or stale).
# The sender records a CLAIM ("pushed"); it never asserts "live" — only
# Glimmung's observed read-back confirms that.
#
# It is BUILD-IS-SENDER-SIDE: the edge never builds. It is REPO-AGNOSTIC: nothing
# here is hardcoded to one app — the per-repo build command, frontend dir and
# dist dir come from convention (a .tank/live-preview.json, env, or flags), so
# the same script serves ambience / glimmung / chess-tactics / kill-me /
# tank-operator and any future UI app.
#
# It is the live-preview lane (scratch, for seeing) — never the faithful
# image-deploy validation lane, and it shares NO vocabulary with the retired
# hot-swap path.
#
# ── Contracts it speaks (all landed on glimmung main) ───────────────────────
#   Edge   PUT    {previewURL}/__live-preview/push   body=gzip(tar(dist/))
#                 header X-Live-Preview-Build: <build id>   Bearer <svc JWT>
#          DELETE {previewURL}/__live-preview/push                Bearer <svc JWT>
#          GET    {previewURL}/__live-preview/status             Bearer <svc JWT>
#   Glimmung GET  {glimmung}/v1/previews                          Bearer <svc JWT>
#          POST   {glimmung}/v1/previews/{project}/{name}/push-receipt
#                 body={"build":"<build id>"}                     Bearer <svc JWT>
#          GET    {glimmung}/v1/previews/{project}/{name}         Bearer <svc JWT>
#
# ── Auth (THE footgun) ──────────────────────────────────────────────────────
#   The edge and Glimmung verify an auth.romaine.life-SIGNED service-principal
#   JWT (RS256, iss=https://auth.romaine.life, role=service). The pod's projected
#   token at /var/run/secrets/auth.romaine.life/token is CLUSTER-signed with that
#   AUDIENCE — it is NOT itself the accepted JWT. It must be EXCHANGED:
#       POST {auth}/api/auth/exchange/k8s   Bearer <projected token>  ->  {token}
#   That returned token (sub=svc:tank:<session>, role=service) is what we present.
#   The edge additionally requires sub == the preview's AUTHORIZED_SUBJECT (set by
#   Glimmung at provision = this session's owner) — a pod may only write its own
#   preview. We read the projected token fresh on every exchange (it rotates).
#
# Usage:
#   live-preview-push.sh [options]
#
#   --build | --no-build   run the repo build first (default: --build). --no-build
#                          assumes dist/ is already fresh (the daemon uses this).
#   --revert               DELETE the edge override (revert to the stable backend)
#   --repo DIR             repo root (default: git toplevel of CWD, else CWD)
#   --frontend-dir DIR     dir holding package.json (default: convention, below)
#   --dist DIR             built output dir, relative to frontend dir or absolute
#                          (default: dist)
#   --build-cmd CMD        build command (default: "npm ci && npm run build")
#   --build-id ID          override the build id (default: content hash of dist/)
#   --project P --name N   target preview env explicitly (skips Glimmung lookup
#                          for resolution; still used for the receipt)
#   --preview-url URL      target edge URL explicitly (with --project/--name)
#   --glimmung-url URL     Glimmung base URL (default: $GLIMMUNG_INTERNAL_URL or
#                          http://glimmung.glimmung.svc.cluster.local)
#   --wait-live [SECONDS]  after pushing, poll Glimmung until the env is observed
#                          live or stale (default timeout 60s). Off by default —
#                          the sender's own success is "pushed (claim recorded)".
#   --no-receipt           push to the edge only; skip the Glimmung receipt
#                          (diagnostic; the verifier will not be woken)
#   -h | --help            this help
#
# Convention (repo-agnostic), each setting resolved flag > env > config > default:
#   config file  $REPO/.tank/live-preview.json  {frontend_dir,dist_dir,
#                build_command,watch_command,glimmung_url,project,name}
#   frontend dir default: $REPO/frontend if it has package.json, else $REPO
#   build cmd    default: npm ci && npm run build
#   dist dir     default: dist
#
# Env overrides: LIVE_PREVIEW_REPO_DIR, LIVE_PREVIEW_FRONTEND_DIR,
#   LIVE_PREVIEW_DIST_DIR, LIVE_PREVIEW_BUILD_CMD, LIVE_PREVIEW_BUILD_ID,
#   GLIMMUNG_INTERNAL_URL, LIVE_PREVIEW_PROJECT, LIVE_PREVIEW_NAME,
#   LIVE_PREVIEW_URL, SESSION_ID, AUTH_ROMAINE_TOKEN_PATH,
#   AUTH_ROMAINE_EXCHANGE_URL.
#
# Exit codes: 0 ok · 2 usage · 3 build · 4 auth · 5 resolve · 6 push · 7 receipt
#   · 8 observed stale/timeout (only with --wait-live).
set -euo pipefail

# ── defaults ────────────────────────────────────────────────────────────────
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
AUTH_EXCHANGE_URL="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
GLIMMUNG_URL="${GLIMMUNG_INTERNAL_URL:-http://glimmung.glimmung.svc.cluster.local}"
BUILD_HEADER="X-Live-Preview-Build"

DO_BUILD=true
DO_REVERT=false
DO_RECEIPT=true
WAIT_LIVE=false
WAIT_LIVE_TIMEOUT=60
REPO_DIR="${LIVE_PREVIEW_REPO_DIR:-}"
FRONTEND_DIR="${LIVE_PREVIEW_FRONTEND_DIR:-}"
DIST_DIR_OPT="${LIVE_PREVIEW_DIST_DIR:-}"
BUILD_CMD="${LIVE_PREVIEW_BUILD_CMD:-}"
BUILD_ID="${LIVE_PREVIEW_BUILD_ID:-}"
PROJECT="${LIVE_PREVIEW_PROJECT:-}"
NAME="${LIVE_PREVIEW_NAME:-}"
PREVIEW_URL="${LIVE_PREVIEW_URL:-}"

log()  { printf 'live-preview: %s\n' "$*" >&2; }
fail() { local code="$1"; shift; log "ERROR: $*"; exit "$code"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --build) DO_BUILD=true ;;
    --no-build) DO_BUILD=false ;;
    --revert) DO_REVERT=true ;;
    --no-receipt) DO_RECEIPT=false ;;
    --wait-live) WAIT_LIVE=true
      if [ "${2:-}" ] && printf '%s' "${2:-}" | grep -qE '^[0-9]+$'; then WAIT_LIVE_TIMEOUT="$2"; shift; fi ;;
    --repo) shift; REPO_DIR="${1:-}" ;;
    --frontend-dir) shift; FRONTEND_DIR="${1:-}" ;;
    --dist) shift; DIST_DIR_OPT="${1:-}" ;;
    --build-cmd) shift; BUILD_CMD="${1:-}" ;;
    --build-id) shift; BUILD_ID="${1:-}" ;;
    --project) shift; PROJECT="${1:-}" ;;
    --name) shift; NAME="${1:-}" ;;
    --preview-url) shift; PREVIEW_URL="${1:-}" ;;
    --glimmung-url) shift; GLIMMUNG_URL="${1:-}" ;;
    -h|--help) sed -n '2,80p' "$0"; exit 0 ;;
    *) fail 2 "unknown arg: $1" ;;
  esac
  shift
done
GLIMMUNG_URL="${GLIMMUNG_URL%/}"

TMP_DIR="$(mktemp -d)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

# ── config resolution (repo-agnostic) ───────────────────────────────────────
if [ -z "$REPO_DIR" ]; then
  REPO_DIR="$(git -C "$PWD" rev-parse --show-toplevel 2>/dev/null || true)"
  [ -n "$REPO_DIR" ] || REPO_DIR="$PWD"
fi
REPO_DIR="${REPO_DIR%/}"

CONFIG_FILE="$REPO_DIR/.tank/live-preview.json"
cfg() { # cfg KEY  -> value from config file, or empty
  [ -f "$CONFIG_FILE" ] || return 0
  jq -er --arg k "$1" '.[$k] // empty' "$CONFIG_FILE" 2>/dev/null || true
}

# frontend dir: flag/env > config > convention (frontend/ if it has package.json,
# else repo root).
if [ -z "$FRONTEND_DIR" ]; then FRONTEND_DIR="$(cfg frontend_dir)"; fi
if [ -n "$FRONTEND_DIR" ]; then
  case "$FRONTEND_DIR" in /*) : ;; *) FRONTEND_DIR="$REPO_DIR/$FRONTEND_DIR" ;; esac
else
  if [ -f "$REPO_DIR/frontend/package.json" ]; then FRONTEND_DIR="$REPO_DIR/frontend"
  elif [ -f "$REPO_DIR/package.json" ]; then FRONTEND_DIR="$REPO_DIR"
  else fail 2 "cannot find a frontend package.json under $REPO_DIR; set --frontend-dir or .tank/live-preview.json"; fi
fi
FRONTEND_DIR="${FRONTEND_DIR%/}"

# dist dir: flag/env > config > default "dist", resolved against the frontend dir.
if [ -z "$DIST_DIR_OPT" ]; then DIST_DIR_OPT="$(cfg dist_dir)"; fi
[ -n "$DIST_DIR_OPT" ] || DIST_DIR_OPT="dist"
case "$DIST_DIR_OPT" in /*) DIST_DIR="$DIST_DIR_OPT" ;; *) DIST_DIR="$FRONTEND_DIR/$DIST_DIR_OPT" ;; esac
DIST_DIR="${DIST_DIR%/}"

# build command: flag/env > config > default.
if [ -z "$BUILD_CMD" ]; then BUILD_CMD="$(cfg build_command)"; fi
[ -n "$BUILD_CMD" ] || BUILD_CMD="npm ci && npm run build"

# glimmung / project / name may also come from config.
[ -n "$PROJECT" ] || PROJECT="$(cfg project)"
[ -n "$NAME" ] || NAME="$(cfg name)"
cfg_glimmung="$(cfg glimmung_url)"
if [ -n "$cfg_glimmung" ] && [ "$GLIMMUNG_URL" = "http://glimmung.glimmung.svc.cluster.local" ]; then
  GLIMMUNG_URL="${cfg_glimmung%/}"
fi

# ── auth: exchange the projected SA token for a service-principal JWT ────────
# Read the projected token FRESH each call (it rotates ~hourly). Retries with
# linear backoff so a transient auth blip degrades instead of dying.
exchange_token() {
  local attempt token body
  [ -f "$AUTH_TOKEN_PATH" ] || { log "projected SA token not found at $AUTH_TOKEN_PATH"; return 1; }
  for attempt in 1 2 3 4 5; do
    body="$(curl -fsS -X POST "$AUTH_EXCHANGE_URL" \
      -H "Authorization: Bearer $(cat "$AUTH_TOKEN_PATH")" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json" \
      -d '{}' 2>/dev/null || true)"
    token="$(printf '%s' "$body" | jq -r '.token // empty' 2>/dev/null || true)"
    if [ -n "$token" ]; then printf '%s\n' "$token"; return 0; fi
    log "auth exchange attempt ${attempt}/5 failed"
    sleep "$attempt"
  done
  return 1
}

# jwt_sub decodes the `sub` claim from a JWT payload (no verification — used only
# to pick THIS owner's preview row out of the list Glimmung returns).
jwt_sub() {
  local tok="$1" payload
  payload="$(printf '%s' "$tok" | cut -d. -f2)"
  case $(( ${#payload} % 4 )) in 2) payload="${payload}==";; 3) payload="${payload}=";; esac
  printf '%s' "$payload" | tr '_-' '/+' | base64 -d 2>/dev/null | jq -r '.sub // empty' 2>/dev/null || true
}

# ── preview resolution ──────────────────────────────────────────────────────
# Resolve the target preview env's edge URL + {project,name}. Order:
#   1. explicit --preview-url + --project + --name        (no Glimmung lookup)
#   2. Glimmung GET /v1/previews, matched by --project/--name, else this
#      session's SESSION_ID, else this token's authorized subject.
resolve_preview() {
  local token="$1" rows match count
  if [ -n "$PREVIEW_URL" ] && [ -n "$PROJECT" ] && [ -n "$NAME" ]; then
    log "using explicit preview ${PROJECT}/${NAME} -> $PREVIEW_URL"
    return 0
  fi
  rows="$(curl -fsS -H "Authorization: Bearer $token" -H "Accept: application/json" \
    "$GLIMMUNG_URL/v1/previews" 2>/dev/null || true)"
  if [ -z "$rows" ] || ! printf '%s' "$rows" | jq -e 'type=="array"' >/dev/null 2>&1; then
    fail 5 "could not list previews from $GLIMMUNG_URL/v1/previews (auth or connectivity?)"
  fi
  local sub; sub="$(jwt_sub "$token")"
  if [ -n "$PROJECT" ] && [ -n "$NAME" ]; then
    match="$(printf '%s' "$rows" | jq -c --arg p "$PROJECT" --arg n "$NAME" \
      '[.[]|select(.project==$p and .name==$n)]')"
  elif [ -n "${SESSION_ID:-}" ]; then
    match="$(printf '%s' "$rows" | jq -c --arg s "$SESSION_ID" '[.[]|select(.session_id==$s)]')"
  elif [ -n "$sub" ]; then
    match="$(printf '%s' "$rows" | jq -c --arg s "$sub" '[.[]|select(.authorized_subject==$s)]')"
  else
    match="$(printf '%s' "$rows" | jq -c '.')"
  fi
  count="$(printf '%s' "$match" | jq 'length')"
  if [ "$count" = "0" ]; then
    fail 5 "no preview environment found for this session (provision one via Glimmung POST /v1/previews, or pass --project/--name/--preview-url)"
  fi
  if [ "$count" != "1" ]; then
    log "multiple preview envs match; disambiguate with --project/--name:"
    printf '%s' "$match" | jq -r '.[]|"  - \(.project)/\(.name) [\(.state)] \(.url)"' >&2
    fail 5 "ambiguous preview resolution ($count candidates)"
  fi
  PROJECT="$(printf '%s' "$match" | jq -r '.[0].project')"
  NAME="$(printf '%s' "$match" | jq -r '.[0].name')"
  PREVIEW_URL="$(printf '%s' "$match" | jq -r '.[0].url')"
  local state; state="$(printf '%s' "$match" | jq -r '.[0].state')"
  log "resolved preview ${PROJECT}/${NAME} [state=$state] -> $PREVIEW_URL"
  [ -n "$PREVIEW_URL" ] && [ "$PREVIEW_URL" != "null" ] || fail 5 "preview ${PROJECT}/${NAME} has no edge URL yet (still provisioning?)"
}

# ── build id: content hash of dist/ ─────────────────────────────────────────
# The build id is a content hash of the built dist/ by default: it reflects the
# EXACT bytes served, so the edge's status read-back (observed-not-claimed) is
# comparing real content, and identical builds get a stable id. Overridable.
dist_build_id() {
  local h
  h="$( cd "$DIST_DIR" && find . -type f ! -name '.*' -print0 2>/dev/null \
        | LC_ALL=C sort -z \
        | xargs -0 sha256sum 2>/dev/null \
        | sha256sum | cut -c1-16 )"
  [ -n "$h" ] && printf 'c-%s\n' "$h" || printf 'c-unknown\n'
}

# http_put_dist streams gzip(tar(dist/)) to the edge push endpoint and prints the
# HTTP status; the response body lands in $TMP_DIR/push.out.
http_put_dist() {
  local url="$1" token="$2" build="$3"
  tar czf - -C "$DIST_DIR" . \
    | curl -sS -o "$TMP_DIR/push.out" -w '%{http_code}' -X PUT \
        -H "Authorization: Bearer $token" \
        -H "Content-Type: application/gzip" \
        -H "$BUILD_HEADER: $build" \
        --data-binary @- \
        "$url/__live-preview/push"
}

# ── revert path ─────────────────────────────────────────────────────────────
if [ "$DO_REVERT" = true ]; then
  TOKEN="$(exchange_token || true)"; [ -n "$TOKEN" ] || fail 4 "auth.romaine.life exchange returned no token"
  resolve_preview "$TOKEN"
  log "reverting edge override -> stable backend ($PREVIEW_URL)"
  status="$(curl -sS -o "$TMP_DIR/del.out" -w '%{http_code}' -X DELETE \
    -H "Authorization: Bearer $TOKEN" "$PREVIEW_URL/__live-preview/push" || true)"
  if [ "$status" = "200" ]; then log "reverted"; exit 0; fi
  log "revert response: $(cat "$TMP_DIR/del.out" 2>/dev/null)"
  fail 6 "revert failed (HTTP $status)"
fi

# ── build ───────────────────────────────────────────────────────────────────
if [ "$DO_BUILD" = true ]; then
  log "building frontend in $FRONTEND_DIR : $BUILD_CMD"
  if ! ( cd "$FRONTEND_DIR" && eval "$BUILD_CMD" ); then
    fail 3 "build failed (\`$BUILD_CMD\` in $FRONTEND_DIR)"
  fi
fi
if [ ! -f "$DIST_DIR/index.html" ]; then
  fail 3 "no built bundle at $DIST_DIR (index.html missing) — build first or set --dist"
fi

[ -n "$BUILD_ID" ] || BUILD_ID="$(dist_build_id)"
log "build id: $BUILD_ID"

# ── auth + resolve ──────────────────────────────────────────────────────────
log "exchanging projected SA identity for a service-principal JWT"
TOKEN="$(exchange_token || true)"; [ -n "$TOKEN" ] || fail 4 "auth.romaine.life exchange returned no token (is $AUTH_TOKEN_PATH projected?)"
resolve_preview "$TOKEN"

# ── push ────────────────────────────────────────────────────────────────────
log "pushing $DIST_DIR -> $PREVIEW_URL/__live-preview/push (build=$BUILD_ID)"
STATUS="$(http_put_dist "$PREVIEW_URL" "$TOKEN" "$BUILD_ID")"
if [ "$STATUS" != "200" ]; then
  detail="$(jq -r '.detail // empty' "$TMP_DIR/push.out" 2>/dev/null || true)"
  [ -n "$detail" ] || detail="$(head -c 400 "$TMP_DIR/push.out" 2>/dev/null)"
  case "$STATUS" in
    401|403) fail 6 "edge rejected push (HTTP $STATUS): $detail
  -> auth: the exchanged JWT must be role=service AND its sub must equal this
     preview's authorized_subject (the owner Glimmung set at provision)." ;;
    413) fail 6 "edge rejected push: bundle too large (HTTP 413): $detail" ;;
    400) fail 6 "edge rejected push: bad archive/build header (HTTP 400): $detail" ;;
    502|503|504) fail 6 "edge unreachable/backend down (HTTP $STATUS): $detail" ;;
    *) fail 6 "edge push failed (HTTP $STATUS): $detail" ;;
  esac
fi
log "pushed to edge (build=$BUILD_ID, $(jq -r '"\(.files) files, \(.bytes) bytes"' "$TMP_DIR/push.out" 2>/dev/null))"

# ── receipt: record the CLAIM with Glimmung (wakes the observed verifier) ────
if [ "$DO_RECEIPT" = true ]; then
  RTOKEN="$(exchange_token || true)"; [ -n "$RTOKEN" ] || RTOKEN="$TOKEN"
  RSTATUS="$(curl -sS -o "$TMP_DIR/receipt.out" -w '%{http_code}' -X POST \
    -H "Authorization: Bearer $RTOKEN" -H "Content-Type: application/json" \
    -d "$(jq -nc --arg b "$BUILD_ID" '{build:$b}')" \
    "$GLIMMUNG_URL/v1/previews/$PROJECT/$NAME/push-receipt" || true)"
  if [ "$RSTATUS" != "200" ]; then
    detail="$(jq -r '.detail // .title // empty' "$TMP_DIR/receipt.out" 2>/dev/null || true)"
    fail 7 "push receipt failed (HTTP $RSTATUS): ${detail:-$(head -c 300 "$TMP_DIR/receipt.out")}
  (the edge has the build, but Glimmung was not told to verify it)"
  fi
  log "pushed (claim recorded with Glimmung — observed-live is confirmed by Glimmung's read-back, not here)"
else
  log "pushed to edge; receipt skipped (--no-receipt)"
fi

# ── optional: wait for OBSERVED live (Glimmung read the edge back) ───────────
if [ "$WAIT_LIVE" = true ] && [ "$DO_RECEIPT" = true ]; then
  log "waiting up to ${WAIT_LIVE_TIMEOUT}s for Glimmung to observe build $BUILD_ID live"
  deadline=$(( SECONDS + WAIT_LIVE_TIMEOUT ))
  while [ "$SECONDS" -lt "$deadline" ]; do
    PTOKEN="$(exchange_token || true)"; [ -n "$PTOKEN" ] || PTOKEN="$TOKEN"
    row="$(curl -fsS -H "Authorization: Bearer $PTOKEN" \
      "$GLIMMUNG_URL/v1/previews/$PROJECT/$NAME" 2>/dev/null || true)"
    state="$(printf '%s' "$row" | jq -r '.state // empty' 2>/dev/null || true)"
    observed="$(printf '%s' "$row" | jq -r '.observed_build_id // empty' 2>/dev/null || true)"
    case "$state" in
      live)  if [ "$observed" = "$BUILD_ID" ]; then log "OBSERVED LIVE: edge is serving build $BUILD_ID"; exit 0; fi ;;
      stale) fail 8 "OBSERVED STALE: pushed $BUILD_ID but the edge is serving '${observed:-<none>}' — push did not take" ;;
    esac
    sleep 3
  done
  fail 8 "timed out after ${WAIT_LIVE_TIMEOUT}s waiting for observed-live (state=${state:-unknown}, observed=${observed:-<none>})"
fi
