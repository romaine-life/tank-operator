import {
  MODE_PROVIDERS,
  PROVIDERS,
  type Provider,
  type SessionMode,
} from "./sessionModes";

export type ProviderQuotaWindowId = "five_hour" | "weekly" | "opus_weekly";
export type ProviderQuotaStatus =
  | "ok"
  | "low"
  | "exhausted"
  | "stale"
  | "unknown";

export interface ProviderQuotaWindow {
  id: ProviderQuotaWindowId;
  label: string;
  shortLabel: string;
  status: ProviderQuotaStatus;
  percentRemaining: number | null;
  resetAt: string | null;
  observedAt: string | null;
}

export interface ProviderQuotaSnapshot {
  provider: Provider;
  status: ProviderQuotaStatus;
  observedAt: string | null;
  windows: ProviderQuotaWindow[];
}

export interface ProviderQuotaEvidence {
  provider: Provider;
  rateLimitType: string;
  status?: string;
  utilization?: number;
  resetsAt?: string | number | null;
  observedAt?: string | null;
}

interface ProviderQuotaSession {
  mode: SessionMode;
  provider_rate_limit_info?: Record<string, unknown> | null;
  provider_rate_limit_observed_at?: string | null;
}

const PROVIDER_QUOTA_WINDOW_DEFS: Record<
  Provider,
  Array<Pick<ProviderQuotaWindow, "id" | "label" | "shortLabel">>
> = {
  anthropic: [
    { id: "five_hour", label: "5-hour window", shortLabel: "5h" },
    { id: "weekly", label: "Weekly", shortLabel: "Week" },
  ],
  anthropic_secondary: [
    { id: "five_hour", label: "5-hour window", shortLabel: "5h" },
    { id: "weekly", label: "Weekly", shortLabel: "Week" },
  ],
  codex: [
    { id: "five_hour", label: "5-hour window", shortLabel: "5h" },
    { id: "weekly", label: "Weekly", shortLabel: "Week" },
  ],
};

function quotaProviderFromInfo(
  info: Record<string, unknown>,
  fallback: Provider,
): Provider {
  const raw =
    typeof info.provider === "string" ? info.provider.toLowerCase() : "";
  if (
    raw.includes("claude2") ||
    raw.includes("claude_secondary") ||
    raw.includes("anthropic_secondary") ||
    (raw.includes("secondary") &&
      (raw.includes("claude") || raw.includes("anthropic")))
  ) {
    return "anthropic_secondary";
  }
  if (raw.includes("codex") || raw.includes("openai")) return "codex";
  if (raw.includes("claude") || raw.includes("anthropic")) return "anthropic";
  return fallback;
}

function quotaWindowIdFromInfo(
  info: Record<string, unknown>,
): ProviderQuotaWindowId {
  const raw =
    typeof info.rateLimitType === "string"
      ? info.rateLimitType.toLowerCase()
      : "";
  const normalized = raw.replace(/[\s-]+/g, "_");
  if (normalized.includes("opus")) return "opus_weekly";
  if (
    normalized.includes("other") ||
    normalized.includes("sonnet") ||
    normalized.includes("non_opus")
  )
    return "weekly";
  if (
    normalized.includes("week") ||
    normalized.includes("seven_day") ||
    normalized.includes("7_day")
  ) {
    return "weekly";
  }
  return "five_hour";
}

function quotaPercentRemaining(info: Record<string, unknown>): number | null {
  const raw = info.utilization;
  if (typeof raw !== "number" || !Number.isFinite(raw)) return null;
  const percentUsed = raw <= 1 ? raw * 100 : raw;
  return Math.max(0, Math.min(100, 100 - percentUsed));
}

