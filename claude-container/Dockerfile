# Stage 0: build tank-supervisor for in-pod test-slot hot-swap.
# The supervisor is the same Go binary the orchestrator pod uses (see
# backend-go/cmd/tank-supervisor/). It runs as PID 1 in the claude-runner
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

# Stage 1: build the pod-side Phase B claude-runner from claude-runner/.
# TypeScript compiled to JS; production node_modules baked into the
# runtime image. Source at /repo/claude-runner/, build context is repo
# root (set by .github/workflows/claude-container-build.yml).
FROM node:20-alpine AS claude-runner-build
WORKDIR /src/claude-runner
COPY claude-runner/package.json claude-runner/package-lock.json* ./
RUN npm ci --no-audit --no-fund
COPY claude-runner/tsconfig.json ./
COPY claude-runner/src ./src
COPY runner-shared /src/runner-shared
RUN npx tsc
# Prune dev deps for a smaller runtime payload.
RUN npm prune --omit=dev

# Stage 1b: build the codex-runner from codex-runner/. Sibling of
# claude-runner â€” drives @openai/codex-sdk for codex_gui sessions instead
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
ARG TANK_AGENT=claude

# Baseline interactive-session tool surface shared by Claude and Codex.
COPY session-images /opt/tank/session-images
RUN TARGETARCH="${TARGETARCH}" sh /opt/tank/session-images/install-common-alpine.sh
ENV PATH="/usr/local/go/bin:${PATH}"

RUN case "${TANK_AGENT}" in \
        claude) npm install -g @anthropic-ai/claude-code ;; \
        codex) npm install -g @openai/codex ;; \
        *) echo "unsupported TANK_AGENT=${TANK_AGENT}; expected claude or codex" >&2; exit 1 ;; \
    esac

# Localhost reverse proxy that injects fresh SA-token bearer auth into
# outbound HTTP MCP calls. Runs as a sidecar container in the session
# pod (same image, command "mcp-auth-proxy"). See server.py for the
# full rationale â€” TL;DR fixes the 1h SA-token expiry that froze the
# bearer in env vars.
COPY claude-container/mcp-auth-proxy /opt/mcp-auth-proxy
RUN pip install --no-cache-dir --break-system-packages /opt/mcp-auth-proxy

# Long-lived pod-side process that drives @anthropic-ai/claude-agent-sdk
# and publishes canonical conversation events to the session bus.
# Entrypoint: `node /opt/claude-runner/dist/index.js`.
COPY --from=claude-runner-build /src/claude-runner/dist /opt/claude-runner/dist
COPY --from=claude-runner-build /src/claude-runner/node_modules /opt/claude-runner/node_modules
COPY --from=claude-runner-build /src/claude-runner/package.json /opt/claude-runner/package.json

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
#  1. Ensure /var/run/claude-runner-hot/node_modules resolves. Hot-swap
#     places only `dist/` at /var/run/claude-runner-hot/dist; node_modules
#     remains baked at /opt/claude-runner/node_modules. ESM bare-specifier
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
# The fallback `exec node /opt/claude-runner/dist/index.js` remains literal
# so scripts/check-session-pod-hot-swap-migration.mjs's regex pin still
# matches (and so the behavior at fresh pod boot — before any hot-swap —
# is identical to pre-change).
RUN cat > /app/claude-runner-launch-binary.sh <<'EOF' && chmod 0755 /app/claude-runner-launch-binary.sh
#!/bin/sh
if [ ! -e /var/run/claude-runner-hot/node_modules ]; then
  ln -sfn /opt/claude-runner/node_modules /var/run/claude-runner-hot/node_modules
fi
if [ -f /var/run/claude-runner-hot/dist/index.js ]; then
  exec node /var/run/claude-runner-hot/dist/index.js
fi
exec node /opt/claude-runner/dist/index.js
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
