import { test, expect } from "vitest";

import {
  contextWindowTokenCount,
  estimateTranscriptCost,
  estimateTurnCost,
  estimateTurnContextTokens,
  estimateUsageCostUSD,
  formatCompactTokens,
  formatComposerCostUsd,
  formatTurnCostUsd,
} from "./sessionCostEstimate";

function assertNearlyEqual(actual: number | null, expected: number): void {
  expect(actual).not.toBe(null);
  expect(Math.abs((actual ?? 0) - expected) < 1e-12, `expected ${expected}, got ${actual}`).toBeTruthy();
}

test("estimates Claude usage with cache write and read tokens", () => {
  const cost = estimateUsageCostUSD({
    input_tokens: 1_000,
    cache_creation_input_tokens: 2_000,
    cache_read_input_tokens: 3_000,
    output_tokens: 4_000,
  }, "claude-opus-4-8");

  assertNearlyEqual(cost, 0.119);
});

test("estimates OpenAI usage with nested cached input tokens", () => {
  const cost = estimateUsageCostUSD({
    input_tokens: 10_000,
    input_tokens_details: { cached_tokens: 4_000 },
    output_tokens: 2_000,
  }, "gpt-5.5");

  assertNearlyEqual(cost, 0.092);
});

test("prefers provider-reported cost when present", () => {
  const cost = estimateUsageCostUSD({
    input_tokens: 10_000,
    output_tokens: 10_000,
    total_cost_usd: "1.2345",
  }, "gpt-5.4");

  expect(cost).toBe(1.2345);
});

