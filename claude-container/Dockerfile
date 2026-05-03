# azure-mcp + github-mcp + k8s-mcp run as in-cluster HTTP MCP servers
# (see k8s-mcp-azure/, k8s-mcp-github/, k8s-mcp-k8s/). The container
# reaches them via the mcp-auth-proxy sidecar on 127.0.0.1, which reads
# the projected SA token fresh per request and forwards as a Bearer
# token. No co-located MCP binaries needed in this image — keep it
# minimal.
FROM node:20-alpine

ARG TARGETARCH=amd64
ARG KUBECTL_VERSION=v1.31.0
ARG TOFU_VERSION=1.9.0
ARG HELM_VERSION=v3.16.3
ARG YQ_VERSION=v4.44.3
ARG UV_VERSION=0.5.18
ARG RUFF_VERSION=0.15.12
ARG PYTEST_VERSION=8.4.2
ARG GO_VERSION=1.26.2
ARG VITE_VERSION=5.4.21
ARG TANK_AGENT=claude

# ripgrep + make round out the bash surface (rg replaces grep for repo
# search; make is needed for several of the consumed repos' dev flows).
# Go is baked in so session pods can run checks for Go services instead of
# punting those to CI or a separate agent image.
RUN apk add --no-cache \
        bash \
        ca-certificates \
        curl \
        git \
        github-cli \
        gnupg \
        jq \
        less \
        make \
        py3-pip \
        python3 \
        ripgrep \
        tmux \
        unzip \
        vim

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
        | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

RUN curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
        -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

RUN curl -fsSL "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip" \
        -o /tmp/tofu.zip \
    && unzip -q /tmp/tofu.zip tofu -d /usr/local/bin \
    && chmod +x /usr/local/bin/tofu \
    && rm /tmp/tofu.zip

# helm — every consumed repo ships a chart; without it `helm template` /
# `helm get manifest` debugging is missing.
RUN curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
        | tar -xz -C /tmp \
    && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
    && rm -rf "/tmp/linux-${TARGETARCH}" \
    && chmod +x /usr/local/bin/helm

# yq — pairs with jq for the YAML half of k8s/helm work.
RUN curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" \
        -o /usr/local/bin/yq \
    && chmod +x /usr/local/bin/yq

# uv — fast pip replacement for the python projects (glimmung,
# mcp-servers, platform-mcp). Hardcoded musl x86_64 path because
# astral-sh/uv release artifacts aren't named with docker's TARGETARCH
# convention; the image is alpine + amd64 today.
RUN curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-musl.tar.gz" \
        | tar -xz -C /tmp \
    && mv /tmp/uv-x86_64-unknown-linux-musl/uv /usr/local/bin/uv \
    && mv /tmp/uv-x86_64-unknown-linux-musl/uvx /usr/local/bin/uvx \
    && rm -rf /tmp/uv-x86_64-unknown-linux-musl \
    && chmod +x /usr/local/bin/uv /usr/local/bin/uvx

RUN case "${TANK_AGENT}" in \
        claude) npm install -g @anthropic-ai/claude-code "vite@${VITE_VERSION}" ;; \
        codex) npm install -g @openai/codex "vite@${VITE_VERSION}" ;; \
        pi) npm install -g @mariozechner/pi-coding-agent "vite@${VITE_VERSION}" ;; \
        *) echo "unsupported TANK_AGENT=${TANK_AGENT}; expected claude, codex, or pi" >&2; exit 1 ;; \
    esac

# Baseline Python dev tools expected by agent workflows. Install them in the
# image, not per-repo, so both claude and codex sessions can assume the
# commands exist on PATH.
RUN pip install --no-cache-dir --break-system-packages \
        "pytest==${PYTEST_VERSION}" \
        "ruff==${RUFF_VERSION}"

COPY claude-container/platform-mcp /opt/platform-mcp
RUN pip install --no-cache-dir --break-system-packages /opt/platform-mcp

# Localhost reverse proxy that injects fresh SA-token bearer auth into
# outbound HTTP MCP calls. Runs as a sidecar container in the session
# pod (same image, command "mcp-auth-proxy"). See server.py for the
# full rationale — TL;DR fixes the 1h SA-token expiry that froze the
# bearer in env vars.
COPY claude-container/mcp-auth-proxy /opt/mcp-auth-proxy
RUN pip install --no-cache-dir --break-system-packages /opt/mcp-auth-proxy

