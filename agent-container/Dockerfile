# Purpose-built image for the issue-runner agent flow (ambience and
# friends). Distinct from claude-container/ — that one is for tank-
# operator's interactive human sessions and stays slim. The agent flow
# needs:
#   - Playwright + chromium for in-pod screenshot evidence
#   - Go for projects whose dev-loop runs `go run ./cmd/...`
#   - claude-code CLI for the agent itself
# It does NOT need kubectl/helm/tofu/yq/uv/ripgrep/MCP plumbing — those
# are tank-operator session-pod concerns.
FROM mcr.microsoft.com/playwright:v1.49.1-noble

ARG TARGETARCH=amd64
ARG GO_VERSION=1.26.2
ARG VITE_VERSION=5.4.21
# Must match the base image tag above. The base image bundles its
# matching chromium under /ms-playwright; an unpinned `npm install
# playwright` would otherwise pull a newer release whose chromium
# revision (e.g. headless_shell-1217) is not on disk and break in-pod
# screenshot capture.
ARG PLAYWRIGHT_VERSION=1.49.1

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        python3 \
        python3-pip \
    && rm -rf /var/lib/apt/lists/*

# gh CLI from GitHub's official Ubuntu repo.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=${TARGETARCH} signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# Go — projects whose dev-loop builds a Go server (ambience).
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
        | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"
RUN printf '%s\n' 'export PATH="/usr/local/go/bin:${PATH}"' > /etc/profile.d/go-path.sh

# claude-code CLI + playwright npm package. The base image preinstalls
# chromium under /ms-playwright but NOT the playwright JS package, so any
# `import "playwright"` from a script (e.g. ambience's
# scripts/agent/capture-screenshot.mjs) would fail without this. Browsers
# are already on disk so we skip re-downloading; the npm playwright
# version is pinned to match the base image so the bundled chromium is
# the version playwright actually requests.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
RUN npm install -g @anthropic-ai/claude-code "playwright@${PLAYWRIGHT_VERSION}" "vite@${VITE_VERSION}"

# NODE_PATH lets bare `import "playwright"` resolve from the global
# install above for CommonJS callers. The Node ESM resolver does NOT
# honor NODE_PATH, so for ESM `import` from arbitrary working
# directories we also export PLAYWRIGHT_PACKAGE_PATH — ambience's
# scripts/agent/capture-screenshot.mjs reads this and imports the
# global install directly via pathToFileURL, side-stepping the
# bare-name resolver entirely.
ENV NODE_PATH=/usr/lib/node_modules
ENV PLAYWRIGHT_PACKAGE_PATH=/usr/lib/node_modules/playwright/index.mjs

# Rename the playwright base's pwuser (uid 1000) to `node` so the agent
# Job's runAsUser:1000 securityContext finds a valid user named `node`,
# matching existing bash bootstraps that mkdir under /home/node.
RUN if id -u pwuser >/dev/null 2>&1; then \
        groupmod -n node pwuser \
        && usermod -l node -d /home/node -m pwuser; \
    else \
        useradd -m -u 1000 -s /bin/bash node; \
    fi

# Playwright browser cache (chromium + deps) is at /ms-playwright in the
# base image. Surface it as PLAYWRIGHT_BROWSERS_PATH so child
# `npx playwright` calls find the bundled browsers instead of trying to
# re-download under /home/node/.cache.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

# /workspace is the agent's working tree (emptyDir mount in the Job spec).
RUN mkdir -p /workspace && chown -R node:node /workspace

WORKDIR /workspace
USER node

CMD ["bash"]
