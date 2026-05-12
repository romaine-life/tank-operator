// tank-codex-runner — pod-side process that drives @openai/codex-sdk
// for one session pod's lifetime. Fans events to (a) Cosmos
// `session-events` for durable history, and (b) a local WebSocket for
// the SPA's live view (proxied through the orchestrator).
//
// Mirrors agent-runner/src/index.ts. Same entrypoint shape; different
// SDK underneath. See ./runner.ts.

import { loadConfig } from "./config.js";
import { Runner } from "./runner.js";

async function main(): Promise<void> {
  const cfg = loadConfig();
  console.log(
    JSON.stringify({
      msg: "codex-runner starting",
      session_id: cfg.sessionId,
      owner_email: cfg.ownerEmail,
      cosmos_endpoint: cfg.cosmosEndpoint,
      cosmos_db: cfg.cosmosDatabase,
      cosmos_container: cfg.sessionEventsContainer,
      workspace: cfg.workspace,
      ws_port: cfg.wsPort,
    }),
  );

  const ctrl = new AbortController();
  const shutdown = (sig: NodeJS.Signals) => {
    console.log(JSON.stringify({ msg: "shutdown", signal: sig }));
    ctrl.abort();
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);

  const runner = new Runner(cfg);
  await runner.run(ctrl.signal);
}

main().catch((err) => {
  console.error("codex-runner exited with error:", err);
  process.exit(1);
});
