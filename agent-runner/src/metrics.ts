// Prometheus instrumentation for the pod-side agent runner. The metric
// taxonomy mirrors the orchestrator's tank_* namespace and is documented
// in docs/observability.md. Cardinality discipline: no `pod_name`,
// `session_id`, `turn_id`, or `email` labels — anything that grows per
// session-pod blows up Prometheus active series at scale.
//
// The single mutable label is `mode`, set to "claude" here. The
// codex-runner ships an identical module with `mode: "codex"`.

import { Counter, Gauge, Histogram, Registry, collectDefaultMetrics } from "prom-client";
import { createServer, type Server } from "node:http";

const RUNNER_MODE = "claude";

export const registry = new Registry();
registry.setDefaultLabels({ mode: RUNNER_MODE });
collectDefaultMetrics({ register: registry });

export const commandsConsumedTotal = new Counter({
  name: "tank_runner_commands_consumed_total",
  help: "Session commands consumed from the JetStream command subject.",
  labelNames: ["kind", "result"],
  registers: [registry],
});

export const turnDurationSeconds = new Histogram({
  name: "tank_runner_turn_duration_seconds",
  help: "End-to-end duration from turn.started to the terminal turn event.",
  labelNames: ["outcome"],
  buckets: [0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300],
  registers: [registry],
});

export const providerErrorTotal = new Counter({
  name: "tank_runner_provider_error_total",
  help: "Errors raised by the provider SDK (the query iterator or interrupt() call).",
  labelNames: ["kind"],
  registers: [registry],
});

export const itemOutcomeTotal = new Counter({
  name: "tank_runner_item_outcome_total",
  help: "Provider item outcomes emitted into the Tank conversation ledger.",
  labelNames: ["outcome", "reason"],
  registers: [registry],
});

// interruptOutcomeTotal records the disposition of every `interrupt_turn`
// command this runner accepts. The four-outcome contract (see
// docs/tank-conversation-protocol.md → "Durable turn interruption" and
// nelsong6/tank-operator#532) is the invariant this counter pins:
//
//   - `terminated_via_sdk` — interrupt arrived during an in-flight turn;
//     sdkQuery.interrupt() was awaited and turn.interrupted published.
//   - `terminated_pre_sdk` — interrupt arrived before the runner had
//     dispatched the matching submit_turn to the SDK; the turn was never
//     fed to query() and turn.interrupted published synthetically.
//   - `buffered` — interrupt arrived with no matching active/pending turn;
//     held in the in-process pendingInterrupts buffer awaiting a
//     submit_turn or the orphan timeout. Transient state; every `buffered`
//     increment must drain to exactly one of the other outcomes.
//   - `orphaned` — buffered interrupt never matched a submit_turn within
//     the buffer window; emitted turn.failed{reason:"interrupt_orphaned"}
//     so the UI's "stopping" projection resolves to a durable terminal.
//   - `publish_failed` — sdkQuery.interrupt() was attempted (or skipped
//     because no SDK), but every retry to publish the durable terminal
//     failed. A fallback turn.failed{reason:"publish_interrupt_failed"}
//     attempt was made on the same channel; this outcome means the
//     fallback also failed. JetStream redelivery will retry; until then
//     the UI is stuck. Alert-worthy.
//   - `turn_already_terminal` — interrupt arrived after the targeted
//     turn had already emitted its own terminal (turn.completed or
//     turn.failed). The race is legitimate; the durable ledger shows
//     the natural terminal; no follow-up is needed.
//   - `invalid_target` — interrupt command missing both target_turn_id
//     and client_nonce. Backend bug; should be zero in production.
//
// The PromQL invariant this enables: every backend
// tank_turn_interrupt_request_total{outcome="persisted"} should be
// followed within bounded time by exactly one increment of this counter
// in a terminal-outcome bucket. The `buffered` bucket counts arrivals,
// not terminals; subtract it when alerting.
export const interruptOutcomeTotal = new Counter({
  name: "tank_runner_interrupt_outcome_total",
  help: "Disposition of every interrupt_turn command accepted by the runner. See docs/tank-conversation-protocol.md and nelsong6/tank-operator#532 for the four-outcome contract.",
  labelNames: ["outcome"],
  registers: [registry],
});

export const pendingWakeupsGauge = new Gauge({
  name: "tank_runner_pending_wakeups",
  help: "Currently-pending ScheduleWakeup timers held in this runner process.",
  registers: [registry],
});

export const natsPublishFailureTotal = new Counter({
  name: "tank_runner_nats_publish_failure_total",
  help: "Publish attempts to the JetStream session bus that returned an error.",
  registers: [registry],
});

