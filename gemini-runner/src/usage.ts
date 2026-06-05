// Usage + quota extraction for the proxyless gemini-runner.
//
// Why this exists: the retired Gemini runtime (purged in #834) dropped the
// gemini CLI's terminal `result` event on the floor, so Gemini sessions
// reported no usage and never appeared in the provider-capacity surface that
// Claude/Codex use. Re-adding Gemini "with usage stats" (the explicit ask)
// means parsing that event into the same two durable signals the other
// providers emit:
//
//   1. Per-turn `turn.usage` token counts (durable Tank events), and
//   2. A `provider_rate_limit_info` snapshot reported through
//      /api/internal/sessions/{id}/runtime-config, which the SPA renders in
//      the provider-capacity strip + session settings.
//
// Gemini auth here is proxyless Google OAuth (Code Assist), whose quota is a
// per-day request budget rather than Claude's 5h/weekly windows. We model a
// single `gemini:daily` window that resets at UTC midnight. Utilization is
// derived from the requests this pod has observed against a configurable daily
// cap (GEMINI_DAILY_REQUEST_CAP). It is per-session-observed, not account-wide
// — documented as such so the number is honest; account-wide aggregation is a
// named follow-up, not a silent fabrication.

export interface GeminiTurnUsage {
  model: string;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  totalTokens: number;
  requests: number;
}

function num(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

// Parse the gemini CLI `-o stream-json` terminal `result` event. The CLI has
// shipped two shapes across versions; handle both rather than pinning one:
//   A) { stats: { models: { "<model>": { api: { totalRequests },
//                                        tokens: { prompt, candidates, cached, total } } } } }
//   B) genai-style { usageMetadata: { promptTokenCount, candidatesTokenCount,
//                                      cachedContentTokenCount, totalTokenCount } }
// Returns null when no usage is present (e.g. an interrupted turn) so callers
// can skip emission rather than publish a zero-usage event.
export function parseGeminiResultStats(result: unknown): GeminiTurnUsage | null {
  if (!result || typeof result !== "object") return null;
  const root = result as Record<string, any>;

  const stats = root.stats ?? root;
  const models = stats?.models;
  if (models && typeof models === "object") {
    let model = "";
    let inputTokens = 0;
    let outputTokens = 0;
    let cachedTokens = 0;
    let totalTokens = 0;
    let requests = 0;
    for (const [name, raw] of Object.entries<any>(models)) {
      if (!model) model = name;
      const tokens = raw?.tokens ?? {};
      const prompt = num(tokens.prompt);
      const candidates = num(tokens.candidates);
      inputTokens += prompt;
      outputTokens += candidates;
      cachedTokens += num(tokens.cached);
      totalTokens += num(tokens.total) || prompt + candidates;
      requests += num(raw?.api?.totalRequests);
    }
    if (totalTokens === 0 && requests === 0) return null;
    return {
      model,
      inputTokens,
      outputTokens,
      cachedTokens,
      totalTokens,
      requests: requests || 1,
    };
  }

  const meta = root.usageMetadata ?? root.usage;
  if (meta && typeof meta === "object") {
    const inputTokens = num(meta.promptTokenCount ?? meta.prompt_tokens ?? meta.input_tokens);
    const outputTokens = num(
      meta.candidatesTokenCount ?? meta.completion_tokens ?? meta.output_tokens,
    );
    const cachedTokens = num(meta.cachedContentTokenCount ?? meta.cached_tokens);
    const totalTokens = num(meta.totalTokenCount ?? meta.total_tokens) || inputTokens + outputTokens;
    if (totalTokens === 0) return null;
    return {
      model: String(root.model ?? ""),
      inputTokens,
      outputTokens,
      cachedTokens,
      totalTokens,
      requests: 1,
    };
  }
  return null;
}

// The durable `turn.usage` payload. Mirrors the field vocabulary the SPA's
// transcript projection already understands (input/output/total tokens) so
// Gemini turns render usage the same way Claude/Codex turns do.
export function turnUsagePayload(usage: GeminiTurnUsage): Record<string, unknown> {
  return {
    provider: "gemini",
    model: usage.model,
    input_tokens: usage.inputTokens,
    output_tokens: usage.outputTokens,
    cached_tokens: usage.cachedTokens,
    total_tokens: usage.totalTokens,
    requests: usage.requests,
  };
}

export interface GeminiUsageAccumulator {
  windowStartMs: number;
  requests: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  lastModel: string;
}

function utcMidnightMs(nowMs: number): number {
  const d = new Date(nowMs);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate());
}

function nextUtcMidnightMs(nowMs: number): number {
  return utcMidnightMs(nowMs) + 24 * 60 * 60 * 1000;
}

export function newAccumulator(nowMs: number): GeminiUsageAccumulator {
  return {
    windowStartMs: utcMidnightMs(nowMs),
    requests: 0,
    totalTokens: 0,
    inputTokens: 0,
    outputTokens: 0,
    lastModel: "",
  };
}

// Fold one turn's usage into the daily accumulator, rolling the window over at
// UTC midnight so the reported utilization tracks the Code Assist daily reset.
export function accumulate(
  acc: GeminiUsageAccumulator,
  usage: GeminiTurnUsage,
  nowMs: number,
): GeminiUsageAccumulator {
  const windowStart = utcMidnightMs(nowMs);
  const base =
    windowStart !== acc.windowStartMs ? newAccumulator(nowMs) : { ...acc };
  base.requests += Math.max(1, usage.requests);
  base.totalTokens += usage.totalTokens;
  base.inputTokens += usage.inputTokens;
  base.outputTokens += usage.outputTokens;
  if (usage.model) base.lastModel = usage.model;
  return base;
}

// Build the provider_rate_limit_info snapshot consumed by the orchestrator's
// runtime-config handler and rendered in the capacity strip. utilization is a
// 0..1 fraction of the configured daily request cap; status flips to "limited"
// once the cap is reached. Only the whitelisted fields are included (see the
// orchestrator's sanitizeProviderRateLimitInfo).
export function providerRateLimitInfo(
  acc: GeminiUsageAccumulator,
  dailyRequestCap: number,
  nowMs: number,
): Record<string, unknown> {
  const cap = dailyRequestCap > 0 ? dailyRequestCap : 0;
  const utilization = cap > 0 ? Math.min(1, acc.requests / cap) : 0;
  const info: Record<string, unknown> = {
    provider: "gemini",
    status: cap > 0 && acc.requests >= cap ? "limited" : "ok",
    rateLimitType: "gemini:daily",
    resetsAt: new Date(nextUtcMidnightMs(nowMs)).toISOString(),
  };
  if (cap > 0) info.utilization = utilization;
  return info;
}
