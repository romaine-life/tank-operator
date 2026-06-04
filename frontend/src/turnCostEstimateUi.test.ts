import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const cssSource = readFileSync(new URL("./index.css", import.meta.url), "utf8");
const projectionSource = readFileSync(
  new URL("./conversationProjection.ts", import.meta.url),
  "utf8",
);

test("composer context indicator restores the pre-regression cost chip", () => {
  assert.match(appSource, /function ComposerCostEstimate/);
  assert.match(appSource, /className=\{`run-cost-estimate/);
  assert.match(cssSource, /\.run-cost-estimate\s*\{/);
  assert.match(appSource, /className="run-cost-estimate-metric run-cost-estimate-metric-tokens"/);
  assert.match(appSource, /className="run-cost-estimate-divider"/);
  assert.match(appSource, /className="run-cost-estimate-metric run-cost-estimate-metric-cost"/);
  assert.match(appSource, /className="run-cost-estimate-label">ctx</);
  assert.match(appSource, /className="run-cost-estimate-label">usd</);
  assert.doesNotMatch(appSource, /ComposerContextIndicator/);
  assert.doesNotMatch(cssSource, /run-context-indicator/);
});

test("turn usage rows stay invisible as transcript messages", () => {
  assert.match(appSource, /metaKind\?:\s*[^;]*turn_usage/);
  assert.match(appSource, /metaKind === "turn_usage"[\s\S]*return null/);
  assert.doesNotMatch(projectionSource, /metaKind\?:\s*[^;]*turn_usage/);
  assert.doesNotMatch(projectionSource, /turn-usage:/);
  assert.doesNotMatch(projectionSource, /Token usage updated/);
});

test("composer token count uses current context, not cumulative session usage", () => {
  assert.match(appSource, /cost=\{\{[\s\S]*?tokens: tokensUsed,[\s\S]*?tokenScopeLabel: "current context tokens"/);
  assert.doesNotMatch(appSource, /tokens=\{sessionCostEstimate\?\.tokens\s*\?\?\s*null\}/);
  assert.doesNotMatch(appSource, /tokens:\s*sessionCostEstimate\?\.tokens\s*\?\?\s*null/);
});

test("composer context usage shows a used/window fraction from the provider-observed session window", () => {
  assert.match(appSource, /runtime_context_window_tokens/);
  assert.match(appSource, /contextWindow: runtimeContextWindowTokens/);
  assert.match(
    appSource,
    /\$\{formatCompactTokens\(safeTokens\)\}\/\$\{formatCompactTokens\(safeWindow\)\}/,
  );
  assert.doesNotMatch(appSource, /ComposerUsageRing/);
  assert.doesNotMatch(appSource, /run-usage-ring/);
  assert.doesNotMatch(cssSource, /run-usage-ring/);
  assert.doesNotMatch(appSource, /CONTEXT_WINDOW_BY_MODEL/);
});

test("active session composer keeps dashes while transcript usage is loading", () => {
  assert.match(appSource, /const sessionUsageLoading =[\s\S]*timelineBootstrap\.status === "idle"[\s\S]*timelineBootstrap\.status === "loading"/);
  assert.match(appSource, /cost=\{\{[\s\S]*?amountUsd: sessionCostEstimate\?\.amountUsd \?\? null,[\s\S]*?tokens: tokensUsed,[\s\S]*?placeholder: sessionUsageLoading/);
});
