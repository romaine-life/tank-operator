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
