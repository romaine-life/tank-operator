import { readFileSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert/strict";
import { test } from "node:test";

const indexCssSource = readFileSync(join(import.meta.dirname, "index.css"), "utf8");

function cssRule(selector: string): string {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = indexCssSource.match(
    new RegExp(`^\\s*${escapedSelector}\\s*\\{([\\s\\S]*?)\\}`, "m"),
  );
  assert.ok(match, `${selector} rule should exist`);
  return match[1];
}

test("chat composer textarea does not expose a native resize handle", () => {
  assert.match(cssRule(".run-composer-textarea"), /resize:\s*none\s*!important;/);
  assert.match(cssRule(".run-composer textarea"), /resize:\s*none;/);
  assert.doesNotMatch(cssRule(".run-composer textarea"), /resize:\s*vertical;/);
});

test("chat composer slash command highlight is drawn behind textarea text", () => {
  const wrapRule = cssRule(".run-composer-textarea-wrap");
  assert.match(wrapRule, /position:\s*relative;/);
  assert.match(wrapRule, /display:\s*grid;/);
  assert.match(wrapRule, /width:\s*100%;/);
  assert.match(wrapRule, /flex:\s*1\s+1\s+auto;/);

  const textareaRule = cssRule(".run-composer-textarea");
  assert.match(textareaRule, /text-align:\s*left;/);
  assert.match(textareaRule, /width:\s*100%;/);

  assert.match(indexCssSource, /\.run-composer-text-preview\s*\{[\s\S]*pointer-events:\s*none;/);
  assert.match(indexCssSource, /\.run-composer-text-preview\s*\{[\s\S]*color:\s*transparent;/);
  assert.match(cssRule(".run-composer-textarea-tokenized"), /font-weight:\s*700;/);

  const tokenRule = cssRule(".run-composer-slash-token");
  assert.match(tokenRule, /border-radius:\s*0\.32rem;/);
  assert.match(tokenRule, /background:\s*rgba\(148,\s*163,\s*184,\s*0\.16\);/);
  assert.match(tokenRule, /color:\s*transparent;/);
  assert.match(tokenRule, /font-weight:\s*700;/);
  assert.match(tokenRule, /line-height:\s*1\.22;/);
});

test("chat composer cost estimate keeps a fixed-width footprint", () => {
  const composerRule = cssRule(".run-cost-estimate");
  assert.match(composerRule, /width:\s*8\.8rem;/);
  assert.match(composerRule, /flex:\s*0\s+0\s+8\.8rem;/);
  assert.match(composerRule, /white-space:\s*nowrap;/);

  const metricRule = cssRule(".run-cost-estimate-metric");
  assert.match(metricRule, /flex:\s*1\s+1\s+0;/);
  assert.match(metricRule, /min-width:\s*0;/);
  assert.match(metricRule, /flex-direction:\s*column;/);

  const valueRule = cssRule(".run-cost-estimate-value");
  assert.match(valueRule, /overflow:\s*hidden;/);
  assert.match(valueRule, /text-overflow:\s*ellipsis;/);

  const labelRule = cssRule(".run-cost-estimate-label");
  assert.match(labelRule, /letter-spacing:\s*0;/);
  assert.match(labelRule, /text-transform:\s*uppercase;/);

  const dividerRule = cssRule(".run-cost-estimate-divider");
  assert.match(dividerRule, /width:\s*1px;/);
  assert.match(dividerRule, /flex:\s*0\s+0\s+1px;/);

  const turnRule = cssRule(".run-turn-view-summary .run-cost-estimate");
  assert.match(turnRule, /width:\s*auto;/);
  assert.match(turnRule, /flex:\s*0\s+0\s+auto;/);
});

test("run pane keeps the composer inside the viewport at high browser zoom", () => {
  const workspaceRule = cssRule(".workspace");
  assert.match(workspaceRule, /overflow:\s*hidden;/);
  assert.doesNotMatch(workspaceRule, /overflow-x:\s*hidden;/);
  assert.doesNotMatch(workspaceRule, /overflow-y:\s*auto;/);

  const runPanelRule = cssRule(".run-panel");
  assert.match(runPanelRule, /^\s*height:\s*100%;/m);
  assert.match(runPanelRule, /min-height:\s*0;/);
  assert.doesNotMatch(runPanelRule, /min-height:\s*100%;/);

  const runMainFrameRule = cssRule(".run-main-frame");
  assert.match(runMainFrameRule, /flex:\s*1\s+1\s+0;/);
  assert.match(runMainFrameRule, /min-height:\s*0;/);

  const composerWrapRule = cssRule(".run-composer-wrap");
  assert.match(composerWrapRule, /flex:\s*0\s+1\s+auto;/);
  assert.match(composerWrapRule, /min-height:\s*0;/);
  assert.match(
    composerWrapRule,
    /padding:\s*var\(--space-3\)\s+var\(--space-5\)\s+max\(var\(--space-5\),\s*env\(safe-area-inset-bottom\)\);/,
  );
});

test("composer footer reflows controls instead of clipping them under zoom", () => {
  const composerRule = cssRule(".run-composer");
  assert.match(composerRule, /container-type:\s*inline-size;/);
  assert.match(composerRule, /min-width:\s*0;/);

  const footerRule = cssRule(".run-composer-footer");
  assert.match(footerRule, /flex-wrap:\s*wrap;/);
  assert.match(footerRule, /min-width:\s*0;/);
  assert.match(footerRule, /max-height:\s*min\(10rem,\s*34dvh\);/);
  assert.match(footerRule, /overflow-y:\s*auto;/);

  const toolsRule = cssRule(".run-composer-tools");
  assert.match(toolsRule, /flex-wrap:\s*nowrap;/);
  assert.match(toolsRule, /flex:\s*1\s+1\s+14rem;/);
  assert.match(toolsRule, /min-width:\s*0;/);
  assert.match(toolsRule, /max-width:\s*100%;/);

  assert.match(indexCssSource, /@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-composer-tools\s*\{[\s\S]*?flex-wrap:\s*wrap;/);
  assert.match(indexCssSource, /@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-cost-estimate\s*\{[\s\S]*?flex-basis:\s*6\.6rem;/);
  assert.match(indexCssSource, /@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-cost-estimate-label\s*\{[\s\S]*?display:\s*none;/);
  assert.match(indexCssSource, /@container \(max-width:\s*460px\)\s*\{[\s\S]*?\.run-model-chip\s*\{[\s\S]*?max-width:\s*min\(11rem,\s*100%\);/);

  assert.match(indexCssSource, /@media \(max-width:\s*760px\)\s*\{[\s\S]*?\.run-composer-hint\s*\{[\s\S]*?flex-basis:\s*100%;/);
  assert.match(indexCssSource, /@media \(max-width:\s*760px\)\s*\{[\s\S]*?\.run-submit-btn\s*\{[\s\S]*?margin-left:\s*auto;/);
});

test("turn view transcript rows share the same avatar gutter", () => {
  const turnViewRowRule = cssRule(".run-turn-view-body .run-transcript-message");
  assert.match(turnViewRowRule, /width:\s*100%;/);
  assert.match(turnViewRowRule, /padding-left:\s*0;/);

  const ownedActivityRule = cssRule('.run-transcript [data-slot="message"][data-owner="activity"]');
  assert.match(ownedActivityRule, /padding-left:\s*0;/);
});
