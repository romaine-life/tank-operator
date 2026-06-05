import assert from "node:assert/strict";
import test from "node:test";

import {
  accumulate,
  newAccumulator,
  parseGeminiResultStats,
  providerRateLimitInfo,
  turnUsagePayload,
} from "./usage.js";

test("parseGeminiResultStats reads the CLI stats.models token shape", () => {
  const usage = parseGeminiResultStats({
    type: "result",
    subtype: "success",
    stats: {
      models: {
        "gemini-3.5-flash": {
          api: { totalRequests: 2 },
          tokens: { prompt: 1200, candidates: 300, cached: 100, total: 1500 },
        },
      },
    },
  });
  assert.ok(usage);
  assert.equal(usage?.model, "gemini-3.5-flash");
  assert.equal(usage?.inputTokens, 1200);
  assert.equal(usage?.outputTokens, 300);
  assert.equal(usage?.cachedTokens, 100);
  assert.equal(usage?.totalTokens, 1500);
  assert.equal(usage?.requests, 2);
});

test("parseGeminiResultStats falls back to genai usageMetadata", () => {
  const usage = parseGeminiResultStats({
    model: "gemini-3.1-pro",
    usageMetadata: {
      promptTokenCount: 50,
      candidatesTokenCount: 25,
      totalTokenCount: 75,
    },
  });
  assert.ok(usage);
  assert.equal(usage?.inputTokens, 50);
  assert.equal(usage?.outputTokens, 25);
  assert.equal(usage?.totalTokens, 75);
  assert.equal(usage?.requests, 1);
});

test("parseGeminiResultStats returns null when no usage is present", () => {
  assert.equal(parseGeminiResultStats({ type: "result", subtype: "success" }), null);
  assert.equal(parseGeminiResultStats(null), null);
});

test("turnUsagePayload mirrors the durable usage vocabulary", () => {
  const payload = turnUsagePayload({
    model: "gemini-3.5-flash",
    inputTokens: 10,
    outputTokens: 4,
    cachedTokens: 1,
    totalTokens: 14,
    requests: 1,
  });
  assert.equal(payload.provider, "gemini");
  assert.equal(payload.input_tokens, 10);
  assert.equal(payload.output_tokens, 4);
  assert.equal(payload.total_tokens, 14);
});

test("accumulate sums within a UTC day and resets across the boundary", () => {
  const day1 = Date.UTC(2026, 5, 5, 10, 0, 0);
  let acc = newAccumulator(day1);
  acc = accumulate(acc, mkUsage(3, 100), day1);
  acc = accumulate(acc, mkUsage(2, 50), Date.UTC(2026, 5, 5, 23, 0, 0));
  assert.equal(acc.requests, 5);
  assert.equal(acc.totalTokens, 150);

  const day2 = Date.UTC(2026, 5, 6, 1, 0, 0);
  acc = accumulate(acc, mkUsage(1, 10), day2);
  assert.equal(acc.requests, 1, "new UTC day resets the request counter");
  assert.equal(acc.totalTokens, 10);
});

test("providerRateLimitInfo reports a daily window with capped utilization", () => {
  const now = Date.UTC(2026, 5, 5, 12, 0, 0);
  let acc = newAccumulator(now);
  acc = accumulate(acc, mkUsage(250, 1000), now);
  const info = providerRateLimitInfo(acc, 1000, now);
  assert.equal(info.provider, "gemini");
  assert.equal(info.rateLimitType, "gemini:daily");
  assert.equal(info.status, "ok");
  assert.equal(info.utilization, 0.25);
  assert.equal(info.resetsAt, new Date(Date.UTC(2026, 5, 6)).toISOString());
});

test("providerRateLimitInfo omits utilization and reports ok without a cap", () => {
  const now = Date.UTC(2026, 5, 5, 12, 0, 0);
  let acc = newAccumulator(now);
  acc = accumulate(acc, mkUsage(5, 100), now);
  const info = providerRateLimitInfo(acc, 0, now);
  assert.equal(info.status, "ok");
  assert.equal(info.utilization, undefined);
});

test("providerRateLimitInfo flips to limited at the cap", () => {
  const now = Date.UTC(2026, 5, 5, 12, 0, 0);
  let acc = newAccumulator(now);
  acc = accumulate(acc, mkUsage(1000, 1), now);
  const info = providerRateLimitInfo(acc, 1000, now);
  assert.equal(info.status, "limited");
  assert.equal(info.utilization, 1);
});

function mkUsage(requests: number, totalTokens: number) {
  return {
    model: "gemini-3.5-flash",
    inputTokens: totalTokens,
    outputTokens: 0,
    cachedTokens: 0,
    totalTokens,
    requests,
  };
}
