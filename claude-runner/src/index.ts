// tank-claude-runner is the pod-side process that drives
// @anthropic-ai/claude-agent-sdk for one session pod's lifetime and publishes
// canonical transcript events to the session bus.

import { createRequire } from "node:module";
import { join } from "node:path";

import { readClaudeCliVersion } from "./cliVersion.js";
import { loadConfig } from "./config.js";
import {
  startMetricsServer,
  transcriptCaptureLagMs,
  transcriptCaptureTotal,
  transcriptResumeTotal,
} from "./metrics.js";
import { Runner } from "./runner.js";
import { runResumeBootstrap } from "./resumeBootstrap.js";
import { TranscriptCapture } from "./transcriptCapture.js";
import { uploadTranscriptSnapshot } from "../../runner-shared/transcriptUpload.js";
import { fetchResumeTranscript } from "../../runner-shared/transcriptDownload.js";

// Best-effort read of the Claude Agent SDK version, captured alongside each
// transcript snapshot so Stage-2 restore can gate resume on SDK-format
// compatibility. Never fatal — package.json may not be exported.
function readClaudeSdkVersion(): string {
  try {
    const req = createRequire(import.meta.url);
    const pkg = req("@anthropic-ai/claude-agent-sdk/package.json") as { version?: string };
    return String(pkg.version ?? "");
  } catch {
    return "";
  }
}

// Metrics listen port. Bound to a separate listener from any other
// pod-internal HTTP surface so the kube-prometheus-stack PodMonitor
// scrape can target it directly. Default matches k8s/templates/
// values.yaml's runner.metricsPort.
const DEFAULT_METRICS_PORT = 9095;

async function main(): Promise<void> {
  const cfg = loadConfig();
  console.log(
    JSON.stringify({
      msg: "claude-runner starting",
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

  // Resurrected pod: materialize the dead session's transcript and pin its SDK
  // session id so the runner `resume`s that conversation instead of starting
  // fresh. Gated by env — normal pods skip this entirely. Any failure starts
  // fresh and never blocks boot. See docs/session-transcript-capture.md.
  if (cfg.resurrectSourceSessionId) {
    try {
      const home = process.env.HOME?.trim() || "/home/node";
      const resumeSessionId = await runResumeBootstrap({
        homeDir: home,
        runningSdkVersion: readClaudeSdkVersion(),
        fetchTranscript: () => fetchResumeTranscript(cfg, cfg.resurrectSourceSessionId ?? ""),
        onOutcome: (outcome) => transcriptResumeTotal.labels(outcome).inc(),
      });
      if (resumeSessionId) {
        cfg.resumeSessionId = resumeSessionId;
        console.log(
          JSON.stringify({ msg: "resuming prior conversation", resume_session_id: resumeSessionId }),
        );
      }
    } catch (err) {
      console.warn("resume bootstrap setup failed (starting fresh):", err);
    }
  }

  // Transcript capture runs in-process as a read-only sink. It is fully
  // crash-isolated: a failure to set it up must never stop the runner from
  // driving turns. See docs/session-transcript-capture.md.
  try {
    const home = process.env.HOME?.trim() || "/home/node";
    const capture = new TranscriptCapture({
      projectsRoot: join(home, ".claude", "projects"),
      homeDir: home,
      sdkVersion: readClaudeSdkVersion(),
      upload: (snap) => uploadTranscriptSnapshot(cfg, snap),
      onResult: (result) => transcriptCaptureTotal.labels(result).inc(),
      setLagMs: (ms) => transcriptCaptureLagMs.set(ms),
    });
    capture.start(ctrl.signal);
    console.log(JSON.stringify({ msg: "transcript capture started", projects_root: join(home, ".claude", "projects") }));
  } catch (err) {
    console.warn("transcript capture setup failed (continuing):", err);
  }

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
  console.error("claude-runner exited with error:", err);
  process.exit(1);
});
