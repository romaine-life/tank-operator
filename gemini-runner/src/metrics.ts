import { Counter, Gauge, Registry, collectDefaultMetrics } from "prom-client";
import { createServer, type Server } from "node:http";

const RUNNER_MODE = "gemini";

export const registry = new Registry();
registry.setDefaultLabels({ mode: RUNNER_MODE });
collectDefaultMetrics({ register: registry });

export const commandsConsumedTotal = new Counter({
  name: "tank_runner_commands_consumed_total",
  help: "Session commands consumed from the JetStream command subject.",
  labelNames: ["kind", "result"],
  registers: [registry],
});

export const turnDurationSeconds = new Counter({
  name: "tank_runner_turn_duration_seconds",
  help: "Cumulative duration of turns (mocked or completed).",
  labelNames: ["outcome"],
  registers: [registry],
});

export const providerErrorTotal = new Counter({
  name: "tank_runner_provider_error_total",
  help: "Errors raised by the provider SDK.",
  labelNames: ["kind"],
  registers: [registry],
});

export const natsPublishFailureTotal = new Counter({
  name: "tank_runner_nats_publish_failure_total",
  help: "Publish attempts to the JetStream session bus that returned an error.",
  registers: [registry],
});

export const eventTruncatedTotal = new Counter({
  name: "tank_runner_event_truncated_total",
  help: "Tank conversation events that exceeded the transport budget and were truncated before publish.",
  labelNames: ["event_type", "severity"],
  registers: [registry],
});

export const turnUsageEmittedTotal = new Counter({
  name: "tank_runner_turn_usage_emitted_total",
  help: "Durable turn.usage events emitted from parsed Gemini CLI result stats.",
  labelNames: ["kind"],
  registers: [registry],
});

export const providerUsageReportTotal = new Counter({
  name: "tank_runner_provider_usage_report_total",
  help: "provider_rate_limit_info snapshots reported to the orchestrator runtime-config endpoint.",
  labelNames: ["result"],
  registers: [registry],
});

export const optionsPinnedTotal = new Counter({
  name: "tank_runner_options_pinned_total",
  help: "Model and effort that the runner pinned into SDK Options at first turn.",
  labelNames: ["model", "effort"],
  registers: [registry],
});

export const optionsOverrideIgnoredTotal = new Counter({
  name: "tank_runner_options_override_ignored_total",
  help: "Submit_turn commands whose model/effort differed from the pinned options.",
  labelNames: ["field"],
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
