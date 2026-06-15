#!/bin/sh
set -eu

# Per-session git setup, mode-aware. Called from the claude/codex runner-launch
# scripts once per pod lifetime.
#
#   restricted   (TANK_RESTRICTED_GIT truthy): install the governed git hook
#                templates (post-commit auto-publish + pre-push block). Direct
#                pushes stay blocked; publishing goes through Tank MCP.
#   unrestricted (default): install the auto-minting credential helper so the
#                agent has full, automatic git access (clone/fetch/push) with no
#                manual token handling.

restricted=false
case "$(printf '%s' "${TANK_RESTRICTED_GIT:-false}" | tr '[:upper:]' '[:lower:]')" in
  1|true|yes|on) restricted=true ;;
esac

install_restricted_hook_templates() {
  post_commit_src="${AGENT_POST_COMMIT_HOOK:-/opt/tank/session-config/agent-post-commit-hook.sh}"
  pre_push_src="${AGENT_PRE_PUSH_HOOK:-/opt/tank/session-config/agent-pre-push-hook.sh}"
  template_dir="${AGENT_GIT_TEMPLATE_DIR:-$HOME/.config/tank/git-template}"

  [ -f "$post_commit_src" ] || return 0

  mkdir -p "$template_dir/hooks"
  cp "$post_commit_src" "$template_dir/hooks/post-commit"
  chmod 755 "$template_dir/hooks/post-commit"
  if [ -f "$pre_push_src" ]; then
    cp "$pre_push_src" "$template_dir/hooks/pre-push"
    chmod 755 "$template_dir/hooks/pre-push"
  fi

  git config --global init.templateDir "$template_dir"
}

install_credential_helper() {
  helper_src="${AGENT_GIT_CREDENTIAL_HELPER_SRC:-/opt/tank/session-config/git-credential-tank.sh}"
  helper_dst="${AGENT_GIT_CREDENTIAL_HELPER_DST:-$HOME/.local/bin/git-credential-tank}"

  # ConfigMap-mounted scripts are read-only and non-executable; copy to a
  # writable path and mark executable so git can run it as a credential helper.
  [ -f "$helper_src" ] || return 0

  mkdir -p "$(dirname "$helper_dst")"
  cp "$helper_src" "$helper_dst"
  chmod 755 "$helper_dst"

  # useHttpPath lets the helper scope each minted token to the exact repo.
  git config --global credential.helper "$helper_dst"
  git config --global credential.useHttpPath true
}

if [ "$restricted" = "true" ]; then
  install_restricted_hook_templates
else
  install_credential_helper
fi
