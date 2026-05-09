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
#   ~/.claude/skills/<name>/      — SKILL.md files baked into the
#                                   agent-specific image at build time.
#
# Pod-environment primers and /workspace/.mcp.json are mounted from the
# chart-managed session ConfigMap. Claude Code reads /workspace/CLAUDE.md;
# Codex reads /workspace/AGENTS.md. They load as project-scope context for
# any cwd under /workspace, including cloned repos.
#
# claude runs inside a named tmux session ("tank") so reconnects re-attach
# the same PTY/scrollback. If claude exits we fall through to bash so the
# WS stays useful.
#
# Some PTY clients do not advertise the U8 terminfo capability, which makes
# tmux attach clients with client_utf8=0. In that mode tmux substitutes
# unsupported Unicode glyphs with underscores on redraw, even though the pane
# history itself still contains UTF-8.
tmux_utf8=(tmux -u)

new_interactive_session() {
  local command="$1"
  if [ "${TANK_SESSION_TRANSPORT:-}" = "sandbox-agent" ]; then
    exec bash -lc "${command}"
  fi
  exec "${tmux_utf8[@]}" new-session -s tank "${command}"
}

configure_git_identity() {
  case "${TANK_SESSION_MODE:-api_key}" in
    codex_config|codex_subscription)
      git config --global user.name "tank-operator-codex[bot]"
      git config --global user.email "tank-operator-codex@romaine.life"
      ;;
    *)
      git config --global user.name "tank-operator-claude[bot]"
      git config --global user.email "tank-operator-claude@romaine.life"
      ;;
  esac
}

# Reconnect fast-path: if the tmux session already exists this is a
# reattach, not a fresh boot. Skip settings/credentials setup (already
# done on first connect; rewriting is idempotent but wasteful, and in
# subscription mode would re-hit the OAuth gateway every reconnect).
if [ "${TANK_SESSION_TRANSPORT:-}" != "sandbox-agent" ] && "${tmux_utf8[@]}" has-session -t tank 2>/dev/null; then
  exec "${tmux_utf8[@]}" attach-session -t tank
fi
bash /opt/tank/write-glimmung-context.sh
configure_git_identity
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

[tui]
notifications = true
notification_condition = "always"
notification_method = "bel"
EOF
  new_interactive_session 'codex login --device-auth; exec bash'
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
if [ "${TANK_SESSION_MODE}" = "codex_subscription" ] || [ "${TANK_SESSION_MODE}" = "codex_headless" ]; then
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

[tui]
notifications = true
notification_condition = "always"
notification_method = "bel"
${mcp_blocks}
EOF
  if [ ! -f /etc/codex-creds/auth.json ]; then
    echo "no codex credentials found in /etc/codex-creds/auth.json" >&2
    echo "spawn a 'Codex (config)' session and complete \`codex login --device-auth\` first," >&2
    echo "then click Save Credentials. Once KV has the auth.json, ESO will mirror it" >&2
    echo "into this namespace and a fresh codex_subscription pod will pick it up." >&2
    new_interactive_session 'exec bash'
  fi
  cp /etc/codex-creds/auth.json $HOME/.codex/auth.json
  chmod 600 $HOME/.codex/auth.json
  new_interactive_session 'codex --no-alt-screen; exec bash'
fi
# Pi-config mode: disposable login sandbox for the Pi Coding Agent. Pi manages
# provider credentials with `/login` and stores them in ~/.pi/agent/auth.json.
if [ "${TANK_SESSION_MODE}" = "pi_config" ]; then
  mkdir -p $HOME/.pi/agent
  cat > $HOME/.pi/agent/AGENTS.md <<'EOF'
# Tank Pi Config Session

Run `/login`, choose your provider, and complete the login flow. This mode is
for manual Pi testing; Tank does not persist Pi's native auth.json.
EOF
  exec "${tmux_utf8[@]}" new-session -s tank 'printf "Run /login in Pi. This sandbox does not persist Pi auth.\\n\\n"; pi; exec bash'
