// Runtime config sourced from env vars. Mirrors agent-runner/src/config.ts —
// same downward-API + Cosmos plumbing, different SDK underneath.
//
// Auth path: codex-sdk wraps the codex CLI subprocess, which reads
// ~/.codex/auth.json. The chart mounts that file from the codex-credentials
// Secret. The SDK inherits process.env and the CLI reads auth.json as
// normal — no CODEX_API_KEY env var required (subscription auth path).

export interface Config {
  sessionId: string;
  ownerEmail: string;
  cosmosEndpoint: string;
  cosmosDatabase: string;
  sessionEventsContainer: string;
  turnQueueContainer: string;
  turnQueuePollMs: number;
  workspace: string;
  wsPort: number;
}

export function loadConfig(): Config {
  const sessionId = (process.env.SESSION_ID ?? "").trim();
  if (!sessionId) {
    throw new Error(
      "SESSION_ID is required (set from downward API: metadata.labels['tank-operator/session-id'])",
    );
  }
  const cosmosEndpoint = (process.env.COSMOS_ENDPOINT ?? "").trim();
  if (!cosmosEndpoint) {
    throw new Error("COSMOS_ENDPOINT is required");
  }
  return {
    sessionId,
    ownerEmail: (process.env.POD_OWNER_EMAIL ?? "").trim().toLowerCase(),
    cosmosEndpoint,
    cosmosDatabase: process.env.COSMOS_DATABASE?.trim() || "tank-operator",
    sessionEventsContainer:
      process.env.COSMOS_SESSION_EVENTS_CONTAINER?.trim() || "session-events",
    turnQueueContainer:
      process.env.COSMOS_TURN_QUEUE_CONTAINER?.trim() || "turn-queue",
    turnQueuePollMs: parseInt(process.env.TURN_QUEUE_POLL_MS?.trim() || "1000", 10),
    workspace: process.env.WORKSPACE?.trim() || "/workspace",
    // The orchestrator reverse-proxies the SPA's /agent-ws onto this port.
    // Same port as agent-runner — only one runner per pod, no collision risk.
    wsPort: parseInt(process.env.AGENT_RUNNER_WS_PORT?.trim() || "8090", 10),
  };
}
