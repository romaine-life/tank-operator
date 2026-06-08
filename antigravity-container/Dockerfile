# Antigravity session container — GLIBC base, unlike the Alpine/musl
# claude/codex images.
#
# Why a separate image: Antigravity CLI (`agy`) is a glibc-only native binary
# (dynamically linked against /lib64/ld-linux + GLIBC_2.x) distributed as a GCS
# tarball, NOT an npm package — so it cannot be `npm install -g`'d into the
# Alpine session image the way the other agents are. This image therefore uses
# a Debian base and bakes a pinned `agy` from the public bucket (the Cloud Run
# auto-updater is intentionally bypassed; the chart pins the version).
#
# It powers two modes:
#   - antigravity_config: terminal login + save-credentials harvest of the
#     OAuth token file (sandbox-agent terminal only).
#   - antigravity_gui: GUI chat. The antigravity-runner sidecar drives agy and
#     maps its structured transcript onto the Tank conversation protocol; the
#     mcp-auth-proxy sidecar (same image) provides the in-cluster MCP gateway.
#     agy's OAuth is proxy-owned: the launch script seeds a placeholder token and
#     agy's Code Assist traffic is host-aliased to the antigravity-api-proxy,
#     which injects the real access token and owns the refresh chain — the real
#     refresh token never lands in the session pod
#     (see docs/api-proxy-auth.md). Only antigravity_config (interactive login)
#     reaches real Google to mint the token that the proxy then owns.

# Stage 0: build tank-supervisor for in-pod test-slot hot-swap.
# Same supervisor binary used by the Claude/Codex session images; Antigravity
# only invokes it when the pod spec sets GLIMMUNG_SUPERVISOR_CHILD.
FROM golang:1.26-alpine AS tank-supervisor-build
WORKDIR /src
COPY backend-go/go.mod backend-go/go.sum ./
RUN go mod download
COPY backend-go/cmd/tank-supervisor ./cmd/tank-supervisor
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tank-supervisor ./cmd/tank-supervisor

# Stage 1: build the antigravity-runner (TS -> JS + production node_modules).
# Build context is the repo root (docker-build-check.yaml sets context: .).
FROM node:20-bookworm-slim AS antigravity-runner-build
WORKDIR /src/antigravity-runner
COPY antigravity-runner/package.json antigravity-runner/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY antigravity-runner/tsconfig.json ./
COPY antigravity-runner/src ./src
COPY runner-shared /src/runner-shared
RUN npx tsc
RUN npm prune --omit=dev

# Stage 2: runtime image.
FROM node:20-bookworm-slim AS runtime

# Pinned Antigravity CLI release. Bump these together; the manifest + chart
# never fetch at runtime.
ARG AGY_BUILD=1.0.5-5009297080451072
ARG AGY_SHA512=72082d89ea71e101c7beb1630241428d53f68c702995897a2bbf55f162a9f71c8d2d7f98e7b310449cfbdb0b053f6e1b869c1f8d9fb23c95851e980d563d8924
ARG SANDBOX_AGENT_VERSION=0.4.2

# python3 + pip are for the mcp-auth-proxy sidecar (a Python package shared with
# the claude/codex images); the rest round out the in-pod shell surface.
RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        openssh-client \
        python3 \
        python3-pip \
        ripgrep \
        vim \
    && rm -rf /var/lib/apt/lists/*

# Ghostty-backed in-browser process terminal — the same package the Alpine
# session images bake. The antigravity_config mode exposes this so the user can
# run `agy` and complete the interactive Google/Ultra login.
RUN npm install -g "@sandbox-agent/cli@${SANDBOX_AGENT_VERSION}"

# Antigravity CLI (`agy`): pinned native binary from the public GCS bucket,
# sha512-verified. The tarball expands to a single `antigravity` binary; we
# symlink it to `agy` (the name the docs/CLI use).
RUN curl -fsSL "https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_BUILD}/linux-x64/cli_linux_x64.tar.gz" -o /tmp/agy.tgz \
    && printf '%s  /tmp/agy.tgz\n' "${AGY_SHA512}" | sha512sum -c - \
    && mkdir -p /opt/antigravity \
    && tar -xzf /tmp/agy.tgz -C /opt/antigravity \
    && ln -s /opt/antigravity/antigravity /usr/local/bin/agy \
    && rm /tmp/agy.tgz \
    && agy --version

# mcp-auth-proxy: localhost reverse proxy that injects fresh SA-token bearer
# auth into outbound HTTP MCP calls. Runs as the antigravity_gui sidecar
# (same image, command "mcp-auth-proxy"). Same Python package the Alpine
# session images bake.
COPY claude-container/mcp-auth-proxy /opt/mcp-auth-proxy
RUN pip install --no-cache-dir --break-system-packages /opt/mcp-auth-proxy

# antigravity-runner: long-lived pod-side process that drives agy and publishes
# canonical Tank conversation events. Entrypoint launched by
# /opt/tank/antigravity-runner-launch.sh -> node /opt/antigravity-runner/dist/index.js.
# The compiled dist imports ../../runner-shared, which resolves to /opt/runner-shared.
COPY --from=antigravity-runner-build /src/antigravity-runner/dist /opt/antigravity-runner/dist
COPY --from=antigravity-runner-build /src/antigravity-runner/node_modules /opt/antigravity-runner/node_modules
COPY --from=antigravity-runner-build /src/antigravity-runner/package.json /opt/antigravity-runner/package.json
COPY runner-shared /opt/runner-shared

# tank-supervisor + baked launch shim for test-slot antigravity-runner hot-swap.
# The baked shim is hot-aware: fresh pods run /opt/antigravity-runner/dist,
# while SIGHUP restarts prefer /var/run/antigravity-runner-hot/dist when a
# Glimmung hot-swap has populated it.
COPY --from=tank-supervisor-build /out/tank-supervisor /app/tank-supervisor
RUN cat > /app/antigravity-runner-launch-binary.sh <<'EOF' && chmod 0755 /app/antigravity-runner-launch-binary.sh
#!/bin/sh
if [ ! -e /var/run/antigravity-runner-hot/node_modules ]; then
  ln -sfn /opt/antigravity-runner/node_modules /var/run/antigravity-runner-hot/node_modules
fi
if [ -f /var/run/antigravity-runner-hot/dist/index.js ]; then
  exec node /var/run/antigravity-runner-hot/dist/index.js
fi
exec node /opt/antigravity-runner/dist/index.js
EOF

# Baked antigravity-runner launch script — self-contained, no session-ConfigMap
# coupling. Copies the read-only KV credential into agy's writable data dir,
# then execs the runner. World-readable so the non-root runner can run it.
COPY antigravity-container/antigravity-runner-launch.sh /opt/tank/antigravity-runner-launch.sh
RUN chmod 0755 /opt/tank/antigravity-runner-launch.sh

# Session dirs + non-root user. node:20-bookworm-slim already ships a `node`
# user at uid/gid 1000 (same as the Alpine images). agy stores its auth + state
# under ~/.gemini/antigravity-cli/ (it creates the subdir itself on first run);
# /opt/tank is the mount point for the chart-managed launch scripts.
RUN mkdir -p /workspace /opt/tank /home/node/.gemini/skills \
    && chown -R node:node /workspace /home/node/.gemini

WORKDIR /workspace
USER node
