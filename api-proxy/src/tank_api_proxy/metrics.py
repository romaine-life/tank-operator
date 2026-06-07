"""Prometheus instrumentation for the api-proxy ext_proc service.

The api-proxy is deployed once per managed provider (Claude, Codex, and
Antigravity). The deployments share this code and the ``provider`` label
distinguishes their metrics at scrape time so Grafana can show them on one
dashboard.

Cardinality discipline matches the orchestrator's: bounded labels only.
``provider`` has a bounded value set; ``result`` is a small enum;
``status_class`` is the 2xx/3xx/4xx/5xx bucket. No per-call labels
(request_id, user, etc.).

The HTTP listener is separate from the gRPC ext_proc listener so the
kube-prometheus-stack ServiceMonitor can scrape /metrics independently
without colliding with the ext_proc port.
"""
from __future__ import annotations

import asyncio
from contextlib import suppress
import json
import logging
import os
from typing import Any, Awaitable, Callable

from aiohttp import web
import httpx
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Gauge, Histogram, generate_latest

log = logging.getLogger(__name__)

# Provider label is bound at module import time from PROXY_PROVIDER, the
# same env var server._config_from_env reads to choose credentials and
# token URL. Values match the server: "claude" or "codex". Setting it
# once avoids threading the label through every call site and keeps the
# series partitioned by deployment.
PROVIDER = (os.environ.get("PROXY_PROVIDER") or "claude").strip().lower() or "claude"

ext_proc_requests_total = Counter(
    "tank_api_proxy_requests_total",
    "Inbound request_headers messages handled by the ext_proc service.",
    ["provider", "outcome"],
)

upstream_status_total = Counter(
    "tank_api_proxy_upstream_status_total",
    "Upstream :status responses observed via response_headers.",
    ["provider", "status_class"],
)

upstream_401_total = Counter(
    "tank_api_proxy_upstream_401_total",
    "Upstream 401 responses on injected requests (the refresh-storm signature).",
    ["provider"],
)

upstream_429_total = Counter(
    "tank_api_proxy_upstream_429_total",
    "Upstream 429 rate-limit responses on injected requests (the provider "
    "rate-limit signature). A sustained rate is the shared account's usage "
    "cap being exhausted; pod-side runners convert a stuck rate-limit retry "
    "into a durable turn.failed{reason:provider_rate_limit}.",
    ["provider"],
)

token_refresh_total = Counter(
    "tank_api_proxy_token_refresh_total",
    "Token refresh attempts against the upstream OAuth endpoint.",
    ["provider", "result"],
)

refresh_duration_seconds = Histogram(
    "tank_api_proxy_refresh_duration_seconds",
    "Wall-clock duration of a token refresh attempt.",
    ["provider", "result"],
    buckets=(0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30),
)

kv_persist_total = Counter(
    "tank_api_proxy_kv_persist_total",
    "Attempts to write a rotated blob back to Key Vault.",
    ["provider", "result"],
)

single_flight_waits_total = Counter(
    "tank_api_proxy_single_flight_waits_total",
    "Callers that awaited an already-in-flight refresh task instead of "
    "starting a new one (the single-flight dedupe success path).",
    ["provider"],
)

envoy_sds_ssl_context_updates = Gauge(
    "tank_api_proxy_envoy_sds_ssl_context_updates",
    "Current Envoy downstream SSL context updates performed through SDS, "
    "re-exported from the localhost Envoy admin stats endpoint.",
    ["provider"],
)

envoy_sds_key_rotation_failed = Gauge(
    "tank_api_proxy_envoy_sds_key_rotation_failed",
    "Current Envoy SDS filesystem key rotation failures by SDS secret, "
    "re-exported from the localhost Envoy admin stats endpoint.",
    ["provider", "secret"],
)

envoy_sds_stats_scrape_total = Counter(
    "tank_api_proxy_envoy_sds_stats_scrape_total",
    "Attempts by the ext_proc metrics sidecar to scrape Envoy SDS stats from "
    "the pod-local admin listener.",
    ["provider", "result"],
)


def record_ext_proc_request(outcome: str) -> None:
    """outcome is one of: injected, passthrough, missing_token."""
    ext_proc_requests_total.labels(provider=PROVIDER, outcome=outcome).inc()


def record_upstream_status(status: int | None) -> None:
    if status is None:
        return
    bucket = _status_class(status)
    upstream_status_total.labels(provider=PROVIDER, status_class=bucket).inc()
    if status == 401:
        upstream_401_total.labels(provider=PROVIDER).inc()
    if status == 429:
        upstream_429_total.labels(provider=PROVIDER).inc()


def record_refresh(result: str, duration_seconds: float | None = None) -> None:
    """result is one of: success, http_error, request_failed, no_refresh_token."""
    token_refresh_total.labels(provider=PROVIDER, result=result).inc()
    if duration_seconds is not None:
        refresh_duration_seconds.labels(provider=PROVIDER, result=result).observe(duration_seconds)


def record_kv_persist(result: str) -> None:
    """result is one of: success, failure, skipped."""
    kv_persist_total.labels(provider=PROVIDER, result=result).inc()


def record_single_flight_wait() -> None:
    single_flight_waits_total.labels(provider=PROVIDER).inc()


