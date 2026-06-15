#!/usr/bin/env bash
set -euo pipefail

WORKSPACE="${WORKSPACE:-/workspace}"
REPOS_JSON="${TANK_REPOS_JSON:-[]}"
AUTH_TOKEN_PATH="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
AUTH_EXCHANGE_URL="${AUTH_ROMAINE_EXCHANGE_URL:-https://auth.romaine.life/api/auth/exchange/k8s}"
MCP_GITHUB_URL="${MCP_GITHUB_URL:-http://mcp-github.mcp-github.svc:80}"
TANK_OPERATOR_INTERNAL_URL="${TANK_OPERATOR_INTERNAL_URL:-http://tank-operator.tank-operator.svc.cluster.local}"
GIT_CLONE_DEPTH="${GIT_CLONE_DEPTH:-50}"
TANK_RESTRICTED_GIT="${TANK_RESTRICTED_GIT:-false}"

REPOS_JSON="$(printf '%s' "$REPOS_JSON" | jq -c '[.[] | select(type == "string")]')"
if [ "$(jq 'length' <<<"$REPOS_JSON")" -eq 0 ]; then
  echo "repo-cloner: no repositories selected"
  exit 0
fi

mapfile -t REPOS < <(jq -r '.[]' <<<"$REPOS_JSON")
restricted_git=false
case "$(printf '%s' "$TANK_RESTRICTED_GIT" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on) restricted_git=true ;;
esac

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

post_control_action() {
  local payload="$1"
  if [ -z "${AUTH_TOKEN:-}" ] || [ -z "${SESSION_ID:-}" ]; then
    return 0
  fi
  if ! curl -fsS -X POST \
    "${TANK_OPERATOR_INTERNAL_URL%/}/api/internal/sessions/${SESSION_ID}/control-actions" \
    -H "Authorization: Bearer ${AUTH_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$payload" >/dev/null; then
    echo "repo-cloner: warning: failed to publish control action" >&2
  fi
}

post_pull_request_link() {
  local pr_url="$1"
  if [ -z "${AUTH_TOKEN:-}" ] || [ -z "${SESSION_ID:-}" ] || [ -z "$pr_url" ]; then
    return 0
  fi
  if ! curl -fsS -X POST \
    "${TANK_OPERATOR_INTERNAL_URL%/}/api/internal/sessions/${SESSION_ID}/pull-request-link" \
    -H "Authorization: Bearer ${AUTH_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$(jq -nc --arg url "$pr_url" '{"url": $url}')" >/dev/null; then
    echo "repo-cloner: warning: failed to publish pull request link" >&2
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

install_repo_agent_reminder() {
  local slug="$1"
  local target="$2"
  local strict="$3"
  local post_commit_src="${AGENT_POST_COMMIT_HOOK:-/opt/tank/session-config/agent-post-commit-hook.sh}"
  local pre_push_src="${AGENT_PRE_PUSH_HOOK:-/opt/tank/session-config/agent-pre-push-hook.sh}"
  local git_dir
  local hook_dst

  [ -f "$post_commit_src" ] || return 0

  echo "repo-cloner: installing agent git reminders for $slug"
  git_dir="$(git -C "$target" rev-parse --absolute-git-dir)"
  hook_dst="$git_dir/hooks/post-commit"
  if [ -e "$hook_dst" ] && ! cmp -s "$post_commit_src" "$hook_dst"; then
    if [ "$strict" = "strict" ]; then
      echo "repo-cloner: refusing to replace existing post-commit hook for $slug: $hook_dst" >&2
      return 1
    fi
    echo "repo-cloner: warning: leaving existing post-commit hook for $slug: $hook_dst" >&2
    return 0
  fi

  if mkdir -p "$(dirname "$hook_dst")" &&
    cp "$post_commit_src" "$hook_dst" &&
    chmod 755 "$hook_dst"; then
    if [ -f "$pre_push_src" ]; then
      cp "$pre_push_src" "$git_dir/hooks/pre-push" &&
        chmod 755 "$git_dir/hooks/pre-push"
    fi
    return 0
  fi

  if [ "$strict" = "strict" ]; then
    echo "repo-cloner: failed to install agent post-commit reminder for $slug" >&2
    return 1
  fi

  echo "repo-cloner: warning: failed to install agent post-commit reminder for existing repo $slug" >&2
  return 0
}

exchange_auth_token() {
  local attempt
  local token
  for attempt in 1 2 3 4 5; do
    token="$(
      curl -fsS -X POST "$AUTH_EXCHANGE_URL" \
        -H "Authorization: Bearer $(cat "$AUTH_TOKEN_PATH")" \
        -H "Content-Type: application/json" \
        -d '{}' | jq -r '.token // empty'
    )" && {
      if [ -n "$token" ]; then
        printf '%s\n' "$token"
        return 0
      fi
    }
    echo "repo-cloner: auth exchange attempt ${attempt}/5 failed" >&2
    sleep "$attempt"
  done
  return 1
}

