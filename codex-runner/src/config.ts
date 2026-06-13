// Runtime config sourced from env vars. Mirrors claude-runner/src/config.ts:
// same downward-API + scoped event ledger plumbing, different SDK underneath.
//
// Auth path: codex-sdk wraps the codex CLI subprocess, which reads
// ~/.codex/auth.json. The launcher writes a placeholder chatgptAuthTokens
// file; codex-api-proxy injects the real ChatGPT bearer and owns token
// rotation centrally. No CODEX_API_KEY env var is required.

export interface Config {
  sessionId: string;
  sessionStorageKey: string;
  ownerEmail: string;
  natsURL: string;
  natsUser?: string;
  natsPasswordFile?: string;
  natsToken: string;
  natsStream: string;
  natsCommandStream: string;
  operatorInternalURL: string;
  operatorTokenPath: string;
  workspace: string;
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
    natsUser: process.env.NATS_USER?.trim() || "",
    natsPasswordFile: process.env.NATS_PASSWORD_FILE?.trim() || "",
    natsToken: process.env.NATS_TOKEN?.trim() || "",
    natsStream: process.env.NATS_STREAM?.trim() || "TANK_SESSION_BUS",
    natsCommandStream: process.env.NATS_COMMAND_STREAM?.trim() || "TANK_SESSION_COMMANDS",
    operatorInternalURL: process.env.TANK_OPERATOR_INTERNAL_URL?.trim() || "",
    operatorTokenPath: process.env.TANK_OPERATOR_TOKEN_PATH?.trim() || "",
    workspace: process.env.WORKSPACE?.trim() || "/workspace",
  };
}
