"""Prometheus instrumentation for the in-pod mcp-auth-proxy sidecar.

This sidecar runs in every session pod (Claude, Codex, Pi modes) and is
responsible for injecting fresh bearer auth on every outbound MCP call.
The cluster-side kube-prometheus-stack picks the /metrics endpoint up via
a PodMonitor (k8s/templates/podmonitor-sessions.yaml).

Cardinality discipline: labels are bounded by the fixed LISTENERS map in
server.py — six MCP servers today. We never label by session_id, pod
name, user, or request URL.
"""
from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager
from typing import AsyncIterator

from aiohttp import web
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest

log = logging.getLogger(__name__)

# Inbound proxy request counters/histogram. The mcp_server label is the
# upstream Kubernetes Service name (without the namespace) — bounded by
# the LISTENERS map in server.py.
proxy_requests_total = Counter(
    "tank_mcp_auth_proxy_requests_total",
    "Inbound proxy requests handled by the mcp-auth-proxy sidecar.",
    ["mcp_server", "status_class"],
)

proxy_upstream_duration_seconds = Histogram(
    "tank_mcp_auth_proxy_upstream_duration_seconds",
    "Wall-clock duration of upstream MCP calls (proxy → MCP server).",
    ["mcp_server"],
    buckets=(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300),
)

# Token read counters. SA token reads happen on every request — a non-
# zero failure rate means kubelet's projected token volume is broken,
# which is a session-pod-level failure mode users see as "MCP suddenly
# stopped working".
sa_token_read_total = Counter(
    "tank_mcp_auth_proxy_sa_token_read_total",
    "Reads of the projected ServiceAccount token file used for MCP bearer auth.",
    ["result"],
)

# GitHub-specific attestation token. Cached and refreshed by the
# orchestrator's /api/internal/github/attestation; this metric tracks
# the local refresh path so we can tell "GitHub MCP is broken because
# the attestation mint failed" apart from "GitHub MCP is broken because
# the upstream server is down".
github_attestation_total = Counter(
    "tank_mcp_auth_proxy_github_attestation_total",
    "Calls to the orchestrator's /api/internal/github/attestation endpoint.",
    ["result"],
)


def _status_class(status: int) -> str:
    if 200 <= status < 300:
        return "2xx"
    if 300 <= status < 400:
        return "3xx"
    if 400 <= status < 500:
        return "4xx"
    if 500 <= status < 600:
        return "5xx"
    return "unknown"


def record_proxy_request(mcp_server: str, status: int) -> None:
    proxy_requests_total.labels(mcp_server=mcp_server, status_class=_status_class(status)).inc()


def record_sa_token_read(result: str) -> None:
    """result is one of: success, failure."""
    sa_token_read_total.labels(result=result).inc()


def record_github_attestation(result: str) -> None:
    """result is one of: success, http_error, exception, invalid_response, cache_hit."""
    github_attestation_total.labels(result=result).inc()


@asynccontextmanager
async def upstream_timer(mcp_server: str) -> AsyncIterator[None]:
    start = time.monotonic()
    try:
        yield
    finally:
        proxy_upstream_duration_seconds.labels(mcp_server=mcp_server).observe(time.monotonic() - start)


async def _handle_metrics(_: web.Request) -> web.Response:
    return web.Response(body=generate_latest(), content_type=CONTENT_TYPE_LATEST.split(";")[0], charset="utf-8")


async def _handle_healthz(_: web.Request) -> web.Response:
    return web.Response(text="ok")


async def start_metrics_server(port: int) -> web.AppRunner:
    app = web.Application()
    app.router.add_get("/metrics", _handle_metrics)
    app.router.add_get("/healthz", _handle_healthz)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "0.0.0.0", port)
    await site.start()
    log.info("mcp-auth-proxy metrics listening on 0.0.0.0:%d", port)
    return runner


__all__ = [
    "record_github_attestation",
    "record_proxy_request",
    "record_sa_token_read",
    "start_metrics_server",
    "upstream_timer",
]