create_session_branch_pr() {
  local slug="$1"
  local target="$2"
  local repo_owner="${slug%%/*}"
  local repo_name="${slug#*/}"
  local branch="tank/session/${SESSION_ID}/${repo_name}"
  local base_branch
  local sha
  local pr_json
  local pr_url
  local pr_number
  local title
  local body
  local rpc_payload
  local mcp_response
  local mcp_json

  if [ -z "${SESSION_ID:-}" ]; then
    echo "repo-cloner: SESSION_ID unavailable; skipping governed branch PR for $slug" >&2
    return 0
  fi

  base_branch="$(git -C "$target" symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##')"
  if [ -z "$base_branch" ]; then
    base_branch="$(git -C "$target" branch --show-current)"
  fi
  if [ -z "$base_branch" ]; then
    echo "repo-cloner: cannot determine base branch for $slug" >&2
    return 1
  fi

  echo "repo-cloner: creating governed session branch $branch for $slug"
  git -C "$target" checkout -B "$branch" "origin/$base_branch"
  git -C "$target" \
    -c user.name="Tank Operator" \
    -c user.email="tank-operator@romaine.life" \
    -c core.hooksPath=/dev/null \
    commit --allow-empty -m "Tank session ${SESSION_ID} start"
  GIT_TERMINAL_PROMPT=0 GIT_ASKPASS="$ASKPASS" GITHUB_TOKEN="$GITHUB_TOKEN" \
    git -C "$target" push --no-verify origin "HEAD:refs/heads/${branch}"
  git -C "$target" config --local branch."$branch".remote origin
  git -C "$target" config --local branch."$branch".merge "refs/heads/${branch}"

  sha="$(git -C "$target" rev-parse HEAD)"
  title="Tank session ${SESSION_ID}: ${repo_name}"
  body="Draft PR automatically opened by Tank for session ${SESSION_ID}. Every agent commit on ${branch} is published through Tank so CI and mergeability can be tracked per commit."
  rpc_payload="$(
    jq -nc \
      --arg owner "$repo_owner" \
      --arg name "$repo_name" \
      --arg title "$title" \
      --arg head "$branch" \
      --arg base "$base_branch" \
      --arg body "$body" \
      '{
        jsonrpc: "2.0",
        id: 2,
        method: "tools/call",
        params: {
          name: "create_pull_request",
          arguments: {
            owner: $owner,
            name: $name,
            title: $title,
            head: $head,
            base: $base,
            body: $body,
            draft: true
          }
        }
      }'
  )"
  if ! mcp_response="$(
    curl -fsS -X POST "${MCP_GITHUB_URL%/}/" \
      -H "Authorization: Bearer ${AUTH_TOKEN}" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json, text/event-stream" \
      -d "$rpc_payload"
  )"; then
    echo "repo-cloner: failed to create session PR for $slug through mcp-github" >&2
    return 1
  fi
  mcp_json="$(printf '%s\n' "$mcp_response" | sed -n 's/^data: //p' | head -n1)"
  if [ -z "$mcp_json" ]; then
    mcp_json="$mcp_response"
  fi
  if [ "$(jq -r 'has("error")' <<<"$mcp_json")" = "true" ]; then
    echo "repo-cloner: mcp-github create_pull_request failed: $(jq -r '.error.message // "unknown error"' <<<"$mcp_json")" >&2
    return 1
  fi
  pr_url="$(jq -r '.result.structuredContent.html_url // .result.structuredContent.url // empty' <<<"$mcp_json")"
  pr_number="$(jq -r '.result.structuredContent.number // empty' <<<"$mcp_json")"
  if [ -z "$pr_url" ]; then
    pr_url="$(jq -r '.. | strings | capture("(?<url>https://github[.]com/[^[:space:]]+/pull/[0-9]+)").url? // empty' <<<"$mcp_json" | head -n1)"
  fi
  if [ -z "$pr_number" ] && [ -n "$pr_url" ]; then
    pr_number="${pr_url##*/}"
  fi
  if [ -z "$pr_url" ] || [ -z "$pr_number" ]; then
    echo "repo-cloner: mcp-github create_pull_request returned no PR URL/number for $slug" >&2
    return 1
  fi
  if [ -n "$pr_url" ]; then
    post_pull_request_link "$pr_url"
  fi
  if [ -n "$pr_url" ] && [ -n "$pr_number" ]; then
    post_control_action "$(
      jq -nc \
        --arg event_id "repo-cloner-pr-${SESSION_ID}-${repo_owner}-${repo_name}" \
        --arg invocation_id "repo-cloner-pr-${SESSION_ID}-${repo_owner}-${repo_name}" \
        --arg repo_owner "$repo_owner" \
        --arg repo_name "$repo_name" \
        --arg pr_url "$pr_url" \
        --arg base "$base_branch" \
        --arg head "$branch" \
        --arg sha "$sha" \
        --argjson pr_number "$pr_number" \
        '{
          event_id: $event_id,
          invocation_id: $invocation_id,
          source_service: "mcp-tank-operator",
          source_tool: "session_repo_prepare",
          action: "github.pull_request.open",
          status: "succeeded",
          target_kind: "github_pull_request",
          target_ref: $pr_url,
          repo_owner: $repo_owner,
          repo_name: $repo_name,
          pr_number: $pr_number,
          result_sha: $sha,
          payload: {base: $base, head: $head, draft: true}
        }'
    )"
  fi
}

