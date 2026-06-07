#!/bin/sh
set -eu

hook_src="${AGENT_POST_COMMIT_HOOK:-/opt/tank/session-config/agent-post-commit-hook.sh}"
template_dir="${AGENT_GIT_TEMPLATE_DIR:-$HOME/.config/tank/git-template}"
hook_dst="$template_dir/hooks/post-commit"

[ -f "$hook_src" ] || exit 0

mkdir -p "$(dirname "$hook_dst")"
cp "$hook_src" "$hook_dst"
chmod 755 "$hook_dst"

git config --global init.templateDir "$template_dir"
