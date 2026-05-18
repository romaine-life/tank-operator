// tank-agent-runner is the pod-side process that drives
// @anthropic-ai/claude-agent-sdk for one session pod's lifetime and publishes
// canonical transcript events to the session bus.

import { readClaudeCliVersion } from "./cliVersion.js";
import { loadConfig } from "./config.js";
import { startMetricsServer } from "./metrics.js";
import { Runner } from "./runner.js";

// Metrics listen port. Bound to a separate listener from any other
// pod-internal HTTP surface so the kube-prometheus-stack PodMonitor
// scrape can target it directly. Default matches k8s/templates/
// values.yaml's runner.metricsPort.
const DEFAULT_METRICS_PORT = 9095;

async function main(): Promise<void> {
  const cfg = loadConfig();
  console.log(
    JSON.stringify({
      msg: "agent-runner starting",
      session_id: cfg.sessionId,
      owner_email: cfg.ownerEmail,
      nats_url: cfg.natsURL,
      nats_stream: cfg.natsStream,
      workspace: cfg.workspace,
      // CC version floats with npm-latest at image-build time (no pin in
      // claude-container/Dockerfile). Captured here so a future "which
      // version was running when X broke?" lookup is a one-liner against
      // kubectl logs instead of a chart-bump-to-build-log archaeology.
      claude_cli_version: readClaudeCliVersion(),
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
  console.error("agent-runner exited with error:", err);
  process.exit(1);
});
