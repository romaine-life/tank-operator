# Stage 0: build tank-supervisor for in-pod test-slot hot-swap.
# The supervisor is the same Go binary the orchestrator pod uses (see
# backend-go/cmd/tank-supervisor/). It runs as PID 1 in the agent-runner
# container only when the chart enables test-slot mode; production sessions
# don't invoke it. Building from source here (rather than COPY --from a
# separately-built image) keeps the build self-contained — no cross-image
# layer dependencies. Source intentionally unchanged from main; this
# Dockerfile just bakes it into a second image.
FROM golang:1.26-alpine AS tank-supervisor-build
WORKDIR /src
COPY backend-go/go.mod backend-go/go.sum ./
RUN go mod download
COPY backend-go/cmd/tank-supervisor ./cmd/tank-supervisor
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tank-supervisor ./cmd/tank-supervisor

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
COPY runner-shared /src/runner-shared
RUN npx tsc
# Prune dev deps for a smaller runtime payload.
RUN npm prune --omit=dev

# Stage 1b: build the codex-runner from codex-runner/. Sibling of
# agent-runner â€” drives @openai/codex-sdk for codex_gui sessions instead
# of @anthropic-ai/claude-agent-sdk. Baked into all three session images
# (claude/codex/pi) for build symmetry; only the codex pod spec runs it.
FROM node:20-alpine AS codex-runner-build
WORKDIR /src/codex-runner
COPY codex-runner/package.json codex-runner/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY codex-runner/tsconfig.json ./
COPY codex-runner/src ./src
COPY runner-shared /src/runner-shared
RUN npx tsc
RUN npm prune --omit=dev

# Stage 2: runtime image. azure-mcp + github-mcp + k8s-mcp run as in-cluster
# HTTP MCP servers. The container reaches them via the mcp-auth-proxy sidecar
# on 127.0.0.1, which injects fresh bearer auth per request; GitHub receives a
# Tank session attestation. No co-located MCP binaries: keep it minimal.
FROM node:20-alpine

ARG TARGETARCH=amd64
ARG KUBECTL_VERSION=v1.31.0
ARG TOFU_VERSION=1.9.0
ARG HELM_VERSION=v3.16.3
ARG YQ_VERSION=v4.44.3
ARG UV_VERSION=0.5.18
ARG TAILSCALE_VERSION=1.98.4
ARG RUFF_VERSION=0.15.12
ARG PYTEST_VERSION=8.4.2
ARG GO_VERSION=1.26.2
ARG VITE_VERSION=5.4.21
ARG TYPESCRIPT_VERSION=5.9.3
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
        openssh-client \
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

# helm â€” every consumed repo ships a chart; without it `helm template` /
# `helm get manifest` debugging is missing.
RUN curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
        | tar -xz -C /tmp \
    && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
    && rm -rf "/tmp/linux-${TARGETARCH}" \
    && chmod +x /usr/local/bin/helm

# yq â€” pairs with jq for the YAML half of k8s/helm work.
RUN curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}" \
        -o /usr/local/bin/yq \
    && chmod +x /usr/local/bin/yq

# uv â€” fast pip replacement for Python projects. Hardcoded musl x86_64 path because
# astral-sh/uv release artifacts aren't named with docker's TARGETARCH
# convention; the image is alpine + amd64 today.
RUN curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-musl.tar.gz" \
        | tar -xz -C /tmp \
    && mv /tmp/uv-x86_64-unknown-linux-musl/uv /usr/local/bin/uv \
    && mv /tmp/uv-x86_64-unknown-linux-musl/uvx /usr/local/bin/uvx \
    && rm -rf /tmp/uv-x86_64-unknown-linux-musl \
    && chmod +x /usr/local/bin/uv /usr/local/bin/uvx

# tailscale â€” lets a session pod join the tailnet in userspace mode to reach
# hosts that live off-cluster (e.g. the SpireLens game host on
# tag:spirelens-host). Baked in but dormant: only a session that opts in runs
# the join (see session-pod-bootstrap.sh + docs/tailnet-host-access.md), so the
# binaries are present for instant, offline opt-in without changing the image.
# Statically linked Go binaries, so they run on alpine/musl (verified on this
# same node:20-alpine base).
RUN curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}.tgz" \
        | tar -xz -C /tmp \
    && mv "/tmp/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}/tailscale" /usr/local/bin/tailscale \
    && mv "/tmp/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}/tailscaled" /usr/local/bin/tailscaled \
    && rm -rf "/tmp/tailscale_${TAILSCALE_VERSION}_${TARGETARCH}" \
    && chmod +x /usr/local/bin/tailscale /usr/local/bin/tailscaled

RUN case "${TANK_AGENT}" in \
        claude) npm install -g @anthropic-ai/claude-code "vite@${VITE_VERSION}" "typescript@${TYPESCRIPT_VERSION}" ;; \
        codex) npm install -g @openai/codex "vite@${VITE_VERSION}" "typescript@${TYPESCRIPT_VERSION}" ;; \
        *) echo "unsupported TANK_AGENT=${TANK_AGENT}; expected claude or codex" >&2; exit 1 ;; \
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

