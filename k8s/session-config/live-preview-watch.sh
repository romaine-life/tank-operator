#!/usr/bin/env bash
# live-preview-watch.sh — the generic, repo-agnostic live-preview DAEMON.
#
# It takes the developer out of the push loop: it watches the built dist/ and,
# whenever its CONTENT changes and settles, pushes the new bundle to the preview
# edge and records a receipt with Glimmung (by invoking live-preview-push.sh).
#
# It is REPO-AGNOSTIC and BUILD-TOOL-AGNOSTIC by watching the OUTPUT dir, not a
# specific tool's log: whether the dev drives vite / next / webpack / esbuild
# (`--watch`, `dev`, etc.) into dist/, the daemon notices the changed bytes and
# pushes. It can also LAUNCH that watcher itself via --watch-cmd / the repo's
# .tank/live-preview.json `watch_command`, so a single daemon both builds and
# pushes. Detection is a content hash poll — no inotify, no tool coupling.
#
# Fail-safe by construction: the build id IS the dist content hash, so a push
# only happens when the bytes actually changed and stabilized; a broken/partial
# build that never updates dist/ is never pushed. A failed push leaves the prior
# bundle live and is retried on the next change (bounded by --max-failures).
#
# This is the live-preview lane (scratch, for seeing) — never the faithful
# image-deploy validation lane; it shares NO vocabulary with the retired
# hot-swap path.
#
# Usage:
#   live-preview-watch.sh [options]
#
#   --watch-cmd CMD      launch CMD (in the frontend dir, own process group) to
#                        keep dist/ fresh; default: the repo's watch_command, else
#                        none (the dev runs their own watcher)
#   --interval SECONDS   dist content poll interval (default 2)
#   --debounce SECONDS   settle window before pushing a change (default 2)
#   --max-failures N     stop after N consecutive push failures (default 10)
#   --revert-on-exit     DELETE the edge override when the daemon stops
#                        (default: leave the last pushed bundle live)
#   --once               run a single detect+push cycle then exit (for tests)
#   --repo/--frontend-dir/--dist/--project/--name/--preview-url/--glimmung-url
#                        forwarded to live-preview-push.sh (see its --help)
#   -h | --help          this help
#
# Env: LIVE_PREVIEW_PUSH_SCRIPT (default: sibling live-preview-push.sh),
#   LIVE_PREVIEW_WATCH_CMD, LIVE_PREVIEW_INTERVAL, LIVE_PREVIEW_DEBOUNCE,
#   plus everything live-preview-push.sh honors (LIVE_PREVIEW_REPO_DIR, etc).
set -uo pipefail

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
PUSH_SCRIPT="${LIVE_PREVIEW_PUSH_SCRIPT:-$SELF_DIR/live-preview-push.sh}"
WATCH_CMD="${LIVE_PREVIEW_WATCH_CMD:-}"
INTERVAL="${LIVE_PREVIEW_INTERVAL:-2}"
DEBOUNCE="${LIVE_PREVIEW_DEBOUNCE:-2}"
MAX_FAILURES=10
REVERT_ON_EXIT=false
RUN_ONCE=false

REPO_DIR="${LIVE_PREVIEW_REPO_DIR:-}"
FRONTEND_DIR="${LIVE_PREVIEW_FRONTEND_DIR:-}"
DIST_DIR_OPT="${LIVE_PREVIEW_DIST_DIR:-}"
PROJECT="${LIVE_PREVIEW_PROJECT:-}"
NAME="${LIVE_PREVIEW_NAME:-}"
PREVIEW_URL="${LIVE_PREVIEW_URL:-}"
GLIMMUNG_URL="${GLIMMUNG_INTERNAL_URL:-}"

log() { printf 'live-preview[watch]: %s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --watch-cmd) shift; WATCH_CMD="${1:-}" ;;
    --interval) shift; INTERVAL="${1:-2}" ;;
    --debounce) shift; DEBOUNCE="${1:-2}" ;;
    --max-failures) shift; MAX_FAILURES="${1:-10}" ;;
    --revert-on-exit) REVERT_ON_EXIT=true ;;
    --once) RUN_ONCE=true ;;
    --repo) shift; REPO_DIR="${1:-}" ;;
    --frontend-dir) shift; FRONTEND_DIR="${1:-}" ;;
    --dist) shift; DIST_DIR_OPT="${1:-}" ;;
    --project) shift; PROJECT="${1:-}" ;;
    --name) shift; NAME="${1:-}" ;;
    --preview-url) shift; PREVIEW_URL="${1:-}" ;;
    --glimmung-url) shift; GLIMMUNG_URL="${1:-}" ;;
    -h|--help) sed -n '2,55p' "$0"; exit 0 ;;
    *) log "unknown arg: $1"; exit 2 ;;
  esac
  shift
done

[ -f "$PUSH_SCRIPT" ] || { log "push script not found at $PUSH_SCRIPT"; exit 2; }

# ── config resolution (mirror live-preview-push.sh so dist/ is the same dir) ─
if [ -z "$REPO_DIR" ]; then
  REPO_DIR="$(git -C "$PWD" rev-parse --show-toplevel 2>/dev/null || true)"
  [ -n "$REPO_DIR" ] || REPO_DIR="$PWD"
fi
REPO_DIR="${REPO_DIR%/}"
CONFIG_FILE="$REPO_DIR/.tank/live-preview.json"
cfg() { [ -f "$CONFIG_FILE" ] || return 0; jq -er --arg k "$1" '.[$k] // empty' "$CONFIG_FILE" 2>/dev/null || true; }

