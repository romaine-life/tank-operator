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
- `tank_stream_auth_ticket_total` — browser EventSource stream-ticket
  create/validate attempts. Labels: `operation` (`create`, `validate`),
  `stream` (`session-list`, `session-events`), and bounded `result`.
  Store failures are the signature where REST timeline reads still work
  but live SSE streams never open.
- `tank_avatar_upload_attempts_total` — admin avatar upload attempts
  labeled by bounded server-side `stage` and `result`. This distinguishes
  wrong HTTP upload shape (`parse_multipart`), invalid metadata
  (`validate_*`), image field failures (`read_avatar` / `read_backing`),
  blob-storage failures (`store_*`), and metadata-write failures
  (`create_metadata`) without using browser devtools.
- `tank_admin_debug_session_event_ledger_reads_total{result}` — admin
  reads of `GET /api/debug/session-event-ledger` (the durable
  `session_events` audit surface that bypasses the registry visibility
  gate). Bounded `result` labels: `ok`, `empty`, `bad_request`,
  `forbidden`, `store_error`, `not_configured`. `empty` is its own
  label so a wave of misdirected lookups (wrong scope, wrong id) is
  visible without grepping the audit slog line.
- `tank_chat_scroll_client_*` - browser-reported transcript scroll
  diagnostics ingested through `POST /api/client-metrics/chat-scroll`.
  Labels are server-bucketed only: `event`, `surface`, `session_mode`,
  `at_bottom`, and `has_scroll_parent`. The endpoint never exposes
  `session_id`, email, raw route paths, or user-supplied event names as
  labels; unknown values collapse to `other` / `unknown`.
- `tank_session_list_debug_capture_reports_total{result,reason}` —
  browser-reported session-list debug captures ingested through
  `POST /api/client-metrics/session-list-debug-capture`. The SPA sends
  a bounded `/_debug/session-list` snapshot only when a session created
  in the current tab later mutates client-side identity fields.
  `reason` is a closed enum and unknown values collapse to `other`; the
  metric never labels by owner, session id, path, or raw user input.
- `tank_admin_debug_session_list_capture_reads_total{result}` — admin
  reads of `GET /api/debug/session-list-captures`, the durable capture
  store for client-side session-list anomalies. Captures are retained at
  the latest 200 records per owner/scope.
