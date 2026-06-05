// Runtime config sourced from env vars. SESSION_ID + POD_OWNER_EMAIL come
// from the downward API on the pod spec. TANK_SESSION_STORAGE_KEY is the
// scoped event ledger partition key. NATS_* points at the JetStream session
// bus; the backend owns Postgres writes after it persists bus events.

export interface Config {
  sessionId: string;
  sessionStorageKey: string;
  ownerEmail: string;
  natsURL: string;
  natsToken: string;
  natsStream: string;
  operatorInternalURL: string;
  operatorTokenPath: string;
  workspace: string;
  mcpConfig: string;
  // Daily Code Assist request budget used to derive the gemini:daily quota
  // utilization reported to the capacity surface. 0 disables the percentage
  // (status is still reported). Per-session-observed, not account-wide.
  geminiDailyRequestCap: number;
}

export function loadConfig(): Config {
  const sessionId = (process.env.SESSION_ID ?? "").trim();
  if (!sessionId) {
    throw new Error(
      "SESSION_ID is required (set from downward API: metadata.labels['tank-operator/session-id'])",
    );
  }
  const natsURL = (process.env.NATS_URL ?? "").trim();
  if (!natsURL) {
    throw new Error("NATS_URL is required");
  }
  return {
    sessionId,
    sessionStorageKey: process.env.TANK_SESSION_STORAGE_KEY?.trim() || sessionId,
    ownerEmail: (process.env.POD_OWNER_EMAIL ?? "").trim().toLowerCase(),
    natsURL,
    natsToken: process.env.NATS_TOKEN?.trim() || "",
    natsStream: process.env.NATS_STREAM?.trim() || "TANK_SESSION_BUS",
    operatorInternalURL: process.env.TANK_OPERATOR_INTERNAL_URL?.trim() || "",
    operatorTokenPath: process.env.TANK_OPERATOR_TOKEN_PATH?.trim() || "",
    workspace: process.env.WORKSPACE?.trim() || "/workspace",
    mcpConfig: process.env.MCP_CONFIG?.trim() || "/workspace/.mcp.json",
    geminiDailyRequestCap: parsePositiveInt(process.env.GEMINI_DAILY_REQUEST_CAP, 1000),
  };
}

function parsePositiveInt(raw: string | undefined, fallback: number): number {
  const trimmed = raw?.trim() ?? "";
  if (trimmed === "") return fallback;
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n < 0) return fallback;
  return Math.trunc(n);
}
