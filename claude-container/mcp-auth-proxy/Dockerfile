# Standalone mcp-auth-proxy image for sibling consumers outside tank-operator.
#
# Tank session pods (claude/codex/pi) keep baking this package inline via
# claude-container/Dockerfile's `pip install /opt/mcp-auth-proxy` step —
# that build is self-contained by design, no cross-image dependency.
#
# This image exists for *other* in-cluster pods that want the same
# fresh-SA-token-injecting reverse proxy as a sidecar without pulling a
# ~1GB session container. Today's first consumer is nelsong6/hermes; the
# RBAC + auth.romaine.life SA-exchange contract is identical.
#
# Source of truth for the package is claude-container/mcp-auth-proxy/src/ —
# the same files Tank session pods install. Any behavior change ships to
# both consumers from one source.
FROM python:3.12-alpine

WORKDIR /opt/mcp-auth-proxy

# Build context is set to the repo root by the build workflow so the
# COPY below picks up the package source from its canonical location.
COPY claude-container/mcp-auth-proxy/pyproject.toml ./pyproject.toml
COPY claude-container/mcp-auth-proxy/src ./src

RUN pip install --no-cache-dir .

# Non-root by construction. The proxy reads token files mounted by the
# kubelet (world-readable inside the pod) and binds local ports only —
# nothing requires uid 0. Hardens against a tool-use exploit chained
# from a misbehaving MCP upstream.
USER nobody

# Defaults match the in-pod sidecar behavior:
#   127.0.0.1:9991..9996  — MCP listener ports (bound inside the proxy)
#   0.0.0.0:9990          — Prometheus /metrics + /healthz
# Consumers wire their MCP client config at 127.0.0.1:9991..9996 the
# same way Tank session pods do.
ENTRYPOINT ["mcp-auth-proxy"]
