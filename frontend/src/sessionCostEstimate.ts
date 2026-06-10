type UsageRow = {
  id?: string;
  turnId?: string;
  turnUsage?: unknown;
  usageObservation?: unknown;
};

export type SessionCostEstimate = {
  amountUsd: number;
  tokens: number;
};

type ModelRates = {
  input: number;
  cachedInput?: number;
  cacheWrite?: number;
  cacheRead?: number;
  output: number;
};

const PER_MILLION = 1_000_000;

// API-equivalent rates in USD per million tokens. This is deliberately a
// coarse UI estimate; provider dashboards remain the billing source of truth.
const MODEL_RATES_USD_PER_MTOK: Record<string, ModelRates> = {
  "claude-opus-4-8": { input: 5, cacheWrite: 6.25, cacheRead: 0.5, output: 25 },
  "claude-opus-4-7": { input: 5, cacheWrite: 6.25, cacheRead: 0.5, output: 25 },
  "claude-opus-4-7-1m": { input: 5, cacheWrite: 6.25, cacheRead: 0.5, output: 25 },
  "claude-sonnet-4-6": { input: 3, cacheWrite: 3.75, cacheRead: 0.3, output: 15 },
  "claude-haiku-4-5": { input: 1, cacheWrite: 1.25, cacheRead: 0.1, output: 5 },
  // Mythos-tier (claude-fable-5): $10/$50 per Mtok; 5m cache write 1.25x input,
  // cache read 0.1x input (the "90% off cached input" headline).
  "claude-fable-5": { input: 10, cacheWrite: 12.5, cacheRead: 1.0, output: 50 },
  "gpt-5.5": { input: 5, cachedInput: 0.5, output: 30 },
  "gpt-5.4": { input: 2.5, cachedInput: 0.25, output: 15 },
  "gpt-5.4-mini": { input: 0.75, cachedInput: 0.075, output: 4.5 },
  "gpt-5.3-codex": { input: 1.75, cachedInput: 0.175, output: 14 },
};

export function estimateUsageCostUSD(usage: unknown, modelId: string): number | null {
  if (!isRecord(usage)) return null;
  const directCost = numberField(usage, "cost_usd") ?? numberField(usage, "total_cost_usd");
  if (directCost !== null && directCost >= 0) return directCost;

  const rates = MODEL_RATES_USD_PER_MTOK[modelId];
  if (!rates) return null;

  const outputTokens =
    numberField(usage, "output_tokens") ??
    numberField(usage, "completion_tokens") ??
    numberField(usage, "reasoning_output_tokens") ??
    0;

  if (modelId.startsWith("claude-")) {
    const inputTokens = numberField(usage, "input_tokens") ?? numberField(usage, "prompt_tokens") ?? 0;
    const cacheWriteTokens = numberField(usage, "cache_creation_input_tokens") ?? 0;
    const cacheReadTokens = numberField(usage, "cache_read_input_tokens") ?? 0;
    return costFromTokens(inputTokens, rates.input) +
      costFromTokens(cacheWriteTokens, rates.cacheWrite ?? rates.input) +
      costFromTokens(cacheReadTokens, rates.cacheRead ?? rates.input) +
      costFromTokens(outputTokens, rates.output);
  }

  const flatInputTokens = numberField(usage, "input_tokens") ?? numberField(usage, "prompt_tokens") ?? 0;
  const cachedInputTokens = openAiCachedInputTokens(usage);
  const uncachedInputTokens = Math.max(0, flatInputTokens - cachedInputTokens);
  return costFromTokens(uncachedInputTokens, rates.input) +
    costFromTokens(cachedInputTokens, rates.cachedInput ?? rates.input) +
    costFromTokens(outputTokens, rates.output);
}

export function estimateTurnCost(
  rows: UsageRow[],
  modelId: string,
  turnId: string,
): SessionCostEstimate | null {
  const targetTurnId = turnId.trim();
  if (!targetTurnId) return null;
  return estimateTranscriptCost(
    rows.filter((row) => row.turnId === targetTurnId),
    modelId,
  );
}

export function estimateTurnContextTokens(
  rows: UsageRow[],
  contextWindow: number,
  turnId: string,
): number | null {
  const targetTurnId = turnId.trim();
  if (!targetTurnId) return null;
  const turns = transcriptTurns(rows.filter((row) => row.turnId === targetTurnId));
  const turn = turns.get(targetTurnId);
  if (!turn) return null;
  for (let index = turn.usage.length - 1; index >= 0; index -= 1) {
    const usage = turn.usage[index];
    if (!usage) continue;
    const tokens = contextWindowTokenCount(usage.usage, contextWindow, usage.usageObservation);
    if (tokens > 0) return tokens;
  }
  return null;
}