test("deduplicates transcript usage rows by turn", () => {
  const estimate = estimateTranscriptCost([
    { id: "a", turnId: "turn-1", turnUsage: { input_tokens: 1_000, output_tokens: 1_000 } },
    { id: "b", turnId: "turn-1", turnUsage: { input_tokens: 1_000, output_tokens: 1_000 } },
    { id: "c", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
  ], "gpt-5.4-mini");

  assertNearlyEqual(estimate?.amountUsd ?? null, 0.01575);
  expect(estimate?.tokens).toBe(6_000);
});

test("ignores transcript rows when provider usage is missing", () => {
  const estimate = estimateTranscriptCost([
    { id: "u", turnId: "turn-1" },
    { id: "a", turnId: "turn-1" },
  ], "gpt-5.4-mini");

  expect(estimate).toBe(null);
});

test("uses provider usage when available", () => {
  const estimate = estimateTranscriptCost([
    {
      id: "u",
      turnId: "turn-1",
      turnUsage: { input_tokens: 1_000, output_tokens: 1_000 },
    },
    { id: "a", turnId: "turn-1" },
  ], "gpt-5.4-mini");

  assertNearlyEqual(estimate?.amountUsd ?? null, 0.00525);
  expect(estimate?.tokens).toBe(2_000);
});

test("estimates one selected turn from mixed transcript rows", () => {
  const estimate = estimateTurnCost([
    { id: "a", turnId: "turn-1", turnUsage: { input_tokens: 10_000, output_tokens: 10_000 } },
    { id: "b", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
    { id: "c", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
  ], "gpt-5.4-mini", "turn-2");

  assertNearlyEqual(estimate?.amountUsd ?? null, 0.0105);
  expect(estimate?.tokens).toBe(4_000);
});

test("formats compact token counts", () => {
  expect(formatCompactTokens(0)).toBe("0");
  expect(formatCompactTokens(999)).toBe("999");
  expect(formatCompactTokens(1_000)).toBe("1k");
  expect(formatCompactTokens(423_999)).toBe("423k");
  expect(formatCompactTokens(999_999)).toBe("999k");
  expect(formatCompactTokens(1_000_000)).toBe("1m");
  expect(formatCompactTokens(1_230_000)).toBe("1.23m");
  expect(formatCompactTokens(1_239_999)).toBe("1.23m");
  expect(formatCompactTokens(1_200_000)).toBe("1.2m");
  expect(formatCompactTokens(12_900_000)).toBe("12.9m");
});

test("context window token count uses active uncached Codex delta for cumulative thread usage", () => {
  expect(contextWindowTokenCount({
        cached_input_tokens: 24_488_064,
        input_tokens: 25_131_214,
        output_tokens: 29_896,
        reasoning_output_tokens: 4_449,
        total_tokens: 25_161_110,
      }, 1_050_000, {
        usage_source: "thread.tokenUsage.updated",
      })).toBe(643_150);
});

test("context window token count uses Codex uncached delta even below the model window", () => {
  expect(contextWindowTokenCount({
        cached_input_tokens: 525_440,
        input_tokens: 608_743,
        output_tokens: 4_238,
        reasoning_output_tokens: 1_291,
        total_tokens: 612_981,
      }, 1_050_000, {
        usage_source: "thread.tokenUsage.updated",
      })).toBe(83_303);
});

test("context window token count keeps in-window cached prompts intact", () => {
  expect(contextWindowTokenCount({
        input_tokens: 180_000,
        cached_input_tokens: 120_000,
      }, 200_000)).toBe(180_000);
});

test("estimates one selected turn context tokens from latest usage row", () => {
  const rows = [
    {
      id: "a",
      turnId: "turn-1",
      turnUsage: {
        input_tokens: 500_000,
        cached_input_tokens: 450_000,
        output_tokens: 1_000,
      },
      usageObservation: { usage_source: "thread.tokenUsage.updated" },
    },
    {
      id: "b",
      turnId: "turn-1",
      turnUsage: {
        input_tokens: 608_743,
        cached_input_tokens: 525_440,
        output_tokens: 4_238,
      },
      usageObservation: { usage_source: "thread.tokenUsage.updated" },
    },
  ];

  expect(estimateTurnContextTokens(rows, 1_050_000, "turn-1")).toBe(83_303);
});

test("formats compact composer costs", () => {
  expect(formatComposerCostUsd(0)).toBe("$0.00");
  expect(formatComposerCostUsd(0.00012)).toBe("<$0.01");
  expect(formatComposerCostUsd(0.0012)).toBe("<$0.01");
  expect(formatComposerCostUsd(0.01234)).toBe("$0.01");
  expect(formatComposerCostUsd(0.025)).toBe("$0.03");
  expect(formatComposerCostUsd(1.2345)).toBe("$1.23");
  expect(formatComposerCostUsd(12.345)).toBe("$12.35");
});

test("formats tiny turn costs without rounding nonzero usage to zero", () => {
  expect(formatTurnCostUsd(0)).toBe("$0.00");
  expect(formatTurnCostUsd(0.000012)).toBe("<$0.01");
  expect(formatTurnCostUsd(0.00012)).toBe("<$0.01");
  expect(formatTurnCostUsd(0.0012)).toBe("<$0.01");
  expect(formatTurnCostUsd(0.012)).toBe("$0.01");
});

test("context window token count sums additive Claude cache tokens for a per-message snapshot", () => {
  // Claude reports cache_read/cache_creation as additive to input_tokens, so
  // the live prompt size is the sum. Real durable blob shape (session 509).
  expect(contextWindowTokenCount({
        input_tokens: 4,
        cache_read_input_tokens: 157_652,
        cache_creation_input_tokens: 161_334,
        output_tokens: 5_016,
      }, 1_000_000, { usage_source: "claude.message" })).toBe(318_990);
});

test("context window token count ignores the cumulative Claude terminal for occupancy", () => {
  // claude.result is cumulative across the turn's tool loop (cache reads
  // summed over every model call), so it over-counts occupancy. Real durable
  // blob shape (session 508): a naive sum would report 3.26M against a window.
  expect(contextWindowTokenCount({
        input_tokens: 266,
        cache_read_input_tokens: 3_219_249,
        cache_creation_input_tokens: 21_332,
        output_tokens: 19_380,
      }, 1_000_000, { usage_source: "claude.result" })).toBe(0);
});

test("context window token count does not fabricate occupancy from a bare Claude blob", () => {
  // The shipped bug returned input_tokens (4) as "occupancy" for Claude. A
  // pre-fix durable turn (cumulative terminal, no usage_observation) must
  // resolve to no occupancy rather than the fabricated uncached sliver.
  expect(contextWindowTokenCount({
        input_tokens: 4,
        cache_read_input_tokens: 157_652,
        cache_creation_input_tokens: 161_334,
      }, 1_000_000)).toBe(0);
});

test("estimates Claude turn context tokens from the latest snapshot, not the cumulative terminal", () => {
  const rows = [
    {
      id: "s1",
      turnId: "turn-1",
      turnUsage: { input_tokens: 2, cache_read_input_tokens: 100_000, cache_creation_input_tokens: 500 },
      usageObservation: { usage_source: "claude.message" },
    },
    {
      id: "s2",
      turnId: "turn-1",
      turnUsage: { input_tokens: 2, cache_read_input_tokens: 540_000, cache_creation_input_tokens: 800 },
      usageObservation: { usage_source: "claude.message" },
    },
    {
      id: "term",
      turnId: "turn-1",
      turnUsage: { input_tokens: 266, cache_read_input_tokens: 3_219_249, cache_creation_input_tokens: 21_332 },
      usageObservation: { usage_source: "claude.result" },
    },
  ];
  // Latest snapshot s2: 2 + 540000 + 800 = 540802. The cumulative terminal is skipped.
  expect(estimateTurnContextTokens(rows, 1_000_000, "turn-1")).toBe(540_802);
});

test("Claude turn cost uses the cumulative terminal, not per-message snapshots", () => {
  const rows = [
    {
      id: "s1",
      turnId: "turn-1",
      turnUsage: { input_tokens: 2, cache_read_input_tokens: 100_000, cache_creation_input_tokens: 500, output_tokens: 50 },
      usageObservation: { usage_source: "claude.message" },
    },
    {
      id: "term",
      turnId: "turn-1",
      turnUsage: { input_tokens: 266, cache_creation_input_tokens: 21_332, cache_read_input_tokens: 3_219_249, output_tokens: 19_380 },
      usageObservation: { usage_source: "claude.result" },
    },
  ];
  const estimate = estimateTurnCost(rows, "claude-opus-4-8", "turn-1");
  const cumulativeOnly = estimateUsageCostUSD(rows[1]?.turnUsage, "claude-opus-4-8");
  assertNearlyEqual(estimate?.amountUsd ?? null, cumulativeOnly ?? 0);
});