fi
# Pi-subscription mode: curate all Tank-backed subscription auth into Pi's
# native ~/.pi/agent/auth.json from existing Claude proxy and Codex credentials.
if [ "${TANK_SESSION_MODE}" = "pi_subscription" ]; then
  mkdir -p $HOME/.pi/agent
  cp /workspace/AGENTS.md $HOME/.pi/agent/AGENTS.md 2>/dev/null || true
  cat >> $HOME/.pi/agent/AGENTS.md <<'EOF'

## Tank Pi Subscription

OpenAI Codex is the default Pi provider for Tank subscription sessions.
Anthropic is available through Tank's Claude proxy, but Pi is a third-party
harness and Anthropic bills that path against extra usage, not Claude plan
limits. If extra usage is exhausted, Anthropic models will fail while Codex
models can continue working.
EOF
  if [ ! -f $HOME/.pi/agent/settings.json ]; then
    cat > $HOME/.pi/agent/settings.json <<'EOF'
{
  "defaultProvider": "openai-codex",
  "defaultModel": "gpt-5.5"
}
EOF
    chmod 600 $HOME/.pi/agent/settings.json
  fi
  printf '{}\n' > $HOME/.pi/agent/auth.json
  node <<'NODE'
const fs = require("fs");
const path = require("path");

const agentDir = path.join(process.env.HOME || "/home/node", ".pi", "agent");
const authPath = path.join(agentDir, "auth.json");
let auth = {};
try {
  auth = JSON.parse(fs.readFileSync(authPath, "utf8"));
} catch {
  auth = {};
}

// Pi decides Anthropic OAuth-vs-API-key behavior from the token shape before
// request headers are applied. Keep an OAuth-looking value in auth.json, then
// force the outbound Authorization header to Tank's proxy placeholder below.
if (!auth.anthropic) {
  auth.anthropic = {
    type: "oauth",
    access: "sk-ant-oat01-tank-placeholder",
    refresh: "tank-placeholder",
    expires: 4102444800000
  };
}

const codexPath = "/etc/codex-creds/auth.json";
if (!auth["openai-codex"] && fs.existsSync(codexPath)) {
  try {
    const codex = JSON.parse(fs.readFileSync(codexPath, "utf8"));
    const tokens = codex.tokens || {};
    const access = tokens.access_token;
    const refresh = tokens.refresh_token;
    if (access && refresh) {
      let payload = {};
      try {
        const body = String(access).split(".")[1] || "";
        payload = JSON.parse(Buffer.from(body, "base64url").toString("utf8"));
      } catch {}
      const accountId =
        tokens.account_id ||
        payload?.["https://api.openai.com/auth"]?.chatgpt_account_id ||
        payload?.chatgpt_account_id;
      const expires =
        typeof payload.exp === "number" ? payload.exp * 1000 : Date.now() + 30 * 60 * 1000;
      auth["openai-codex"] = {
        type: "oauth",
        access,
        refresh,
        expires,
        ...(accountId ? { accountId } : {})
      };
    }
  } catch (error) {
    console.error(`could not translate Codex credentials for Pi: ${error.message}`);
  }
}

fs.writeFileSync(authPath, JSON.stringify(auth, null, 2));
fs.chmodSync(authPath, 0o600);

const modelsPath = path.join(agentDir, "models.json");
let models = {};
try {
  models = JSON.parse(fs.readFileSync(modelsPath, "utf8"));
} catch {
  models = {};
}
models.providers = models.providers || {};
models.providers.anthropic = {
  ...(models.providers.anthropic || {}),
  headers: {
    ...((models.providers.anthropic || {}).headers || {}),
    Authorization: "Bearer managed-by-tank-operator"
  }
};
fs.writeFileSync(modelsPath, JSON.stringify(models, null, 2));
fs.chmodSync(modelsPath, 0o600);
NODE
  chmod 600 $HOME/.pi/agent/auth.json
  exec "${tmux_utf8[@]}" new-session -s tank 'pi; exec bash'
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
  subscription|subscription_headless)
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

new_interactive_session 'claude; exec bash'
