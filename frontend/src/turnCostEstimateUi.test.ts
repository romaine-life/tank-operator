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

test("composer context percentage uses provider-observed session window", () => {
  assert.match(appSource, /runtime_context_window_tokens/);
  assert.match(appSource, /runtimeContextWindowTokens > 0/);
  assert.match(appSource, /contextWindow: runtimeContextWindowTokens/);
  assert.match(cssSource, /run-usage-ring/);
  assert.doesNotMatch(appSource, /CONTEXT_WINDOW_BY_MODEL/);
  assert.doesNotMatch(appSource, /getContextWindow/);
});

test("turn token count uses current context pressure", () => {
  assert.match(appSource, /tokens=\{selected\.contextTokens\}/);
  assert.match(appSource, /tokenScopeLabel="current context tokens"/);
  assert.doesNotMatch(appSource, /tokenScopeLabel="processed tokens in this turn"/);
});
