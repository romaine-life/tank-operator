type UsageRow = {
  id?: string;
  kind?: string;
  role?: string;
  text?: string;
  turnId?: string;
  turnUsage?: unknown;
};

export type SessionCostEstimateBasis = "reported_usage" | "visible_transcript";

export type SessionCostEstimate = {
  amountUsd: number;
  basis: SessionCostEstimateBasis;
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

export function estimateTranscriptCostUSD(rows: UsageRow[], modelId: string): number | null {
  return estimateTranscriptCost(rows, modelId)?.amountUsd ?? null;
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

export function estimateTranscriptCost(rows: UsageRow[], modelId: string): SessionCostEstimate | null {
  let total = 0;
  let reportedTurns = 0;
  let fallbackTurns = 0;
  const turns = transcriptTurns(rows);

  for (const turn of turns.values()) {
    let reportedCost: number | null = null;
    for (const usage of turn.usage) {
      reportedCost = estimateUsageCostUSD(usage, modelId);
      if (reportedCost !== null) break;
    }
    if (reportedCost !== null) {
      total += reportedCost;
      reportedTurns += 1;
      continue;
    }
    const visibleCost = estimateVisibleTurnCostUSD(turn, modelId);
    if (visibleCost !== null) {
      total += visibleCost;
      fallbackTurns += 1;
    }
  }

  if (reportedTurns + fallbackTurns === 0) return null;
  return {
    amountUsd: total,
    basis: fallbackTurns > 0 ? "visible_transcript" : "reported_usage",
  };
}

export function formatComposerCostUsd(value: number): string {
  const safeValue = Number.isFinite(value) ? Math.max(0, value) : 0;
  return `$${safeValue.toFixed(2)}`;
}

export function formatTurnCostUsd(value: number): string {
  const safeValue = Number.isFinite(value) ? Math.max(0, value) : 0;
  if (safeValue === 0) return "$0.00";
  if (safeValue < 0.0001) return "<$0.0001";
  if (safeValue < 0.001) return `$${safeValue.toFixed(4)}`;
  if (safeValue < 0.01) return `$${safeValue.toFixed(3)}`;
  return formatComposerCostUsd(safeValue);
}

function costFromTokens(tokens: number, ratePerMillion: number): number {
  return (Math.max(0, tokens) / PER_MILLION) * ratePerMillion;
}

function transcriptTurns(rows: UsageRow[]): Map<string, { usage: unknown[]; inputText: string[]; outputText: string[] }> {
  const turns = new Map<string, { usage: unknown[]; inputText: string[]; outputText: string[] }>();
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
      turn = { usage: [], inputText: [], outputText: [] };
      turns.set(turnId, turn);
    }
    if (row.turnUsage !== undefined && row.turnUsage !== null) {
      turn.usage.push(row.turnUsage);
    }
    if (row.kind === "message" && typeof row.text === "string" && row.text.trim()) {
      if (row.role === "assistant") {
        turn.outputText.push(row.text);
      } else if (row.role === "user") {
        turn.inputText.push(row.text);
      }
    }
  }

  return turns;
}

function estimateVisibleTurnCostUSD(
  turn: { inputText: string[]; outputText: string[] },
  modelId: string,
): number | null {
  const inputTokens = estimateTextTokens(turn.inputText.join("\n"));
  const outputTokens = estimateTextTokens(turn.outputText.join("\n"));
  if (inputTokens + outputTokens === 0) return null;
  return estimateUsageCostUSD({ input_tokens: inputTokens, output_tokens: outputTokens }, modelId);
}

function estimateTextTokens(text: string): number {
  const trimmed = text.trim();
  if (!trimmed) return 0;
  return Math.max(1, Math.ceil(trimmed.length / 4));
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
