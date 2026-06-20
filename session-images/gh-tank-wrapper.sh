#!/bin/sh
# Durable `gh` for Tank sessions.
#
# Installed at /usr/local/bin/gh — ahead of the real /usr/bin/gh on PATH — so it
# shadows gh. On each invocation it mints a fresh GitHub App token (scoped to the
# session's /workspace repos, plus any --repo/-R on the command line) via the
# in-pod mcp-github MCP and runs the real gh with it, so the agent never has to
# re-auth. Non-restricted sessions mint a full read/write token. Restricted
# sessions mint a READ-ONLY token (contents:read) so `gh` reads (pr view, run
# view, api, …) work without handing the shell a write credential — writes still
# go through the governed Tank MCP path and fail loudly via the real gh's 403.
set -u
REAL_GH="${TANK_REAL_GH:-/usr/bin/gh}"

# Restricted sessions mint read-only; non-restricted mint full read/write.
restricted=false
case "$(printf '%s' "${TANK_RESTRICTED_GIT:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on) restricted=true ;;
esac

# Honor an explicitly-provided token.
if [ -n "${GH_TOKEN:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
  exec "$REAL_GH" "$@"
fi

mcp_url="${TANK_GIT_CRED_MCP_URL:-http://127.0.0.1:9992/}"
auth_tok="$(cat "${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}" 2>/dev/null || true)"
[ -n "$auth_tok" ] || exec "$REAL_GH" "$@"

# Egress-proxy mode (restricted_git via the wall). api.github.com is pinned at the
# agent egress proxy, which exchanges this token, mints the App token server-side,
# records the action, and rejects merges. So hand gh the pod's RAW token and let the
# wall govern — no in-pod mint, no /create-session-pr or /pr-write brokering. gh (Go)
# reads SSL_CERT_FILE as its WHOLE root set, so trust the proxy leaf by combining the
# OS roots with the gateway CA (non-proxied hosts like uploads.github.com keep working).
case "$(printf '%s' "${TANK_GIT_EGRESS_PROXY:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on)
    _ca="${TANK_GIT_PROXY_CA:-/etc/oauth-gateway-ca/ca.crt}"
    _bundle="${TANK_GIT_PROXY_CA_BUNDLE:-$HOME/.config/tank/gh-ca-bundle.crt}"
    if [ ! -s "$_bundle" ] && [ -s "$_ca" ]; then
      mkdir -p "$(dirname "$_bundle")"
      if [ -s /etc/ssl/certs/ca-certificates.crt ]; then
        cat /etc/ssl/certs/ca-certificates.crt "$_ca" > "$_bundle" 2>/dev/null || cp "$_ca" "$_bundle"
      else
        cp "$_ca" "$_bundle"
      fi
    fi
    [ -s "$_bundle" ] && export SSL_CERT_FILE="$_bundle" GIT_SSL_CAINFO="$_bundle"
    export GH_TOKEN="$auth_tok"
    exec "$REAL_GH" "$@"
    ;;
esac

# Restricted-mode `gh pr create` on a Tank session branch is DELEGATED to the
# in-pod governed handler (the mcp-auth-proxy sidecar at :9999/create-session-pr).
# That handler holds the GitHub credential and opens the draft PR for the branch,
# so the agent shell never gets a write token — the same boundary as the
# read-only mint, extended to the one write the agent needs. Every other `gh`
# verb (pr view/edit/ready/merge, issue, …) falls through to the normal
# read-only / break-glass path below.
if [ "$restricted" = "true" ] && [ "${1:-}" = "pr" ] && [ "${2:-}" = "create" ]; then
  pr_top="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  pr_branch=""
  [ -n "$pr_top" ] && pr_branch="$(git -C "$pr_top" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
  case "$pr_branch" in
    tank/session/*)
      pr_title=""; pr_body=""; pr_base=""
      shift 2
      while [ "$#" -gt 0 ]; do
        case "$1" in
          -t|--title) pr_title="${2:-}"; shift 2 ;;
          --title=*) pr_title="${1#*=}"; shift ;;
          -b|--body) pr_body="${2:-}"; shift 2 ;;
          --body=*) pr_body="${1#*=}"; shift ;;
          -B|--base) pr_base="${2:-}"; shift 2 ;;
          --base=*) pr_base="${1#*=}"; shift ;;
          *) shift ;;
        esac
      done
      pr_payload="$(jq -nc --arg rp "$pr_top" --arg t "$pr_title" --arg b "$pr_body" --arg base "$pr_base" \
        '{repo_path:$rp, title:$t, body:$b, base:$base}')"
      pr_endpoint="${TANK_CREATE_SESSION_PR_URL:-http://127.0.0.1:9999/create-session-pr}"
      pr_resp="$(curl -sS -m 30 -H "Authorization: Bearer ${auth_tok}" -H "Content-Type: application/json" \
        -X POST "$pr_endpoint" -d "$pr_payload" 2>/dev/null || true)"
      pr_ok="$(printf '%s' "$pr_resp" | jq -r '.ok // false' 2>/dev/null || echo false)"
      pr_link="$(printf '%s' "$pr_resp" | jq -r '.pr_url // empty' 2>/dev/null || true)"
      if [ "$pr_ok" = "true" ] && [ -n "$pr_link" ]; then
        printf '%s\n' "$pr_link"
        exit 0
      fi
      pr_reason="$(printf '%s' "$pr_resp" | jq -r '.reason // "unknown error"' 2>/dev/null || echo "unknown error")"
      printf 'tank(gh): governed PR create failed: %s\n' "$pr_reason" >&2
      exit 1
      ;;
  esac
fi

# Token repo scope: the session's cloned repos under /workspace, plus an explicit
# `--repo owner/name` / `-R owner/name` if present on the command line.
repos=""
seen=" "
add_repo() {
  case "$1" in */*) : ;; *) return ;; esac
  case "$seen" in *" $1 "*) return ;; esac
  seen="$seen$1 "
  repos="$repos\"$1\","
}
for g in "${TANK_WORKSPACE_DIR:-/workspace}"/*/.git; do
  [ -e "$g" ] || continue
  d=${g%/.git}
  url="$(git -C "$d" remote get-url origin 2>/dev/null || true)"
  slug="$(printf '%s' "$url" | sed -E 's#^https://github\.com/##; s#^git@github\.com:##; s#\.git$##')"
  add_repo "$slug"
done
prev=""
for a in "$@"; do
  case "$prev" in --repo|-R) add_repo "$a" ;; esac
  prev="$a"
done
[ -n "$repos" ] || exec "$REAL_GH" "$@"
repos="[${repos%,}]"

# Break-glass elevation (restricted only): before the read-only mint, ask the
# in-pod break-glass server (:9999, the grant source of truth) whether an
# active, repo-covering, UNLIMITED-branch grant exists. If so it mints the App's
# FULL permission set and audits the use, so `gh pr edit`/`ready`/merge, issues,
# etc. work automatically while the grant is live. No qualifying grant ->
# {"active":false} and we keep the read-only default.
#
# FAIL LOUD, never silent: the mint endpoint answers `{"active":true,"token":…}`
# (elevate) or `{"active":false}` (genuine no-grant, stay read-only). Anything
# else — a JSON-RPC error like `{"error":{"code":-32600,...}}` (the symptom when
# the mcp-auth-proxy sidecar predates the /mint-git-token route and the POST
# falls through to the MCP catch-all), an HTTP error, a timeout, or any
# unrecognized shape — is reported to stderr instead of being silently
# collapsed to read-only. A silent downgrade here is exactly what made the
# :9999 sidecar-skew regression nearly impossible to diagnose, so it is part of
# the bug, not an acceptable fallback.
if [ "$restricted" = "true" ]; then
  bg_url="${TANK_BREAK_GLASS_MINT_URL:-http://127.0.0.1:9999/mint-git-token}"
  # Capture body and HTTP status together; the cold full-mint path does a Tank
  # grant lookup + GitHub App mint, so allow headroom rather than a tight
  # timeout that would masquerade as "no grant".
  bg_raw="$(curl -sS -m 8 -w 'HTTPSTATUS:%{http_code}' \
    -H "Authorization: Bearer ${auth_tok}" \
    -H "Content-Type: application/json" \
    -X POST "$bg_url" \
    -d "$(printf '{"repos":%s}' "$repos")" 2>/dev/null || printf 'HTTPSTATUS:000')"
  bg_code="${bg_raw##*HTTPSTATUS:}"
  bg_body="${bg_raw%HTTPSTATUS:*}"
  bg_token="$(printf '%s' "$bg_body" | jq -r 'select(.active==true) | .token // empty' 2>/dev/null | head -n1 || true)"
  if [ -n "$bg_token" ]; then
    export GH_TOKEN="$bg_token"
    exec "$REAL_GH" "$@"
  fi
  # No elevation token. Only a clean `{"active":false}` over HTTP 200 is the
  # expected, quiet no-grant case; everything else is surfaced loudly.
  if [ "$bg_code" != "200" ] || ! printf '%s' "$bg_body" | jq -e '.active == false' >/dev/null 2>&1; then
    printf 'tank(gh): break-glass elevation FAILED — POST %s returned an unexpected response (HTTP %s); falling back to a READ-ONLY token. If an active break-glass grant exists, gh/git writes WILL fail. Most likely the in-pod mcp-auth-proxy sidecar predates the /mint-git-token route (image/version skew) or the break-glass server errored. Response: %.300s\n' \
      "$bg_url" "$bg_code" "$bg_body" >&2
  fi

  # Branch-lane PR writes (restricted, scoped/no-grant only — an unlimited grant
  # already exec'd real gh above with a full token, so we never reach here for
  # it). There is no branch-scoped GitHub token, so a scoped grant cannot hand
  # the shell a credential that `gh pr create|edit|ready|comment` could use.
  # Instead Tank brokers the PR write server-side through /pr-write: it resolves
  # the PR to its head branch, verifies head ∈ lane scope, performs the write
  # with Tank's credential, and audits it. Only these four write subcommands are
  # intercepted; merge/close/view/list/checks/diff and every read stay on native
  # gh (which the read-only token below authenticates).
  if [ "${1:-}" = "pr" ]; then
    pr_sub="${2:-}"
    case "$pr_sub" in
      create|edit|ready|comment)
        # Parse the gh pr <sub> flags we map to /pr-write WITHOUT consuming the
        # script's positional params ($@) — on a no_grant fall-through the
        # original `gh pr …` invocation is re-run against native gh below, so it
        # must stay intact. We walk a copy via `for`, skipping `pr <sub>` and
        # tracking a one-arg-ahead state for value flags. Collected: --title/-t,
        # --body/-b, --base/-B, --head/-H (each takes a value) and the first bare
        # positional as the PR number (edit/ready/comment take <number|url|branch>).
        pw_title=""
        pw_body=""
        pw_base=""
        pw_head=""
        pw_number=""
        pw_pos=0      # how many leading positionals (pr, sub) we've skipped
        pw_want=""    # which flag's value the next arg supplies
        for pw_arg in "$@"; do
          if [ "$pw_pos" -lt 2 ]; then
            pw_pos=$((pw_pos + 1))
            continue
          fi
          if [ -n "$pw_want" ]; then
            case "$pw_want" in
              title) pw_title="$pw_arg" ;;
              body) pw_body="$pw_arg" ;;
              base) pw_base="$pw_arg" ;;
              head) pw_head="$pw_arg" ;;
            esac
            pw_want=""
            continue
          fi
          case "$pw_arg" in
            --title|-t) pw_want="title" ;;
            --title=*) pw_title="${pw_arg#*=}" ;;
            -t*) pw_title="${pw_arg#-t}" ;;
            --body|-b) pw_want="body" ;;
            --body=*) pw_body="${pw_arg#*=}" ;;
            -b*) pw_body="${pw_arg#-b}" ;;
            --base|-B) pw_want="base" ;;
            --base=*) pw_base="${pw_arg#*=}" ;;
            -B*) pw_base="${pw_arg#-B}" ;;
            --head|-H) pw_want="head" ;;
            --head=*) pw_head="${pw_arg#*=}" ;;
            -H*) pw_head="${pw_arg#-H}" ;;
            --) : ;;
            -*) : ;;
            *) [ -n "$pw_number" ] || pw_number="$pw_arg" ;;
          esac
        done

        # The PR-write endpoint operates on a single repo; use the first repo in
        # the resolved scope (the explicit --repo/-R if present, else the
        # workspace repo).
        pw_repo="$(printf '%s' "$repos" | jq -r '.[0] // empty' 2>/dev/null | head -n1 || true)"

        # Map the subcommand to the /pr-write action + required fields.
        #   create  -> open  (head defaults to the current branch)
        #   edit    -> edit  (title and/or body, on pr_number)
        #   ready   -> ready (on pr_number)
        #   comment -> comment (comment text from --body, on pr_number)
        pw_action=""
        case "$pr_sub" in
          create) pw_action="open" ;;
          edit) pw_action="edit" ;;
          ready) pw_action="ready" ;;
          comment) pw_action="comment" ;;
        esac
        if [ "$pw_action" = "open" ] && [ -z "$pw_head" ]; then
          pw_head="$(git symbolic-ref --short HEAD 2>/dev/null || true)"
        fi

        # Build the JSON body field-by-field so empty fields are omitted.
        pw_json="$(jq -nc \
          --arg repo "$pw_repo" \
          --arg action "$pw_action" \
          --arg number "$pw_number" \
          --arg head "$pw_head" \
          --arg base "$pw_base" \
          --arg title "$pw_title" \
          --arg body "$pw_body" \
          --arg comment "$pw_body" \
          '{repo:$repo, action:$action}
            + (if $number != "" then {pr_number: ($number|tonumber? // $number)} else {} end)
            + (if $head != "" then {head:$head} else {} end)
            + (if $base != "" then {base:$base} else {} end)
            + (if $title != "" then {title:$title} else {} end)
            + (if $action == "comment" then (if $comment != "" then {comment:$comment} else {} end)
               elif $body != "" then {body:$body} else {} end)' \
          2>/dev/null || true)"

        pw_url="${TANK_BREAK_GLASS_PR_WRITE_URL:-http://127.0.0.1:9999/pr-write}"
        pw_resp="$(curl -sS -m 30 \
          -H "Authorization: Bearer ${auth_tok}" \
          -H "Content-Type: application/json" \
          -X POST "$pw_url" \
          -d "$pw_json" 2>/dev/null || true)"
        pw_resp_ok="$(printf '%s' "$pw_resp" | jq -r '.ok // empty' 2>/dev/null | head -n1 || true)"
        if [ "$pw_resp_ok" = "true" ]; then
          printf '%s\n' "$(printf '%s' "$pw_resp" | jq -r '.pr_url // empty' 2>/dev/null | head -n1)"
          exit 0
        fi
        pw_reason="$(printf '%s' "$pw_resp" | jq -r '.reason // empty' 2>/dev/null | head -n1 || true)"
        if [ "$pw_reason" = "no_grant" ]; then
          # No branch-lane grant covers this PR write. Name the escalation tool
          # LOUDLY, then fall through to native gh so the user also sees gh's own
          # result (typically a 403 from the read-only token).
          printf 'tank(gh): no active break-glass grant covers this PR write — call the Tank MCP request_git_break_glass tool (once) to request a branch lane, then retry. Falling through to native gh.\n' >&2
        else
          # Any other ok:false (e.g. branch_out_of_scope) is a hard failure: the
          # governed path refused and there is no native fallback that would work.
          printf 'tank(gh): PR write refused by Tank (%s). Response: %.300s\n' "${pw_reason:-unknown}" "$pw_resp" >&2
          exit 1
        fi
        ;;
    esac
  fi

  mint_args="$(printf '{"repos":%s,"write":false,"workflows":false,"full":false}' "$repos")"
else
  mint_args="$(printf '{"repos":%s,"full":true,"write":true,"workflows":true}' "$repos")"
fi
req="$(printf '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mint_clone_token","arguments":%s}}' "$mint_args")"
resp="$(curl -sS -m 25 \
  -H "Authorization: Bearer ${auth_tok}" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -X POST "$mcp_url" -d "$req" 2>/dev/null || true)"
token="$(printf '%s\n' "$resp" | sed -n 's/^data: //p' | jq -r '.result.structuredContent.token // empty' 2>/dev/null | head -n1)"
[ -n "$token" ] || token="$(printf '%s' "$resp" | jq -r '.result.structuredContent.token // empty' 2>/dev/null || true)"
[ -n "$token" ] && export GH_TOKEN="$token"

exec "$REAL_GH" "$@"
