// Runtime config sourced from env vars. Mirrors agent-runner/src/config.ts:
// same downward-API + scoped Cosmos plumbing, different SDK underneath.
//
// Auth path: codex-sdk wraps the codex CLI subprocess, which reads
// ~/.codex/auth.json. The launcher writes a placeholder chatgptAuthTokens
// file; codex-api-proxy injects the real ChatGPT bearer and owns token
// rotation centrally. No CODEX_API_KEY env var is required.

export interface Config {
  sessionId: string;
  sessionStorageKey: string;
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
    sessionStorageKey: process.env.TANK_SESSION_STORAGE_KEY?.trim() || sessionId,
    ownerEmail: (process.env.POD_OWNER_EMAIL ?? "").trim().toLowerCase(),
    cosmosEndpoint,
    cosmosDatabase: process.env.COSMOS_DATABASE?.trim() || "tank-operator",
    sessionEventsContainer:
      process.env.COSMOS_SESSION_EVENTS_CONTAINER?.trim() || "session-events",
    turnQueueContainer:
      process.env.COSMOS_TURN_QUEUE_CONTAINER?.trim() || "turn-queue",
    turnQueuePollMs: parseInt(
      process.env.TURN_QUEUE_POLL_MS?.trim() || "1000",
      10,
    ),
    workspace: process.env.WORKSPACE?.trim() || "/workspace",
    // The orchestrator reverse-proxies the SPA's /agent-ws onto this port.
    // Same port as agent-runner: only one runner per pod, no collision risk.
    wsPort: parseInt(process.env.AGENT_RUNNER_WS_PORT?.trim() || "8090", 10),
  };
}
