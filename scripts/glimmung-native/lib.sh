#!/usr/bin/env bash

set -Eeuo pipefail

native_require_env() {
  local missing=()
  for name in "$@"; do
    if [ -z "${!name:-}" ]; then
      missing+=("$name")
    fi
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    printf 'missing required env: %s\n' "${missing[*]}" >&2
    exit 2
  fi
}

native_managed_runner() {
  [ "${GLIMMUNG_MANAGED_RUNNER:-}" = "1" ]
}

native_init() {
  native_require_env \
    GLIMMUNG_ATTEMPT_TOKEN \
    GLIMMUNG_COMPLETED_URL \
    GLIMMUNG_EVENTS_URL \
    GLIMMUNG_GITHUB_TOKEN_URL \
    GLIMMUNG_JOB_ID \
    GLIMMUNG_RUN_ID
  NATIVE_SEQ_FILE="${NATIVE_SEQ_FILE:-/tmp/glimmung-native-seq}"
  printf '0\n' >"$NATIVE_SEQ_FILE"
}

native_next_seq() {
  local current next
  current="$(cat "$NATIVE_SEQ_FILE" 2>/dev/null || printf '0')"
  case "$current" in
    *[!0-9]*|"") current=0 ;;
  esac
  next=$((current + 1))
  printf '%s\n' "$next" >"$NATIVE_SEQ_FILE"
  printf '%s' "$next"
}

native_post_json() {
  local url="$1"
  local payload="$2"
  curl -fsS \
    --retry 5 \
    --retry-delay 1 \
    --retry-all-errors \
    -H "Content-Type: application/json" \
    -H "X-Glimmung-Attempt-Token: ${GLIMMUNG_ATTEMPT_TOKEN}" \
    -d "$payload" \
    "$url" >/dev/null
}

native_event() {
  if native_managed_runner; then
    return 0
  fi
  local event="$1"
  local step_slug="${2:-}"
  local message="${3:-}"
  local exit_code="${4:-}"
  local seq exit_json payload
  seq="$(native_next_seq)"
  if [ -n "$exit_code" ]; then
    exit_json="$exit_code"
  else
    exit_json="null"
  fi
  payload="$(
    jq -nc \
      --arg job_id "$GLIMMUNG_JOB_ID" \
      --argjson seq "$seq" \
      --arg event "$event" \
      --arg step_slug "$step_slug" \
      --arg message "$message" \
      --argjson exit_code "$exit_json" \
      '{
        job_id: $job_id,
        seq: $seq,
        event: $event
      }
      + (if $step_slug != "" then {step_slug: $step_slug} else {} end)
      + (if $message != "" then {message: $message} else {} end)
      + (if $exit_code != null then {exit_code: $exit_code} else {} end)'
  )"
  native_post_json "$GLIMMUNG_EVENTS_URL" "$payload" || true
}

native_log_chunk() {
  local step_slug="$1"
  local message="$2"
  local chunk_dir part
  if [ -z "$message" ]; then
    return 0
  fi
  chunk_dir="$(mktemp -d)"
  printf '%s' "$message" | split -b 12000 - "${chunk_dir}/chunk-"
  for part in "${chunk_dir}"/chunk-*; do
    [ -f "$part" ] || continue
    native_event "log" "$step_slug" "$(cat "$part")" || true
  done
  rm -rf "$chunk_dir"
}

native_log_stream() {
  local step_slug="$1"
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    native_log_chunk "$step_slug" "${line}"$'\n' || true
  done
}

native_failed() {
  local reason="$1"
  local payload
  if native_managed_runner; then
    if [ -n "${GLIMMUNG_COMPLETION_FILE:-}" ]; then
      jq -nc --arg summary "$reason" '{summary_markdown: $summary}' >"$GLIMMUNG_COMPLETION_FILE"
    fi
    echo "$reason" >&2
    return 0
  fi
  payload="$(
    jq -nc \
      --arg conclusion "failure" \
      --arg reason "$reason" \
      --arg job_id "$GLIMMUNG_JOB_ID" \
      '{
        conclusion: $conclusion,
        job_id: $job_id,
        summary_markdown: $reason
      }'
  )"
  native_post_json "$GLIMMUNG_COMPLETED_URL" "$payload" || true
}

native_completed() {
  local outputs_json="${1:-null}"
  local verification_json="${2:-null}"
  local summary_markdown="${3:-}"
  local payload
  if native_managed_runner; then
    if [ "$outputs_json" != "null" ] && [ -n "${GLIMMUNG_OUTPUT_FILE:-}" ]; then
      jq -c . <<<"$outputs_json" >>"$GLIMMUNG_OUTPUT_FILE"
    fi
    if [ -n "${GLIMMUNG_COMPLETION_FILE:-}" ]; then
      jq -nc \
        --argjson verification "$verification_json" \
        --arg summary "$summary_markdown" \
        '{
          verification: $verification,
          summary_markdown: $summary
        }
        | with_entries(select(.value != null and .value != ""))' \
        >"$GLIMMUNG_COMPLETION_FILE"
    fi
    return 0
  fi
  payload="$(
    jq -nc \
      --arg conclusion "success" \
      --arg job_id "$GLIMMUNG_JOB_ID" \
      --argjson outputs "$outputs_json" \
      --argjson verification "$verification_json" \
      --arg summary "$summary_markdown" \
      '{
        conclusion: $conclusion,
        job_id: $job_id
      }
      + (if $outputs != null then {outputs: $outputs} else {} end)
      + (if $verification != null then {verification: $verification} else {} end)
      + (if $summary != "" then {summary_markdown: $summary} else {} end)'
  )"
  native_post_json "$GLIMMUNG_COMPLETED_URL" "$payload"
}

native_step() {
  local step_slug="$1"
  shift
  local fifo log rc stream_pid
  native_event "step_started" "$step_slug"
  fifo="$(mktemp -u)"
  log="$(mktemp)"
  mkfifo "$fifo"
  native_log_stream "$step_slug" <"$fifo" &
  stream_pid=$!
  set +e
  "$@" > >(tee -a "$log" "$fifo") 2>&1
  rc=$?
  wait "$stream_pid" || true
  set -e
  rm -f "$fifo" "$log"
  if [ "$rc" -ne 0 ]; then
    native_event "step_failed" "$step_slug" "step exited ${rc}" "$rc"
    native_failed "step ${step_slug} exited ${rc}"
    exit "$rc"
  fi
  native_event "step_completed" "$step_slug" "" "0"
}

native_github_token() {
  curl -fsS \
    --retry 5 \
    --retry-delay 1 \
    --retry-all-errors \
    -X POST \
    -H "X-Glimmung-Attempt-Token: ${GLIMMUNG_ATTEMPT_TOKEN}" \
    "$GLIMMUNG_GITHUB_TOKEN_URL" \
    | jq -r '.token'
}

native_git_auth_header() {
  local token="$1"
  local encoded
  encoded="$(printf 'x-access-token:%s' "$token" | base64 | tr -d '\n')"
  printf 'Authorization: Basic %s' "$encoded"
}

tank_slug() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' \
    | cut -c1-40
}

tank_run_slug() {
  local issue="${GLIMMUNG_ISSUE_NUMBER:-unknown}"
  local run_slug
  run_slug="$(tank_slug "$GLIMMUNG_RUN_ID")"
  printf 'glim-%s-%s' "$(tank_slug "$issue")" "$run_slug"
}

tank_validation_url() {
  printf '%s' "${TANK_OPERATOR_URL:-https://operator.romaine.life}"
}
