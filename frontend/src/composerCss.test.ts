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

  const tokenRule = cssRule(".run-composer-slash-token");
  assert.match(tokenRule, /border-radius:\s*0\.4rem;/);
  assert.match(tokenRule, /background:\s*rgba\(148,\s*163,\s*184,\s*0\.16\);/);
  assert.match(tokenRule, /color:\s*transparent;/);
});

test("chat composer cost estimate keeps a fixed-width footprint", () => {
  const composerRule = cssRule(".run-cost-estimate");
  assert.match(composerRule, /width:\s*4\.75rem;/);
  assert.match(composerRule, /flex:\s*0\s+0\s+4\.75rem;/);
  assert.match(composerRule, /white-space:\s*nowrap;/);

  const turnRule = cssRule(".run-turn-view-summary .run-cost-estimate");
  assert.match(turnRule, /width:\s*auto;/);
  assert.match(turnRule, /flex:\s*0\s+0\s+auto;/);
});

test("turn view transcript rows share the same avatar gutter", () => {
  const turnViewRowRule = cssRule(".run-turn-view-body .run-transcript-message");
  assert.match(turnViewRowRule, /width:\s*100%;/);
  assert.match(turnViewRowRule, /padding-left:\s*0;/);

  const ownedActivityRule = cssRule('.run-transcript [data-slot="message"][data-owner="activity"]');
  assert.match(ownedActivityRule, /padding-left:\s*0;/);
});
