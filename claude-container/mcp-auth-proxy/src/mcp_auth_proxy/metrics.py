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

# auth.romaine.life service-principal exchange. Cached and refreshed by
# the sidecar; this metric tracks the local exchange path so operators
# can tell "spawn_service_session is broken because exchange mint
# failed" apart from "...because tank-operator rejected the JWT". See
# romaine-life/tank-operator#486.
auth_romaine_exchange_total = Counter(
    "tank_mcp_auth_proxy_auth_romaine_exchange_total",
    "Calls to auth.romaine.life's /api/auth/exchange/k8s endpoint.",
    ["result"],
)

# Per-attempt retry counter. Outcomes:
#   - transport_error: aiohttp.ClientError on the upstream call
#   - transient_status: upstream returned 502 / 503 / 504
#   - exhausted: all attempts failed; surfaced to the SDK as JSON-shaped 502
# Non-zero rate is expected during normal upstream pod rotations; a
# sustained high rate with no exhaustion means the retry budget is
# masking a real upstream regression that operators should still see.
proxy_retries_total = Counter(
    "tank_mcp_auth_proxy_retries_total",
    "Per-attempt retries the mcp-auth-proxy ran against an upstream MCP server.",
    ["mcp_server", "outcome"],
)

# Restricted-git decisions on the mcp-github write-tool denylist. In restricted
# mode the proxy blocks write-capable GitHub tools, but a *read-only*
# mint_clone_token call (no write/workflows/full) is allowed through so `gh` and
# `git fetch`/`clone` keep working for reads — the same capability the session
# already has via the GitHub read MCP tools. This counter makes the carve-out
# auditable: a spike in allowed_read_only is the read path doing its job; any
# blocked count is a write attempt that correctly hit the governed-path wall.
# Cardinality is bounded by the fixed denylist (≤6 tools) × 2 decisions.
github_write_tool_decision_total = Counter(
    "tank_mcp_auth_proxy_github_write_tool_decision_total",
    "Restricted-git decisions on mcp-github write-denylist tools.",
    ["tool", "decision"],
)


# Branch-lane grant brokered-path outcomes. The in-pod break-glass server's
# /push-head (git pre-push hook) and /pr-write (gh wrapper) routes broker
# governed pushes and PR-own writes server-side for an approved break-glass
# grant, scope-enforced. The `result` label is the load-bearing signal: a
# SUCCESS also lands a github.break_glass.* row in Tank's durable control-action
# ledger, but a REFUSAL (no_grant, branch_out_of_scope, dirty, detached, ...)
# writes no ledger row at all — so without these counters a firing scope
# boundary, or a wave of agents tripping on the no-grant wall, is invisible to
# operators. Named tank_break_glass_* (not the tank_mcp_auth_proxy_* sidecar
# prefix) so the whole branch-lane feature — these plus the orchestrator's
# tank_break_glass_retired_path_total — dashboards and alerts as one family.
# Cardinality is bounded: `result` is drawn from the fixed _pr_write_refusal
# reason set plus {succeeded}; `action` on pr_write is the 4 gh PR verbs.
break_glass_push_total = Counter(
    "tank_break_glass_push_total",
    "Brokered governed branch pushes via the in-pod /push-head route, by outcome.",
    ["result"],
)

break_glass_pr_open_total = Counter(
    "tank_break_glass_pr_open_total",
    "Brokered branch-lane draft-PR opens via the in-pod /pr-write route, by outcome.",
    ["result"],
)

break_glass_pr_write_total = Counter(
    "tank_break_glass_pr_write_total",
    "Brokered branch-lane PR-own writes (edit/ready/comment) via /pr-write, by outcome.",
    ["result"],
)


def record_break_glass_push(result: str) -> None:
    """result is succeeded, error, or a _pr_write_refusal reason (no_grant,
    branch_out_of_scope, dirty, detached, bad_repo_path, bad_origin,
    repo_mismatch)."""
    break_glass_push_total.labels(result=result).inc()


def record_break_glass_pr_open(result: str) -> None:
    """result is succeeded, error, or a _pr_write_refusal reason (no_grant,
    branch_out_of_scope, bad_repo, bad_action, missing_head, ...)."""
    break_glass_pr_open_total.labels(result=result).inc()


def record_break_glass_pr_write(result: str) -> None:
    """result is succeeded, error, or a _pr_write_refusal reason (no_grant,
    branch_out_of_scope, missing_pr_number, pr_not_found, nothing_to_edit,
    missing_comment, ...)."""
    break_glass_pr_write_total.labels(result=result).inc()


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


def record_auth_romaine_exchange(result: str) -> None:
    """result is one of: success, http_error, exception, invalid_response, cache_hit."""
    auth_romaine_exchange_total.labels(result=result).inc()


def record_proxy_retry(mcp_server: str, outcome: str) -> None:
    """outcome is one of: transport_error, transient_status, exhausted."""
    proxy_retries_total.labels(mcp_server=mcp_server, outcome=outcome).inc()


def record_github_write_tool_decision(tool: str, decision: str) -> None:
    """decision is one of: blocked, allowed_read_only."""
    github_write_tool_decision_total.labels(tool=tool, decision=decision).inc()


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
    "record_auth_romaine_exchange",
    "record_break_glass_pr_open",
    "record_break_glass_pr_write",
    "record_break_glass_push",
    "record_github_write_tool_decision",
    "record_proxy_request",
    "record_sa_token_read",
    "start_metrics_server",
    "upstream_timer",
]
