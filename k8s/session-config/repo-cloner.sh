#!/usr/bin/env bash
set -u

log() {
  printf '[repo-cloner] %s\n' "$*" >&2
}

now_utc() {
  date -u '+%Y-%m-%dT%H:%M:%SZ'
}

short_error() {
  tr '\r\n' '  ' | cut -c1-300
}

workspace="${WORKSPACE:-/workspace}"
workspace="${workspace%/}"
if [ -z "$workspace" ]; then
  workspace="/workspace"
fi
repos_json="${TANK_SELECTED_REPOS:-[]}"
repo_pattern='^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$'
tank_url="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
tank_url="${tank_url%/}"
session_id="${SESSION_ID:-}"
auth_exchange_url="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
auth_token_path="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
mcp_github_url="${MCP_GITHUB_URL:-http://mcp-github.mcp-github.svc:80}"
mcp_github_url="${mcp_github_url%/}"

repos_file="$(mktemp)"
state_file="$(mktemp)"
auth_jwt=""
git_config=""

cleanup() {
  rm -f "$repos_file" "$state_file"
  if [ -n "$git_config" ]; then
    rm -f "$git_config"
  fi
}
trap cleanup EXIT

if ! command -v jq >/dev/null 2>&1; then
  log "jq is missing; cannot parse selected repos"
  exit 0
fi
if ! command -v curl >/dev/null 2>&1; then
  log "curl is missing; cannot mint clone token"
  exit 0
fi
if ! command -v git >/dev/null 2>&1; then
  log "git is missing; cannot clone selected repos"
  exit 0
fi

if ! printf '%s' "$repos_json" | jq -cer --arg pattern "$repo_pattern" '
  if type != "array" then
    error("not an array")
  elif all(.[]; type == "string" and test($pattern)) then
    .
  else
    error("invalid repo slug")
  end
' > "$repos_file"; then
  log "TANK_SELECTED_REPOS was not a valid JSON repo slug array"
  exit 0
fi

repo_count="$(jq -r 'length' "$repos_file")"
if [ "$repo_count" = "0" ]; then
  exit 0
fi

jq -n --slurpfile repos "$repos_file" --arg started "$(now_utc)" '
  reduce $repos[0][] as $slug ({}; .[$slug] = {
    status: "pending",
    started_at: $started
  })
' > "$state_file"

exchange_auth() {
  if [ -n "$auth_jwt" ]; then
    return 0
  fi
  if [ ! -r "$auth_token_path" ]; then
    log "auth.romaine token is not readable at $auth_token_path"
    return 1
  fi

  local response token
  response="$(mktemp)"
  if ! curl -fsS --max-time 20 \
    -X POST "$auth_exchange_url" \
    -H "Authorization: Bearer $(cat "$auth_token_path")" \
    -H 'Content-Type: application/json' \
    -d '{}' > "$response"; then
    rm -f "$response"
    log "auth.romaine exchange failed"
    return 1
  fi
  token="$(jq -er '.token' "$response" 2>/dev/null || true)"
  rm -f "$response"
  if [ -z "$token" ]; then
    log "auth.romaine exchange response did not include a token"
    return 1
  fi
  auth_jwt="$token"
}

post_state() {
  if [ -z "$session_id" ]; then
    return 0
  fi
  if [ -z "$auth_jwt" ]; then
    return 0
  fi

  local body
  body="$(mktemp)"
  jq -c '{clone_state: .}' "$state_file" > "$body"
  if ! curl -fsS --max-time 10 \
    -X POST "$tank_url/api/internal/sessions/$session_id/clone-state" \
    -H "Authorization: Bearer $auth_jwt" \
    -H 'Content-Type: application/json' \
    --data-binary @"$body" >/dev/null; then
    log "clone-state update failed; continuing"
  fi
  rm -f "$body"
}

mark_cloning() {
  local slug="$1"
  local path="$2"
  local tmp
  tmp="$(mktemp)"
  jq --arg slug "$slug" --arg path "$path" --arg started "$(now_utc)" '
    .[$slug] = ((.[$slug] // {}) + {
      status: "cloning",
      path: $path,
      started_at: $started
    })
  ' "$state_file" > "$tmp" && mv "$tmp" "$state_file"
}

mark_cloned() {
  local slug="$1"
  local path="$2"
  local commit="$3"
  local tmp
  tmp="$(mktemp)"
  jq --arg slug "$slug" --arg path "$path" --arg commit "$commit" --arg finished "$(now_utc)" '
    .[$slug] = ((.[$slug] // {}) + {
      status: "cloned",
      path: $path,
      finished_at: $finished
    } + (if $commit == "" then {} else {commit: $commit} end))
  ' "$state_file" > "$tmp" && mv "$tmp" "$state_file"
}

