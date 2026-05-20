#!/usr/bin/env bash
set -euo pipefail

WORKSPACE="${WORKSPACE:-/workspace}"
REPOS_JSON="${TANK_REPOS_JSON:-[]}"
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
AUTH_EXCHANGE_URL="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
MCP_GITHUB_URL="${MCP_GITHUB_URL:-http://mcp-github.mcp-github.svc:80}"
TANK_OPERATOR_INTERNAL_URL="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
GIT_CLONE_DEPTH="${GIT_CLONE_DEPTH:-50}"

REPOS_JSON="$(printf '%s' "$REPOS_JSON" | jq -c '[.[] | select(type == "string")]')"
if [ "$(jq 'length' <<<"$REPOS_JSON")" -eq 0 ]; then
  echo "repo-cloner: no repositories selected"
  exit 0
fi

mapfile -t REPOS < <(jq -r '.[]' <<<"$REPOS_JSON")

for slug in "${REPOS[@]}"; do
  if [[ ! "$slug" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; then
    echo "repo-cloner: invalid repository slug: $slug" >&2
    exit 1
  fi
done

CLONE_STATE="$(jq -nc --argjson repos "$REPOS_JSON" 'reduce $repos[] as $repo ({}; .[$repo] = {"status": "pending"})')"
AUTH_TOKEN=""

post_clone_state() {
  if [ -z "${AUTH_TOKEN:-}" ] || [ -z "${SESSION_ID:-}" ]; then
    return 0
  fi
  local payload
  payload="$(jq -nc --argjson clone_state "$CLONE_STATE" '{"clone_state": $clone_state}')"
  if ! curl -fsS -X POST \
    "${TANK_OPERATOR_INTERNAL_URL%/}/api/internal/sessions/${SESSION_ID}/clone-state" \
    -H "Authorization: Bearer ${AUTH_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$payload" >/dev/null; then
    echo "repo-cloner: warning: failed to publish clone_state" >&2
  fi
}

set_repo_state() {
  local slug="$1"
  local status="$2"
  local path="${3:-}"
  local error="${4:-}"
  local now
  now="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  CLONE_STATE="$(
    jq -c \
      --arg slug "$slug" \
      --arg status "$status" \
      --arg path "$path" \
      --arg error "$error" \
      --arg now "$now" \
      '
        .[$slug] = ((.[$slug] // {}) + {"status": $status}
          + (if $status == "cloning" then {"started_at": $now}
             elif $status == "cloned" then {"finished_at": $now, "path": $path}
             elif $status == "failed" then {"finished_at": $now, "path": $path, "error": $error}
             else {} end))
      ' <<<"$CLONE_STATE"
  )"
  post_clone_state
}

fail_all_repos() {
  local msg="$1"
  for slug in "${REPOS[@]}"; do
    set_repo_state "$slug" "failed" "" "$msg"
  done
  echo "repo-cloner: $msg" >&2
  exit 1
}

echo "repo-cloner: exchanging pod identity"
AUTH_TOKEN="$(
  curl -fsS -X POST "$AUTH_EXCHANGE_URL" \
    -H "Authorization: Bearer $(cat "$AUTH_TOKEN_PATH")" \
    -H "Content-Type: application/json" \
    -d '{}' | jq -r '.token // empty'
)"
if [ -z "$AUTH_TOKEN" ]; then
  echo "repo-cloner: auth.romaine.life exchange returned no token" >&2
  exit 1
fi

post_clone_state

RPC_PAYLOAD="$(
  jq -nc --argjson repos "$REPOS_JSON" '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "mint_clone_token",
      "arguments": {"repos": $repos}
    }
  }'
)"
MCP_RESPONSE="$(
  curl -fsS -X POST "${MCP_GITHUB_URL%/}/" \
    -H "Authorization: Bearer ${AUTH_TOKEN}" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -d "$RPC_PAYLOAD"
)"
MCP_JSON="$(printf '%s\n' "$MCP_RESPONSE" | sed -n 's/^data: //p' | head -n1)"
if [ -z "$MCP_JSON" ]; then
  MCP_JSON="$MCP_RESPONSE"
fi
if [ "$(jq -r 'has("error")' <<<"$MCP_JSON")" = "true" ]; then
  fail_all_repos "mcp-github returned an error: $(jq -r '.error.message // "unknown error"' <<<"$MCP_JSON")"
fi
GITHUB_TOKEN="$(jq -r '.result.structuredContent.token // empty' <<<"$MCP_JSON")"
if [ -z "$GITHUB_TOKEN" ]; then
  fail_all_repos "mcp-github mint_clone_token returned no token"
fi

ASKPASS="$(mktemp)"
cleanup() {
  rm -f "$ASKPASS"
}
trap cleanup EXIT
cat >"$ASKPASS" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  *Username*) printf '%s\n' x-access-token ;;
  *Password*) printf '%s\n' "${GITHUB_TOKEN:?}" ;;
  *) printf '\n' ;;
esac
EOF
chmod 0700 "$ASKPASS"

mkdir -p "$WORKSPACE"
failures=0
for slug in "${REPOS[@]}"; do
  repo_name="${slug##*/}"
  target="${WORKSPACE%/}/${repo_name}"
  set_repo_state "$slug" "cloning" "$target"

  if [ -d "$target/.git" ]; then
    origin="$(git -C "$target" config --get remote.origin.url || true)"
    if [[ "$origin" == *"github.com/${slug}.git" || "$origin" == *"github.com/${slug}" ]]; then
      echo "repo-cloner: $slug already exists at $target"
      set_repo_state "$slug" "cloned" "$target"
      continue
    fi
    msg="target already contains a different git repository: $target"
    echo "repo-cloner: $msg" >&2
    set_repo_state "$slug" "failed" "$target" "$msg"
    failures=1
    continue
  fi
  if [ -e "$target" ]; then
    msg="target path already exists and is not a git repository: $target"
    echo "repo-cloner: $msg" >&2
    set_repo_state "$slug" "failed" "$target" "$msg"
    failures=1
    continue
  fi

  tmp_target="${target}.tmp.$$"
  rm -rf "$tmp_target"
  clone_args=()
  if [ "$GIT_CLONE_DEPTH" != "0" ]; then
    clone_args+=(--depth "$GIT_CLONE_DEPTH")
  fi
  echo "repo-cloner: cloning $slug into $target"
  if GIT_TERMINAL_PROMPT=0 GIT_ASKPASS="$ASKPASS" GITHUB_TOKEN="$GITHUB_TOKEN" \
    git clone "${clone_args[@]}" "https://github.com/${slug}.git" "$tmp_target"; then
    mv "$tmp_target" "$target"
    git -C "$target" config --local credential.helper ""
    set_repo_state "$slug" "cloned" "$target"
  else
    rc=$?
    rm -rf "$tmp_target"
    msg="git clone failed with exit code $rc"
    set_repo_state "$slug" "failed" "$target" "$msg"
    failures=1
  fi
done

if [ "$failures" -ne 0 ]; then
  echo "repo-cloner: one or more repositories failed to clone" >&2
  exit 1
fi

echo "repo-cloner: cloned ${#REPOS[@]} repos"
