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
- `tank_session_transcript_invisible_row_reads_total` — authorized
  projected transcript reads requested against `sessions.visible=false` rows.
  This counts copied-link or MCP recovery after sidebar deletion without
  logging session ids or message contents.
- `tank_admin_debug_session_event_ledger_reads_total{result}` — admin
  reads of `GET /api/debug/session-event-ledger` (the durable
  `session_events` raw audit surface for cases where projected transcript
  rows are not enough). Bounded `result` labels: `ok`, `empty`, `bad_request`,
  `forbidden`, `store_error`, `not_configured`. `empty` is its own
  label so a wave of misdirected lookups (wrong scope, wrong id) is
  visible without grepping the audit slog line.
- `tank_admin_debug_conversation_read_state_reads_total{result}` —
  admin reads of `GET /api/debug/conversation-read-state` (the
  per-session, per-owner read-cursor + activity-summary diagnostic
  surface). Bounded `result` labels: `ok`, `empty`, `bad_request`,
  `forbidden`, `store_error`, `not_configured`. Pair with the
  `TankChatScrollUserAtBottomLatched` alert: when the alert fires,
  the runbook points operators at this endpoint to resolve which
  sessions are durably lagging.
- `tank_conversation_read_cursor_stagnant_total{session_mode, scope}` —
  the orchestrator-side cross-check counter for the transcript
  navigation latch failure mode. Increments once per sample pass for
  every open SSE stream whose user's `conversation_read_state` cursor
  lags the session's durable tail while the session is durably idle
  (`status=ready` or `Active` with `active_turn_id=null`). The
  sampler runs every 60s in `internal/conversationreadstate.Sampler`;
  the `session_mode` allowlist mirrors the chat-scroll label set, and
  `scope` collapses to `default` / `slot` / `other` / `unknown` so
  test-slot proliferation does not bloat cardinality. Paired
  negative-confirmation counters
  (`tank_conversation_read_cursor_skipped_active_turn_total`,
  `tank_conversation_read_cursor_skipped_caught_up_total`,
  `tank_conversation_read_cursor_skipped_missing_total`,
  `tank_conversation_read_cursor_sample_errors_total{reason}`)
  surface "the sampler is running, it just isn't finding stagnation"
  so a flat stagnant series is distinguishable from a stopped
  sampler. The `TankChatScrollUserAtBottomLatched` alert ANDs this
  rate with the client-side
  `navigation-mode-entered-historical-anchor` rate; either alone is
  ambiguous (real user gesture vs. real user reading history), both
  elevated together is the load-bearing evidence.
- `tank_chat_scroll_client_*` - browser-reported transcript scroll
  diagnostics ingested through `POST /api/client-metrics/chat-scroll`.
  Labels are server-bucketed only: `event`, `surface`, `session_mode`,
  `at_bottom`, and `has_scroll_parent`. The endpoint never exposes
  `session_id`, email, raw route paths, or user-supplied event names as
  labels; unknown values collapse to `other` / `unknown`. Two
  event-name buckets are bound to the durable NavigationMode state
  machine in `frontend/src/navigationMode.ts`:
  `navigation-mode-entered-live-tail` and
  `navigation-mode-entered-historical-anchor`. Their rate is the
  smoking-gun signal the `TankChatScrollUserAtBottomLatched` alert
  watches; the structured slog line carries the bounded
  `reason` (`user-scroll-up`, `up-button`, `keyboard-home`,
  `session-open-anchored`, etc.) for per-transition diagnosis.
- `tank_session_list_debug_capture_reports_total{result,reason}` —
  browser-reported session-list debug captures ingested through
  `POST /api/client-metrics/session-list-debug-capture`. The SPA sends
  bounded `/_debug/session-list` snapshots only from explicit
  Settings -> Admin or debug-page capture/record controls. `reason` is
  a closed enum and unknown values collapse to `other`; the metric never
  labels by owner, session id, path, or raw user input.
- `tank_admin_debug_session_list_capture_reads_total{result}` — admin
  reads of `GET /api/debug/session-list-captures`, the durable capture
  store for client-side session-list diagnostics. Captures are retained
  at the latest 200 records per owner/scope.
- `tank_github_pinned_repos_update_total{result}`,
  `tank_github_pinned_repos_publish_total{result}`, and
  `tank_github_pinned_repos_stream_*` — the profile-backed repo-pins
  convergence surface. Writes are durable on `profiles.pinned_repos`;
  publish counters show whether the post-write NATS wake path is working;
  stream open/emit/heartbeat/error counters show whether browser tabs are
  receiving owner-profile snapshots. Labels are bounded (`result` and
  stream `reason` collapse unknown values to `other`) and never include
  owner email or repo slugs.
