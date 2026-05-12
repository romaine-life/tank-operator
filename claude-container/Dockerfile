# Stage 1: build the pod-side Phase B agent-runner from agent-runner/.
# TypeScript compiled to JS; production node_modules baked into the
# runtime image. Source at /repo/agent-runner/, build context is repo
# root (set by .github/workflows/claude-container-build.yml).
FROM node:20-alpine AS agent-runner-build
WORKDIR /src/agent-runner
COPY agent-runner/package.json agent-runner/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY agent-runner/tsconfig.json ./
COPY agent-runner/src ./src
RUN npx tsc
# Prune dev deps for a smaller runtime payload.
RUN npm prune --omit=dev

# Stage 1b: build the codex-runner from codex-runner/. Sibling of
# agent-runner — drives @openai/codex-sdk for codex_gui sessions instead
# of @anthropic-ai/claude-agent-sdk. Baked into all three session images
# (claude/codex/pi) for build symmetry; only the codex pod spec runs it.
FROM node:20-alpine AS codex-runner-build
WORKDIR /src/codex-runner
COPY codex-runner/package.json codex-runner/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY codex-runner/tsconfig.json ./
COPY codex-runner/src ./src
RUN npx tsc
RUN npm prune --omit=dev

# Stage 2: runtime image. azure-mcp + github-mcp + k8s-mcp run as in-cluster
# HTTP MCP servers. The container reaches them via the mcp-auth-proxy sidecar
# on 127.0.0.1, which reads the projected SA token fresh per request and
# forwards as a Bearer token. No co-located MCP binaries — keep it minimal.
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
ARG TYPESCRIPT_VERSION=5.9.3
ARG TANK_AGENT=claude
ARG PLATFORM_MCP_REF=main

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
        unzip \
        vim

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
        | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"
RUN printf '%s\n' 'export PATH="/usr/local/go/bin:${PATH}"' > /etc/profile.d/go-path.sh

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

# uv — fast pip replacement for Python projects. Hardcoded musl x86_64 path because
# astral-sh/uv release artifacts aren't named with docker's TARGETARCH
# convention; the image is alpine + amd64 today.
RUN curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-musl.tar.gz" \
        | tar -xz -C /tmp \
    && mv /tmp/uv-x86_64-unknown-linux-musl/uv /usr/local/bin/uv \
    && mv /tmp/uv-x86_64-unknown-linux-musl/uvx /usr/local/bin/uvx \
    && rm -rf /tmp/uv-x86_64-unknown-linux-musl \
    && chmod +x /usr/local/bin/uv /usr/local/bin/uvx

RUN case "${TANK_AGENT}" in \
        claude) npm install -g @anthropic-ai/claude-code "vite@${VITE_VERSION}" "typescript@${TYPESCRIPT_VERSION}" ;; \
        codex) npm install -g @openai/codex "vite@${VITE_VERSION}" "typescript@${TYPESCRIPT_VERSION}" ;; \
        pi) npm install -g @mariozechner/pi-coding-agent "vite@${VITE_VERSION}" "typescript@${TYPESCRIPT_VERSION}" ;; \
        *) echo "unsupported TANK_AGENT=${TANK_AGENT}; expected claude, codex, or pi" >&2; exit 1 ;; \
    esac

# Ghostty-backed in-browser process terminal support. The frontend talks to
# sandbox-agent's process-terminal API for the run-pane Shell view.
RUN npm install -g @sandbox-agent/cli@0.4.2

# Baseline Python dev tools expected by agent workflows. Install them in the
# image, not per-repo, so both claude and codex sessions can assume the
# commands exist on PATH.
RUN pip install --no-cache-dir --break-system-packages \
        "pytest==${PYTEST_VERSION}" \
        "ruff==${RUFF_VERSION}"

RUN pip install --no-cache-dir --break-system-packages \
        "git+https://github.com/nelsong6/platform-mcp.git@${PLATFORM_MCP_REF}"

# Localhost reverse proxy that injects fresh SA-token bearer auth into
# outbound HTTP MCP calls. Runs as a sidecar container in the session
# pod (same image, command "mcp-auth-proxy"). See server.py for the
# full rationale — TL;DR fixes the 1h SA-token expiry that froze the
# bearer in env vars.
COPY claude-container/mcp-auth-proxy /opt/mcp-auth-proxy
RUN pip install --no-cache-dir --break-system-packages /opt/mcp-auth-proxy

# Phase B agent-runner: long-lived pod-side process that drives
# @anthropic-ai/claude-agent-sdk, fans events to Cosmos (durable) + a
# local WebSocket (live tap). Entrypoint: `node /opt/agent-runner/dist/index.js`.
COPY --from=agent-runner-build /src/agent-runner/dist /opt/agent-runner/dist
COPY --from=agent-runner-build /src/agent-runner/node_modules /opt/agent-runner/node_modules
COPY --from=agent-runner-build /src/agent-runner/package.json /opt/agent-runner/package.json

# Codex-runner sibling: same pattern, @openai/codex-sdk underneath.
# Baked into all three session images so the build is symmetric; only
# the codex pod spec invokes it as a sidecar. The same WS port + Cosmos
# container shape lets the orchestrator's reverse-proxy and the SPA's
# chat pane consume both runtimes uniformly.
COPY --from=codex-runner-build /src/codex-runner/dist /opt/codex-runner/dist
COPY --from=codex-runner-build /src/codex-runner/node_modules /opt/codex-runner/node_modules
COPY --from=codex-runner-build /src/codex-runner/package.json /opt/codex-runner/package.json

# Session-facing MCP wiring, AGENTS/CLAUDE primers, bundled skill docs, and the
# bootstrap script are mounted from the chart-managed tank-session-config
# ConfigMap. Keep the parent directories in the image so the orchestrator can
# mount individual files with subPath without rebuilding the image for
# docs/config/script changes.
RUN mkdir -p /workspace \
        /opt/tank \
        /home/node/.claude/skills \
        /home/node/.codex/skills \
        /home/node/.pi/agent

# Pi keeps MCP support in installable packages rather than core. Install the
# adapter into the baked Pi image so session startup can stay offline and Pi
# can consume the same /workspace/.mcp.json that Claude and Codex use.
RUN if [ "${TANK_AGENT}" = "pi" ]; then \
      HOME=/home/node pi install npm:pi-mcp-adapter; \
    fi

# Run the agent as a non-root user so claude's safety check accepts
# bypassPermissions mode (it refuses to enable bypass when uid 0). Also
# defense-in-depth: a tool-use exploit can't escape to a privileged shell.
# node:20-alpine already ships a `node` user at uid/gid 1000 with
# /home/node, so reuse it (creating another user with gid 1000 collides).
RUN chown -R node:node /workspace /home/node/.claude /home/node/.codex /home/node/.pi

WORKDIR /workspace
USER node

CMD ["bash"]
