import { loadConfig } from "./config.js";
import { startMetricsServer } from "./metrics.js";
import { Runner } from "./runner.js";

const DEFAULT_METRICS_PORT = 9097; // Matches sessionmodel.GeminiRunnerMetricsPort

async function main(): Promise<void> {
  const cfg = loadConfig();
  console.log(
    JSON.stringify({
      msg: "gemini-runner starting",
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
  let shutdownTimer: NodeJS.Timeout | null = null;
  const shutdown = (sig: NodeJS.Signals) => {
    console.log(JSON.stringify({ msg: "shutdown", signal: sig }));
    ctrl.abort();
    metricsServer.close();
    if (!shutdownTimer) {
      shutdownTimer = setTimeout(() => {
        console.error(JSON.stringify({ msg: "shutdown timeout elapsed, exiting", signal: sig }));
        process.exit(0);
      }, 5_000);
      shutdownTimer.unref();
    }
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

function parseMetricsPort(raw: string | undefined): number {
  const trimmed = raw?.trim() ?? "";
  if (trimmed === "") return DEFAULT_METRICS_PORT;
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n <= 0 || n > 65535) {
    throw new Error(`TANK_RUNNER_METRICS_PORT is not a valid port: ${raw}`);
  }
  return Math.trunc(n);
}

main().catch((err) => {
  console.error("gemini-runner exited with error:", err);
  process.exit(1);
});
