// tank-codex-runner is the pod-side process that drives @openai/codex-sdk
// for one session pod's lifetime and publishes canonical transcript events to
// the session bus. Mirrors claude-runner/src/index.ts with a different SDK underneath.

import { loadConfig } from "./config.js";
import { startMetricsServer } from "./metrics.js";
import { Runner } from "./runner.js";

// Metrics port defaults to 9096 (one higher than the claude runner's
// 9095) so a session pod that briefly co-locates both runners during a
// rolling restart doesn't collide on the listen socket.
const DEFAULT_METRICS_PORT = 9096;

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
      msg: "codex-runner starting",
      session_id: cfg.sessionId,
      owner_email: cfg.ownerEmail,
      nats_url: cfg.natsURL,
      nats_stream: cfg.natsStream,
      workspace: cfg.workspace,
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
  console.error("codex-runner exited with error:", err);
  process.exit(1);
});