- `tank_session_event_wake_published_total` /
  `tank_session_event_wake_received_total` /
  `tank_session_event_persist_to_wake_seconds` — the per-session SSE
  wake fabric stethoscope. Published vs received delta over the same
  window is the candidate-A wake-key-mismatch signature (the persister
  is publishing to a subject the SSE subscriber is not listening for).
  All unlabeled aggregates per the cardinality rules below; per-stream
  resolution lives in `GET /api/debug/session-event-streams`
  (admin-only) and in the persister's `slog.Info("session event
  persister wake published", subject=..., storage_key=...,
  event_type=..., order_key=..., tank_session_id=...)` line.
- `tank_session_event_stream_emitted_by_type_total{event_type}` —
  per-Tank-event-type counter paired with
  `tank_session_event_client_received_total{event_type, session_mode}`.
  Divergence is the candidate-C reducer-drop signature (server emitted,
  browser didn't render). `event_type` is the closed enum from
  `internal/conversation/types.go`; unknown shapes collapse to
  `other`.
- `tank_session_event_client_*` — browser-reported per-session SSE
  stream diagnostics ingested through
  `POST /api/client-metrics/session-events-stream`. Bounded labels:
  `event` (opened, ready, tank_event_received,
  stream_silent_while_running, terminal_matched_by_turn_id,
  terminal_local_run_mismatch, queued_followup_blocked_after_terminal,
  resync_required, stream_error, closed_unmount, closed_error,
  reconnect_scheduled),
  `session_mode`, and on the `_received_total` variant `event_type`.
  The `_stream_silent_seconds{session_mode}` histogram is the
  candidate-B zombie-SSE detector: the browser's silence watchdog
  observes the idle interval whenever a connected stream has gone
  >30 s without emitting events while a turn is in flight.
- `tank_turn_terminal_missing_client_nonce_total{source,event_type}` —
  durable turn terminal rows (`turn.completed`, `turn.failed`,
  `turn.command_failed`, `turn.interrupted`) persisted without
  `client_nonce`. This catches the contract violation where the server
  lifecycle is closed, so silent-stranding does not fire, but an
  already-open browser tab cannot correlate the terminal to its local
  run latch and may keep follow-up input queued until refresh.
- `tank_turn_interrupt_request_total` — counter of stop requests posted
  to `/interrupt`. Single label `outcome` with three bounded values:
  `persisted`, `persist_failed`, `publish_failed`. Steady-state
  expectation: `persisted` dominates; the two failure outcomes are
  alerted on `> 0` rate. Owned by the durable `turn.interrupt_requested`
  migration — see `docs/tank-conversation-protocol.md` for the boundary
  contract. Paired with the pod-side
  `tank_runner_commands_consumed_total{kind="interrupt_turn"}` and
  `tank_runner_turn_duration_seconds_count{outcome="interrupted"}`
  series to drive the `TankStopNotDelivered` / `TankStopNotTerminated`
  self-telling alerts (see Alerts § below).
- `tank_runner_*` — pod-side runner counters/histograms. The default
  `mode` label is "claude" or "codex", bound at module import.
  `tank_runner_item_outcome_total{outcome,reason}` counts bounded item
  classifications emitted by runner adapters: `ok`, `result_failed`, and
  `execution_failed`. `tank_runner_provider_control_total{action,outcome}`
  counts bounded provider control calls, including Claude foreground-task
  backgrounding before interrupt and the interrupt signal itself.
- `tank_session_runtime_config_update_total` - pod-side runner reports of
  the model/effort actually applied to the provider runtime. Labels:
  `provider` (`claude`, `codex`, `unknown`) and bounded `result`.
- `tank_api_proxy_*` — api-proxy ext_proc counters/histograms. Single
  label: `provider` ("claude" or "codex"), bound from `PROXY_PROVIDER`.
- `tank_mcp_auth_proxy_*` — sidecar counters/histograms. Label
  `mcp_server` is bounded by the LISTENERS table in `server.py`.

## Scripted access via Grafana

Grafana's `[auth.jwt]` block validates the `auth.romaine.life` JWKS,
so any auth.romaine.life bearer token (including the admin bot-token
from `CLAUDE.md` → "Break-glass CLI auth") works directly as an
`Authorization` header on Grafana's REST API. The datasource proxy
exposes the kube-prometheus-stack's Prometheus and Alertmanager
without `kubectl port-forward`:

```
# firing alerts (the canonical first stop in diagnostic-discipline.md step 3)
curl -H "Authorization: Bearer $JWT" \
  https://grafana.romaine.life/api/datasources/proxy/uid/prometheus/api/v1/alerts

# arbitrary PromQL
curl -H "Authorization: Bearer $JWT" \
  "https://grafana.romaine.life/api/datasources/proxy/uid/prometheus/api/v1/query?query=tank_session_event_client_events_total"

# Alertmanager-side view (silences, groupings)
curl -H "Authorization: Bearer $JWT" \
  https://grafana.romaine.life/api/datasources/proxy/uid/alertmanager/api/v2/alerts
