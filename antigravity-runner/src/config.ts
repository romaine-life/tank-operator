// Runtime config for the Antigravity runner. Mirrors codex-runner/src/config.ts
// (downward-API session identity + scoped event-ledger plumbing). agy auth is
// proxy-owned: the launch script seeds a placeholder OAuth token and agy's
// cloudcode-pa.googleapis.com traffic is host-aliased to the
// antigravity-api-proxy, which injects the real access token. This process only
// drives the CLI; it never sees the real Google credential.

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
  // agy data dir (it writes conversations + the structured transcript here).
  agyHome: string;
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
  const home = (process.env.HOME ?? "/home/node").trim() || "/home/node";
  return {
    sessionId,
    sessionStorageKey:
      process.env.TANK_SESSION_STORAGE_KEY?.trim() || sessionId,
    ownerEmail: (process.env.POD_OWNER_EMAIL ?? "").trim().toLowerCase(),
    natsURL,
    natsToken: process.env.NATS_TOKEN?.trim() || "",
    natsStream: process.env.NATS_STREAM?.trim() || "TANK_SESSION_BUS",
    operatorInternalURL: process.env.TANK_OPERATOR_INTERNAL_URL?.trim() || "",
    operatorTokenPath: process.env.TANK_OPERATOR_TOKEN_PATH?.trim() || "",
    workspace: process.env.WORKSPACE?.trim() || "/workspace",
    agyHome:
      process.env.ANTIGRAVITY_HOME?.trim() || `${home}/.gemini/antigravity-cli`,
  };
}