def record_envoy_sds_stats(stats_text: str) -> None:
    """Re-export bounded Envoy SDS counters from /stats text output.

    Envoy admin stays bound to 127.0.0.1 for safety; this sidecar polls it
    inside the pod and publishes the small SDS subset through the already
    scraped /metrics surface.
    """
    ssl_context_updates = 0.0
    key_rotation_failures: dict[str, float] = {}

    for raw_line in stats_text.splitlines():
        if ":" not in raw_line:
            continue
        name, raw_value = raw_line.split(":", 1)
        name = name.strip()
        try:
            value = float(raw_value.strip())
        except ValueError:
            continue
        if name.endswith(".ssl_context_update_by_sds"):
            ssl_context_updates += value
            continue
        if name.startswith("sds.") and name.endswith(".key_rotation_failed"):
            parts = name.split(".")
            if len(parts) >= 3:
                key_rotation_failures[parts[1]] = value

    envoy_sds_ssl_context_updates.labels(provider=PROVIDER).set(ssl_context_updates)
    for secret, value in key_rotation_failures.items():
        envoy_sds_key_rotation_failed.labels(provider=PROVIDER, secret=secret).set(value)


async def _poll_envoy_sds_stats(admin_url: str, interval_seconds: float) -> None:
    stats_url = admin_url.rstrip("/") + "/stats"
    params = {"filter": "sds|ssl_context_update_by_sds"}
    async with httpx.AsyncClient(timeout=2.0) as client:
        while True:
            try:
                response = await client.get(stats_url, params=params)
                response.raise_for_status()
                record_envoy_sds_stats(response.text)
                envoy_sds_stats_scrape_total.labels(provider=PROVIDER, result="success").inc()
            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("envoy SDS stats scrape failed")
                envoy_sds_stats_scrape_total.labels(provider=PROVIDER, result="failure").inc()
            await asyncio.sleep(interval_seconds)


async def _cleanup_envoy_sds_task(app: web.Application) -> None:
    task = app.get("envoy_sds_task")
    if not isinstance(task, asyncio.Task):
        return
    task.cancel()
    with suppress(asyncio.CancelledError):
        await task


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


async def _handle_metrics(_: web.Request) -> web.Response:
    return web.Response(body=generate_latest(), content_type=CONTENT_TYPE_LATEST.split(";")[0], charset="utf-8")


async def _handle_healthz(_: web.Request) -> web.Response:
    return web.Response(text="ok")


HealthSnapshotProvider = Callable[[], dict[str, Any]]
UsageSnapshotProvider = Callable[[], Awaitable[dict[str, Any]]]


def _make_health_handler(snapshot: HealthSnapshotProvider) -> Callable[[web.Request], web.Response]:
    """Wrap a health-snapshot callable as an aiohttp handler.

    The orchestrator polls this endpoint every 30s. Schema:
        {
          "provider": "codex" | "claude",
          "result": "success" | "http_error" | "request_failed" | ...,
          "reason": "refresh_token_reused" | "" | ...,
          "text": "Sign-in expired. ...",
          "last_attempted_at": <unix-seconds float | null>,
          "last_succeeded_at": <unix-seconds float | null>,
          "attempt_id": <int>
        }
    Snapshot exceptions degrade to 503 — the orchestrator treats
    503s as "skip this poll cycle" so a momentary proxy issue doesn't
    flip the transcript banner.
    """
    def handler(_: web.Request) -> web.Response:
        try:
            payload = snapshot()
        except Exception:
            log.exception("health snapshot failed")
            return web.Response(status=503, text="snapshot unavailable")
        return web.Response(
            body=json.dumps(payload),
            content_type="application/json",
        )
    return handler


def _make_usage_handler(snapshot: UsageSnapshotProvider) -> Callable[[web.Request], Awaitable[web.Response]]:
    async def handler(_: web.Request) -> web.Response:
        try:
            payload = await snapshot()
        except Exception:
            log.exception("usage snapshot failed")
            return web.Response(status=503, text="usage unavailable")
        status = 200 if payload.get("status") == "ok" else 502
        return web.Response(
            status=status,
            body=json.dumps(payload),
            content_type="application/json",
        )
    return handler


async def start_metrics_server(
    port: int,
    health_snapshot: HealthSnapshotProvider | None = None,
    usage_snapshot: UsageSnapshotProvider | None = None,
    envoy_admin_url: str | None = None,
    envoy_sds_poll_interval_seconds: float = 30.0,
) -> web.AppRunner:
    app = web.Application()
    app.router.add_get("/metrics", _handle_metrics)
    app.router.add_get("/healthz", _handle_healthz)
    if health_snapshot is not None:
        # Route is /health/<provider> so a Grafana dashboard or a
        # multi-provider orchestrator can disambiguate which deployment
        # answered without parsing the body. The shape matches the
        # transcript-surfaced banner contract documented on tank-operator's
        # side (docs/features/transcript/contract.md).
        app.router.add_get(f"/health/{PROVIDER}", _make_health_handler(health_snapshot))
    if usage_snapshot is not None:
        app.router.add_get(f"/usage/{PROVIDER}", _make_usage_handler(usage_snapshot))
    if envoy_admin_url:
        app["envoy_sds_task"] = asyncio.create_task(
            _poll_envoy_sds_stats(envoy_admin_url, envoy_sds_poll_interval_seconds)
        )
        app.on_cleanup.append(_cleanup_envoy_sds_task)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, host="0.0.0.0", port=port)
    await site.start()
    log.info("api-proxy metrics listening on 0.0.0.0:%d (provider=%s)", port, PROVIDER)
    return runner


__all__ = [
    "PROVIDER",
    "record_ext_proc_request",
    "record_envoy_sds_stats",
    "record_kv_persist",
    "record_refresh",
    "record_single_flight_wait",
    "record_upstream_status",
    "start_metrics_server",
]