# Localhost reverse proxy that injects fresh SA-token bearer auth into
# outbound HTTP MCP calls. Runs as a sidecar container in the session
# pod (same image, command "mcp-auth-proxy"). See server.py for the
# full rationale â€” TL;DR fixes the 1h SA-token expiry that froze the
# bearer in env vars.
COPY claude-container/mcp-auth-proxy /opt/mcp-auth-proxy
RUN pip install --no-cache-dir --break-system-packages /opt/mcp-auth-proxy

# Long-lived pod-side process that drives @anthropic-ai/claude-agent-sdk
# and publishes canonical conversation events to the session bus.
# Entrypoint: `node /opt/agent-runner/dist/index.js`.
COPY --from=agent-runner-build /src/agent-runner/dist /opt/agent-runner/dist
COPY --from=agent-runner-build /src/agent-runner/node_modules /opt/agent-runner/node_modules
COPY --from=agent-runner-build /src/agent-runner/package.json /opt/agent-runner/package.json

COPY runner-shared /opt/runner-shared

# tank-supervisor + baked launch shims for test-slot SDK-runner hot-swap.
# The supervisor runs as PID 1 in a runner container only when the
# orchestrator sets GLIMMUNG_SUPERVISOR_CHILD on the container (see
# k8s/session-config/*-runner-launch.sh's conditional exec). In
# production, both files are dormant: the supervisor binary is never invoked,
# the shim is never resolved. Image gains ~5MB of dead weight in prod; the
# tradeoff is "the same image works for both prod and test-slot iteration."
COPY --from=tank-supervisor-build /out/tank-supervisor /app/tank-supervisor
# Hot-aware baked launch shim.
#
# Two responsibilities, both at every supervisor restart:
#
#  1. Ensure /var/run/agent-runner-hot/node_modules resolves. Hot-swap
#     places only `dist/` at /var/run/agent-runner-hot/dist; node_modules
#     remains baked at /opt/agent-runner/node_modules. ESM bare-specifier
#     resolution walks UP from the importing file, so without a sibling
#     `node_modules` next to the hot dist, `import "@anthropic-ai/..."`
#     fails ERR_MODULE_NOT_FOUND. A symlink fixes that without copying
#     ~100MB on every hot-swap. Idempotent: skip if symlink already there.
#
#  2. Prefer hot dist if present, else baked. The shim itself encodes
#     "hot-aware" so the apply-hot-swap path can deliver just the dist
#     directory (the contract surface) without also delivering a separate
#     launch shim. Supervisor's existing `GLIMMUNG_SUPERVISOR_HOT_ARTIFACT`
#     hot-shim override remains supported for callers who do want a custom
#     entrypoint, but the default baked shim is itself hot-aware.
#
# The fallback `exec node /opt/agent-runner/dist/index.js` remains literal
# so scripts/check-session-pod-hot-swap-migration.mjs's regex pin still
# matches (and so the behavior at fresh pod boot — before any hot-swap —
# is identical to pre-change).
RUN cat > /app/agent-runner-launch-binary.sh <<'EOF' && chmod 0755 /app/agent-runner-launch-binary.sh
#!/bin/sh
if [ ! -e /var/run/agent-runner-hot/node_modules ]; then
  ln -sfn /opt/agent-runner/node_modules /var/run/agent-runner-hot/node_modules
fi
if [ -f /var/run/agent-runner-hot/dist/index.js ]; then
  exec node /var/run/agent-runner-hot/dist/index.js
fi
exec node /opt/agent-runner/dist/index.js
EOF

# Codex-runner sibling: same pattern, @openai/codex-sdk underneath.
# Baked into all three session images so the build is symmetric; only
# the codex pod spec invokes it as a sidecar. It follows the same
# session-command and durable event-ledger contract as the Claude runner.
COPY --from=codex-runner-build /src/codex-runner/dist /opt/codex-runner/dist
COPY --from=codex-runner-build /src/codex-runner/node_modules /opt/codex-runner/node_modules
COPY --from=codex-runner-build /src/codex-runner/package.json /opt/codex-runner/package.json
RUN cat > /app/codex-runner-launch-binary.sh <<'EOF' && chmod 0755 /app/codex-runner-launch-binary.sh
#!/bin/sh
if [ ! -e /var/run/codex-runner-hot/node_modules ]; then
  ln -sfn /opt/codex-runner/node_modules /var/run/codex-runner-hot/node_modules
fi
if [ -f /var/run/codex-runner-hot/dist/index.js ]; then
  exec node /var/run/codex-runner-hot/dist/index.js
fi
exec node /opt/codex-runner/dist/index.js
EOF

# Session-facing MCP wiring, AGENTS/CLAUDE primers, bundled skill docs, and
# launcher scripts are mounted from the chart-managed tank-session-config
# ConfigMap. Keep the parent directories in the image so the orchestrator can
# mount individual files with subPath without rebuilding the image for
# docs/config/script changes.
RUN mkdir -p /workspace \
        /opt/tank \
        /home/node/.claude/skills \
        /home/node/.codex/skills

# Run the agent as a non-root user so claude's safety check accepts
# bypassPermissions mode (it refuses to enable bypass when uid 0). Also
# defense-in-depth: a tool-use exploit can't escape to a privileged shell.
# node:20-alpine already ships a `node` user at uid/gid 1000 with
# /home/node, so reuse it (creating another user with gid 1000 collides).
RUN chown -R node:node /workspace /home/node/.claude /home/node/.codex

WORKDIR /workspace
USER node

CMD ["bash"]