# Default MCP wiring for the in-container agent. Lives in /workspace so
# Claude Code picks it up as project-scope config; also kept at
# /opt/claude-container/mcp.json for users who mount their own /workspace.
COPY claude-container/mcp.json /opt/claude-container/mcp.json
RUN mkdir -p /workspace && cp /opt/claude-container/mcp.json /workspace/.mcp.json

# Default project-scope primers. They land in /workspace so each CLI
# auto-loads its native instruction file for any cwd under /workspace
# (including cloned repos): Claude Code reads CLAUDE.md; Codex and Pi read
# AGENTS.md. The files describe the pod environment without conflating
# with user-scope config, which is left free for genuine user prefs. They
# are also kept under /opt/claude-container/ for users who mount their own
# /workspace.
#
# Source filename intentionally avoids CLAUDE.md/AGENTS.md so agents
# working inside claude-container/ in this repo do not auto-load the seed
# as project docs.
COPY claude-container/default-claude.md /opt/claude-container/CLAUDE.md
RUN cp /opt/claude-container/CLAUDE.md /workspace/CLAUDE.md \
    && cp /opt/claude-container/CLAUDE.md /opt/claude-container/AGENTS.md \
    && cp /opt/claude-container/AGENTS.md /workspace/AGENTS.md

# Thin passthrough entrypoint kept for direct `docker run` / `kubectl
# exec` consumers. The orchestrator's bridge bypasses ENTRYPOINT (sets
# command: ["sleep", "infinity"]) and runs its own bootstrap shell.
# In-cluster MCP auth is handled by the mcp-auth-proxy sidecar above.
COPY claude-container/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Bootstrap script the orchestrator exec's on every WS connect. Lives
# in the image so the kubectl-exec request URL stays small — the
# kube-apiserver rejects oversized exec command lines with HTTP 400.
COPY claude-container/tank-bootstrap.sh /opt/tank/bootstrap.sh
RUN chmod +x /opt/tank/bootstrap.sh

# Repo-bundled skills are baked into the agent-specific home directory so
# the CLI can discover them before it starts. Rebuild the image to update
# skills; do not fetch them during session bootstrap.
COPY claude-container/skills /opt/claude-container/skills
RUN mkdir -p /home/node/.claude/skills /home/node/.codex/skills \
    && case "${TANK_AGENT}" in \
        claude) \
          cp -R /opt/claude-container/skills/done /home/node/.claude/skills/done; \
          cp -R /opt/claude-container/skills/rollout /home/node/.claude/skills/rollout; \
          ;; \
        codex) \
          cp -R /opt/claude-container/skills/rollout /home/node/.codex/skills/rollout; \
          ;; \
        pi) \
          mkdir -p /home/node/.pi/agent; \
          ;; \
    esac

# Pi keeps MCP support in installable packages rather than core. Install the
# adapter into the baked Pi image so session startup can stay offline and Pi
# can consume the same /workspace/.mcp.json that Claude and Codex use.
RUN if [ "${TANK_AGENT}" = "pi" ]; then \
      HOME=/home/node pi install npm:pi-mcp-adapter; \
    fi

# Hide tmux's status bar so the TUI gets the full terminal height.
# tmux is the WS-reconnect-survival wrapper for agent sessions
# (see tank-bootstrap.sh); the status bar would steal one row. Mouse mode
# lets tmux handle wheel scrolling when the browser is attached to tmux.
RUN printf 'set -g status off\nset -g default-terminal "tmux-256color"\nset -g mouse on\n' > /home/node/.tmux.conf \
    && chown node:node /home/node/.tmux.conf

# Run the agent as a non-root user so claude's safety check accepts
# bypassPermissions mode (it refuses to enable bypass when uid 0). Also
# defense-in-depth: a tool-use exploit can't escape to a privileged shell.
# node:20-alpine already ships a `node` user at uid/gid 1000 with
# /home/node, so reuse it (creating another user with gid 1000 collides).
RUN chown -R node:node /workspace /home/node/.claude /home/node/.codex /home/node/.pi

WORKDIR /workspace
USER node

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["bash"]
