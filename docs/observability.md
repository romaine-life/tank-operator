# Observability

Tank-operator publishes Prometheus metrics, structured logs on every 5xx,
and a hand-built Grafana dashboard. The metric surface is owned end-to-end
by this repo: orchestrator (Go), pod-side runners (TypeScript), api-proxy
and mcp-auth-proxy (Python). External MCP servers ship their own metrics
in their own repos.

## Surfaces

| Subsystem | Endpoint | Scrape source |
|---|---|---|
| Orchestrator | `GET /metrics` on the orchestrator Service (port 80, named `http`) | `ServiceMonitor/tank-operator` in `k8s/templates/observability.yaml` |
| api-proxy (Claude + Codex) | `GET /metrics` on the ext_proc sidecar port `metrics` (9100) | `ServiceMonitor/tank-api-proxy` |
| Session-pod mcp-auth-proxy | `GET /metrics` on container port `metrics` (9990) | `PodMonitor/tank-session-pods` (endpoint `metrics`) |
| Session-pod agent-runner | `GET /metrics` on container port `runner-metrics` (9095) | `PodMonitor/tank-session-pods` (endpoint `runner-metrics`) |
| Session-pod codex-runner | `GET /metrics` on container port `runner-metrics` (9096) | same PodMonitor |

The kube-prometheus-stack operator in the `monitoring` namespace
auto-discovers all of these. The prior `expvar` JSON surface was deleted
end-to-end in the observability cutover; see
`scripts/check-removed-chat-runtime.mjs` for the migration guard that
blocks reintroduction of the package, its handler, and the old route.

## Metric taxonomy

All metric names are prefixed `tank_`. The full namespace:

- `tank_http_*` — orchestrator request-path metrics (counter +
  duration histogram). Labels: `method`, `route`, `status_class`. The
  duration histogram intentionally omits `status_class` to keep series
  count at routes × methods × 11 buckets ≈ 1000.
- `tank_pg_*` — orchestrator Postgres query tracer (counter + duration
  histogram). Labels: `operation`, `outcome`. `operation` is a bounded
  keyword extracted from the SQL text by `pgstore.operationFromSQL`;
  unmapped SQL falls into `operation="other"` and triggers an alert.
- `tank_nats_*` — orchestrator NATS connection lifecycle counters
  (disconnect, reconnect, async error). No labels.
- `tank_session_event_*` — session-bus + SSE-stream counters and the
  `tank_session_event_stream_lag_seconds` histogram. Same names as the
  prior expvar counters (`_total` suffix added where missing per Prom
  convention).
- `tank_runner_*` — pod-side runner counters/histograms. Single label:
  `mode` ("claude" or "codex"), bound at module import.
- `tank_api_proxy_*` — api-proxy ext_proc counters/histograms. Single
  label: `provider` ("claude" or "codex"), bound from `PROXY_PROVIDER`.
- `tank_mcp_auth_proxy_*` — sidecar counters/histograms. Label
  `mcp_server` is bounded by the LISTENERS table in `server.py`.

## Cardinality rules

These are the rules that keep Prometheus' active-series count bounded
regardless of how many sessions, users, or upstream calls happen:

- **Never label by anything that grows per user, per session, or per
  request.** Forbidden labels (do not add): `pod`, `instance`, `email`,
  `session_id`, `turn_id`, `user`, `request_id`, `provider_item_id`,
  any raw URL path.
- **Status codes are bucketed.** Use the four-value `status_class`
  label (2xx/3xx/4xx/5xx/unknown), never the full numeric status code.
- **Routes are matched-pattern, not raw URLs.** The HTTP middleware
  reads `http.Request.Pattern` (Go 1.22+ ServeMux), which gives one
  series per registered route, not per `session_id` substitution.
- **PG operations are an allowlist.** New tables added to
  `backend-go/internal/pgstore/tracer.go:knownTables` are the only way
  to label new operations. Anything unmapped lands in `operation="other"`
  and triggers `TankPgUnmappedOperation`.
