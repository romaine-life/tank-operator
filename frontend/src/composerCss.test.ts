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

test("recognized skill tokens use a mirrored visual layer without replacing the textarea", () => {
  const mirrorRule = cssRule(".run-composer-skill-mirror");
  assert.match(mirrorRule, /position:\s*absolute;/);
  assert.match(mirrorRule, /pointer-events:\s*none;/);
  assert.match(mirrorRule, /white-space:\s*pre-wrap;/);

  const textareaRule = cssRule(".run-composer-skill-highlighted .run-composer-textarea");
  assert.match(textareaRule, /color:\s*transparent\s*!important;/);
  assert.match(textareaRule, /caret-color:\s*var\(--text-primary\);/);

  const baseTextareaRule = cssRule(".run-composer-textarea");
  assert.match(baseTextareaRule, /line-height:\s*1\.5\s*!important;/);

  const tokenRule = cssRule(".run-composer-skill-token");
  assert.match(tokenRule, /--run-composer-skill-token-bg:\s*rgba\(14,\s*165,\s*233,\s*0\.32\);/);
  assert.match(tokenRule, /--run-composer-skill-token-border:\s*rgba\(125,\s*211,\s*252,\s*0\.78\);/);
  assert.match(tokenRule, /font-weight:\s*750;/);
  assert.match(tokenRule, /0 0 0 3px var\(--run-composer-skill-token-glow\)/);

  const tokenIconRule = cssRule(".run-composer-skill-token-icon");
  assert.match(tokenIconRule, /border-radius:\s*999px;/);
  assert.match(tokenIconRule, /background:\s*var\(--run-composer-skill-token-icon-bg\);/);
  assert.match(tokenIconRule, /inset 0 0 0 1px var\(--run-composer-skill-token-icon-border\)/);
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