```

This is the default scripted-access path; `kubectl exec` +
`wget /metrics` is reserved for the operator-only failure mode where
Grafana itself is down, per
`features/observability/contract.md → "Migration Rules"`. The single
auth surface means an agent investigating a runtime bug does not need
a separate Grafana SA token, KV secret, or device-flow handshake —
the same bot token that curls `tank.romaine.life/api/sessions/...`
curls `grafana.romaine.life/api/datasources/...`.

## Per-stream debug surface

`GET /api/debug/session-event-streams` (admin-only) returns the
in-memory snapshot of every open `/api/sessions/{id}/events` SSE
handler on the queried orchestrator replica. Each stream row carries
`opened_at`, `last_wake_at`, `last_wake_subject`, `last_page_read_at`,
`last_emit_at`, `last_emit_order_key`, `last_emit_event_type`,
`cursor_after_order_key`, `wakes_received`, `pages_read_empty`,
`pages_read_non_empty`, `emits_total`, and `heartbeats_sent`. The
endpoint accepts a `?session_id=<id>` filter for focused diagnosis.

Pair it with the Prometheus counters above: if the durable ledger has
new rows for a session but the matching stream's `wakes_received`
counter stays at 0, the persister is publishing to a subject the
subscriber is not listening for (candidate A); if `wakes_received`
climbs but `emits_total` stays flat, the page read returned nothing
(candidate B for read-replica visibility lag, or the cursor jumped
past pending rows); if `emits_total` climbs on the server but the
matching browser's `tank_session_event_client_received_total` stays
flat, the SPA reducer is dropping (candidate C).

## Avatar Upload Debug Surface

`GET /api/debug/avatar-upload-attempts` (admin-only) returns durable
diagnostic rows for `POST /api/admin/avatars`. A failed avatar upload
response includes `attempt_id`; pass it as
`?attempt_id=<attempt_id>` to retrieve the exact server-side stage,
bounded result, request content-type classification, parsed file-field
summaries, and safe parser/store diagnostics. Without `attempt_id`,
the endpoint returns the most recent attempts, capped by `?limit=`
(maximum 100).

This endpoint is the no-devtools path for avatar upload failures. The
user-visible form error gives the reference id; the operator queries
the debug endpoint and then checks
`tank_avatar_upload_attempts_total{stage,result}` to see whether the
same failure is isolated or systemic. Parser failures are usually a
client request-shape bug; `read_*` failures usually mean the client
sent the wrong field name, empty bytes, an unsupported MIME type, or a
file over the configured limit; `store_*` and `create_metadata`
failures point at blob storage or Postgres.

For browser-independent reproduction, `scripts/debug-avatar-upload.sh`
posts the same multipart contract with `curl -F` and prints the raw
API response, including `attempt_id` on success or failure.

## Session Event Ledger Debug Surface

`GET /api/debug/session-event-ledger` (admin-only) returns events from
the durable `session_events` Postgres table for one tank session,
bypassing the registry visibility gate. Use this when picking up work
from a session whose `sessions.visible` row is `false` (the user
deleted it through the SPA) and whose pod is gone — the user-facing
`GET /api/sessions/{id}/timeline` returns 404 in that case by design,
but the underlying `session_events` rows are durable (no FK, no
cascade) and recoverable through this surface.

Query params:

- `session_id` (required) — public session id (e.g., `203`).
- `session_scope` — defaults to this orchestrator's scope (`default`
  in prod). Admin power can target a different scope.
- `limit` — page size, default 200, max 500.
- `after_order_key` — forward-paginate ASC strictly after this key.
- `before_order_key` — backward-paginate DESC strictly before this
  key. At most one of `after_order_key` / `before_order_key`.
- `direction` — `asc` (default) or `desc`. `desc` with no cursor
  returns the tail (latest events).

Response: `events[]` is always ASC by `order_key`. `has_more`,
`found_oldest`, and `found_newest` let the caller decide when to stop
paginating. `storage_key` is the underlying partition key (`scope:
session_id`) for cross-referencing the raw `session_events` table.

Standard workflow for "pick up a deleted session":

1. `GET /api/debug/session-list-state?owner=<email>` to locate the
   `visible=false` row and confirm the session id.
2. `GET /api/debug/session-event-ledger?session_id=<id>` to read the
   chat. Page with `after_order_key=<next_order_key>` if needed.

This is the admin counterpart to the deliberate visibility gate on the
user-facing timeline (the SPA tombstones soft-deleted sessions; an
admin pickup-the-prior-codex-pod workflow shouldn't have to
un-soft-delete or open a one-off `psql` pod to recover the chat).

Counts as an admin cross-user audit read. Emits a structured `slog`
line per call (`caller_email`, `session_id`, `session_scope`,
`limit`, cursor fields, `result`, `count`) and increments
`tank_admin_debug_session_event_ledger_reads_total{result}` at
`/metrics`. `result` labels: `ok`, `empty`, `bad_request`,
`forbidden`, `store_error`, `not_configured`.

## Session List Capture Debug Surface

`GET /api/debug/session-list-captures` (admin-only) returns durable
browser-side session-list captures posted by
`POST /api/client-metrics/session-list-debug-capture`. Each record
contains the captured client snapshot, the anomaly detail, and the
server registry rows at ingest time.

Standard workflow for "new session showed another session's name or
avatar":

1. Ask the user to reproduce in a fresh tab or session normally. They
   do not need to open `/_debug/session-list` at failure time.
2. Read `GET /api/debug/session-list-captures?owner=<email>&limit=10`
   and inspect the latest capture for the new session id and reason.
3. Compare the captured browser `snapshot` and `detail.observed` row
   with `server_rows` recorded at ingest time. If `server_rows` is
   stable while the browser snapshot shows the wrong `name` or avatar
   id, the bug is in the client store/render/avatar resolution layer.
   If both disagree with the create response, the bug is server-side.

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
- **Hermes bridge**: startup capability probe failures, `/v1/runs`
  create failures, and translator schema drift. The bridge also emits
  `tank_hermes_run_event_total{event_type}` and
  `tank_hermes_run_duration_seconds{terminal}` with bounded labels; the
  durable recovery pointer is `sessions.hermes_active_run`.
- **Avatar uploads**: sustained parse/read/validate failures and any
  storage/metadata failures. The runbook starts from
  `GET /api/debug/avatar-upload-attempts?attempt_id=...`, using the
  reference emitted in the UI error.
- **Session bus / live transport**: schema-rejected events (steady-state
  must be zero), wake-publish failures, stream auth ticket store failures,
  terminal events missing `client_nonce`, browser terminal/local-run
  mismatches, browser queued-followup-after-terminal reports, and
  `turn.interrupt_requested` persist/publish failures (the durable stop
  boundary; non-zero rate means stops are losing durability or never
  reaching the runner).
- **Stop chain self-telling**: `TankStopNotDelivered` fires if the
  backend persists Stop requests faster than runners' control-plane
  consumer claims `interrupt_turn` commands (the data/control plane
  split has regressed — interrupts are queueing somewhere). 
  `TankStopNotTerminated` fires if runners claim interrupts faster than
  they emit terminal `turn.interrupted` events (the SDK / codex Thread
  is ignoring the abort, or the terminal-event publish is failing).
  Both are `critical` — Stop is a user-trust control surface and a
  silent regression here is exactly the failure mode that necessitated
  the control-plane split. See
  `docs/tank-conversation-protocol.md` → "Durable turn interruption"
  for the architecture they protect.
- **NATS**: disconnect storm (>6/min for 5m).
- **api-proxy**: upstream 401 rate (refresh-storm signature), refresh
  failures (any non-success result).
- **mcp-auth-proxy**: SA token read failures, MCP upstream 5xx rate.
- **Runners**: provider error rate, pending wakeup queue depth.
- **Session spawn**: median spawn time across the trailing 24h (image
  distribution health), and any single-spawn outlier above 60s in the
  trailing hour. Backed by recording rules
  `tank:session_pod_spawn_seconds:p50_24h`,
  `tank:session_pod_spawn_seconds:p95_24h`, and
  `tank:session_pod_spawn_seconds:max_1h`, derived from
  `kube_pod_status_container_ready_time - kube_pod_created` on every
  session-pod namespace — the production namespace
  (`tank-operator-sessions`) plus every test-slot sessions namespace
  (`tank-operator-slot-<N>-sessions`). Recording rules collapse the
  per-pod series to single scalars so the forbidden-labels rule above
  is respected for stored series; the per-pod dashboard panel renders
  the same primitive on-demand, labeled by `namespace/pod` so slot vs
  production pods are distinguishable. The two failure modes worth
  separating: image distribution (cold pulls cluster around 27-33s)
  and cluster CPU/memory request packing (FailedScheduling, pods sit
  Pending for minutes). The latter typically only shows up in the
  outlier alert (`max_1h > 60s`) since one stuck pod doesn't move the
  median.

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
runner turn duration + commands consumed, Hermes run volume/event mix,
and Hermes run duration.

The "Session spawn" row at the bottom of the dashboard is the
diagnostic surface for "why did my session take N seconds to start."
Four panels: aggregate p50/p95 over 24h, per-pod spawn duration (one
line per recent session pod), big-image pull rate by node (kubelet
metric, 500MB-1GB bucket — the three session images all land in that
bucket), and an inferred warm-spawn ratio stat (fraction of recent
spawns under 10s, which sits between the warm-cache cluster at 2-3s
and the cold-pull cluster at 27-33s).

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