- **Histograms use minimal labels.** The HTTP duration histogram is
  labeled by `{method, route}` only — 4× series cost of adding
  `status_class` is not worth the operational signal.

## 5xx logging

Every 5xx response from the orchestrator emits a structured `slog.Error`
with `method`, `route`, `status`, `duration_ms`, `email` (when the
handler authenticated), and the response body's `detail` field. The
middleware lives in `backend-go/cmd/tank-operator/middleware_http.go`;
the `attachAuthToRequest` hook in `requireAuth` is what plumbs the
caller's email through the per-request metadata struct so the log line
carries who saw the failure.

A 5xx with no auth context will still log `method`, `route`, `status`,
and `detail` — useful for unauthenticated probes that 500 anyway.

The middleware also serves as the contract: a handler that wants its
500 logged with extra context should write the detail string into
`writeError`'s message argument (it ends up in the response body, which
the middleware extracts).

## Alerts

`PrometheusRule/tank-operator` in `k8s/templates/observability.yaml`
declares one rule group per subsystem:

- **HTTP**: 5xx rate, Postgres p99 latency, unmapped operations.
- **Session bus**: schema-rejected events (steady-state must be zero),
  wake-publish failures.
- **NATS**: disconnect storm (>6/min for 5m).
- **api-proxy**: upstream 401 rate (refresh-storm signature), refresh
  failures (any non-success result).
- **mcp-auth-proxy**: SA token read failures, MCP upstream 5xx rate.
- **Runners**: provider error rate, pending wakeup queue depth.

Severity is `info` for "diagnostic-only, page nobody", `warning` for
"a user feature is degraded", `critical` for "user-trust is on the line"
(refresh-chain dead, schema-rejected events). AlertManager routing
lives in the kube-prometheus-stack config, not in this chart.

## Dashboard

`k8s/templates/grafana-dashboard.yaml` ships a ConfigMap discovered by
the Grafana sidecar via the `grafana_dashboard: "1"` label. Panels:
HTTP request rate, 5xx rate by route, HTTP latency p50/p95/p99,
Postgres rate/latency, session-event persister failures, NATS
connection events, api-proxy refresh outcomes + 401 rate,
mcp-auth-proxy request rate + SA token failures + GitHub attestation,
runner turn duration + commands consumed.

The dashboard is hand-built JSON. If panel count grows past ~20 we
should migrate to grafonnet — out of scope today.

## Cost / scaling

On the current cluster (3 × Standard_B2s, kube-prometheus-stack
deployed with default resource requests):

- Active series added by this surface: ~4k. Most are HTTP histogram
  buckets (30 routes × 3 methods × 11 buckets ≈ 1000) and the PG
  histogram buckets.
- Prometheus RAM cost: ~50–100MB on the existing 1Gi limit.
- Scrape network: ~5KB/s aggregate at 30s intervals.

At ~50 concurrent session pods, the PodMonitor adds another ~1.5k
series. At ~500 concurrent sessions Prometheus would push past its
current 1Gi RAM limit — that's the scaling cliff that triggers the
migration to managed Prometheus (Azure Monitor Workspace).

## Adding a new metric

1. Decide the label set up front. Apply the cardinality rules above;
   never use a label that grows per session/user/request.
2. Register the metric next to its peers in the appropriate file:
   - Orchestrator: `backend-go/cmd/tank-operator/observability.go`
   - Runners: `agent-runner/src/metrics.ts` and `codex-runner/src/metrics.ts`
   - Python services: `tank_api_proxy/metrics.py` or
     `mcp_auth_proxy/metrics.py`
3. Add a Grafana panel if it's worth seeing on the dashboard.
4. Add a PrometheusRule alert if it represents a user-trust failure.
5. If it's a Postgres query against a new table, add the table name to
   `pgstore.knownTables` so the operation label resolves to the table
   instead of `"other"`.
