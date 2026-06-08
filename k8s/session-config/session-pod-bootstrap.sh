#!/bin/bash
# Session-pod bootstrap. Runs once at pod boot, before sandbox-agent's HTTP
# listener comes up, so any state the in-browser CLI shell (or a later
# `codex` / `claude` invocation) depends on is on disk before the
# user can issue commands.
#
# Required env: TANK_SESSION_MODE (set on the claude container by the
# orchestrator at pod creation; see sessionmodel.go).
#
# This is the migration target for the Python `tank-bootstrap.sh` that
# 650c282 deleted as "dead" — it was not dead, it was the bootstrap.
# The cli-process 400 it caused was patched in #637; this script
# restores the per-mode seeding that broke at the same time.

set -e

start_spirelens_tailnet() {
  if [ "${SPIRELENS_MCP_ENABLED:-}" != "true" ]; then
    return 0
  fi

  for cmd in curl jq tailscaled tailscale; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      echo "session-pod-bootstrap: SpireLens MCP requested but $cmd is not installed" >&2
      return 1
    fi
  done

  token_path="${AUTH_ROMAINE_TOKEN_PATH:-/var/run/secrets/auth.romaine.life/token}"
  oidc_client_id="${SPIRELENS_TAILSCALE_OIDC_CLIENT_ID:-}"
  tailnet="${SPIRELENS_TAILSCALE_TAILNET:-}"
  auth_tag="${SPIRELENS_TAILSCALE_AUTH_TAG:-tag:spirelens-orchestrator}"
  socket="${SPIRELENS_TAILSCALE_SOCKET:-/tmp/tailscaled.sock}"
  state_dir="${SPIRELENS_TAILSCALE_STATE_DIR:-/workspace/.tailscale-state}"
  proxy_listen="${SPIRELENS_TAILSCALE_OUTBOUND_HTTP_PROXY_LISTEN:-127.0.0.1:1055}"
  hostname="${SPIRELENS_TAILSCALE_HOSTNAME:-tank-session}"
  expiry="${SPIRELENS_TAILSCALE_AUTHKEY_EXPIRY_SECONDS:-3600}"
  auth_url="${AUTH_ROMAINE_URL:-https://auth.romaine.life}"

  case "$expiry" in
    ""|*[!0-9]*) expiry=3600 ;;
  esac

  if [ ! -r "$token_path" ] || [ -z "$oidc_client_id" ] || [ -z "$tailnet" ]; then
    echo "session-pod-bootstrap: SpireLens MCP tailnet config is incomplete" >&2
    return 1
  fi

  mkdir -p "$state_dir"
  if ! tailscale --socket="$socket" status >/dev/null 2>&1; then
    tailscaled \
      --tun=userspace-networking \
      --statedir="$state_dir" \
      --socket="$socket" \
      --outbound-http-proxy-listen="$proxy_listen" \
      >/tmp/tailscaled-spirelens.log 2>&1 &

    i=0
    while [ "$i" -lt 50 ] && [ ! -e "$socket" ]; do
      i=$((i + 1))
      sleep 0.1
    done
    if [ ! -e "$socket" ]; then
      echo "session-pod-bootstrap: tailscaled did not create socket $socket" >&2
      return 1
    fi
  fi

  pod_token="$(cat "$token_path")"
  federation_jwt="$(
    curl -fsS -X POST "${auth_url%/}/api/auth/exchange/federation" \
      -H "Authorization: Bearer $pod_token" \
      -H "Content-Type: application/json" \
      -d "$(jq -nc --arg audience "api.tailscale.com/$oidc_client_id" '{audience:$audience}')" \
      | jq -r '.token // empty'
  )"
  if [ -z "$federation_jwt" ]; then
    echo "session-pod-bootstrap: auth.romaine.life federation exchange returned no token" >&2
    return 1
  fi

  tailscale_access_token="$(
    curl -fsS -X POST "https://api.tailscale.com/api/v2/oauth/token-exchange" \
      -H "Content-Type: application/x-www-form-urlencoded" \
      --data-urlencode "client_id=$oidc_client_id" \
      --data-urlencode "jwt=$federation_jwt" \
      | jq -r '.access_token // empty'
  )"
  if [ -z "$tailscale_access_token" ]; then
    echo "session-pod-bootstrap: Tailscale token exchange returned no access_token" >&2
    return 1
  fi

  auth_key="$(
    curl -fsS -X POST "https://api.tailscale.com/api/v2/tailnet/$tailnet/keys" \
      -H "Authorization: Bearer $tailscale_access_token" \
      -H "Content-Type: application/json" \
      -d "$(jq -nc --arg tag "$auth_tag" --argjson expiry "$expiry" '{capabilities:{devices:{create:{ephemeral:true,preauthorized:true,reusable:false,tags:[$tag]}}},expirySeconds:$expiry}')" \
      | jq -r '.key // empty'
  )"
  if [ -z "$auth_key" ]; then
    echo "session-pod-bootstrap: Tailscale key mint returned no key" >&2
    return 1
  fi

  tailscale --socket="$socket" up \
    --authkey="$auth_key" \
    --hostname="$hostname" \
    --accept-routes=false \
    --accept-dns=false
  echo "session-pod-bootstrap: SpireLens tailnet join complete" >&2
}

install_agent_git_template() {
  script="${INSTALL_AGENT_GIT_TEMPLATE_SCRIPT:-/opt/tank/session-config/install-agent-git-template.sh}"
  if [ -f "$script" ]; then
    sh "$script" || true
  fi
}

mode="${TANK_SESSION_MODE:-}"
if [ -z "$mode" ]; then
  echo "session-pod-bootstrap: TANK_SESSION_MODE unset; nothing to seed" >&2
  exit 0
fi

start_spirelens_tailnet
install_agent_git_template

case "$mode" in
  codex_config | codex_cli | codex_gui | codex_exec_gui | codex_app_server)
    mkdir -p "$HOME/.codex"
    # cli_auth_credentials_store=file forces the file-backed store.
    # Codex defaults to the OS keychain, which doesn't exist in a
    # Linux container — without this, `codex login` either fails or
    # writes to a location codex can't read back. The save-credentials
    # button reads $HOME/.codex/auth.json out of the pod, so this
    # setting is load-bearing for the credentials wizard.
    cat > "$HOME/.codex/config.toml" <<'TOML'
cli_auth_credentials_store = "file"

[projects."/workspace"]
trust_level = "trusted"

[tui]
notifications = true
notification_condition = "always"
notification_method = "bel"
TOML
    ;;
  config)
    # Minimal seeds for the claude credentials-refresh wizard. The
    # save-credentials button later reads $HOME/.claude/.credentials.json
    # out of the pod — we deliberately do not pre-seed that file; the
    # user must complete /login.
    mkdir -p "$HOME/.claude"
    cat > "$HOME/.claude/settings.json" <<'JSON'
{"theme":"dark"}
JSON
    cat > "$HOME/.claude.json" <<'JSON'
{"hasCompletedOnboarding": true}
JSON
    ;;
esac
