#!/bin/sh
# Tank agent pre-push hook (POSIX sh, no bashisms).
#
# Direct `git push` does not hand a raw GitHub credential to the shell in a
# Tank restricted session. Instead, when a branch-lane break-glass grant is
# active, this hook brokers the push through Tank's in-pod break-glass server
# (:9999) — the single source of truth for grants — which enforces the branch
# scope server-side and pushes with Tank's own credential. There are two shapes
# of grant:
#
#   - UNLIMITED grant: the whole-repo escape hatch. The credential helper can
#     mint the App's full token, so a native `git push` works directly. We
#     detect that here (via /mint-git-token) and simply `exit 0` to let git's
#     own push proceed with the full token the credential helper provides.
#   - Branch-scoped grant: there is no branch-scoped GitHub token, so Tank
#     brokers the push server-side via /push-head. On success the commits are
#     already on the remote; git's subsequent native push is doomed (it has no
#     write credential) and we `exit 1` to stop it — after telling the user the
#     governed push already SUCCEEDED so git's "failed to push" line is expected.
#
# With no active grant (the normal Tank mode, and restricted-no-grant) we
# preserve the historical block byte-for-byte and `exit 1`, keeping GitHub write
# credentials inside Tank-controlled MCP/server paths.

# Break-glass server endpoints (overridable for tests; defaults match the pod).
BREAK_GLASS_MINT_URL="${TANK_BREAK_GLASS_MINT_URL:-http://127.0.0.1:9999/mint-git-token}"
PUSH_HEAD_URL="${TANK_BREAK_GLASS_PUSH_HEAD_URL:-http://127.0.0.1:9999/push-head}"

# Resolve repo root, current branch, and origin slug. Any failure here just
# falls through to the preserved no-grant block below.
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
branch="$(git symbolic-ref --short HEAD 2>/dev/null || true)"
origin_url="$(git remote get-url origin 2>/dev/null || true)"
slug="$(printf '%s' "$origin_url" | sed -E 's#^https://github\.com/##; s#^git@github\.com:##; s#\.git$##')"
auth_tok="$(cat "${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}" 2>/dev/null || true)"

# Only attempt the governed paths when we have the inputs they need. Without a
# repo root + branch + a slug shaped like owner/name, fall through.
have_inputs=false
case "$slug" in
  */*)
    if [ -n "$repo_root" ] && [ -n "$branch" ]; then
      have_inputs=true
    fi
    ;;
esac

if [ "$have_inputs" = "true" ]; then
  # 1) UNLIMITED grant probe. If /mint-git-token returns a token, the whole-repo
  #    escape hatch is live and the credential helper can hand git a full token,
  #    so let the native push proceed.
  bg_token="$(curl -sS -m 8 \
    -H "Authorization: Bearer ${auth_tok}" \
    -H "Content-Type: application/json" \
    -X POST "$BREAK_GLASS_MINT_URL" \
    -d "$(printf '{"repos":["%s"]}' "$slug")" 2>/dev/null \
    | jq -r 'select(.active==true) | .token // empty' 2>/dev/null | head -n1 || true)"
  if [ -n "$bg_token" ]; then
    printf 'tank(pre-push): unlimited break-glass grant active — allowing native push.\n'
    exit 0
  fi

  # 2) Branch-scoped grant. Ask Tank to perform the governed push server-side.
  #    Tank derives the current branch + HEAD from repo_path, checks the durable
  #    grant (push op + branch scope), and pushes (creating the remote branch if
  #    absent).
  ph_body="$(printf '{"repo_path":"%s","repo":"%s"}' "$repo_root" "$slug")"
  ph_resp="$(curl -sS -m 30 \
    -H "Authorization: Bearer ${auth_tok}" \
    -H "Content-Type: application/json" \
    -X POST "$PUSH_HEAD_URL" \
    -d "$ph_body" 2>/dev/null || true)"
  ph_ok="$(printf '%s' "$ph_resp" | jq -r '.ok // empty' 2>/dev/null | head -n1 || true)"

  if [ "$ph_ok" = "true" ]; then
    ph_branch="$(printf '%s' "$ph_resp" | jq -r '.branch // empty' 2>/dev/null | head -n1 || true)"
    ph_sha="$(printf '%s' "$ph_resp" | jq -r '.sha // empty' 2>/dev/null | head -n1 || true)"
    [ -n "$ph_branch" ] || ph_branch="$branch"
    cat >&2 <<EOF

[tank-agent] Push SUCCEEDED via Tank's governed branch-lane path.
  repo:   $slug
  branch: $ph_branch
  commit: $ph_sha
Your commits ARE on the remote. The "failed to push some refs" line git prints
next is EXPECTED: a branch-scoped grant pushes through Tank's audited server-side
path, not a raw token handed to the shell, so git's own push has no credential
and cannot complete. Nothing further is needed — the governed push already ran.

EOF
    exit 1
  fi

  ph_reason="$(printf '%s' "$ph_resp" | jq -r '.reason // empty' 2>/dev/null | head -n1 || true)"
  if [ "$ph_reason" = "branch_out_of_scope" ]; then
    cat >&2 <<EOF

[tank-agent] Direct git push is disabled: the active break-glass grant does not
cover this branch.
  repo:   $slug
  branch: $branch
The grant you were approved for bounds which branches you may push. Re-request a
break-glass grant whose branch scope includes "$branch" (Tank MCP
request_git_break_glass) and have it approved, then push again.

EOF
    exit 1
  fi
  # Any other ok:false (including no_grant) falls through to the preserved block.
fi

cat <<'EOF'

[tank-agent] Direct git push is disabled in Tank normal mode.

Tank owns the session branch, PR, GitHub write credential, and CI watcher.
Commits are auto-published by the post-commit hook. To retry explicitly, call
the Tank MCP publish_current_head tool for this repo.

Break-glass full-token access must be requested through the Tank MCP
request_git_break_glass approval flow; do not bypass this with raw GitHub
tokens. If approval has been granted, call request_git_break_glass again to
activate the separate tank-git-break-glass MCP server for this session/repo.

EOF
exit 1