function quotaResetAt(info: Record<string, unknown>): string | null {
  const raw = info.resetsAt ?? info.overageResetsAt;
  if (typeof raw === "number" && Number.isFinite(raw)) {
    const ms = raw > 1_000_000_000_000 ? raw : raw * 1000;
    const date = new Date(ms);
    return Number.isFinite(date.getTime()) ? date.toISOString() : null;
  }
  if (typeof raw === "string" && raw.trim()) {
    const ms = Date.parse(raw);
    return Number.isFinite(ms) ? new Date(ms).toISOString() : null;
  }
  return null;
}

function quotaWindowStatus(
  info: Record<string, unknown> | null,
  percentRemaining: number | null,
  resetAt: string | null,
  observedAt: string | null,
): ProviderQuotaStatus {
  if (!info || !observedAt) return "unknown";
  const now = Date.now();
  const resetMs = resetAt ? Date.parse(resetAt) : Number.NaN;
  if (Number.isFinite(resetMs) && resetMs < now) return "stale";
  const observedMs = Date.parse(observedAt);
  if (Number.isFinite(observedMs) && now - observedMs > 24 * 60 * 60 * 1000)
    return "stale";
  const status =
    typeof info.status === "string" ? info.status.toLowerCase() : "";
  if (status.includes("reject") || status.includes("exhaust"))
    return "exhausted";
  if (percentRemaining !== null && percentRemaining <= 0) return "exhausted";
  if (percentRemaining !== null && percentRemaining <= 20) return "low";
  return "ok";
}

function quotaSnapshotStatus(
  windows: ProviderQuotaWindow[],
): ProviderQuotaStatus {
  if (windows.some((window) => window.status === "exhausted"))
    return "exhausted";
  if (windows.some((window) => window.status === "low")) return "low";
  if (windows.some((window) => window.status === "ok")) return "ok";
  if (windows.some((window) => window.status === "stale")) return "stale";
  return "unknown";
}

export function providerQuotaEvidenceFromPayload(
  value: unknown,
): ProviderQuotaEvidence[] {
  if (!value || typeof value !== "object") return [];
  const raw = value as Record<string, unknown>;
  const rows = Array.isArray(raw.rate_limits) ? raw.rate_limits : [];
  const out: ProviderQuotaEvidence[] = [];
  for (const row of rows) {
    if (!row || typeof row !== "object") continue;
    const item = row as Record<string, unknown>;
    const provider =
      typeof item.provider === "string"
        ? quotaProviderFromInfo(item, "anthropic")
        : null;
    const rateLimitType =
      typeof item.rateLimitType === "string" ? item.rateLimitType : "";
    if (!provider || !rateLimitType) continue;
    const utilization =
      typeof item.utilization === "number" && Number.isFinite(item.utilization)
        ? item.utilization
        : undefined;
    out.push({
      provider,
      rateLimitType,
      ...(typeof item.status === "string" ? { status: item.status } : {}),
      ...(utilization !== undefined ? { utilization } : {}),
      ...(typeof item.resetsAt === "string" ||
      typeof item.resetsAt === "number" ||
      item.resetsAt === null
        ? { resetsAt: item.resetsAt }
        : {}),
      ...(typeof item.observedAt === "string"
        ? { observedAt: item.observedAt }
        : {}),
    });
  }
  return out;
}

function providerQuotaEvidenceFromSessions(
  sessions: readonly ProviderQuotaSession[],
): ProviderQuotaEvidence[] {
  const out: ProviderQuotaEvidence[] = [];
  for (const session of sessions) {
    const info = session.provider_rate_limit_info;
    const observedAt = session.provider_rate_limit_observed_at;
    if (!info || !observedAt) continue;
    const fallbackProvider = MODE_PROVIDERS[session.mode];
    const provider = quotaProviderFromInfo(info, fallbackProvider);
    const rateLimitType =
      typeof info.rateLimitType === "string"
        ? info.rateLimitType
        : quotaWindowIdFromInfo(info);
    out.push({
      provider,
      rateLimitType,
      ...(typeof info.status === "string" ? { status: info.status } : {}),
      ...(typeof info.utilization === "number" &&
      Number.isFinite(info.utilization)
        ? { utilization: info.utilization }
        : {}),
      ...(typeof info.resetsAt === "string" || typeof info.resetsAt === "number"
        ? { resetsAt: info.resetsAt }
        : typeof info.overageResetsAt === "string" ||
            typeof info.overageResetsAt === "number"
          ? { resetsAt: info.overageResetsAt }
          : {}),
      observedAt,
    });
  }
  return out;
}

