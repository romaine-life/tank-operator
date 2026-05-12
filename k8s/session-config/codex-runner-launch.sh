#!/bin/sh
# Pod-side launch shim for the codex-runner Node service. Sibling of
# agent-runner-launch.sh — same shape, codex-specific credential setup
# instead of claude-specific.
#
# Codex auth path: the chart mounts ~/.codex/auth.json from the
# codex-credentials Secret (subscription OAuth tokens obtained from a
# one-time interactive `codex login` on the user's machine). The
# codex CLI subprocess that @openai/codex-sdk spawns reads that file
# directly; we don't touch the tokens here. Token refresh happens
# inside the CLI subprocess at use time.
#
# Why bash + exec node: same reason as agent-runner-launch.sh — the
# credential blob shape is small but platform-specific, and keeping it
# in shell matches the existing per-turn pattern (headless-run.sh)
# pieces of which still run for non-codex_gui codex modes.

set -eu

configure_codex() {
  mkdir -p "$HOME/.codex"

  # The codex CLI reads ~/.codex/auth.json. The chart mounts the
  # codex-credentials Secret at /etc/codex-creds (same volume the
  # legacy headless-run.sh uses). Copy + chmod 600 — not symlink,
  # because the CLI rewrites this file on token refresh and the
  # secret mount is read-only tmpfs. The rewritten file is lost on
  # pod restart; that's fine, the cached refresh_token in the secret
  # is still valid for the next pod.
  if [ ! -f /etc/codex-creds/auth.json ]; then
    echo "no codex credentials found in /etc/codex-creds/auth.json" >&2
    echo "spawn a 'Codex config' session and save credentials first." >&2
    exit 78
  fi
  cp /etc/codex-creds/auth.json "$HOME/.codex/auth.json"
  chmod 600 "$HOME/.codex/auth.json"

  # config.toml — codex CLI reads this for non-credential settings
  # (model, sandbox mode, mcp servers). Keep it minimal; the SPA's
  # per-turn TurnOptions can override most things via SDK turn config.
  mcp_block=""
  if [ -f /workspace/.mcp.json ]; then
    # codex's mcp config lives in config.toml under [mcp_servers.<name>]
    # blocks. Generate them from the same .mcp.json the claude path uses
    # so both runtimes see the same MCP surface.
    mcp_block="$(jq -r '
      .mcpServers | to_entries[] |
      "[mcp_servers." + .key + "]\nurl = \"" + (.value.url // "") + "\""
    ' /workspace/.mcp.json 2>/dev/null || true)"
  fi

  cat > "$HOME/.codex/config.toml" <<EOF
# Generated at pod start by codex-runner-launch.sh. Do not hand-edit;
# changes will be overwritten on the next pod boot.
sandbox_mode = "danger-full-access"
approval_policy = "never"

${mcp_block}
EOF

  unset OPENAI_API_KEY
  unset CODEX_API_KEY
}

# Git identity for any commits the agent makes from /workspace.
git config --global user.name "tank-operator-codex[bot]"
git config --global user.email "tank-operator-codex@romaine.life"

configure_codex

# Optional: install tank-flavored skills into the agent's home dir.
# Same script the per-turn path uses; idempotent.
if [ -f /opt/tank/session-config/install-tank-skills.sh ]; then
  sh /opt/tank/session-config/install-tank-skills.sh || true
fi

# Hand off to the runner. Node is PID 1 from this point — SIGTERM goes
# straight to it, which propagates AbortSignal to any in-flight codex
# turn via TurnOptions.signal.
exec node /opt/codex-runner/dist/index.js
