#!/usr/bin/env bash
# workspace-repo-reporter.sh — pod-side background loop that records which
# GitHub repos a session actually has checked out under /workspace, so the
# Tank sidebar can search and show every repo a session worked on, not just
# the ones the user picked at create time (the durable sessions.repos list).
#
# Why this exists: sessions.repos is write-once "intent" — the slugs picked
# on the splash page that drive the repo-cloner init container. But an agent
# frequently clones more repos on demand mid-session (mint_clone_token +
# git clone, or a plain public clone), and migration 0035 explicitly noted
# that on-demand shape had no durable record. This reporter observes reality:
# it scans the workspace's git remotes and folds whatever it finds into the
# durable sessions.discovered_repos set via the orchestrator's internal API.
#
# Where it runs: launched in the background by agent-runner-launch.sh and
# codex-runner-launch.sh — the runner sidecars are the only session shapes
# with a shared /workspace emptyDir, and they already carry SESSION_ID, the
# projected auth.romaine.life SA token, and TANK_OPERATOR_INTERNAL_URL. The
# init container (repo-cloner) has already finished by the time the sidecar
# starts, so the first scan also captures the create-time clones.
#
# Reliability posture: best-effort and self-healing. It never exits non-zero
# on a transient failure, it only POSTs when the observed set actually grows
# (steady-state network cost is ~0), and the orchestrator treats a re-report
# of an already-known set as a no-op (no row_version bump, no SSE fan-out).
# Credentials embedded in remote URLs are stripped before reporting.

set -u

WORKSPACE="${WORKSPACE:-/workspace}"
SESSION_ID="${SESSION_ID:-}"
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
AUTH_EXCHANGE_URL="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
TANK_OPERATOR_INTERNAL_URL="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
INTERVAL="${WORKSPACE_REPO_REPORT_INTERVAL:-30}"
SCAN_MAXDEPTH="${WORKSPACE_REPO_SCAN_MAXDEPTH:-3}"

log() { echo "workspace-repo-reporter: $*" >&2; }

if [ -z "$SESSION_ID" ]; then
  log "SESSION_ID unset; not reporting workspace repos"
  exit 0
fi

# scan_slugs prints the sorted, unique set of "owner/name" GitHub slugs found
# across every git remote under $WORKSPACE.
#
#   - find -name .git -prune locates each repo without descending into its
#     object store; -maxdepth bounds the walk so a deep node_modules tree
#     can't dominate the scan.
#   - config --get-regexp enumerates ALL remotes (origin, upstream, forks),
#     not just origin, so a session that adds an upstream is captured too.
#   - the trailing ".git" is stripped first, then the slug is matched by
#     anchoring on github.com. Because the match keeps only the owner/name
#     that follows github.com, any "x-access-token:<TOKEN>@" credentials in
#     the URL land in the discarded prefix and never reach the orchestrator.
scan_slugs() {
  find "$WORKSPACE" -maxdepth "$SCAN_MAXDEPTH" -type d -name .git -prune 2>/dev/null \
    | while IFS= read -r gitdir; do
        git -C "${gitdir%/.git}" config --get-regexp '^remote\..*\.url$' 2>/dev/null \
          | awk '{ print $2 }'
      done \
    | sed -E 's#\.git$##' \
    | sed -nE 's#^.*github\.com[:/]+([A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+)$#\1#p' \
    | sort -u
}

# exchange_token swaps the projected auth.romaine.life SA token for a
# role=service JWT whose actor_email is this session's owner — the same
# exchange the repo-cloner uses. Prints the JWT, or nothing on failure.
exchange_token() {
  if [ ! -r "$AUTH_TOKEN_PATH" ]; then
    log "auth token not readable at $AUTH_TOKEN_PATH"
    return 1
  fi
  curl -fsS -X POST "$AUTH_EXCHANGE_URL" \
    -H "Authorization: Bearer $(cat "$AUTH_TOKEN_PATH")" \
    -H "Content-Type: application/json" \
    -d '{}' 2>/dev/null \
    | jq -r '.token // empty'
}

# post_repos reports the full observed slug set (passed as a newline-
# separated list) to the orchestrator. The server unions it into the
# durable set, so sending the complete current set each time is correct
# and idempotent.
post_repos() {
  local slugs="$1" token payload
  token="$(exchange_token)"
  if [ -z "$token" ]; then
    log "token exchange returned nothing; will retry next tick"
    return 1
  fi
  payload="$(printf '%s\n' "$slugs" | jq -Rsc 'split("\n") | map(select(length > 0)) | {repos: .}')"
  curl -fsS -X POST \
    "${TANK_OPERATOR_INTERNAL_URL%/}/api/internal/sessions/${SESSION_ID}/discovered-repos" \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json" \
    -d "$payload" >/dev/null 2>&1
}

log "watching $WORKSPACE for github repos (interval ${INTERVAL}s, session ${SESSION_ID})"

# reported holds the cumulative set of slugs we've already successfully
# reported (sorted, newline-separated). We only POST when the current scan
# introduces a slug not already in this set; a repo that later disappears
# from the workspace is not re-reported (the durable set is monotonic — the
# session still "worked on" it).
reported=""
while true; do
  current="$(scan_slugs)"
  if [ -n "$current" ]; then
    new="$(comm -23 <(printf '%s\n' "$current") <(printf '%s\n' "$reported" | sed '/^$/d') 2>/dev/null)"
    if [ -n "$new" ]; then
      if post_repos "$current"; then
        reported="$(printf '%s\n%s\n' "$reported" "$current" | sed '/^$/d' | sort -u)"
        log "reported: $(printf '%s ' $new)"
      fi
    fi
  fi
  sleep "$INTERVAL"
done