if [ -z "$FRONTEND_DIR" ]; then FRONTEND_DIR="$(cfg frontend_dir)"; fi
if [ -n "$FRONTEND_DIR" ]; then
  case "$FRONTEND_DIR" in /*) : ;; *) FRONTEND_DIR="$REPO_DIR/$FRONTEND_DIR" ;; esac
else
  if [ -f "$REPO_DIR/frontend/package.json" ]; then FRONTEND_DIR="$REPO_DIR/frontend"
  elif [ -f "$REPO_DIR/package.json" ]; then FRONTEND_DIR="$REPO_DIR"
  else log "cannot find a frontend package.json under $REPO_DIR; set --frontend-dir"; exit 2; fi
fi
FRONTEND_DIR="${FRONTEND_DIR%/}"
if [ -z "$DIST_DIR_OPT" ]; then DIST_DIR_OPT="$(cfg dist_dir)"; fi
[ -n "$DIST_DIR_OPT" ] || DIST_DIR_OPT="dist"
case "$DIST_DIR_OPT" in /*) DIST_DIR="$DIST_DIR_OPT" ;; *) DIST_DIR="$FRONTEND_DIR/$DIST_DIR_OPT" ;; esac
DIST_DIR="${DIST_DIR%/}"
[ -n "$WATCH_CMD" ] || WATCH_CMD="$(cfg watch_command)"

# dist_build_id MUST match live-preview-push.sh's algorithm so a changed hash
# means a new build id (and we forward it as --build-id for an exact match).
dist_build_id() {
  [ -f "$DIST_DIR/index.html" ] || { printf ''; return 0; }
  local h
  h="$( cd "$DIST_DIR" && find . -type f ! -name '.*' -print0 2>/dev/null \
        | LC_ALL=C sort -z | xargs -0 sha256sum 2>/dev/null \
        | sha256sum | cut -c1-16 )"
  [ -n "$h" ] && printf 'c-%s' "$h" || printf ''
}

# Forwarded args so push.sh does not re-resolve (and cannot diverge from us).
push_args=( --no-build --frontend-dir "$FRONTEND_DIR" --dist "$DIST_DIR" )
[ -n "$PROJECT" ] && push_args+=( --project "$PROJECT" )
[ -n "$NAME" ] && push_args+=( --name "$NAME" )
[ -n "$PREVIEW_URL" ] && push_args+=( --preview-url "$PREVIEW_URL" )
[ -n "$GLIMMUNG_URL" ] && push_args+=( --glimmung-url "$GLIMMUNG_URL" )

WATCH_PGID=""
start_watch_cmd() {
  [ -n "$WATCH_CMD" ] || return 0
  log "launching watch command in $FRONTEND_DIR: $WATCH_CMD"
  setsid bash -c 'cd "$1" && exec bash -c "$2"' _ "$FRONTEND_DIR" "$WATCH_CMD" &
  WATCH_PGID=$!
}
stop_watch_cmd() {
  [ -n "$WATCH_PGID" ] || return 0
  log "stopping watch command (pgid $WATCH_PGID)"
  kill -TERM "-$WATCH_PGID" 2>/dev/null || true
  wait "$WATCH_PGID" 2>/dev/null || true
  WATCH_PGID=""
}

cleanup() {
  stop_watch_cmd
  if [ "$REVERT_ON_EXIT" = true ]; then
    log "reverting edge override on exit"
    bash "$PUSH_SCRIPT" --revert "${push_args[@]:1}" 2>/dev/null || log "revert on exit failed"
  fi
}
trap cleanup EXIT INT TERM

# push_current pushes the current dist/ (build id = its content hash). Returns 0
# on a successful push, non-zero otherwise.
push_current() {
  local bid="$1"
  bash "$PUSH_SCRIPT" "${push_args[@]}" --build-id "$bid"
}

log "daemon starting (dist=$DIST_DIR interval=${INTERVAL}s debounce=${DEBOUNCE}s max-failures=$MAX_FAILURES)"
start_watch_cmd

LAST_PUSHED=""
FAILURES=0
while true; do
  BID="$(dist_build_id)"
  if [ -n "$BID" ] && [ "$BID" != "$LAST_PUSHED" ]; then
    # Debounce: let a burst of writes settle, then confirm the content is stable
    # before pushing (so we push the final state once, not each intermediate).
    sleep "$DEBOUNCE"
    BID2="$(dist_build_id)"
    if [ "$BID2" != "$BID" ]; then
      log "dist still changing ($BID -> $BID2); will retry next cycle"
    else
      log "change detected: $BID (was: ${LAST_PUSHED:-<none>}) — pushing"
      if push_current "$BID"; then
        LAST_PUSHED="$BID"
        FAILURES=0
      else
        FAILURES=$(( FAILURES + 1 ))
        log "push failed ($FAILURES/$MAX_FAILURES); prior bundle stays live"
        if [ "$FAILURES" -ge "$MAX_FAILURES" ]; then
          log "giving up after $MAX_FAILURES consecutive push failures"
          exit 6
        fi
        # Back off proportionally to the failure streak (bounded).
        sleep "$(( FAILURES < 5 ? FAILURES : 5 ))"
      fi
    fi
  fi
  if [ "$RUN_ONCE" = true ]; then
    exit 0
  fi
  sleep "$INTERVAL"
done