mark_failed() {
  local slug="$1"
  local error="$2"
  local tmp
  tmp="$(mktemp)"
  jq --arg slug "$slug" --arg error "$error" --arg finished "$(now_utc)" '
    .[$slug] = ((.[$slug] // {}) + {
      status: "failed",
      error: $error,
      finished_at: $finished
    })
  ' "$state_file" > "$tmp" && mv "$tmp" "$state_file"
}

mark_all_failed() {
  local error="$1"
  while IFS= read -r slug; do
    mark_failed "$slug" "$error"
  done < <(jq -r '.[]' "$repos_file")
}

mint_clone_token() {
  local request raw rpc token message
  request="$(mktemp)"
  raw="$(mktemp)"
  rpc="$(mktemp)"
  jq -n --slurpfile repos "$repos_file" '{
    jsonrpc: "2.0",
    id: 1,
    method: "tools/call",
    params: {
      name: "mint_clone_token",
      arguments: {
        repos: $repos[0],
        write: false
      }
    }
  }' > "$request"

  if ! curl -fsS --max-time 30 \
    -X POST "$mcp_github_url/" \
    -H "Authorization: Bearer $auth_jwt" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    --data-binary @"$request" > "$raw"; then
    rm -f "$request" "$raw" "$rpc"
    log "mcp-github mint_clone_token call failed"
    return 1
  fi

  awk '
    /^data: / { sub(/^data: /, ""); print; exit }
    /^\{/ { print; exit }
  ' "$raw" > "$rpc"

  if [ ! -s "$rpc" ]; then
    rm -f "$request" "$raw" "$rpc"
    log "mcp-github response was empty"
    return 1
  fi

  if jq -e '.error' "$rpc" >/dev/null 2>&1; then
    message="$(jq -r '.error.message // .error // "unknown mcp-github error"' "$rpc" | short_error)"
    rm -f "$request" "$raw" "$rpc"
    log "mcp-github returned error: $message"
    return 1
  fi

  token="$(jq -er '
    ((.result.structuredContent? // {}) | .token?) //
    (.result.content[]? | select(.type == "text" and .text) | .text | fromjson? | .token?) //
    empty
  ' "$rpc" 2>/dev/null || true)"

  rm -f "$request" "$raw" "$rpc"
  if [ -z "$token" ]; then
    log "mcp-github mint_clone_token response did not include a token"
    return 1
  fi
  printf '%s' "$token"
}

resolve_dest() {
  local slug="$1"
  local owner="${slug%%/*}"
  local name="${slug##*/}"
  local dest="$workspace/$name"
  if [ ! -e "$dest" ]; then
    printf '%s' "$dest"
    return 0
  fi

  dest="$workspace/${owner}__${name}"
  if [ ! -e "$dest" ]; then
    printf '%s' "$dest"
    return 0
  fi

  local i=2
  while [ -e "$dest-$i" ]; do
    i=$((i + 1))
  done
  printf '%s' "$dest-$i"
}

remove_failed_dest() {
  local dest="$1"
  case "$dest" in
    "$workspace"/*) rm -rf "$dest" ;;
    *) log "refusing to remove unexpected clone path $dest" ;;
  esac
}

if ! exchange_auth; then
  mark_all_failed "failed to exchange pod service-account token"
  exit 0
fi
post_state

clone_token="$(mint_clone_token || true)"
if [ -z "$clone_token" ]; then
  mark_all_failed "failed to mint GitHub clone token"
  post_state
  exit 0
fi

auth_header="$(printf 'x-access-token:%s' "$clone_token" | base64 | tr -d '\n')"
git_config="$(mktemp)"
chmod 0600 "$git_config"
printf '[http "https://github.com/"]\n\textraheader = Authorization: Basic %s\n' "$auth_header" > "$git_config"

mkdir -p "$workspace"
while IFS= read -r slug; do
  dest="$(resolve_dest "$slug")"
  mark_cloning "$slug" "$dest"
  post_state

  clone_log="$(mktemp)"
  if GIT_CONFIG_GLOBAL="$git_config" git clone --depth=50 "https://github.com/$slug.git" "$dest" > "$clone_log" 2>&1; then
    commit="$(git -C "$dest" rev-parse --short HEAD 2>/dev/null || true)"
    mark_cloned "$slug" "$dest" "$commit"
    log "cloned $slug into $dest"
  else
    error="$(tail -20 "$clone_log" | short_error)"
    remove_failed_dest "$dest"
    mark_failed "$slug" "${error:-git clone failed}"
    log "failed to clone $slug"
  fi
  rm -f "$clone_log"
done < <(jq -r '.[]' "$repos_file")

rm -f "$git_config"
git_config=""
post_state
exit 0