echo "repo-cloner: exchanging pod identity"
AUTH_TOKEN="$(exchange_auth_token || true)"
if [ -z "$AUTH_TOKEN" ]; then
	echo "repo-cloner: auth.romaine.life exchange returned no token" >&2
	exit 1
fi

post_clone_state

RPC_PAYLOAD="$(
  jq -nc --argjson repos "$REPOS_JSON" --argjson write "$restricted_git" '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "mint_clone_token",
      "arguments": {"repos": $repos, "write": $write}
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
      if [ "$restricted_git" = "true" ]; then
        install_repo_agent_reminder "$slug" "$target" "best-effort"
        if ! create_session_branch_pr "$slug" "$target"; then
          msg="governed session branch PR setup failed"
          set_repo_state "$slug" "failed" "$target" "$msg"
          failures=1
          continue
        fi
      fi
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
    if [ "$restricted_git" = "true" ]; then
      # Scrub the local credential helper so the one-shot clone token cannot be
      # reused and pushes must go through the governed Tank MCP publish path.
      # Non-restricted clones intentionally keep no local override, so they
      # inherit the global auto-minting credential helper (full git access).
      git -C "$target" config --local credential.helper ""
      if ! create_session_branch_pr "$slug" "$target"; then
        msg="governed session branch PR setup failed"
        set_repo_state "$slug" "failed" "$target" "$msg"
        failures=1
        continue
      fi
      if ! install_repo_agent_reminder "$slug" "$target" "strict"; then
        msg="agent post-commit reminder install failed"
        set_repo_state "$slug" "failed" "$target" "$msg"
        failures=1
        continue
      fi
    fi
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
