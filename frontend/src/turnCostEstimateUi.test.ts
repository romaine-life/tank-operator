import { readFileSync } from "node:fs";
import { test, expect } from "vitest";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const cssSource = readFileSync(new URL("./index.css", import.meta.url), "utf8");
const projectionSource = readFileSync(
  new URL("./conversationProjection.ts", import.meta.url),
  "utf8",
);

test("composer context indicator restores the pre-regression cost chip", () => {
  expect(appSource).toMatch(/function ComposerCostEstimate/);
  expect(appSource).toMatch(/className=\{`run-cost-estimate/);
  expect(cssSource).toMatch(/\.run-cost-estimate\s*\{/);
  expect(appSource).toMatch(/className="run-cost-estimate-metric run-cost-estimate-metric-tokens"/);
  expect(appSource).toMatch(/className="run-cost-estimate-divider"/);
  expect(appSource).toMatch(/className="run-cost-estimate-metric run-cost-estimate-metric-cost"/);
  expect(appSource).toMatch(/className="run-cost-estimate-label">ctx</);
  expect(appSource).toMatch(/className="run-cost-estimate-label">usd</);
  expect(appSource).not.toMatch(/ComposerContextIndicator/);
  expect(cssSource).not.toMatch(/run-context-indicator/);
});

test("turn usage rows stay invisible as transcript messages", () => {
  expect(appSource).toMatch(/metaKind\?:\s*[^;]*turn_usage/);
  expect(appSource).toMatch(/metaKind === "turn_usage"[\s\S]*return null/);
  expect(projectionSource).not.toMatch(/metaKind\?:\s*[^;]*turn_usage/);
  expect(projectionSource).not.toMatch(/turn-usage:/);
  expect(projectionSource).not.toMatch(/Token usage updated/);
});

test("composer token count uses current context, not cumulative session usage", () => {
  expect(appSource).toMatch(/cost=\{\{[\s\S]*?tokens: tokensUsed,[\s\S]*?tokenScopeLabel: "current context tokens"/);
  expect(appSource).not.toMatch(/tokens=\{sessionCostEstimate\?\.tokens\s*\?\?\s*null\}/);
  expect(appSource).not.toMatch(/tokens:\s*sessionCostEstimate\?\.tokens\s*\?\?\s*null/);
});

test("composer context usage shows a used/window fraction from the provider-observed session window", () => {
  expect(appSource).toMatch(/runtime_context_window_tokens/);
  expect(appSource).toMatch(/contextWindow: runtimeContextWindowTokens/);
  expect(appSource).toMatch(/\$\{formatCompactTokens\(safeTokens\)\}\/\$\{formatCompactTokens\(safeWindow\)\}/);
  expect(appSource).not.toMatch(/ComposerUsageRing/);
  expect(appSource).not.toMatch(/run-usage-ring/);
  expect(cssSource).not.toMatch(/run-usage-ring/);
  expect(appSource).not.toMatch(/CONTEXT_WINDOW_BY_MODEL/);
});

test("active session composer keeps dashes while transcript usage is loading", () => {
  expect(appSource).toMatch(/const sessionUsageLoading =[\s\S]*timelineBootstrap\.status === "idle"[\s\S]*timelineBootstrap\.status === "loading"/);
  expect(appSource).toMatch(/cost=\{\{[\s\S]*?amountUsd: sessionCostEstimate\?\.amountUsd \?\? null,[\s\S]*?tokens: tokensUsed,[\s\S]*?placeholder: sessionUsageLoading/);
});

test("composer renders a durable compaction metric from the session row", () => {
  // A third metric, labeled cmp, renders whenever the session row supplies the
  // durable count, including the zero state before the first compaction.
  expect(appSource).toMatch(/className="run-cost-estimate-metric run-cost-estimate-metric-compactions"/);
  expect(appSource).toMatch(/className="run-cost-estimate-label">cmp</);
  expect(appSource).toMatch(/\{hasCompactionMetric && \(/);
  expect(appSource).toMatch(/const hasCompactionMetric =\s*typeof compactionCount === "number" && Number\.isFinite\(compactionCount\)/);
  // Sourced from the durable row field, never inferred from loaded transcript
  // entries — the same durable model as the window denominator.
  expect(appSource).toMatch(/compactionCount: sessionCompactionCount,/);
  expect(appSource).toMatch(/const sessionCompactionCount =[\s\S]*session\.compaction_count/);
  // The chip widens via a modifier instead of squeezing the ctx fraction.
  expect(appSource).toMatch(/has-compaction-metric/);
  expect(cssSource).toMatch(/\.run-cost-estimate\.has-compaction-metric\s*\{/);
  expect(cssSource).toMatch(/\.run-cost-estimate-metric-compactions\s*\{/);
});

test("splash composer keeps the compaction metric present at zero", () => {
  expect(appSource).toMatch(/className="run-composer-home run-composer-interactive"[\s\S]*?cost=\{\{[\s\S]*?amountUsd: 0,[\s\S]*?tokens: 0,[\s\S]*?compactionCount: 0,/);
  expect(appSource).toMatch(/Cost estimate appears after usage is available, 0 context compactions/);
});

test("compaction metric is session-scoped and absent from the per-turn pill", () => {
  // The per-turn ComposerCostEstimate (turn scope) must not pass a compaction
  // count — compactions are a session-lifetime fact, not a turn fact.
  expect(appSource).toMatch(/tokens=\{selected\.contextTokens\}/);
  expect(appSource).not.toMatch(/compactionCount=\{selected/);
});
