# Observability Contract

This contract applies to the metric surface, structured logs, admin debug
endpoints, client-side telemetry, Grafana dashboard, and PrometheusRule alerts
that operators use to diagnose every other contracted feature. Specific metric
names, label budgets, and the scrape topology are owned by
[docs/observability.md](../../observability.md); this contract owns the
*user-trust invariant* that the observability surface itself must hold.

## Product Model

Operators diagnosing a user-trust bug are users of this product, not
spectators of it. The observability surface should let an operator localize a
contracted-feature failure from `/metrics`, `/api/debug/*`, structured logs,
and Grafana alone. Escalating to browser devtools, ad-hoc `kubectl exec`
sessions, or live re-debugging is a defect in the observability surface, not a
normal diagnostic step.

This is not a debug aid bolted on after shipping. It is a load-bearing part of
every contracted feature: a feature is not done by the repo's quality bar
until its failure modes are observable from this surface.

## Sources Of Truth

- `/metrics` on each service owns aggregate counters, gauges, and histograms.
  Scraped by the kube-prometheus-stack in the `monitoring` namespace.
- `/api/debug/*` admin endpoints own per-entity diagnostic state (per-session,
  per-stream, per-pod) that violates the cardinality rules if exposed as
  metric labels.
- Structured `slog` lines own per-event diagnostic detail keyed by the exact
  identifier an operator would search for.
- `k8s/templates/grafana-dashboard.yaml` owns the operator's first-stop
  visual interface; the kube-prometheus-stack Grafana sidecar discovers it
  by label.
- `k8s/templates/observability.yaml` owns ServiceMonitor / PodMonitor /
  PrometheusRule resources. Alerts there own the wake-someone-up boundary.
- Browser devtools are not a source of truth and not a supported diagnostic
  surface. Client-side observation Prom-routes through orchestrator
  endpoints, in-app debug pages, or `kubectl`-pullable logs.

## Migration Rules

- Browser devtools (Network, Console, Application panels) must not be in any
  documented diagnostic workflow. Client-side observation must flow through a
  scraped or curl-able orchestrator surface.
- Per-session, per-pod, per-user, per-request, per-turn, and raw-URL labels
  are forbidden in stored metrics. Resolution at those dimensions lives in
  admin-gated `/api/debug/*` endpoints, `slog` lines, and recording-rule
  aggregations, not in label sets.
- A counter, histogram, or alert must not be retired without confirming the
  failure mode it observed is now observable through another path. Orphaned
  PromQL queries in dashboards or alerts are a regression.
- "Add a counter" alone is insufficient evidence for a contracted-feature PR.
  Evidence must show the new surface distinguishes specific failure modes
  named by the affected contract.
- A workflow that requires `kubectl exec` + `wget /metrics` is not a
  supported workflow when Grafana or PromQL exposes the same data; the
  default operational stance is "read it from Prom/Grafana, escalate to
  admin debug endpoints, escalate to logs."
- Old expvar/JSON metric surfaces, ad-hoc debug routes, and unauthenticated
  diagnostic endpoints are deletion targets when a contracted equivalent
  exists. See `scripts/check-removed-chat-runtime.mjs` for the existing
  guard pattern.

## Live Behavior

- Every contracted feature's failure modes are distinguishable from
  `/metrics` + admin endpoints + slog alone — no re-running code, no
  asking a user to inspect their browser.
- Counters and histograms increment under expected steady-state traffic so
  the diagnostic question is "what does the data show," not "is this even
  wired up."
- New PrometheusRule alerts include a runbook annotation that names the
  durable source of truth (the table, the ledger, or the K8s resource) and
  enumerates the candidate root causes the alert distinguishes.
- New admin debug endpoints are admin-gated through the auth.romaine.life
  role claim, are curl-able with a bot token, and embed an inline
  `description` field so an operator reading the JSON understands how to
  interpret each field without leaving the terminal.
- Client-side telemetry that bears on user-trust failures Prom-routes
  through `POST /api/client-metrics/*` endpoints with orchestrator-bucketed
  labels; the SPA never sets metric labels directly.
- Cardinality budgets in `docs/observability.md` are respected for every
  new series.

## Failure And Recovery

- A scrape failure or Grafana outage must not silently disable a
  contract. Counters retain their values at the orchestrator until scraped;
  alerts use `for:` durations long enough to survive a single scrape gap.
- A counter renamed or removed in a code change is paired with dashboard
  and alert updates in the same PR. `helm template` rendering must still
  succeed; the panel and alert names in source must continue to match the
  counter names in code.
- Cardinality blowouts (a `WithLabelValues` call taking an unbounded
  string) must be caught at code review or by a label-validation guard;
  shipping one is a user-trust regression because Prometheus eviction can
  drop unrelated series under memory pressure.
- A removed admin debug endpoint must leave the equivalent diagnostic
  capability reachable through Prom or another endpoint; closing a
  diagnostic surface without a replacement violates the user-trust
  invariant for operators.

## Observability Of Observability

- The kube-prometheus-stack's own `up{job=...}` and `scrape_duration_seconds`
  series tell on the scrape pipeline itself.
- `tank_http_*` and `tank_pg_*` cover the orchestrator's request and
  storage paths, including the routes that serve `/metrics` and the admin
  debug endpoints. A breakage of the observability surface produces
  signals on those baseline counters before any feature alert fires.

## Acceptance Checks

- A contracted-feature PR's Feature Contracts evidence cites at least one
  specific counter, histogram, log line, or admin endpoint per affected
  contract — and the citation maps to a failure mode named in that
  contract's Observability section.
- A new admin debug endpoint is admin-role-gated, returns JSON with an
  inline `description` field, and is documented under
  [docs/observability.md](../../observability.md) → "Per-stream debug
  surface" (or analogous section for its scope).
- A new metric respects the cardinality rules in
  `docs/observability.md` and the help string explains both what the
  metric measures and which failure mode an operator would query for.
- A new PrometheusRule alert names the durable source of truth in its
  runbook annotation and distinguishes at least one specific candidate
  root cause from one other candidate; alerts that fire for "something is
  wrong" without distinguishing causes do not meet the bar.
- An operator can diagnose a contracted-feature failure mode without
  browser devtools and without `kubectl exec` if Grafana plus the admin
  debug endpoints would suffice; the documented workflow follows that
  ordering.
- A removed or renamed metric is paired with the corresponding dashboard
  and alert updates in the same PR. `helm template` and `npm test` both
  pass.
