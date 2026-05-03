#!/bin/bash
# Session-pod bootstrap, exec'd by the tank-operator orchestrator over
# the kubectl-exec WebSocket. Reads pod env (TANK_SESSION_MODE,
# ANTHROPIC_API_KEY) and seeds claude state so a fresh pod boots
# straight to the chat prompt.
#
# Lives in the image (not inlined in the orchestrator's exec args)
# because the kube-apiserver rejects oversized exec request URLs:
# every byte of the exec command is URL-encoded into ?command=... and
# the bootstrap had grown past the apiserver's request-line limit,
# causing reconnects to 400 with WSServerHandshakeError.
#
# State seeded here:
#   ~/.claude/settings.json       — theme + bypassPermissions defaultMode +
#                                   skipDangerousModePermissionPrompt
#   ~/.claude.json                — onboarding flag + API-key trust list
#                                   (claude keys off the last 20 chars; we
#                                   include 22 too in case that flips back) +
#                                   per-project trust for /workspace +
#                                   official-marketplace auto-install flags +
#                                   pre-approved set of project-level MCP
#                                   servers (read from /workspace/.mcp.json so
#                                   it stays correct as the image evolves) +
#                                   remoteDialogSeen so the `/remote-control`
#                                   slash command skips its first-run consent
#                                   prompt when the user clicks the frontend's
#                                   "Remote control" button
#   ~/.claude/.credentials.json   — only in subscription mode: a static
#                                   placeholder blob. The real token is
#                                   never written to the pod. The
#                                   in-cluster api-proxy strips claude's
#                                   Authorization on every request and
#                                   injects the current real Bearer.
#   ~/.claude/skills/<name>/      — SKILL.md files pulled from external
#                                   repos via /opt/tank/fetch-skills.py
#                                   (uses the github MCP for auth; soft
#                                   fails so a transient MCP error does
#                                   not block boot).
#
# Pod-environment primers are baked into the image at build time
# alongside /workspace/.mcp.json — see Dockerfile. Claude Code reads
# /workspace/CLAUDE.md; Codex reads /workspace/AGENTS.md. They load as
# project-scope context for any cwd under /workspace, including cloned
# repos.
#
# claude runs inside a named tmux session ("tank") so reconnects re-attach
# the same PTY/scrollback. If claude exits we fall through to bash so the
# WS stays useful.

# Reconnect fast-path: if the tmux session already exists this is a
# reattach, not a fresh boot. Skip settings/credentials setup (already
# done on first connect; rewriting is idempotent but wasteful, and in
# subscription mode would re-hit the OAuth gateway every reconnect).
if tmux has-session -t tank 2>/dev/null; then
  exec tmux attach-session -t tank
fi
if [ -n "${TANK_GLIMMUNG_CONTEXT_JSON:-}" ]; then
  cat > /workspace/GLIMMUNG_CONTEXT.json <<EOF
${TANK_GLIMMUNG_CONTEXT_JSON}
EOF
  cat > /workspace/GLIMMUNG_CONTEXT.md <<EOF
# Glimmung Context

This session was launched from glimmung for an attended pickup.

- Run id: ${TANK_GLIMMUNG_RUN_ID:-}
- Issue id: ${TANK_GLIMMUNG_ISSUE_ID:-}
- PR id: ${TANK_GLIMMUNG_PR_ID:-}
- Validation URL: ${TANK_GLIMMUNG_VALIDATION_URL:-}

Use the glimmung MCP server to read the canonical Issue, Run, PR, graph,
comments, reviews, and signals before making changes. Treat GitHub as a
syndication surface when glimmung has the richer record.
EOF
  cat >> /workspace/CLAUDE.md <<'EOF'

## Glimmung attended pickup

This pod was launched from glimmung. Read `/workspace/GLIMMUNG_CONTEXT.md`
first, then use the glimmung MCP server to fetch the canonical Issue / Run /
PR state before acting.
EOF
fi
# Config-mode: short-circuit the regular session bootstrap. The user is
# here to do `claude /login` once so we can capture credentials.json and
# write it to KV. No MCP wiring, no onboarding bypass, no credentials
# pre-seed — claude needs to see a clean state to walk through OAuth.
# The orchestrator's POST /api/sessions/{id}/save-credentials reads the
# resulting ~/.claude/.credentials.json out of this pod via exec.
if [ "${TANK_SESSION_MODE}" = "config" ]; then
  mkdir -p $HOME/.claude
  cat > $HOME/.claude/settings.json <<'EOF'
{"theme":"dark"}
EOF
  cat > $HOME/.claude.json <<'EOF'
{"hasCompletedOnboarding": true}
EOF
  exec claude /login
