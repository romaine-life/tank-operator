// tank-codex-runner is the pod-side process that drives @openai/codex-sdk
// for one session pod's lifetime and publishes canonical transcript events to
// the session bus. Mirrors agent-runner/src/index.ts with a different SDK underneath.

import { loadConfig } from "./config.js";
import { Runner } from "./runner.js";

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
