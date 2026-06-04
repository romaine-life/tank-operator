import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const cssSource = readFileSync(new URL("./index.css", import.meta.url), "utf8");
const styleguideSource = readFileSync(
  new URL("./styleguide/portfolio-transcript.tsx", import.meta.url),
  "utf8",
);
const projectionSource = readFileSync(
  new URL("./conversationProjection.ts", import.meta.url),
  "utf8",
);

test("token usage and cost chips stay retired from the run UI", () => {
  for (const source of [appSource, cssSource, styleguideSource]) {
    assert.doesNotMatch(source, /run-cost-estimate/);
    assert.doesNotMatch(source, /ComposerCostEstimate/);
  }
});

test("turn usage does not project as a transcript meta surface", () => {
  assert.doesNotMatch(appSource, /metaKind\?:\s*[^;]*turn_usage/);
  assert.doesNotMatch(projectionSource, /metaKind\?:\s*[^;]*turn_usage/);
  assert.doesNotMatch(projectionSource, /turn-usage:/);
  assert.doesNotMatch(projectionSource, /Token usage updated/);
});

test("composer does not derive visible token usage from transcript rows", () => {
  assert.doesNotMatch(appSource, /latestContextTokens/);
  assert.doesNotMatch(appSource, /tokensUsed/);
  assert.doesNotMatch(appSource, /sessionCostEstimate/);
  assert.doesNotMatch(appSource, /estimateTranscriptCost/);
  assert.doesNotMatch(appSource, /estimateTurnCost/);
  assert.doesNotMatch(appSource, /estimateTurnContextTokens/);
  assert.doesNotMatch(appSource, /formatComposerCostUsd/);
  assert.doesNotMatch(appSource, /formatTurnCostUsd/);
});

test("slash command palette no longer advertises token billing usage", () => {
  assert.doesNotMatch(appSource, /name:\s*"\/usage"/);
  assert.doesNotMatch(appSource, /token \/ billing usage/i);
});
