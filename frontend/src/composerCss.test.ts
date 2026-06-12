import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test, expect } from "vitest";

const indexCssSource = readFileSync(join(import.meta.dirname, "index.css"), "utf8");
const appSource = readFileSync(join(import.meta.dirname, "App.tsx"), "utf8");
const portfolioTranscriptSource = readFileSync(
  join(import.meta.dirname, "styleguide/portfolio-transcript.tsx"),
  "utf8",
);

function cssRule(selector: string): string {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = indexCssSource.match(
    new RegExp(`^\\s*${escapedSelector}\\s*\\{([\\s\\S]*?)\\}`, "m"),
  );
  expect(match, `${selector} rule should exist`).toBeTruthy();
  return match[1];
}

test("chat composer textarea does not expose a native resize handle", () => {
  expect(cssRule(".run-composer-textarea")).toMatch(/resize:\s*none\s*!important;/);
  expect(cssRule(".run-composer textarea")).toMatch(/resize:\s*none;/);
  expect(cssRule(".run-composer textarea")).not.toMatch(/resize:\s*vertical;/);
});

test("chat composer slash command highlight is drawn behind textarea text", () => {
  const wrapRule = cssRule(".run-composer-textarea-wrap");
  expect(wrapRule).toMatch(/position:\s*relative;/);
  expect(wrapRule).toMatch(/display:\s*grid;/);
  expect(wrapRule).toMatch(/width:\s*100%;/);
  expect(wrapRule).toMatch(/flex:\s*1\s+1\s+auto;/);

  const textareaRule = cssRule(".run-composer-textarea");
  expect(textareaRule).toMatch(/text-align:\s*left;/);
  expect(textareaRule).toMatch(/width:\s*100%;/);

  expect(indexCssSource).toMatch(/\.run-composer-text-preview\s*\{[\s\S]*pointer-events:\s*none;/);
  expect(indexCssSource).toMatch(/\.run-composer-text-preview\s*\{[\s\S]*color:\s*transparent;/);
  expect(cssRule(".run-composer-textarea-tokenized")).toMatch(/font-weight:\s*700;/);

  const tokenRule = cssRule(".run-composer-slash-token");
  expect(tokenRule).toMatch(/border-radius:\s*0\.32rem;/);
  expect(tokenRule).toMatch(/background:\s*rgba\(148,\s*163,\s*184,\s*0\.16\);/);
  expect(tokenRule).toMatch(/color:\s*transparent;/);
  expect(tokenRule).toMatch(/font-weight:\s*700;/);
  expect(tokenRule).toMatch(/line-height:\s*1\.22;/);
});

test("interactive composer active rail does not paint over the textarea", () => {
  const interactiveRule = cssRule(".run-composer.run-composer-interactive");
  expect(interactiveRule).toMatch(/position:\s*relative;/);
  expect(interactiveRule).toMatch(/isolation:\s*isolate;/);

  const railRule = cssRule(".run-composer.run-composer-interactive::before");
  expect(railRule).toMatch(/position:\s*absolute;/);
  expect(railRule).toMatch(/z-index:\s*0;/);

  const contentRule = cssRule(".run-composer.run-composer-interactive > *");
  expect(contentRule).toMatch(/position:\s*relative;/);
  expect(contentRule).toMatch(/z-index:\s*1;/);
});

test("chat composer cost estimate keeps a fixed-width footprint", () => {
  const composerRule = cssRule(".run-cost-estimate");
  expect(composerRule).toMatch(/width:\s*11rem;/);
  expect(composerRule).toMatch(/flex:\s*0\s+0\s+11rem;/);
  expect(composerRule).toMatch(/white-space:\s*nowrap;/);

  const iconRule = cssRule(".run-composer-icon-btn");
  expect(iconRule).toMatch(/flex:\s*0\s+0\s+2rem;/);

  const modelRule = cssRule(".run-model-chip");
  expect(modelRule).toMatch(/flex:\s*0\s+0\s+auto;/);
  expect(modelRule).toMatch(/min-width:\s*0;/);

  const modelLabelRule = cssRule(".run-model-chip-label");
  expect(modelLabelRule).toMatch(/flex:\s*1\s+1\s+auto;/);
  expect(modelLabelRule).toMatch(/text-overflow:\s*ellipsis;/);

  const metricRule = cssRule(".run-cost-estimate-metric");
  expect(metricRule).toMatch(/flex:\s*1\s+1\s+0;/);
  expect(metricRule).toMatch(/min-width:\s*0;/);
  expect(metricRule).toMatch(/flex-direction:\s*column;/);

  const valueRule = cssRule(".run-cost-estimate-value");
  expect(valueRule).toMatch(/overflow:\s*hidden;/);
  expect(valueRule).toMatch(/text-overflow:\s*ellipsis;/);

  const labelRule = cssRule(".run-cost-estimate-label");
  expect(labelRule).toMatch(/letter-spacing:\s*0;/);
  expect(labelRule).toMatch(/text-transform:\s*uppercase;/);

  const dividerRule = cssRule(".run-cost-estimate-divider");
  expect(dividerRule).toMatch(/width:\s*1px;/);
  expect(dividerRule).toMatch(/flex:\s*0\s+0\s+1px;/);

  const turnRule = cssRule(".run-turn-view-summary .run-cost-estimate");
  expect(turnRule).toMatch(/width:\s*auto;/);
  expect(turnRule).toMatch(/flex:\s*0\s+0\s+auto;/);
});

test("composer compaction metric stays compact and widens the chip instead of squeezing ctx", () => {
  // Base chip footprint is unchanged for turn-scope pills without a compaction
  // metric; the session chip widens via the modifier below even at cmp=0.
  const baseRule = cssRule(".run-cost-estimate");
  expect(baseRule).toMatch(/width:\s*11rem;/);

  // The compaction metric sizes to content (a short count) rather than claiming
  // an equal third of the row, so the ctx fraction is not squeezed.
  const compactionMetricRule = cssRule(".run-cost-estimate-metric-compactions");
  expect(compactionMetricRule).toMatch(/flex:\s*0\s+0\s+auto;/);

  // When present, the chip widens to seat the extra metric + divider.
  const hasCompactionMetricRule = cssRule(".run-cost-estimate.has-compaction-metric");
  expect(hasCompactionMetricRule).toMatch(/width:\s*13\.5rem;/);
  expect(hasCompactionMetricRule).toMatch(/flex-basis:\s*13\.5rem;/);
});

test("run pane keeps the composer inside the viewport at high browser zoom", () => {
  const workspaceRule = cssRule(".workspace");
  expect(workspaceRule).toMatch(/overflow:\s*hidden;/);
  expect(workspaceRule).not.toMatch(/overflow-x:\s*hidden;/);
  expect(workspaceRule).not.toMatch(/overflow-y:\s*auto;/);

  const runPanelRule = cssRule(".run-panel");
  expect(runPanelRule).toMatch(/^\s*height:\s*100%;/m);
  expect(runPanelRule).toMatch(/min-height:\s*0;/);
  expect(runPanelRule).not.toMatch(/min-height:\s*100%;/);

  const runMainFrameRule = cssRule(".run-main-frame");
  expect(runMainFrameRule).toMatch(/flex:\s*1\s+1\s+0;/);
  expect(runMainFrameRule).toMatch(/min-height:\s*0;/);

  const composerWrapRule = cssRule(".run-composer-wrap");
  expect(composerWrapRule).toMatch(/flex:\s*0\s+1\s+auto;/);
  expect(composerWrapRule).toMatch(/min-height:\s*0;/);
  expect(composerWrapRule).toMatch(/padding:\s*var\(--space-3\)\s+var\(--space-5\)\s+max\(var\(--space-5\),\s*env\(safe-area-inset-bottom\)\);/);

  const runPaneComposerWrapRule = cssRule(".run-composer-wrap-runpane");
  expect(runPaneComposerWrapRule).toMatch(/--run-composer-transcript-content-offset:\s*calc\(0\.7rem\s+\+\s+2\.625rem\s+\+\s+0\.55rem\);/);
  expect(runPaneComposerWrapRule).toMatch(/padding-left:\s*calc\(var\(--space-5\)\s+\+\s+var\(--run-composer-transcript-content-offset\)\);/);

  expect(appSource).toMatch(/composerWrapClassName=\{\[\s*"run-composer-wrap-runpane",[\s\S]*?dragActive \? "run-composer-wrap-drag" : "",[\s\S]*?\]\.filter\(Boolean\)\.join\(" "\)\}/);
  expect(portfolioTranscriptSource).toMatch(/composerWrapClassName="run-composer-wrap-runpane"/);
  expect(indexCssSource).toMatch(/@media \(max-width:\s*760px\)\s*\{[\s\S]*?\.run-composer-wrap-runpane\s*\{[\s\S]*?padding-left:\s*var\(--space-3\);/);
});

test("composer footer reflows controls instead of clipping them under zoom", () => {
  const composerRule = cssRule(".run-composer");
  expect(composerRule).toMatch(/container-type:\s*inline-size;/);
  expect(composerRule).toMatch(/min-width:\s*0;/);

  const footerRule = cssRule(".run-composer-footer");
  expect(footerRule).toMatch(/flex-wrap:\s*wrap;/);
  expect(footerRule).toMatch(/min-width:\s*0;/);
  expect(footerRule).toMatch(/max-height:\s*min\(10rem,\s*34dvh\);/);
  expect(footerRule).toMatch(/overflow-y:\s*auto;/);

  const toolsRule = cssRule(".run-composer-tools");
  expect(toolsRule).toMatch(/flex-wrap:\s*nowrap;/);
  expect(toolsRule).toMatch(/flex:\s*1\s+1\s+14rem;/);
  expect(toolsRule).toMatch(/min-width:\s*0;/);
  expect(toolsRule).toMatch(/max-width:\s*100%;/);

  expect(indexCssSource).toMatch(/@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-composer-tools\s*\{[\s\S]*?flex-wrap:\s*wrap;/);
  expect(indexCssSource).toMatch(/@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-cost-estimate\s*\{[\s\S]*?flex-basis:\s*6\.6rem;/);
  expect(indexCssSource).toMatch(/@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-cost-estimate-label\s*\{[\s\S]*?display:\s*none;/);
  expect(indexCssSource).toMatch(/@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-model-chip\s*\{[\s\S]*?max-width:\s*min\(11rem,\s*100%\);/);

  expect(indexCssSource).toMatch(/@media \(max-width:\s*760px\)\s*\{[\s\S]*?\.run-composer-hint\s*\{[\s\S]*?flex-basis:\s*100%;/);
  expect(indexCssSource).toMatch(/@media \(max-width:\s*760px\)\s*\{[\s\S]*?\.run-submit-btn\s*\{[\s\S]*?margin-left:\s*auto;/);
});

test("turn view transcript rows share the same avatar gutter", () => {
  const turnViewRowRule = cssRule(".run-turn-view-body .run-transcript-message");
  expect(turnViewRowRule).toMatch(/width:\s*100%;/);
  expect(turnViewRowRule).toMatch(/padding-left:\s*0;/);

  const ownedActivityRule = cssRule('.run-transcript [data-slot="message"][data-owner="activity"]');
  expect(ownedActivityRule).toMatch(/padding-left:\s*0;/);

  const turnViewActivityRule = cssRule('.run-turn-view-body [data-slot="message"][data-owner="activity"]');
  expect(turnViewActivityRule).toMatch(/grid-template-columns:\s*2\.625rem\s+minmax\(0,\s*1fr\);/);
  expect(turnViewActivityRule).toMatch(/max-width:\s*100%;/);

  const turnViewActivityContentRule = cssRule(
    '.run-turn-view-body [data-slot="message"][data-owner="activity"] .run-transcript-message-content',
  );
  expect(turnViewActivityContentRule).toMatch(/grid-column:\s*2;/);
  expect(turnViewActivityContentRule).toMatch(/min-width:\s*0;/);
  expect(turnViewActivityContentRule).toMatch(/max-width:\s*100%;/);

  const inlineActivityRule = cssRule('.run-turn-activity-body [data-slot="message"][data-owner="activity"]');
  expect(inlineActivityRule).toMatch(/grid-template-columns:\s*2rem\s+minmax\(0,\s*1fr\);/);
  expect(inlineActivityRule).toMatch(/max-width:\s*100%;/);

  const inlineActivityContentRule = cssRule(
    '.run-turn-activity-body [data-slot="message"][data-owner="activity"] .run-transcript-message-content',
  );
  expect(inlineActivityContentRule).toMatch(/grid-column:\s*2;/);
  expect(inlineActivityContentRule).toMatch(/min-width:\s*0;/);
  expect(inlineActivityContentRule).toMatch(/max-width:\s*100%;/);
});