fi
# Codex-config mode: parallel of `config` mode for the OpenAI codex CLI.
# Drops the user into `codex login --device-auth`, which is the headless-
# friendly OAuth flow (prints a URL + one-time code, vs the default flow
# that opens a browser callback on localhost:1455 — unreachable from a
# pod). Once the user completes login, ~/.codex/auth.json contains the
# token bundle (auth_mode + tokens.{access_token, id_token, refresh_token}
# + last_refresh per developers.openai.com/codex/auth/ci-cd-auth) and the
# tank-operator save-credentials button harvests it to KV. tmux-wrapped
# so a tab reload during the device-code wait doesn't lose the flow.
if [ "${TANK_SESSION_MODE}" = "codex_config" ]; then
  mkdir -p $HOME/.codex
  # cli_auth_credentials_store=file forces the file-backed store; without
  # it codex may try the OS keychain, which doesn't exist in the pod.
  # projects./workspace.trust_level pre-accepts the per-project trust
  # prompt symmetric to ~/.claude.json hasTrustDialogAccepted — without
  # it the user gets "trust this directory?" the first time they run
  # codex against /workspace (relevant here only if they `codex` after
  # `codex login`, but cheap and keeps the two codex modes symmetric).
  cat > $HOME/.codex/config.toml <<'EOF'
cli_auth_credentials_store = "file"

[projects."/workspace"]
trust_level = "trusted"
EOF
  exec tmux new-session -s tank 'codex login --device-auth; exec bash'
fi
# Codex-subscription mode: consume the harvested auth.json. The
# orchestrator mounts the ESO-mirrored codex-credentials Secret read-only
# at /etc/codex-creds/auth.json (mount is `optional: true` so the pod
# still boots if no harvest has happened yet — we surface that as a
# bootstrap error here instead of letting the kubelet hang).
#
# Copy semantics: codex refreshes its token bundle in place on a ~8-day
# cadence and on upstream 401 (per OpenAI's CI/CD auth doc), and Secret
# volumes are read-only. Copying to ~/.codex/auth.json gives codex a
# writable file to rotate in.
#
# Multi-pod gap: in-pod rotation does not flow back to KV today, so two
# concurrent pods will both inherit the same auth.json and may race on
# refresh — known issue, Phase 2 (write-back sidecar or codex-api-proxy)
# decides which way. See backend/src/tank_operator/sessions.py near
# CODEX_SUBSCRIPTION_MODE for the full picture.
if [ "${TANK_SESSION_MODE}" = "codex_subscription" ]; then
  mkdir -p $HOME/.codex
  # Translate /workspace/.mcp.json (claude shape) to codex's
  # [mcp_servers.X] TOML blocks. mcp-auth-proxy is transparent — the
  # same upstream MCP services serve both clients on 127.0.0.1:999X
  # with the SA-token Bearer injected by the sidecar; codex doesn't
  # need to know about the auth at all.
  #
  # Per developers.openai.com/codex/mcp:
  #   streamable_http servers take `url` (and optionally
  #     `bearer_token_env_var`/`http_headers` — unused here, proxy
  #     handles auth);
  #   stdio servers take `command`, `args`, and an `env` table.
  #
  # Verified end-to-end (raw `initialize` JSON-RPC against both
  # transports + `codex mcp list`/`get`) before this landed — see
  # 17e24a3's follow-up investigation.
  mcp_blocks=""
  if [ -f /workspace/.mcp.json ]; then
    mcp_blocks=$(jq -r '.mcpServers | to_entries[] |
      "\n[mcp_servers.\(.key)]" +
      (if .value.type == "http" then
         "\nurl = \"\(.value.url)\""
       elif .value.command then
         "\ncommand = \"\(.value.command)\"" +
         (if .value.args then "\nargs = " + (.value.args | tojson) else "" end)
       else "" end) +
      (if .value.env then
         "\n\n[mcp_servers.\(.key).env]" +
         (.value.env | to_entries | map("\n\(.key) = " + (.value | tojson)) | join(""))
       else "" end)
    ' /workspace/.mcp.json)
  fi
  # approval_policy=never + sandbox_mode=danger-full-access mirrors the
  # bypassPermissions+skipDangerousModePermissionPrompt we set for claude:
  # the pod is the sandbox, so codex itself doesn't need one. Symmetric
  # rationale to ~/.claude/settings.json's defaultMode=bypassPermissions.
  cat > $HOME/.codex/config.toml <<EOF
cli_auth_credentials_store = "file"
approval_policy = "never"
sandbox_mode = "danger-full-access"

[projects."/workspace"]
trust_level = "trusted"
${mcp_blocks}
EOF
  if [ ! -f /etc/codex-creds/auth.json ]; then
    echo "no codex credentials found in /etc/codex-creds/auth.json" >&2
    echo "spawn a 'Codex (config)' session and complete \`codex login --device-auth\` first," >&2
    echo "then click Save Credentials. Once KV has the auth.json, ESO will mirror it" >&2
    echo "into this namespace and a fresh codex_subscription pod will pick it up." >&2
    exec tmux new-session -s tank 'exec bash'
  fi
  cp /etc/codex-creds/auth.json $HOME/.codex/auth.json
  chmod 600 $HOME/.codex/auth.json
  exec tmux new-session -s tank 'codex; exec bash'
fi
# MCP auth is delegated to the mcp-auth-proxy sidecar — claude reaches
# in-cluster HTTP MCP servers via 127.0.0.1 ports declared in
# /workspace/.mcp.json, and the sidecar reads the projected SA token
# fresh per request. No bearer-env-var wiring needed here anymore.
mkdir -p $HOME/.claude
cat > $HOME/.claude/settings.json <<'EOF'
{"theme":"dark","permissions":{"defaultMode":"bypassPermissions"},"skipDangerousModePermissionPrompt":true}
EOF
mcp_enabled='[]'
if [ -f /workspace/.mcp.json ]; then
  mcp_enabled="$(jq -c '.mcpServers | keys' /workspace/.mcp.json)"
fi
case "${TANK_SESSION_MODE:-api_key}" in
  subscription)
    # Static placeholder credentials. The api-proxy in front of
    # api.anthropic.com strips this Authorization on every request and
    # injects the real token, so claude never needs valid creds locally.
    # expiresAt is set to year 2286 so claude never decides to refresh
    # on its own; the placeholder refreshToken would 400 immediately at
    # platform.claude.com if it ever did.
    creds_path=$HOME/.claude/.credentials.json
    cat > "$creds_path" <<'EOF'
{
  "claudeAiOauth": {
    "accessToken": "managed-by-tank-operator",
    "refreshToken": "managed-by-tank-operator",
    "expiresAt": 9999999999000,
    "scopes": ["user:inference", "user:profile"],
    "subscriptionType": "max",
    "rateLimitTier": "max"
  }
}
EOF
    chmod 600 "$creds_path"
    unset ANTHROPIC_API_KEY
    api_key_block=''
    ;;
  *)
    last20="${ANTHROPIC_API_KEY: -20}"
    last22="${ANTHROPIC_API_KEY: -22}"
    api_key_block="\"customApiKeyResponses\": {\"approved\": [\"${last20}\", \"${last22}\"], \"rejected\": []},"
    ;;
