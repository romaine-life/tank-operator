import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const cssSource = readFileSync(new URL("./index.css", import.meta.url), "utf8");

test("turn cost estimate is not suppressed while a turn is running", () => {
  assert.match(appSource, /costEstimate:\s*estimateTurnCost\(costRows,\s*modelId,\s*turnId\)/);
  assert.doesNotMatch(appSource, /costEstimate:\s*isActive\s*\?\s*null\s*:\s*estimateTurnCost/);
  assert.match(appSource, /\{selected\.costEstimate\s*&&\s*\(/);
  assert.doesNotMatch(appSource, /\{!selected\.active\s*&&\s*selected\.costEstimate\s*&&\s*\(/);
});

test("turn cost UI does not expose transcript cost fallback copy", () => {
  assert.doesNotMatch(appSource, /visible transcript text/i);
  assert.doesNotMatch(appSource, /Partial \$\{normalizedScope\} cost floor/);
  assert.doesNotMatch(appSource, /SessionCostEstimateBasis/);
  assert.doesNotMatch(appSource, /basis=\{selected\.costEstimate\.basis\}/);
  assert.doesNotMatch(appSource, /basis=\{sessionCostEstimate\?\.basis/);
});

test("composer token count uses current context, not cumulative session usage", () => {
  assert.match(appSource, /cost=\{\{[\s\S]*?tokens: tokensUsed,[\s\S]*?tokenScopeLabel: "current context tokens"/);
  assert.doesNotMatch(appSource, /tokens=\{sessionCostEstimate\?\.tokens\s*\?\?\s*null\}/);
  assert.doesNotMatch(appSource, /tokens:\s*sessionCostEstimate\?\.tokens\s*\?\?\s*null/);
});

test("composer cost estimate separates context tokens from dollars", () => {
  assert.match(appSource, /className="run-cost-estimate-metric run-cost-estimate-metric-tokens"/);
  assert.match(appSource, /className="run-cost-estimate-divider"/);
  assert.match(appSource, /className="run-cost-estimate-metric run-cost-estimate-metric-cost"/);
  assert.match(appSource, /className="run-cost-estimate-label">ctx</);
  assert.match(appSource, /className="run-cost-estimate-label">usd</);
  assert.doesNotMatch(appSource, /run-cost-estimate-separator/);
});

test("composer context percentage is always visible and uses provider-observed session window", () => {
  assert.match(appSource, /runtime_context_window_tokens/);
  assert.match(appSource, /contextWindow: runtimeContextWindowTokens/);
  assert.match(appSource, /usage=\{\{ tokensUsed: 0, contextWindow: 0 \}\}/);
  assert.doesNotMatch(appSource, /usage=\{[\s\S]*runtimeContextWindowTokens > 0/);
  assert.match(cssSource, /run-usage-ring/);
  assert.doesNotMatch(appSource, /run-usage-ring-svg/);
  assert.doesNotMatch(appSource, /data-level="mid"/);
  assert.doesNotMatch(appSource, /CONTEXT_WINDOW_BY_MODEL/);
  assert.doesNotMatch(appSource, /getContextWindow/);
});

test("composer cost estimate reserves dashes for explicit loading placeholders", () => {
  assert.match(appSource, /const unavailable = placeholder;/);
  assert.doesNotMatch(appSource, /placeholder\s*\|\|\s*amountUsd === null/);
  assert.match(appSource, /typeof amountUsd === "number"[\s\S]*?: 0;/);
  assert.match(appSource, /typeof tokens === "number"[\s\S]*?: 0;/);
});

test("home composer starts cost and context values at zero", () => {
  assert.match(appSource, /cost=\{\{[\s\S]*?amountUsd: 0,[\s\S]*?tokens: 0,[\s\S]*?title: "Cost estimate appears after usage is available"/);
  assert.doesNotMatch(appSource, /cost=\{\{[\s\S]*?amountUsd: null,[\s\S]*?placeholder: true,[\s\S]*?Cost estimate appears after the session starts/);
});

test("active session composer keeps dashes while transcript usage is loading", () => {
  assert.match(appSource, /const sessionUsageLoading =[\s\S]*timelineBootstrap\.status === "idle"[\s\S]*timelineBootstrap\.status === "loading"/);
  assert.match(appSource, /cost=\{\{[\s\S]*?amountUsd: sessionCostEstimate\?\.amountUsd \?\? null,[\s\S]*?tokens: tokensUsed,[\s\S]*?placeholder: sessionUsageLoading/);
});

test("turn token count uses current context pressure", () => {
  assert.match(appSource, /tokens=\{selected\.contextTokens\}/);
  assert.match(appSource, /tokenScopeLabel="current context tokens"/);
  assert.doesNotMatch(appSource, /tokenScopeLabel="processed tokens in this turn"/);
});
