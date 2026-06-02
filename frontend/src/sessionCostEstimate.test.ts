import assert from "node:assert/strict";
import test from "node:test";

import {
  currentContextTokenCount,
  estimateTranscriptCost,
  estimateTurnCost,
  estimateTurnContextTokens,
  estimateUsageCostUSD,
  formatCompactTokens,
  formatComposerCostUsd,
  formatTurnCostUsd,
} from "./sessionCostEstimate";

function assertNearlyEqual(actual: number | null, expected: number): void {
  assert.notEqual(actual, null);
  assert.ok(Math.abs((actual ?? 0) - expected) < 1e-12, `expected ${expected}, got ${actual}`);
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

  assert.equal(cost, 1.2345);
});

test("deduplicates transcript usage rows by turn", () => {
  const estimate = estimateTranscriptCost([
    { id: "a", turnId: "turn-1", turnUsage: { input_tokens: 1_000, output_tokens: 1_000 } },
    { id: "b", turnId: "turn-1", turnUsage: { input_tokens: 1_000, output_tokens: 1_000 } },
    { id: "c", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
  ], "gpt-5.4-mini");

  assertNearlyEqual(estimate?.amountUsd ?? null, 0.01575);
  assert.equal(estimate?.tokens, 6_000);
});

test("ignores transcript rows when provider usage is missing", () => {
  const estimate = estimateTranscriptCost([
    { id: "u", turnId: "turn-1" },
    { id: "a", turnId: "turn-1" },
  ], "gpt-5.4-mini");

  assert.equal(estimate, null);
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
  assert.equal(estimate?.tokens, 2_000);
});

test("estimates one selected turn from mixed transcript rows", () => {
  const estimate = estimateTurnCost([
    { id: "a", turnId: "turn-1", turnUsage: { input_tokens: 10_000, output_tokens: 10_000 } },
    { id: "b", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
    { id: "c", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
  ], "gpt-5.4-mini", "turn-2");

  assertNearlyEqual(estimate?.amountUsd ?? null, 0.0105);
  assert.equal(estimate?.tokens, 4_000);
});

test("formats compact token counts", () => {
  assert.equal(formatCompactTokens(0), "0");
  assert.equal(formatCompactTokens(999), "999");
  assert.equal(formatCompactTokens(1_000), "1k");
  assert.equal(formatCompactTokens(999_999), "999k");
  assert.equal(formatCompactTokens(1_000_000), "1m");
  assert.equal(formatCompactTokens(12_900_000), "12m");
});

test("current context token count uses active uncached Codex delta for cumulative thread usage", () => {
  assert.equal(currentContextTokenCount({
    cached_input_tokens: 24_488_064,
    input_tokens: 25_131_214,
    output_tokens: 29_896,
    reasoning_output_tokens: 4_449,
    total_tokens: 25_161_110,
  }, {
    usage_source: "thread.tokenUsage.updated",
  }), 643_150);
});

test("current context token count uses Codex uncached delta even below a model window", () => {
  assert.equal(currentContextTokenCount({
    cached_input_tokens: 525_440,
    input_tokens: 608_743,
    output_tokens: 4_238,
    reasoning_output_tokens: 1_291,
    total_tokens: 612_981,
  }, {
    usage_source: "thread.tokenUsage.updated",
  }), 83_303);
});

test("current context token count keeps non-cumulative cached prompts intact", () => {
  assert.equal(currentContextTokenCount({
    input_tokens: 180_000,
    cached_input_tokens: 120_000,
  }), 180_000);
});

test("context token count does not infer cumulative usage from token magnitude", () => {
  assert.equal(currentContextTokenCount({
    input_tokens: 1_250_000,
    cached_input_tokens: 900_000,
  }), 1_250_000);
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

  assert.equal(estimateTurnContextTokens(rows, "turn-1"), 83_303);
});

test("formats compact composer costs", () => {
  assert.equal(formatComposerCostUsd(0), "$0.00");
  assert.equal(formatComposerCostUsd(0.00012), "<$0.01");
  assert.equal(formatComposerCostUsd(0.0012), "<$0.01");
  assert.equal(formatComposerCostUsd(0.01234), "$0.01");
  assert.equal(formatComposerCostUsd(0.025), "$0.03");
  assert.equal(formatComposerCostUsd(1.2345), "$1.23");
  assert.equal(formatComposerCostUsd(12.345), "$12.35");
});

test("formats tiny turn costs without rounding nonzero usage to zero", () => {
  assert.equal(formatTurnCostUsd(0), "$0.00");
  assert.equal(formatTurnCostUsd(0.000012), "<$0.01");
  assert.equal(formatTurnCostUsd(0.00012), "<$0.01");
  assert.equal(formatTurnCostUsd(0.0012), "<$0.01");
  assert.equal(formatTurnCostUsd(0.012), "$0.01");
});
