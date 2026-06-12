#!/bin/sh
set -eu

post_commit_src="${AGENT_POST_COMMIT_HOOK:-/opt/tank/session-config/agent-post-commit-hook.sh}"
pre_push_src="${AGENT_PRE_PUSH_HOOK:-/opt/tank/session-config/agent-pre-push-hook.sh}"
template_dir="${AGENT_GIT_TEMPLATE_DIR:-$HOME/.config/tank/git-template}"

[ -f "$post_commit_src" ] || exit 0

mkdir -p "$template_dir/hooks"
cp "$post_commit_src" "$template_dir/hooks/post-commit"
chmod 755 "$template_dir/hooks/post-commit"
if [ -f "$pre_push_src" ]; then
  cp "$pre_push_src" "$template_dir/hooks/pre-push"
  chmod 755 "$template_dir/hooks/pre-push"
fi

git config --global init.templateDir "$template_dir"
