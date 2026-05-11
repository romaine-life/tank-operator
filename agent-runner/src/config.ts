// Runtime config sourced from env vars. SESSION_ID + POD_OWNER_EMAIL come
// from the downward API on the pod spec (label tank-operator/session-id,
// annotation tank-operator/owner-email). COSMOS_* mirror the orchestrator's
// env. Azure workload-identity envs (AZURE_CLIENT_ID + AZURE_TENANT_ID +
// AZURE_FEDERATED_TOKEN_FILE) are injected by the WI webhook because the
// pod carries azure.workload.identity/use=true and the SA's federated
// credential covers system:serviceaccount:tank-operator-sessions:claude-session
// (see infra/tank_session_identity.tf). We don't read those directly —
// DefaultAzureCredential picks them up.

export interface Config {
  sessionId: string;
  ownerEmail: string;
  cosmosEndpoint: string;
  cosmosDatabase: string;
  sessionEventsContainer: string;
  workspace: string;
  mcpConfig: string;
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
    workspace: process.env.WORKSPACE?.trim() || "/workspace",
    mcpConfig: process.env.MCP_CONFIG?.trim() || "/workspace/.mcp.json",
    wsPort: parseInt(process.env.AGENT_RUNNER_WS_PORT?.trim() || "8090", 10),
  };
}
