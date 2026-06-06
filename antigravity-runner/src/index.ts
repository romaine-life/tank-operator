// tank-antigravity-runner is the pod-side process that drives the Antigravity
// CLI (agy, Gemini-Ultra) for one session pod's lifetime and publishes
// canonical transcript events to the session bus. Mirrors codex-runner/src/
// index.ts with a subprocess driver underneath.

import { loadConfig } from "./config.js";
import { startMetricsServer } from "./metrics.js";
import { Runner } from "./runner.js";

// Metrics port 9097: one above codex (9096) / claude (9095) so a pod that
// briefly co-locates runners during a rolling restart does not collide.
const DEFAULT_METRICS_PORT = 9097;

function parseMetricsPort(raw: string | undefined): number {
  const trimmed = raw?.trim() ?? "";
  if (trimmed === "") return DEFAULT_METRICS_PORT;
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n <= 0 || n > 65535) {
    throw new Error(`TANK_RUNNER_METRICS_PORT is not a valid port: ${raw}`);
  }
  return Math.trunc(n);
}

async function main(): Promise<void> {
  const cfg = loadConfig();
  console.log(
    JSON.stringify({
      msg: "antigravity-runner starting",
      session_id: cfg.sessionId,
      owner_email: cfg.ownerEmail,
      nats_url: cfg.natsURL,
      nats_stream: cfg.natsStream,
      workspace: cfg.workspace,
      agy_home: cfg.agyHome,
    }),
  );

  const metricsPort = parseMetricsPort(process.env.TANK_RUNNER_METRICS_PORT);
  const metricsServer = startMetricsServer(metricsPort);
  console.log(JSON.stringify({ msg: "metrics listening", port: metricsPort }));

  const ctrl = new AbortController();
  const shutdown = (sig: NodeJS.Signals) => {
    console.log(JSON.stringify({ msg: "shutdown", signal: sig }));
    ctrl.abort();
    metricsServer.close();
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);

  const runner = new Runner(cfg);
  try {
    await runner.run(ctrl.signal);
  } finally {
    metricsServer.close();
  }
}

main().catch((err) => {
  console.error("antigravity-runner exited with error:", err);
  process.exit(1);
});
