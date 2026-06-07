// Prometheus metrics for the Antigravity runner. Same `tank_*` namespace +
// per-runner metrics HTTP server shape as the claude/codex runners; the
// PodMonitor scrapes the named "runner-metrics" container port. The
// interrupt-outcome counter is the load-bearing observability for the
// four-outcome Stop contract (docs/tank-conversation-protocol.md → #532): every
// interrupt must drain to exactly one terminal-outcome bucket.

import http from "node:http";
import { Counter, Histogram, Registry, collectDefaultMetrics } from "prom-client";

export const registry = new Registry();
collectDefaultMetrics({ register: registry, prefix: "tank_antigravity_runner_" });

export const commandsConsumedTotal = new Counter({
  name: "tank_antigravity_runner_commands_consumed_total",
  help: "Session commands consumed, by kind.",
  labelNames: ["kind"] as const,
  registers: [registry],
});

export const turnTerminalTotal = new Counter({
  name: "tank_antigravity_runner_turn_terminal_total",
  help: "Durable turn terminals published, by type.",
  labelNames: ["type"] as const,
  registers: [registry],
});

export const interruptOutcomeTotal = new Counter({
  name: "tank_antigravity_runner_interrupt_outcome_total",
  help: "Interrupt resolutions; every interrupt must drain to one terminal bucket.",
  labelNames: ["outcome"] as const,
  registers: [registry],
});

export const providerErrorTotal = new Counter({
  name: "tank_antigravity_runner_provider_error_total",
  help: "agy process failures, by reason.",
  labelNames: ["reason"] as const,
  registers: [registry],
});

export const natsPublishFailureTotal = new Counter({
  name: "tank_antigravity_runner_nats_publish_failure_total",
  help: "Tank event publish failures to the session bus.",
  registers: [registry],
});

export const eventTruncatedTotal = new Counter({
  name: "tank_antigravity_runner_event_truncated_total",
  help: "Tank events truncated before publish to fit max_payload.",
  labelNames: ["event_type", "severity"] as const,
  registers: [registry],
});

export const scheduledWakeupRegisterTotal = new Counter({
  name: "tank_antigravity_runner_scheduled_wakeup_register_total",
  help: "Antigravity schedule registrations attempted against the orchestrator durable wakeup API.",
  labelNames: ["result"] as const,
  registers: [registry],
});

// agy transcript steps observed, by how the adapter classified them. The
// `dropped` bucket (user echo / system history) is expected and non-zero; a
// spike in any other class names a step the adapter handled.
export const agyStepTotal = new Counter({
  name: "tank_antigravity_runner_agy_step_total",
  help: "agy transcript steps observed, by adapter classification.",
  labelNames: ["kind"] as const,
  registers: [registry],
});

export const usageReportTotal = new Counter({
  name: "tank_antigravity_runner_usage_report_total",
  help: "loadCodeAssist usage reports, by result.",
  labelNames: ["result"] as const,
  registers: [registry],
});

export const turnDurationSeconds = new Histogram({
  name: "tank_antigravity_runner_turn_duration_seconds",
  help: "Wall-clock duration of an agy-driven turn.",
  buckets: [0.5, 1, 2, 5, 10, 20, 45, 90, 180, 360],
  registers: [registry],
});

export function startMetricsServer(port: number): http.Server {
  const server = http.createServer((req, res) => {
    if (req.url === "/healthz") {
      res.writeHead(200, { "content-type": "text/plain" });
      res.end("ok");
      return;
    }
    if (req.url === "/metrics") {
      registry
        .metrics()
        .then((body) => {
          res.writeHead(200, { "content-type": registry.contentType });
          res.end(body);
        })
        .catch((err) => {
          res.writeHead(500);
          res.end(String(err));
        });
      return;
    }
    res.writeHead(404);
    res.end("not found");
  });
  server.listen(port, "0.0.0.0");
  return server;
}
