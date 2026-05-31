#!/bin/bash
# Session-pod bootstrap. Runs once at pod boot, before sandbox-agent's HTTP
# listener comes up, so any state the in-browser CLI shell (or a later
# `codex` / `claude` / `gemini` invocation) depends on is on disk before the
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

mode="${TANK_SESSION_MODE:-}"
if [ -z "$mode" ]; then
  echo "session-pod-bootstrap: TANK_SESSION_MODE unset; nothing to seed" >&2
  exit 0
fi

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
  gemini_gui | gemini_config | gemini_test)
    mkdir -p "$HOME/.gemini"
    cat > "$HOME/.gemini/settings.json" <<'JSON'
{
  "security": {
    "auth": {
      "selectedType": "oauth-personal"
    }
  }
}
JSON
    chmod 600 "$HOME/.gemini/settings.json"
    if [ -f /etc/gemini-credentials/oauth_creds.json ]; then
      cp /etc/gemini-credentials/oauth_creds.json "$HOME/.gemini/oauth_creds.json"
      chmod 600 "$HOME/.gemini/oauth_creds.json"
    fi
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