// eventTruncatedTotal counts Tank conversation events whose stamped
// JSON-encoded size exceeded the per-runner transport budget (default
// 900 KiB, configurable via SESSION_EVENT_MAX_BYTES) and were
// truncated by runner-shared/sessionBus.js's truncateEventIfOversized
// before reaching the JetStream publish. Severity:
//
//   - `strings-truncated` — one or more oversized string fields were
//     replaced with a typed marker; the event's envelope (turn_id,
//     event_id, type, payload shape) is preserved. The transcript
//     renders a "[truncated N bytes]" marker for the affected field.
//   - `payload-dropped` — even after aggressive string truncation the
//     event was still over budget; the entire payload was replaced
//     with `{__payload_dropped: true, original_bytes, reason}`. The
//     event still lands durably (the user sees that an event existed)
//     but the body is unrecoverable from the wire path. Alert-worthy.
//
// Each increment corresponds to one Tank event lost-or-degraded
// because of payload size; sustained `payload-dropped` traffic
// indicates a producer (typically a tool_result.output) that needs to
// chunk or stream rather than emit one giant event. See
// nelsong6/tank-operator#532 Stage 3 for context.
export const eventTruncatedTotal = new Counter({
  name: "tank_runner_event_truncated_total",
  help: "Tank conversation events that exceeded the transport budget and were truncated before publish. Severity 'strings-truncated' preserves envelope; 'payload-dropped' loses body. See nelsong6/tank-operator#532 Stage 3.",
  labelNames: ["event_type", "severity"],
  registers: [registry],
});

// optionsPinnedTotal records the model/effort decision the runner made
// on its first observable submit_turn. Cardinality is bounded by the
// SDK's EffortLevel enum (5 values + "default") and the small set of
// shipped Claude models. The labels are deliberately the *applied*
// values, not the requested ones — so a downgrade from xhigh on a
// model that doesn't support it would be invisible here (the SDK does
// the downgrade silently per its docstring). This counter is the
// operator's answer to "what did this pod actually boot with?" when a
// session looks slower or thinkier than expected.
export const optionsPinnedTotal = new Counter({
  name: "tank_runner_options_pinned_total",
  help: "Model and effort that the runner pinned into SDK Options at first turn.",
  labelNames: ["model", "effort"],
  registers: [registry],
});

// optionsOverrideIgnoredTotal increments when a submit_turn after the
// first one carries a different model or effort than what's already
// pinned. The runner ignores the override (Options is sealed once
// query() runs), but the counter exposes the silent-divergence so a
// future bug or product change ("let me switch model mid-session") is
// visible in metrics before it surfaces as a user-reported "why didn't
// my pick take effect?" ticket. Labels intentionally name the *field*
// that diverged, not the values, to keep cardinality low.
export const optionsOverrideIgnoredTotal = new Counter({
  name: "tank_runner_options_override_ignored_total",
  help: "Submit_turn commands whose model/effort differed from the pinned options (override is silently ignored — model/effort are pod-lifetime).",
  labelNames: ["field"],
  registers: [registry],
});

// AskUserQuestion observability. The pending gauge gates the
// "TankAskUserQuestionStuck" alert (gauge > 0 for > 24h means a
// session is parked waiting on a human, which may indicate a UX bug
// — e.g., the SPA never rendered the question). The wait-time
// histogram backs Grafana panels for p50/p99 answer latency. Buckets
// span from "user clicked while reading" (1s) up to "abandoned
// overnight" (24h) because real answer latencies span both ends.
export const askUserQuestionPendingGauge = new Gauge({
  name: "tank_runner_askuser_question_pending",
  help: "Currently-pending AskUserQuestion tool calls awaiting a durable input_reply.",
  registers: [registry],
});

export const askUserQuestionWaitSeconds = new Histogram({
  name: "tank_runner_askuser_question_wait_seconds",
  help: "Wall-clock seconds from AskUserQuestion canUseTool request to durable input_reply resolution.",
  buckets: [1, 5, 30, 60, 300, 1800, 3600, 86400],
  registers: [registry],
});

// turnStartTimes tracks turn.started timestamps so we can observe a
// duration when the terminal turn event arrives. The map is per-runner-
// process and unbounded only if turns leak (no terminal event ever
// arrives); steady-state size is the active-turn count, at most a
// handful even under fan-in.
const turnStartTimes = new Map<string, number>();

export function recordTurnStart(turnID: string): void {
  if (!turnID) return;
  turnStartTimes.set(turnID, Date.now());
}

export function recordTurnTerminal(
  turnID: string,
  outcome: "completed" | "failed" | "interrupted",
): void {
  if (!turnID) return;
  const start = turnStartTimes.get(turnID);
  if (start === undefined) return;
  turnStartTimes.delete(turnID);
  turnDurationSeconds.labels(outcome).observe((Date.now() - start) / 1000);
}

export function startMetricsServer(port: number): Server {
  const server = createServer((req, res) => {
    if (!req.url) {
      res.statusCode = 400;
      res.end();
      return;
    }
    if (req.url === "/metrics" || req.url.startsWith("/metrics?")) {
      registry
        .metrics()
        .then((body) => {
          res.statusCode = 200;
          res.setHeader("Content-Type", registry.contentType);
          res.end(body);
        })
        .catch((err: unknown) => {
          res.statusCode = 500;
          res.end(`metrics collection failed: ${err instanceof Error ? err.message : String(err)}`);
        });
      return;
    }
    if (req.url === "/healthz") {
      res.statusCode = 200;
      res.end("ok");
      return;
    }
    res.statusCode = 404;
    res.end();
  });
  server.listen(port);
  return server;
}
