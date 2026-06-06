# Antigravity session container — GLIBC base, unlike the Alpine/musl
# claude/codex/gemini images.
#
# Why a separate image: Antigravity CLI (`agy`) is a glibc-only native binary
# (dynamically linked against /lib64/ld-linux + GLIBC_2.x) distributed as a GCS
# tarball, NOT an npm package — so it cannot be `npm install -g`'d into the
# Alpine session image the way the other agents are. This image therefore uses
# a Debian base and bakes a pinned `agy` from the public bucket (the Cloud Run
# auto-updater is intentionally bypassed; the chart pins the version).
#
# It powers the `antigravity_config` credential-mint mode today (terminal login
# + save-credentials harvest of the Codeium-style OAuth token file). The same
# image later carries the antigravity-runner that drives the Cascade language
# server for `antigravity_gui`.
FROM node:20-bookworm-slim AS runtime

# Pinned Antigravity CLI release. Bump these together; the manifest + chart
# never fetch at runtime.
ARG AGY_BUILD=1.0.5-5009297080451072
ARG AGY_SHA512=72082d89ea71e101c7beb1630241428d53f68c702995897a2bbf55f162a9f71c8d2d7f98e7b310449cfbdb0b053f6e1b869c1f8d9fb23c95851e980d563d8924
ARG SANDBOX_AGENT_VERSION=0.4.2

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        openssh-client \
        ripgrep \
        vim \
    && rm -rf /var/lib/apt/lists/*

# Ghostty-backed in-browser process terminal — the same package the Alpine
# session images bake. This is the surface the antigravity_config mode exposes
# so the user can run `agy` and complete the interactive Google/Ultra login.
RUN npm install -g "@sandbox-agent/cli@${SANDBOX_AGENT_VERSION}"

# Antigravity CLI (`agy`): pinned native binary from the public GCS bucket,
# sha512-verified. The tarball expands to a single `antigravity` binary; we
# symlink it to `agy` (the name the docs/CLI use). The bundled Cascade
# language server is fetched/extracted by `agy` on first run into the user's
# data dir.
RUN curl -fsSL "https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_BUILD}/linux-x64/cli_linux_x64.tar.gz" -o /tmp/agy.tgz \
    && printf '%s  /tmp/agy.tgz\n' "${AGY_SHA512}" | sha512sum -c - \
    && mkdir -p /opt/antigravity \
    && tar -xzf /tmp/agy.tgz -C /opt/antigravity \
    && ln -s /opt/antigravity/antigravity /usr/local/bin/agy \
    && rm /tmp/agy.tgz \
    && agy --version

# Session dirs + non-root user. node:20-bookworm-slim already ships a `node`
# user at uid/gid 1000 (same as the Alpine images), so reuse it — claude's
# bypassPermissions safety check and our fsGroup wiring both expect uid 1000.
# `.codeium` is where the Antigravity/Cascade auth + state lives (Windsurf
# heritage); the config-mode harvest reads the OAuth token file from there.
RUN mkdir -p /workspace /opt/tank /home/node/.codeium /home/node/.antigravity \
    && chown -R node:node /workspace /home/node/.codeium /home/node/.antigravity

WORKDIR /workspace
USER node
