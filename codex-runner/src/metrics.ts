// Prometheus instrumentation for the pod-side codex runner. The metric
// taxonomy mirrors the orchestrator's tank_* namespace and is documented
// in docs/observability.md. Cardinality discipline: no `pod_name`,
// `session_id`, `turn_id`, or `email` labels — anything that grows per
// session-pod blows up Prometheus active series at scale.
//
// The single mutable label is `mode`, set to "codex" here. The
// agent-runner ships an identical module with `mode: "claude"`.

import { Counter, Gauge, Histogram, Registry, collectDefaultMetrics } from "prom-client";
import { createServer, type Server } from "node:http";

const RUNNER_MODE = "codex";

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

export const providerControlTotal = new Counter({
  name: "tank_runner_provider_control_total",
  help: "Provider control-plane calls issued by the runner, such as interrupt and background foreground tasks.",
  labelNames: ["action", "outcome"],
  registers: [registry],
});

export const itemOutcomeTotal = new Counter({
  name: "tank_runner_item_outcome_total",
  help: "Provider item outcomes emitted into the Tank conversation ledger.",
  labelNames: ["outcome", "reason"],
  registers: [registry],
});

// interruptOutcomeTotal records the disposition of every `interrupt_turn`
// command this runner accepts. Sibling of the agent-runner counter
// shipped with #535 (PR 1 of #532); same labels and same four-outcome
// contract so the per-stop SLO alert can sum across both runners.
//
// See agent-runner/src/metrics.ts for the bucket-by-bucket docstring;
// the contract is also pinned in
// docs/tank-conversation-protocol.md → "Four-outcome contract on the
// runner side". The codex-runner's mapping to the buckets:
//
//   - `terminated_via_sdk` — interrupt arrived during an in-flight
//     codex thread; AbortController.abort() propagates through
//     thread.runStreamed and the catch branch publishes turn.interrupted.
//   - `terminated_pre_sdk` — interrupt arrived before the codex thread
//     consumed the turn (matched via pendingInterrupts in the run-loop
//     dequeue); the AbortController fires before thread.runStreamed
//     emits any event, and the catch branch publishes turn.interrupted.
//   - `buffered` — interrupt arrived with no matching active or pending
//     turn; held in orphanInterrupts awaiting either a matching submit
//     or the orphan timer.
//   - `orphaned` — buffered interrupt's matching submit_turn never
//     arrived within SESSION_INTERRUPT_BUFFER_MS; turn.failed
//     {interrupt_orphaned} published so the UI resolves.
//   - `publish_failed` — publishTerminalWithRetry exhausted both the
//     happy-path turn.interrupted and the fallback turn.failed
//     {publish_interrupt_failed} attempts.
//   - `turn_already_terminal` — interrupt arrived after the targeted
//     turn already emitted its own terminal; durable ledger shows the
//     natural terminal; race is legitimate.
//   - `invalid_target` — interrupt missing both target_turn_id and
//     client_nonce. Backend bug; should be zero.
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

// eventTruncatedTotal — see agent-runner/src/metrics.ts for the
// docstring. Sibling counter; the mode label distinguishes runners.
// Bucketed by event_type so the operator can see "huge Read tool
// outputs" vs. "huge assistant.message.text" at a glance. Per #532
// Stage 3.
export const eventTruncatedTotal = new Counter({
  name: "tank_runner_event_truncated_total",
  help: "Tank conversation events that exceeded the transport budget and were truncated before publish. Severity 'strings-truncated' preserves envelope; 'payload-dropped' loses body. See nelsong6/tank-operator#532 Stage 3.",
  labelNames: ["event_type", "severity"],
  registers: [registry],
});

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