export function estimateTranscriptCost(rows: UsageRow[], modelId: string): SessionCostEstimate | null {
  let total = 0;
  let tokens = 0;
  let reportedTurns = 0;
  const turns = transcriptTurns(rows);

  for (const turn of turns.values()) {
    let reportedCost: number | null = null;
    let reportedTokens = 0;
    for (let index = turn.usage.length - 1; index >= 0; index -= 1) {
      const usage = turn.usage[index]?.usage;
      reportedCost = estimateUsageCostUSD(usage, modelId);
      reportedTokens = usageTokenCount(usage);
      if (reportedCost !== null || reportedTokens > 0) break;
    }
    if (reportedCost !== null) {
      total += reportedCost;
      tokens += reportedTokens;
      reportedTurns += 1;
      continue;
    }
  }

  if (reportedTurns === 0) return null;
  return {
    amountUsd: total,
    tokens,
  };
}

export function formatComposerCostUsd(value: number): string {
  return formatCostUsdAtCents(value);
}

export function formatTurnCostUsd(value: number): string {
  return formatCostUsdAtCents(value);
}

export function formatCompactTokens(value: number): string {
  const safeValue = Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
  if (safeValue < 1_000) return String(safeValue);
  if (safeValue < 1_000_000) return `${Math.floor(safeValue / 1_000)}k`;
  return formatCompactMillionTokens(safeValue);
}

function formatCompactMillionTokens(value: number): string {
  const wholeMillions = Math.floor(value / 1_000_000);
  const millionHundredths = Math.floor((value % 1_000_000) / 10_000);
  if (millionHundredths === 0) return `${wholeMillions}m`;
  return `${wholeMillions}.${String(millionHundredths).padStart(2, "0").replace(/0+$/, "")}m`;
}

// classifyUsageShape distinguishes the two provider usage architectures Tank
// consumes. They treat cached input tokens OPPOSITELY:
//
//   - "claude": cache_read_input_tokens / cache_creation_input_tokens are
//     ADDITIVE to input_tokens. With prompt caching always on, input_tokens
//     is only the tiny uncached sliver; the live prompt size is
//     input + cache_read + cache_creation.
//   - "openai": input_tokens (or prompt_tokens) is the WHOLE prompt and the
//     cached count is a SUBSET of it; the uncached delta is input - cached.
//
// Reading a Claude blob with OpenAI semantics treats cache_read as a subset
// to subtract, leaving the uncached sliver (e.g. 4 tokens) as the reported
// "context occupancy" — the bug this classifier exists to prevent.
type UsageShape = "claude" | "openai" | "unknown";

function classifyUsageShape(usage: Record<string, unknown>): UsageShape {
  if ("cache_read_input_tokens" in usage || "cache_creation_input_tokens" in usage) {
    return "claude";
  }
  if (
    "cached_input_tokens" in usage ||
    "input_cached_tokens" in usage ||
    "total_tokens" in usage ||
    "input_tokens_details" in usage ||
    "prompt_tokens_details" in usage
  ) {
    return "openai";
  }
  return "unknown";
}

function usageSourceTag(usageObservation: unknown): string {
  if (!isRecord(usageObservation)) return "";
  const source = usageObservation.usage_source;
  return typeof source === "string" ? source : "";
}

