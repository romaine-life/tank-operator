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

test("composer renders a durable compaction metric from the session row", () => {
  // A third metric, labeled cmp, renders whenever the session row supplies the
  // durable count, including the zero state before the first compaction.
  assert.match(appSource, /className="run-cost-estimate-metric run-cost-estimate-metric-compactions"/);
  assert.match(appSource, /className="run-cost-estimate-label">cmp</);
  assert.match(appSource, /\{hasCompactionMetric && \(/);
  assert.match(
    appSource,
    /const hasCompactionMetric =\s*typeof compactionCount === "number" && Number\.isFinite\(compactionCount\)/,
  );
  // Sourced from the durable row field, never inferred from loaded transcript
  // entries — the same durable model as the window denominator.
  assert.match(appSource, /compactionCount: sessionCompactionCount,/);
  assert.match(appSource, /const sessionCompactionCount =[\s\S]*session\.compaction_count/);
  // The chip widens via a modifier instead of squeezing the ctx fraction.
  assert.match(appSource, /has-compaction-metric/);
  assert.match(cssSource, /\.run-cost-estimate\.has-compaction-metric\s*\{/);
  assert.match(cssSource, /\.run-cost-estimate-metric-compactions\s*\{/);
});

test("splash composer keeps the compaction metric present at zero", () => {
  assert.match(
    appSource,
    /className="run-composer-home run-composer-interactive"[\s\S]*?cost=\{\{[\s\S]*?amountUsd: 0,[\s\S]*?tokens: 0,[\s\S]*?compactionCount: 0,/,
  );
  assert.match(
    appSource,
    /Cost estimate appears after usage is available, 0 context compactions/,
  );
});

test("compaction metric is session-scoped and absent from the per-turn pill", () => {
  // The per-turn ComposerCostEstimate (turn scope) must not pass a compaction
  // count — compactions are a session-lifetime fact, not a turn fact.
  assert.match(appSource, /tokens=\{selected\.contextTokens\}/);
  assert.doesNotMatch(appSource, /compactionCount=\{selected/);
});
