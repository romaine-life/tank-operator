#!/bin/sh
set -eu

# Per-session git setup, mode-aware. Called from the claude/codex runner-launch
# scripts once per pod lifetime.
#
#   restricted   (TANK_RESTRICTED_GIT truthy): install the governed git hook
#                templates (post-commit auto-publish + pre-push block) AND the
#                mode-aware credential helper, which mints READ-ONLY tokens in
#                this mode. Direct pushes stay blocked; reads (clone/fetch/pull)
#                work; publishing goes through Tank MCP. The elevated cluster
#                kubeconfig stays non-restricted-only.
#   unrestricted (default): install the auto-minting credential helper so the
#                agent has full, automatic git access (clone/fetch/push) with no
#                manual token handling, plus the elevated cluster kubeconfig.

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

install_cluster_kubeconfig() {
  # Non-restricted sessions get a kubeconfig whose credential comes from an exec
  # plugin that mints a trusted (cluster-admin) SA token on demand. The pod's own
  # SA stays read-only; only this kubeconfig elevates kubectl. It is NOT written
  # for restricted sessions, so their kubectl falls back to the read-only pod SA.
  plugin_src="${AGENT_KUBECTL_CREDENTIAL_PLUGIN_SRC:-/opt/tank/session-config/kubectl-credential-tank.sh}"
  plugin_dst="${AGENT_KUBECTL_CREDENTIAL_PLUGIN_DST:-$HOME/.local/bin/kubectl-credential-tank}"
  kubeconfig="${AGENT_KUBECONFIG_PATH:-$HOME/.kube/config}"
  ca_path="${AGENT_KUBE_CA_PATH:-/var/run/secrets/kubernetes.io/serviceaccount/ca.crt}"
  api_host="${KUBERNETES_SERVICE_HOST:-kubernetes.default.svc}"
  api_port="${KUBERNETES_SERVICE_PORT:-443}"

  [ -f "$plugin_src" ] || return 0
  [ -f "$ca_path" ] || return 0   # not running in-cluster; nothing to wire

  mkdir -p "$(dirname "$plugin_dst")"
  cp "$plugin_src" "$plugin_dst"
  chmod 755 "$plugin_dst"

  mkdir -p "$(dirname "$kubeconfig")"
  cat > "$kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: tank
    cluster:
      server: https://${api_host}:${api_port}
      certificate-authority: ${ca_path}
contexts:
  - name: tank
    context:
      cluster: tank
      user: tank
current-context: tank
users:
  - name: tank
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: ${plugin_dst}
        interactiveMode: Never
EOF
}

if [ "$restricted" = "true" ]; then
  install_restricted_hook_templates
  # The credential helper is mode-aware: in restricted mode it mints a
  # read-only token, so clone/fetch/pull work for reads. Writes stay governed
  # (pre-push hook blocks pushes; a read-only token cannot push). The elevated
  # cluster kubeconfig is intentionally NOT installed here.
  install_credential_helper
else
  install_credential_helper
  install_cluster_kubeconfig
fi