esac
# `remoteDialogSeen` skips the one-time interactive
#   "Enable Remote Control? (y/n)"
# consent prompt the first time `/remote-control` runs in a session.
# Set unconditionally because the frontend's "Remote control" button
# can fire the slash command on any subscription session, and the
# consent prompt would block stdin and break the flow.
#
# Earlier (cf57df6) we also wrote a placeholder `oauthAccount` with fake
# UUIDs to satisfy `claude remote-control`'s (bridge mode) startup
# eligibility check. That placeholder is GONE: the slash-command path
# runs its eligibility check against the actor's real org (resolved by
# the api-proxy's OAuth injection), and the fake UUIDs caused the
# command to refuse to launch with "/remote-control isn't available in
# this environment". The whole `remote_control` session mode was
# removed in favor of an in-TUI button — see frontend/src/App.tsx.
cat > $HOME/.claude.json <<EOF
{
  "hasCompletedOnboarding": true,
  ${api_key_block}
  "remoteDialogSeen": true,
  "officialMarketplaceAutoInstallAttempted": true,
  "officialMarketplaceAutoInstalled": true,
  "projects": {
    "/workspace": {
      "allowedTools": [],
      "mcpContextUris": [],
      "mcpServers": {},
      "enabledMcpjsonServers": ${mcp_enabled},
      "disabledMcpjsonServers": [],
      "hasTrustDialogAccepted": true,
      "projectOnboardingSeenCount": 1,
      "hasClaudeMdExternalIncludesApproved": false,
      "hasClaudeMdExternalIncludesWarningShown": false,
      "lastGracefulShutdown": false
    }
  }
}
EOF
# Pull SKILL.md files from external repos via the github MCP. Soft fail
# — a transient MCP error logs `[skills]` lines but does not block boot.
if [ -x /opt/tank/fetch-skills.py ]; then
  python3 /opt/tank/fetch-skills.py 2>&1 | sed 's/^/[skills] /' || true
fi

exec tmux new-session -s tank 'claude; exec bash'
