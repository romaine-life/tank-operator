import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");

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
  assert.match(appSource, /tokens=\{tokensUsed\}/);
  assert.match(appSource, /tokenScopeLabel="current context tokens"/);
  assert.doesNotMatch(appSource, /tokens=\{sessionCostEstimate\?\.tokens\s*\?\?\s*null\}/);
});

test("turn token count uses current context pressure", () => {
  assert.match(appSource, /tokens=\{selected\.contextTokens\}/);
  assert.match(appSource, /tokenScopeLabel="current context tokens"/);
  assert.doesNotMatch(appSource, /tokenScopeLabel="processed tokens in this turn"/);
});