export function buildProviderQuotaSnapshots(
  sessions: readonly ProviderQuotaSession[],
  remoteEvidence: readonly ProviderQuotaEvidence[] = [],
): Record<Provider, ProviderQuotaSnapshot> {
  const latest: Partial<
    Record<
      Provider,
      Partial<
        Record<
          ProviderQuotaWindowId,
          {
            info: Record<string, unknown>;
            observedAt: string;
          }
        >
      >
    >
  > = {};
  const evidence = [
    ...providerQuotaEvidenceFromSessions(sessions),
    ...remoteEvidence,
  ];
  for (const row of evidence) {
    const observedAt =
      typeof row.observedAt === "string" && row.observedAt
        ? row.observedAt
        : new Date().toISOString();
    const info: Record<string, unknown> = {
      provider: row.provider,
      rateLimitType: row.rateLimitType,
      ...(row.status ? { status: row.status } : {}),
      ...(typeof row.utilization === "number"
        ? { utilization: row.utilization }
        : {}),
      ...(row.resetsAt !== undefined && row.resetsAt !== null
        ? { resetsAt: row.resetsAt }
        : {}),
    };
    const provider = row.provider;
    const windowId = quotaWindowIdFromInfo(info);
    const current = latest[provider]?.[windowId];
    if (!current || Date.parse(observedAt) > Date.parse(current.observedAt)) {
      latest[provider] = {
        ...(latest[provider] ?? {}),
        [windowId]: { info, observedAt },
      };
    }
  }

  const out = {} as Record<Provider, ProviderQuotaSnapshot>;
  for (const provider of PROVIDERS) {
    const windows = PROVIDER_QUOTA_WINDOW_DEFS[provider].map(
      (def): ProviderQuotaWindow => {
        const evidence = latest[provider]?.[def.id];
        const percentRemaining = evidence
          ? quotaPercentRemaining(evidence.info)
          : null;
        const resetAt = evidence ? quotaResetAt(evidence.info) : null;
        return {
          ...def,
          status: quotaWindowStatus(
            evidence?.info ?? null,
            percentRemaining,
            resetAt,
            evidence?.observedAt ?? null,
          ),
          percentRemaining,
          resetAt,
          observedAt: evidence?.observedAt ?? null,
        };
      },
    );
    const observedAt =
      windows
        .map((window) => window.observedAt)
        .filter((value): value is string => Boolean(value))
        .sort((a, b) => Date.parse(b) - Date.parse(a))[0] ?? null;
    out[provider] = {
      provider,
      status: quotaSnapshotStatus(windows),
      observedAt,
      windows,
    };
  }
  return out;
}

export function providerQuotaSummary(
  window: ProviderQuotaWindow | undefined,
): string {
  if (!window || window.status === "unknown") return "unknown";
  if (window.status === "stale") return "stale";
  if (window.percentRemaining === null) {
    return window.status === "exhausted" ? "exhausted" : "captured";
  }
  return `${Math.round(window.percentRemaining)}% left`;
}

export function formatProviderQuotaTimestamp(
  iso: string | null | undefined,
): string {
  if (!iso) return "";
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return "";
  return new Intl.DateTimeFormat([], {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  }).format(new Date(ms));
}

export function providerQuotaResetLabel(window: ProviderQuotaWindow): string {
  const resetAt = formatProviderQuotaTimestamp(window.resetAt);
  return resetAt ? `resets ${resetAt}` : "";
}