- `tank_session_event_wake_published_total` /
  `tank_session_event_wake_received_total` /
  `tank_session_event_persist_to_wake_seconds` — the per-session SSE
  wake fabric throughput surface. Published and received are not a
  delivery-loss ratio: published increments once per durable event wake,
  while received increments once per delivery to an open subscriber.
  All unlabeled aggregates per the cardinality rules below; per-stream
  resolution lives in `GET /api/debug/session-event-streams`
  (admin-only) and in the persister's `slog.Info("session event
  persister wake published", subject=..., storage_key=...,
  event_type=..., order_key=..., tank_session_id=...)` line.
- `tank_session_event_stream_heartbeat_catchup_total` — an open
  `/api/sessions/{id}/events` stream emitted durable transcript rows
  immediately after heartbeat polling, rather than after a NATS wake.
  Each increment proves a connected stream was behind the durable
  `session_transcript_rows` projection until the heartbeat path caught
  it up. The matching `slog.Warn("session event stream caught up from
  heartbeat", session_id=..., stream_id=..., storage_key=...,
  cursor_before=..., cursor_after=...)` line is the per-stream
  investigation entry point.
- `tank_session_event_stream_emitted_by_type_total{event_type}` —
  per-emitted browser stream counter paired with
  `tank_session_event_client_received_total{event_type, session_mode}`.
  The main transcript stream emits projected transcript-row batches as
  `event_type="transcript_rows"`; older raw Tank event labels are retained
  only for pre-migration metric continuity. Divergence means the server
  emitted projected rows that the browser did not receive or process.
- `tank_session_event_client_*` — browser-reported per-session SSE
  stream diagnostics ingested through
  `POST /api/client-metrics/session-events-stream`. Bounded labels:
  `event` (opened, ready, transcript_rows_received,
  transcript_rows_applied,
  stream_silent_while_running, terminal_matched_by_turn_id,
  terminal_local_run_mismatch, queued_followup_blocked_after_terminal,
  stale_running_blocked_submit, turn_activity_load_started,
  turn_activity_load_succeeded, turn_activity_load_failed,
  turn_activity_load_timed_out, turn_activity_load_stale,
  turn_activity_refresh_failed, turn_activity_refresh_gave_up,
  turn_activity_refresh_recovered,
  resync_required, stream_error, closed_unmount, closed_error, reconnect_scheduled),
  `session_mode`, and on the `_received_total` variant `event_type`.
  The `_stream_silent_seconds{session_mode}` histogram is the
  candidate-B zombie-SSE detector: the browser's silence watchdog
  observes the idle interval whenever a connected stream has gone
  >30 s without emitting events while a turn is in flight.
- `tank_session_bus_orphan_consumers` /
  `tank_session_bus_consumers_scanned` /
  `tank_session_bus_orphan_consumers_deleted_total` /
  `tank_session_bus_orphan_consumer_delete_errors_total` /
  `tank_session_bus_orphan_sweep_passes_total{result}` — the durable
  remediation surface for stranded JetStream consumers. Every (session,
  provider) pair owns two durable consumers (data + control); the
  runner-side `ensureConsumer` / `ensureControlConsumer` only creates,
  so deleted sessions leak consumers indefinitely (observed at 725
  consumers / 6 live sessions on 2026-05-25, ~50 % of the JetStream
  RAM budget). The orchestrator runs `SweepOrphanConsumers` on a
  5-minute initial delay then hourly; each pass lists consumers,
  decodes session_id, deletes any orphan older than 15 minutes
  (`MinAge` floor). The gauges are last-pass snapshots; the
  `_deleted_total` / `_delete_errors_total` counters are cumulative.
  Alerts: `TankSessionBusOrphanSweepFailing` (sweep itself broken),
  `TankSessionBusOrphanConsumersHigh` (sweep running but backlog
  growing). See `backend-go/internal/sessionbus/sweep.go` for the
  decoder + per-pass logic.
- `tank_client_long_task_*` — browser-reported main-thread long-task
  diagnostics ingested through `POST /api/client-metrics/long-tasks`.
  The SPA installs a `PerformanceObserver({type: "longtask"})` probe
  at boot (`frontend/src/longTaskTelemetry.ts`) and reports every
  ≥50 ms main-thread block along with three correlation deltas: time
  since the last projected-row SSE delivery (wire field name
  `sinceTankEventMs` is historical), since the last session switch, and
  since the last user scroll. Server-bucketed labels:
  `session_mode` (the chat-scroll mode allowlist), `attribution`
  (`self` / `other` / `unknown`, bucketed from
  `PerformanceLongTaskTiming.name`), and `correlation` (`event_burst`
  / `session_switch` / `scroll` / `idle`, picked from the most-recent
  in-window signal). The duration histogram buckets target the
  input-responsiveness band (50 ms - 5 s); anything past 2 s is the
  "page feels frozen" zone. This is the operational replacement for
  devtools' Performance panel — the SPA user can't open devtools, so
  the click-unresponsiveness failure mode otherwise has no surface.
- `tank_turn_terminal_missing_client_nonce_total{source,event_type}` —
  durable turn terminal rows (`turn.completed`, `turn.failed`,
  `turn.command_failed`, `turn.interrupted`) persisted without
  `client_nonce`. This catches the contract violation where the server
  lifecycle is closed, so silent-stranding does not fire, but an
  already-open browser tab cannot correlate the terminal to its local
  run latch and may keep follow-up input queued until refresh.
- `tank_sessions_stuck_in_progress` — the session-lifecycle
  observability surface for the wedged/crashed-runner stall. A
  last-pass gauge of sessions whose durable
  `sessions.activity_summary.status` is `submitted`/`claimed`
  (accepted, no provider progress) and whose `activity_summary`
  `updated_at` is older than the stall threshold (default 10m,
  deliberately above the runner's 240s `PROVIDER_RETRY_STALL_MS`
  terminal). It is the orchestrator-side complement to the runner's
  `api_retry{rate_limit}` terminal: the runner force-fails its own turn
  on a bounded retry storm, but a fully-wedged or crashed runner emits
  nothing and cannot fail its own turn, so the only durable footprint is
  this no-terminal `submitted`/`claimed` row. Steady state is 0. The
  sampler runs every 60s in `internal/stuckturns.Sampler`; per-session
  detail (session_id, stuck_seconds, provider rate-limit state) rides
  the per-session `slog.Warn` line and the
  `GET /api/debug/stuck-turns` endpoint, never a metric label, per the
  cardinality rules. Drives the `TankSessionStuckInProgress` alert,
  which is per-session and durable-state-based — the localizing
  complement to the aggregate, rate-based `TankTurnSilentStranding`.
- `tank_stuck_turn_sample_errors_total{reason}` — stuck-turn sampler
  pass errors. Bounded `reason`: `list` (the durable query failed),
  collapsing anything else to `other`. A nonzero rate means the
  detector itself is blind (the gauge is not being refreshed), so the
  absence of `TankSessionStuckInProgress` cannot be trusted.
- `tank_admin_debug_stuck_turns_reads_total{result}` — admin reads of
  `GET /api/debug/stuck-turns` (the per-session localizer for the
  stuck-turn story). Bounded `result` labels: `ok`, `empty`,
  `forbidden`, `store_error`, `not_configured`. Pair with the
  `TankSessionStuckInProgress` alert: when the gauge is nonzero, the
  runbook points operators here for the session_ids + stuck_seconds +
  provider rate-limit state of the wedged turns.
- `tank_turn_interrupt_request_total` — counter of stop requests posted
  to `/interrupt`. Single label `outcome` with bounded values:
  `persisted`, `already_terminal`, `terminal_lookup_failed`,
  `persist_failed`, `publish_failed`. Steady-state expectation:
  `persisted` dominates; `already_terminal` is a legitimate late-click
  race; the failure outcomes are alerted on `> 0` rate. Owned by the
  durable `turn.interrupt_requested` migration — see
  `docs/tank-conversation-protocol.md` for the boundary contract. Paired
  with the pod-side
  `tank_runner_commands_consumed_total{kind="interrupt_turn"}` and
  `tank_runner_turn_duration_seconds_count{outcome="interrupted"}`
  series to drive the `TankStopNotDelivered` / `TankStopNotTerminated`
  self-telling alerts (see Alerts § below).
- `tank_session_activity_late_interrupt_ignored_total{status}` — the
  chat→sidebar activity fold saw `turn.interrupt_requested` after the
  durable fold had already reached a non-active status, so it preserved
  `ready` / `error` / `stopped` instead of downgrading the session row
  back to `stopping`. This is the server-side detector for stale
  stop-clicks racing behind terminal turn events.
- `tank_session_compaction_total{provider,trigger}` — durable context
  compactions recorded per session by the chat→activity emitter, labeled by
  `provider` (`claude`, `codex`, `other`) and `trigger` (`auto`, `manual`,
  `other`). It increments once per newly-observed compaction — the emitter
  recomputes the durable `sessions.compaction_count` from the `session_events`
  ledger and dedups at-least-once redelivery — so it is the rate view of how
  often sessions compact and how much is a manual `/compact` vs. automatic. The
  exact per-session total is the durable `compaction_count` column the composer
  renders as its `cmp` metric, not this counter.
- `tank_runner_*` — pod-side runner counters/histograms. The default
  `mode` label is "claude" or "codex", bound at module import.
  `tank_runner_item_outcome_total{outcome,reason}` counts bounded item
  classifications emitted by runner adapters: `ok`, `result_failed`, and
  `execution_failed`. `tank_runner_provider_control_total{action,outcome}`
  counts bounded provider control calls, including Claude foreground-task
  backgrounding before interrupt and the interrupt signal itself.
  `tank_runner_provider_rate_limit_event_total` counts Claude SDK
  `rate_limit_event` frames. `tank_runner_provider_rate_limit_decision_total{decision}`
  classifies each frame by the bounded runner handling decision:
  `failed_turn` for primary rejected/exhausted quota frames that emit
  `turn.failed{reason:"provider_rate_limit"}`, `observed_allowed_active` /
  `observed_allowed_idle` for primary allowed quota frames recorded as
  capacity observations, `terminal_without_active_turn` for terminal
  frames that arrive with no active turn to fail, and `retry_stall_failed`
  for a turn the runner force-failed after a sustained Claude SDK
  `system/api_retry{error:"rate_limit"}` storm produced no progress and the
  SDK never surfaced a terminal `rate_limit_event` (the silent-stranding
  class that wedged session 638 on 2026-06-06).
  `tank_runner_provider_api_retry_total{error}` counts those SDK internal
  HTTP-retry frames by bounded `error` (`rate_limit` | `overloaded` |
  `api_error` | `other`); a `rate_limit` storm with no turn progress is the
  signature, and it now resolves to a durable terminal instead of vanishing
  into `tank_runner_unmapped_provider_event_total`. The pre-start half of the
  stall (the runner is wedged and cannot emit its own terminal) is covered by
  the orchestrator-side detector in the session-lifecycle observability
  surface, not here.
  `tank_runner_turn_pre_start_latency_seconds{stage}` measures the previously
  invisible interval before provider output: `command_created_to_claimed`
  covers JetStream delivery plus runner acceptance, and `claimed_to_started`
  covers provider/SDK startup until the first `turn.started`.
  `tank_runner_turn_usage_emitted_total{kind}` counts durable usage events by
  the closed set `kind` ∈ {`snapshot`,`terminal`}: `snapshot` is the
  per-assistant-message `turn.usage` used for backend accounting/diagnostics
  (Claude synthesizes it from each model call's own usage because
  `result.usage` is cumulative and its `input_tokens` is the uncached sliver
  under prompt caching), `terminal` is the cumulative usage on the turn
  terminal. A regression signature for usage observability is a Claude
  turn (`mode="claude"`) with assistant messages but zero `snapshot`
  increments; compare against `tank_runner_turn_duration_seconds_count`.
  `tank_runner_input_reply_answer_shape_total{shape}` counts AskUserQuestion
  `input_reply` answers at the runner/provider boundary after durable
  annotations have been applied. `shape` is a closed set:
  `selection_only`, `free_form_only`, `selection_with_notes`, `empty`.
  This localizes "typed answer text disappeared" reports between durable
  persistence (`turn.input_answered.payload.annotations`) and provider
  delivery without per-session labels.
- `tank_antigravity_runner_*` — Antigravity/Gemini pod-side runner metrics.
  This runner has its own namespace because it drives the native `agy` binary
  rather than the Claude/Codex SDK path. `tank_antigravity_runner_provider_error_total{reason}`
  is the red signal for failed agy turns; `reason="skill_missing"` means the
  backend accepted a skill turn but the runner could not find
  `$HOME/.gemini/skills/<skill>/SKILL.md`, so the turn fails before provider
  execution. `tank_antigravity_runner_agy_diagnostic_total{kind}` records
  bounded non-terminal diagnostics from agy output: `auxiliary_userinfo_401`
  and `telemetry_clearcut_401` classify the known placeholder-token
  profile/telemetry noise and must not be treated as Code Assist auth failure.
  The real Antigravity auth failure signature is proxy-observed
  `tank_api_proxy_upstream_401_total{provider="antigravity"}` or a
  `provider_auth_failed` terminal.
  `tank_antigravity_runner_schedule_intent_total{kind}` records the schedule
  decision boundary before durable wakeup registration. `native_schedule_call`
  means agy emitted the native `schedule` tool, `malformed_schedule_call` means
  the tool call existed but was not registerable, `parked_after_schedule` means
  the runner interrupted the native timer after durable registration, and
  `wait_text_without_schedule` is the diagnostic signature for a planner text
  step that says it will wait while emitting no native schedule tool call.
- `tank_session_runtime_config_update_total` - pod-side runner reports of
  the model/effort actually applied to the provider runtime. Labels:
  `provider` (`claude`, `codex`, `unknown`) and bounded `result`.
- `tank_session_container_terminations_total{container,reason,exit_code}` —
  session pod container terminations observed by the leader-elected K8s watch.
  Labels are bounded: `reason="oom_killed"` is the runtime-death signal that a
  runner cannot reliably emit from inside the killed process. Correlate it with
  durable turn lifecycle and scheduled-wakeup rows to distinguish a missing
  schedule intent from a fired wake whose runner died mid-turn.
- `tank_api_proxy_*` — api-proxy ext_proc counters/histograms. Single
  label: `provider` ("claude", "codex", or "antigravity"), bound from
  `PROXY_PROVIDER`.
  `tank_api_proxy_upstream_status_total{provider,status_class}` buckets every
  upstream response; `tank_api_proxy_upstream_401_total` and
  `tank_api_proxy_upstream_429_total` are the two named signatures — 401 is the
  refresh-storm, 429 is the shared account's usage cap being exhausted (the
  upstream cause of the rate-limit-stall class the runner's
  `provider_rate_limit` terminal and the `tank_sessions_stuck_in_progress`
  detector handle downstream). The ext_proc metrics sidecar also polls the
  pod-local Envoy admin listener and re-exports the bounded SDS cert-rotation
  subset: `tank_api_proxy_envoy_sds_ssl_context_updates{provider}`,
  `tank_api_proxy_envoy_sds_key_rotation_failed{provider,secret}`, and
  `tank_api_proxy_envoy_sds_stats_scrape_total{provider,result}`. Envoy admin
  remains bound to localhost; Prometheus scrapes the sidecar's existing
  `/metrics` endpoint. `TankApiProxyEnvoySdsKeyRotationFailed` and
  `TankApiProxyEnvoySdsStatsScrapeFailing` cover "Certificate Ready but Envoy
  did not absorb the rotated leaf" and "SDS stats are no longer observable."
- `tank_mcp_auth_proxy_*` — sidecar counters/histograms. Label
  `mcp_server` is bounded by the LISTENERS table in `server.py`.
- `tank_schema_migration*` — startup schema-migration engine
  (`pgstore.RunMigrationsWithMetrics`) counters emitted once per boot,
  before the HTTP/`/metrics` server comes up. `tank_schema_migrations_pending`
  (gauge) is the count detected un-applied at this process's boot;
  `tank_schema_migrations_applied_total` / `tank_schema_migrations_skipped_total`
  count migrations applied vs. skipped-as-already-recorded;
  `tank_schema_migration_apply_duration_seconds` (histogram) times each
  applied migration. The steady-state contract these prove: every boot
  skips the full set and applies zero — the durable `schema_migrations`
  ledger replaced the retired engine that re-ran all statements (incl.
  full-table backfills) on every boot under one shared timeout and
  crashlooped on the slow ones. `tank_schema_migration_failures_total{id}`
  carries the bounded, slow-growing migration `id` (`0001`…), within the
  cardinality budget. Caveat: a failed migration `os.Exit(1)`s *before*
  the metrics server starts, so a failure is not scrapeable — the
  observable failure signal is the pod crashloop (kube-prometheus-stack's
  `KubePodCrashLooping`) plus the orchestrator slog
  `postgres schema migration failed` line, which names the failing
  migration id and the underlying error. The apply-once-then-skip
  behavior itself is proven by the DSN-gated
  `TestLedgerAppliesOnceThenSkips` integration test, and the engine shape
  is pinned by the `TestMigrationEngineRetiredPathStaysOut` reintroduction
  guard.
- `tank_transcript_row_materialization_total{trigger,result}` and
  `tank_transcript_row_materialization_duration_seconds{trigger}` —
  per-session transcript-row freshness checks and backfills before `/timeline`
  or transcript SSE read from `session_transcript_rows`. `trigger` is bounded
  to `on_demand` and `unknown`; `result` is bounded to `fresh`, `backfilled`,
  `failed`, `timeout`, and `unknown`. These metrics replace the retired
  serving-pod startup fleet backfill surface: projection-version catch-up is
  now visible as user-requested per-session work, not pod-count-scaled
  background work. `TankTranscriptRowMaterializationFailing` alerts on
  `failed`/`timeout` results.
- `tank_transcript_materialization_invariant_violation_total{invariant,terminal_status}` —
  the turn-activity projection produced a shell that contradicts durable state.
  The load-bearing label value is `active_shell_after_terminal`: a shell still
  marked active for a turn that has a durable terminal. This is the
  compaction/long-turn stall class — a finished turn rendering as perpetually
  running. Steady-state expectation is zero; `TankTurnActiveWithDurableTerminal`
  pages on it. The old fixed-size per-turn read could not raise this (it only
  inspected the truncated window, which lacked the terminal); the projection now
  folds the complete turn, so the check actually sees the terminal.
- `tank_turn_activity_event_count` and `tank_turn_activity_page_count` — label-less
  histograms of how many durable events a turn-activity projection folded and how
  many pages it split into (pages seal at `turnPageEventLimit` events). A growing
  tail past the threshold means long (often post-compaction) turns are common,
  signalling the bounded-cost live-page incremental projection is worth landing.

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
flat, the browser did not receive/process the stream event; if
`transcript_rows_received` climbs but `transcript_rows_applied` in
`tank_session_event_client_events_total` does not, the SPA live-row
application path received durable rows but did not merge them into the
rendered projection.

## Cluster Health Sidebar Surface

`GET /api/cluster-health` (authenticated) returns the backend snapshot rendered
above the profile avatar in the sidebar. It combines Kubernetes node
readiness/pressure, Tank session pod readiness, and NATS JetStream monitor data
from `NATS_MONITOR_URLS`.

The endpoint is intentionally a compact user-visible health surface, not a
replacement for Prometheus or Grafana. It exists so the home page can show the
cluster-level failure modes that otherwise look like "Tank just died":
not-ready or pressured nodes, pending/not-ready session pods, unreachable NATS
replicas, JetStream memory saturation, slow consumers, metadata backlog, and
`TANK_SESSION_BUS` live-delivery replica health.

For JetStream streams, the sidebar treats `config.num_replicas` as the
configured replica count and separately reports current replicas, preferring
the stream leader's replica view when it is reachable. It does not use the raw
length of NATS `/jsz`'s `cluster.replicas` array as the configured count
because that array is reported from a server-local raft view and omits the
local participant. A healthy three-replica stream should therefore show healthy
delivery, not a misleading `2/3` warning just because the leader or local
replica is represented outside the array.

## Admin Observability Summary

`GET /api/debug/observability-summary` (admin-only) returns the Settings ->
Admin observability inbox. It reads the in-cluster Prometheus API
(`PROMETHEUS_URL`, defaulting to the kube-prometheus-stack service) and
summarizes:

- firing Tank alerts, counted by severity, with the alert runbook text when
  present;
- recent orchestrator 5xx routes from
  `increase(tank_http_requests_total{status_class="5xx"}[30m])`;
- links to the scoped `/api/debug/*` endpoints that own per-entity detail.

The endpoint is intentionally not a log store. Alertmanager/Prometheus own the
aggregate "needs attention" state; structured logs remain the exact-event
detail layer after an operator has a route, alert, session id, or reference id.
The Settings tab indicator is driven from this endpoint: critical Tank alerts
render critical, Tank warnings or recent 5xxs render warning, and info-only
alerts do not mark the tab as degraded.

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
the durable `session_events` Postgres table for one tank session. Use
this when the projected transcript is not enough for a deleted session:
`sessions.visible=false` tombstones sidebar/list membership, but owner
and admin `/timeline` and copied-message-link reads still resolve while
the durable session row and transcript ledger remain. The debug endpoint
is the raw-ledger counterpart, not the only recovery path.

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
2. Prefer the projected transcript path for normal pickup:
   `GET /api/sessions/<id>/timeline?anchor=newest&rows=24` or a copied
   message link's `timeline_url`.
3. Use `GET /api/debug/session-event-ledger?session_id=<id>` only when
   raw event audit detail is needed. Page with
   `after_order_key=<next_order_key>` if needed.

Authorized `/timeline` or copied-message-link reads requested against
`visible=false` rows increment
`tank_session_transcript_invisible_row_reads_total`, so the volume of
post-sidebar-deletion transcript recovery is visible without logging
session ids or message contents.

Counts as an admin cross-user audit read. Emits a structured `slog`
line per call (`caller_email`, `session_id`, `session_scope`,
`limit`, cursor fields, `result`, `count`) and increments
`tank_admin_debug_session_event_ledger_reads_total{result}` at
`/metrics`. `result` labels: `ok`, `empty`, `bad_request`,
`forbidden`, `store_error`, `not_configured`.

## Conversation Read State Debug Surface

`GET /api/debug/conversation-read-state` (admin-only) returns the
per-(owner, scope, session_id) durable read cursor alongside the
session's durable `activity_summary` view. The endpoint is the
per-session diagnostic counterpart to the transcript-navigation
observability story: when the
`TankChatScrollUserAtBottomLatched` alert fires, the runbook directs
the operator here to compute the lag for a specific session.

Query params:

- `session_id` (required) — public session id (e.g. `269`).
- `owner` — defaults to the caller; admin can target another user's
  cursor. Cross-user reads increment
  `tank_admin_cross_user_session_reads_total` like the per-session
  ledger surface.
- `session_scope` — defaults to this orchestrator's scope.

Response fields the runbook uses:

- `session_status` + `activity_status` — the durable
  `sessions.activity_summary.status` snapshot. The session-269 case
  (2026-05-27) had both at `"ready"`.
- `active_turn_id` — `""` if no turn is active. Required to be empty
  for the latch diagnosis (a live turn is expected to lag).
- `last_durable_order_key` — the latest `session_events.order_key`
  from `sessions.activity_summary.last_order_key`.
- `last_read_order_key` — the cursor in `conversation_read_state`
  for `(email, session_scope, session_id)`.
- `cursor_lags` — `true` when `last_durable_order_key >
  last_read_order_key`. With `active_turn_id=""` and
  `session_status="ready"`, a `true` value is the durable footprint
  of the bug the navigation-mode refactor retired.

Counts as an admin cross-user audit read when `owner` differs from
the caller. Emits a structured `slog` line per call
(`caller_email`, `owner`, `session_scope`, `session_id`,
`session_status`, `active_turn_id`, `last_durable_order_key`,
`last_read_order_key`, `cursor_lags`) and increments
`tank_admin_debug_conversation_read_state_reads_total{result}` at
`/metrics`. `result` labels: `ok`, `empty`, `bad_request`,
`forbidden`, `store_error`, `not_configured`.

## Stuck turn debug surface

`GET /api/debug/stuck-turns` (admin-only) lists the turns the
orchestrator-side detector has flagged as durably accepted but
unprogressed: sessions whose `sessions.activity_summary.status` is
`submitted`/`claimed` and whose `activity_summary.updated_at` is older
than the threshold, with no terminal event resolving the turn. It is
the per-session localizer for the stuck-turn observability story: when
the `TankSessionStuckInProgress` gauge is nonzero, the alert runbook
directs the operator here to enumerate the wedged turns without
kubectl.

A row here means the runner did NOT fail the turn itself — it is the
orchestrator-side complement to the runner's `api_retry` rate-limit
terminal (`PROVIDER_RETRY_STALL_MS`, 240s). The default threshold
(600s) sits above the runner's 240s terminal so a turn the runner-side
terminal would have resolved never appears here; only the genuine wedge
(fully-wedged/crashed runner, or a stall class the runner cannot see)
does.

Query params:

- `threshold_seconds` — accepted-but-unprogressed age cutoff. Defaults
  to `600`, clamped to `[60, 86400]`.
- `limit` — max rows returned. Defaults to `100`, clamped to
  `[1, 500]`.
- `session_scope` — defaults to this orchestrator's scope.

Response fields:

- `scope`, `threshold_seconds`, `count` — echo the resolved query and
  the number of stuck turns.
- `stuck_turns[]` — one object per wedged turn:
  - `session_id` — public session id (allowed here and in the slog
    line, never as a metric label).
  - `mode` — the session mode (e.g. `claude_gui`).
  - `activity_status` — `submitted` or `claimed`.
  - `active_turn_id` — the durably claimed turn, `""` if absent.
  - `stuck_seconds` — how long it has been accepted-but-unprogressed,
    computed from `activity_summary.updated_at`.
  - `provider_rate_limit_status` — the last provider rate-limit status
    the runner reported on this session (`provider_rate_limit_info`),
    `""` if none. A throttled value distinguishes "wedged on upstream
    rate limits" from "wedged for another reason."
  - `provider_rate_limit_observed_at` — RFC3339-Z timestamp of that
    observation, `""` if none.

To localize a listed session, read its agent-runner logs and its
`session_events` ledger. The endpoint never mutates state. Emits a
structured `slog` line per call (`caller_email`, `session_scope`,
`threshold_seconds`, `count`) and increments
`tank_admin_debug_stuck_turns_reads_total{result}` at `/metrics`.
`result` labels: `ok`, `empty`, `forbidden`, `store_error`,
`not_configured`.

## Control Action Audit Surface

Privileged cross-system effects initiated from session pods through MCP
servers are recorded in `control_action_events`. This is the durable answer
to "which session asked an MCP server to do something that changed another
system?" and complements, rather than replaces, the chat transcript and MCP
pod logs.

Current producers:

- `mcp-github` records `github.pull_request.ready_for_review` and
  `github.pull_request.merge` around the MCP tools
  `mark_pull_request_ready_for_review` and `merge_pull_request`.

Each invocation writes immutable events sharing one `invocation_id`:

- `started` before the external mutation is attempted. The MCP tool fails
  closed if this write fails.
- `succeeded` after the external system accepts the mutation.
- `failed` when the external system rejects the mutation.

The browser reads the per-session ledger through:

```
GET /api/sessions/<id>/control-actions?limit=100
```

The Run Background page renders these rows in the `Control` tab, with the MCP
tool, action, repository, PR number, target URL, and result SHA. This is the
user-facing surface for confirming whether a session merged or marked a PR
ready without reading pod logs.

Prometheus counters:

- `tank_control_action_events_total{source_service,source_tool,action,status,result}`
  counts accepted/rejected Tank ledger writes. Labels are deliberately bounded;
  PR numbers, emails, session ids, and SHAs live in Postgres, not metrics.
- MCP servers may expose their own action counters. For `mcp-github`, use
  `mcp_github_control_action_total{tool,action,status,result}` and
  `mcp_github_control_action_audit_append_total{status,result}`.

Loki remains useful for raw HTTP evidence, for example:

```
{namespace="mcp-github"} |= "pulls/857"
```

Loki is not the source of truth for attribution. It does not carry the complete
session identity/tool/action contract and may age out. The durable ledger is
the traceable product model.

## Session List Capture Debug Surface

`GET /api/debug/session-list-captures` (admin-only) returns durable
browser-side session-list captures posted by
`POST /api/client-metrics/session-list-debug-capture`. Each record
contains the captured client snapshot, the capture detail, and the
server registry rows at ingest time.

Standard workflow for "new session showed another session's name or
avatar":

1. Ask the user to open Settings -> Admin in the affected browser and
   click `Record 2m` before reproducing, or click `Capture Now` while
   the bad render is visible. The standalone `/_debug/session-list`
   page exposes the same controls plus raw client/server row state.
   Recording runs in a page-level singleton, so it continues while the
   user leaves Settings to create or open a session. Besides the 10s
   interval samples, session-list debug events also trigger a debounced
   `manual-record-sample` with `detail.phase=event-sample`; this keeps
   short-lived bad renders in the durable capture stream.
2. Read `GET /api/debug/session-list-captures?owner=<email>&limit=10`
   and inspect the latest captures. Recording samples share
   `detail.run_id`.
3. Compare the captured browser `snapshot` rows with `server_rows`
   recorded at ingest time. If `server_rows` is stable while the browser
   snapshot shows the wrong `name` or avatar id, the bug is in the client
   store/render/avatar resolution layer. If both disagree with the
   create response, the bug is server-side.

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
- **Avatar uploads**: sustained parse/read/validate failures and any
  storage/metadata failures. The runbook starts from
  `GET /api/debug/avatar-upload-attempts?attempt_id=...`, using the
  reference emitted in the UI error.
- **Session bus / live transport**: schema-rejected events (steady-state
  must be zero), wake-publish failures, stream auth ticket store failures,
  terminal events missing `client_nonce`, browser terminal/local-run
  mismatches, browser queued-followup-after-terminal reports, stale
  browser `running` latches blocking submit, and
  `turn.interrupt_requested` persist/publish failures (the durable stop
  boundary; non-zero rate means stops are losing durability or never
  reaching the runner).
- **Transcript navigation**: `TankChatScrollUserAtBottomLatched` fires
  when BOTH the browser-side NavigationMode state machine reports
  rising "entered historical-anchor" transitions AND the
  orchestrator-side cursor-stagnation sampler observes idle sessions
  whose `conversation_read_state` cursor lags the durable tail at
  the same rate. The AND is load-bearing: either signal alone is
  ambiguous (real user gesture vs. real user reading history); both
  elevated together is the durable evidence the retired DOM-distance
  latch bug class is recurring. The new state machine
  (`frontend/src/navigationMode.ts`) is driven by user-gesture
  events only, with `virtuoso-at-bottom-true` as a one-way
  return-to-tail signal. Runbook walks the aggregate trends, the
  per-session debug endpoint
  (`GET /api/debug/conversation-read-state`), structured SPA logs,
  and the per-stream registry.
- **Stop chain self-telling**: `TankStopBackendFailed` fires if the
  backend cannot resolve terminal state, persist `turn.interrupt_requested`,
  or publish the control-plane command after receiving a Stop request.
  `TankStopNotDelivered` fires if the backend persists Stop requests
  faster than runners' control-plane consumer claims `interrupt_turn`
  commands (the data/control plane split has regressed — interrupts are
  queueing somewhere). `TankStopNotTerminated` fires if runners claim
  interrupts faster than they emit terminal `turn.interrupted` events
  (the SDK / codex Thread is ignoring the abort, or the terminal-event
  publish is failing). These are `critical` — Stop is a user-trust
  control surface and a silent regression here is exactly the failure
  mode that necessitated the control-plane split. See
  `docs/tank-conversation-protocol.md` → "Durable turn interruption"
  for the architecture they protect.
- **NATS**: disconnect storm (>6/min for 5m).
- **api-proxy**: upstream 401 rate (refresh-storm signature), refresh
  failures (any non-success result), and sustained upstream 429s
  (`TankApiProxyRateLimited` — the shared provider account's usage cap is
  exhausted; the upstream-cause view of the rate-limit-stall class, paired
  downstream with the runner's `provider_rate_limit` terminal and
  `TankSessionStuckInProgress`).
- **mcp-auth-proxy**: SA token read failures, MCP upstream 5xx rate.
- **Runners**: provider error rate, durable scheduled wakeup registration
  outcomes, and backend due-wakeup backlog. Pending, fired, and failed
  scheduled wakeups are also user-visible from Background -> Scheduled via
  the backend scheduled-wakeup read model, so confirmation does not depend on
  runner logs. Background-task-completion wakes (a `run_in_background` task
  finishing while the session is idle) are counted the same way:
  `tank_runner_background_task_wake_total{result}` on the runner and
  `tank_background_task_wake_register_total` /
  `tank_background_task_wake_fire_total` (+ the `tank_background_task_wakes_due`
  gauge) on the orchestrator. `TankBackgroundTaskWakeFireFailing` pages when a
  registered wake errors on fire — a silent stranding the Agent Runners contract
  counts.
- **Stuck turn (wedged/crashed runner)**: `TankSessionStuckInProgress`
  fires (`tank_sessions_stuck_in_progress > 0` for 5m) when a turn was
  durably `claimed`/`submitted` but produced no `turn.started`/terminal
  for longer than the stall threshold (10m, above the runner's 240s
  `PROVIDER_RETRY_STALL_MS` terminal). It is per-session and
  durable-state-based — the localizing complement to the aggregate,
  rate-based `TankTurnSilentStranding`. The runner force-fails its own
  turn on a bounded `api_retry{rate_limit}` storm, so a nonzero gauge
  means the runner did NOT fail it: a fully-wedged or crashed runner
  that can emit nothing, or a stall class the runner cannot see. The
  runbook localizes with `GET /api/debug/stuck-turns` (session_ids +
  stuck_seconds + provider rate-limit state), then reads that session's
  agent-runner logs and `session_events`. The detector lives in
  `internal/stuckturns.Sampler` (60s pass against the durable
  `sessions` table). If `tank_stuck_turn_sample_errors_total{reason}` is
  nonzero, the detector is blind and the absence of this alert cannot be
  trusted.
- **Session spawn**: any single-spawn outlier above 60s in the trailing
  hour. Backed by recording rules
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
  production pods are distinguishable. The p50/p95 recording rules are
  dashboard context only; the alertable failure mode is a concrete recent
  outlier (`max_1h > 60s`) where an operator can inspect the affected pod.

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
and runner turn duration + commands consumed.

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
