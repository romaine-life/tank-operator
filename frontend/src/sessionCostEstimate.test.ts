import assert from "node:assert/strict";
import test from "node:test";

import {
  estimateTranscriptCost,
  estimateTranscriptCostUSD,
  estimateTurnCost,
  estimateUsageCostUSD,
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
  const cost = estimateTranscriptCostUSD([
    { id: "a", turnId: "turn-1", turnUsage: { input_tokens: 1_000, output_tokens: 1_000 } },
    { id: "b", turnId: "turn-1", turnUsage: { input_tokens: 1_000, output_tokens: 1_000 } },
    { id: "c", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
  ], "gpt-5.4-mini");

  assertNearlyEqual(cost, 0.01575);
});

test("falls back to visible transcript text when provider usage is missing", () => {
  const estimate = estimateTranscriptCost([
    {
      id: "u",
      kind: "message",
      role: "user",
      turnId: "turn-1",
      text: "x".repeat(400),
    },
    {
      id: "a",
      kind: "message",
      role: "assistant",
      turnId: "turn-1",
      text: "y".repeat(800),
    },
  ], "gpt-5.4-mini");

  assert.equal(estimate?.basis, "visible_transcript");
  assertNearlyEqual(estimate?.amountUsd ?? null, 0.000975);
});

test("reported usage wins over visible text fallback for the same turn", () => {
  const estimate = estimateTranscriptCost([
    {
      id: "u",
      kind: "message",
      role: "user",
      turnId: "turn-1",
      turnUsage: { input_tokens: 1_000, output_tokens: 1_000 },
      text: "x".repeat(10_000),
    },
    {
      id: "a",
      kind: "message",
      role: "assistant",
      turnId: "turn-1",
      text: "y".repeat(10_000),
    },
  ], "gpt-5.4-mini");

  assert.equal(estimate?.basis, "reported_usage");
  assertNearlyEqual(estimate?.amountUsd ?? null, 0.00525);
});

test("estimates one selected turn from mixed transcript rows", () => {
  const estimate = estimateTurnCost([
    { id: "a", turnId: "turn-1", turnUsage: { input_tokens: 10_000, output_tokens: 10_000 } },
    { id: "b", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
    { id: "c", turnId: "turn-2", turnUsage: { input_tokens: 2_000, output_tokens: 2_000 } },
  ], "gpt-5.4-mini", "turn-2");

  assert.equal(estimate?.basis, "reported_usage");
  assertNearlyEqual(estimate?.amountUsd ?? null, 0.0105);
});

test("formats compact composer costs", () => {
  assert.equal(formatComposerCostUsd(0), "$0.00");
  assert.equal(formatComposerCostUsd(0.01234), "$0.01");
  assert.equal(formatComposerCostUsd(0.025), "$0.03");
  assert.equal(formatComposerCostUsd(1.2345), "$1.23");
  assert.equal(formatComposerCostUsd(12.345), "$12.35");
});

test("formats tiny turn costs without rounding nonzero usage to zero", () => {
  assert.equal(formatTurnCostUsd(0), "$0.00");
  assert.equal(formatTurnCostUsd(0.000012), "<$0.0001");
  assert.equal(formatTurnCostUsd(0.00012), "$0.0001");
  assert.equal(formatTurnCostUsd(0.0012), "$0.001");
  assert.equal(formatTurnCostUsd(0.012), "$0.01");
});