export function contextWindowTokenCount(
  usage: unknown,
  contextWindow: number,
  usageObservation?: unknown,
): number {
  if (!isRecord(usage)) return 0;
  const safeContextWindow = Number.isFinite(contextWindow)
    ? Math.max(1, Math.floor(contextWindow))
    : 1;

  if (classifyUsageShape(usage) === "claude") {
    // For Claude, occupancy is derivable ONLY from a per-message snapshot
    // (usage_source="claude.message"), where input + cache_read +
    // cache_creation is that single model call's prompt size. The cumulative
    // terminal (claude.result) sums cache reads across every tool-loop
    // iteration, so it over-counts occupancy by multiples — it drives cost,
    // not the context gauge. Anything not explicitly a snapshot (the
    // terminal, or pre-fix rows with no observation) yields no occupancy, so
    // the gauge reads "unavailable" rather than a fabricated number.
    if (usageSourceTag(usageObservation) !== "claude.message") return 0;
    const inputTokens = numberField(usage, "input_tokens") ?? 0;
    const cacheReadTokens = numberField(usage, "cache_read_input_tokens") ?? 0;
    const cacheCreationTokens = numberField(usage, "cache_creation_input_tokens") ?? 0;
    return Math.max(0, Math.floor(inputTokens + cacheReadTokens + cacheCreationTokens));
  }

  const inputTokens = numberField(usage, "input_tokens") ?? numberField(usage, "prompt_tokens") ?? 0;
  if (inputTokens <= 0) return 0;

  const cachedInputTokens =
    numberField(usage, "cached_input_tokens") ??
    openAiCachedInputTokens(usage) ??
    0;
  const uncachedInputTokens = Math.max(0, inputTokens - cachedInputTokens);

  // Codex thread.tokenUsage.updated reports cumulative thread input and
  // cached-input totals. Those totals can climb far beyond the model context
  // window, while the uncached delta is the current active context pressure.
  // Providers that report an in-window prompt count keep the raw input total.
  if (
    (isCodexCumulativeThreadUsage(usageObservation) || inputTokens > safeContextWindow) &&
    uncachedInputTokens > 0
  ) {
    return Math.floor(uncachedInputTokens);
  }
  return Math.floor(inputTokens);
}

function formatCostUsdAtCents(value: number): string {
  const safeValue = Number.isFinite(value) ? Math.max(0, value) : 0;
  if (safeValue === 0) return "$0.00";
  if (safeValue < 0.01) return "<$0.01";
  return `$${safeValue.toFixed(2)}`;
}

function costFromTokens(tokens: number, ratePerMillion: number): number {
  return (Math.max(0, tokens) / PER_MILLION) * ratePerMillion;
}

function usageTokenCount(usage: unknown): number {
  if (!isRecord(usage)) return 0;
  const totalTokens = numberField(usage, "total_tokens");
  if (totalTokens !== null) return Math.max(0, Math.floor(totalTokens));

  const outputTokens =
    numberField(usage, "output_tokens") ??
    numberField(usage, "completion_tokens") ??
    0;
  const reasoningOutputTokens = numberField(usage, "reasoning_output_tokens") ?? 0;
  const inputTokens = numberField(usage, "input_tokens") ?? numberField(usage, "prompt_tokens") ?? 0;
  const cacheWriteTokens = numberField(usage, "cache_creation_input_tokens") ?? 0;
  const cacheReadTokens =
    numberField(usage, "cache_read_input_tokens") ??
    openAiCachedInputTokens(usage);
  return Math.max(0, Math.floor(
    inputTokens +
    cacheWriteTokens +
    cacheReadTokens +
    outputTokens +
    reasoningOutputTokens,
  ));
}

function transcriptTurns(rows: UsageRow[]): Map<string, { usage: Array<{ usage: unknown; usageObservation?: unknown }> }> {
  const turns = new Map<string, { usage: Array<{ usage: unknown; usageObservation?: unknown }> }>();
  let anonymousUsageIndex = 0;

  for (const row of rows) {
    const turnId = row.turnId || (
      row.turnUsage !== undefined && row.turnUsage !== null
        ? `usage:${row.id || anonymousUsageIndex++}`
        : ""
    );
    if (!turnId) continue;
    let turn = turns.get(turnId);
    if (!turn) {
      turn = { usage: [] };
      turns.set(turnId, turn);
    }
    if (row.turnUsage !== undefined && row.turnUsage !== null) {
      turn.usage.push({
        usage: row.turnUsage,
        usageObservation: row.usageObservation,
      });
    }
  }

  return turns;
}

function openAiCachedInputTokens(usage: Record<string, unknown>): number {
  const flat =
    numberField(usage, "cached_input_tokens") ??
    numberField(usage, "cache_read_input_tokens") ??
    numberField(usage, "input_cached_tokens");
  if (flat !== null) return flat;

  const inputDetails = recordField(usage, "input_tokens_details") ?? recordField(usage, "prompt_tokens_details");
  return inputDetails ? numberField(inputDetails, "cached_tokens") ?? 0 : 0;
}

function isCodexCumulativeThreadUsage(value: unknown): boolean {
  if (!isRecord(value)) return false;
  return value.usage_source === "thread.tokenUsage.updated";
}

function numberField(record: Record<string, unknown>, key: string): number | null {
  const value = record[key];
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return null;
}

function recordField(record: Record<string, unknown>, key: string): Record<string, unknown> | null {
  const value = record[key];
  return isRecord(value) ? value : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
