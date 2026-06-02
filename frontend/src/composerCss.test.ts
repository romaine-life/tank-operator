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
  assert.match(composerRule, /width:\s*8\.4rem;/);
  assert.match(composerRule, /flex:\s*0\s+0\s+8\.4rem;/);
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

test("workspace can scroll the full composer into view at high browser zoom", () => {
  const workspaceRule = cssRule(".workspace");
  assert.match(workspaceRule, /overflow-x:\s*hidden;/);
  assert.match(workspaceRule, /overflow-y:\s*auto;/);

  const runPanelRule = cssRule(".run-panel");
  assert.match(runPanelRule, /min-height:\s*100%;/);
  assert.doesNotMatch(runPanelRule, /^\s*height:\s*100%;/m);

  const composerWrapRule = cssRule(".run-composer-wrap");
  assert.match(
    composerWrapRule,
    /padding:\s*var\(--space-3\)\s+var\(--space-5\)\s+max\(var\(--space-5\),\s*env\(safe-area-inset-bottom\)\);/,
  );
});

test("composer footer reflows controls instead of clipping them under zoom", () => {
  assert.match(cssRule(".run-composer-footer"), /flex-wrap:\s*wrap;/);

  const toolsRule = cssRule(".run-composer-tools");
  assert.match(toolsRule, /flex-wrap:\s*wrap;/);
  assert.match(toolsRule, /min-width:\s*0;/);

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
