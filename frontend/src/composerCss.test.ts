import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test, expect } from "vitest";

const indexCssSource = readFileSync(join(import.meta.dirname, "index.css"), "utf8");
const appSource = readFileSync(join(import.meta.dirname, "App.tsx"), "utf8");
const workspaceShellSource = readFileSync(join(import.meta.dirname, "WorkspaceShell.tsx"), "utf8");
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

function cssRuleFlexible(selector: string): string {
  const selectorPattern = selector
    .trim()
    .split(/\s+/)
    .map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
    .join("\\s+");
  const match = indexCssSource.match(
    new RegExp(`^\\s*${selectorPattern}\\s*\\{([\\s\\S]*?)\\}`, "m"),
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
  expect(runPaneComposerWrapRule).toMatch(/padding-left:\s*var\(--space-5\);/);
  expect(runPaneComposerWrapRule).not.toMatch(/run-composer-transcript-content-offset/);

  expect(appSource).toMatch(/composerWrapClassName=\{\[\s*"run-composer-wrap-runpane",[\s\S]*?dragActive \? "run-composer-wrap-drag" : "",[\s\S]*?\]\.filter\(Boolean\)\.join\(" "\)\}/);
  expect(appSource).not.toMatch(/composerWrapStyle=\{chatFontScaleStyle\}/);
  expect(portfolioTranscriptSource).toMatch(/composerWrapClassName="run-composer-wrap-runpane"/);
  expect(indexCssSource).toMatch(/@media \(max-width:\s*768px\)\s*\{[\s\S]*?\.run-composer-wrap-runpane\s*\{[\s\S]*?padding-left:\s*var\(--space-3\);/);
});

test("session font scaling does not leak into the shared composer", () => {
  expect(appSource).toMatch(/style=\{chatFontScaleStyle\}/);
  expect(appSource).not.toMatch(/composerWrapStyle=\{chatFontScaleStyle\}/);

  expect(workspaceShellSource).toMatch(
    /<section className=\{\["run-panel", className\]\.filter\(Boolean\)\.join\(" "\)\}>/,
  );
  expect(workspaceShellSource).not.toMatch(
    /<section[^>]*className=\{\["run-panel", className\]\.filter\(Boolean\)\.join\(" "\)\}[^>]*style=\{style\}/,
  );
  expect(workspaceShellSource).toMatch(
    /<main[\s\S]*?className=\{\["run-main", bodyClassName\]\.filter\(Boolean\)\.join\(" "\)\}[\s\S]*?style=\{style\}/,
  );
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

  expect(indexCssSource).toMatch(/@media \(max-width:\s*768px\)\s*\{[\s\S]*?\.run-composer-hint\s*\{[\s\S]*?flex-basis:\s*100%;/);
  expect(indexCssSource).toMatch(/@media \(max-width:\s*768px\)\s*\{[\s\S]*?\.run-submit-btn\s*\{[\s\S]*?margin-left:\s*auto;/);
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

test("question page answer card aligns with assistant bubble content gutter", () => {
  const questionCardRule = cssRule('.run-turn-view-body[data-page-kind="question"] .run-tool-ask');
  expect(questionCardRule).toMatch(
    /width:\s*calc\(100%\s*-\s*\(2\.625rem\s*\+\s*0\.55rem\)\);/,
  );
  expect(questionCardRule).toMatch(
    /margin-left:\s*calc\(2\.625rem\s*\+\s*0\.55rem\);/,
  );

  expect(indexCssSource).toMatch(
    /@media \(max-width:\s*640px\)\s*\{[\s\S]*?\.run-turn-view-body\[data-page-kind="question"\]\s+\.run-tool-ask\s*\{[\s\S]*?width:\s*100%;[\s\S]*?margin-left:\s*0;/,
  );
});

test("non-compact user message footer floats into the final prose line", () => {
  expect(appSource).toMatch(
    /const inlineFooter =\s*variant === "user" && !isSkillAction && visibleAttachments\.length === 0;/,
  );
  expect(appSource).toMatch(/data-inline-footer=\{inlineFooter \? "true" : undefined\}/);
  // The prompt text and its inline footer render once, in a stable position
  // inside the message text, for both collapsed and expanded states — so the
  // collapse toggle is a pure CSS restyle and never remounts them.
  expect(appSource).toMatch(/<RunPlainText>\{visibleText\}<\/RunPlainText>/);
  expect(appSource).toMatch(/\{inlineFooter && messageFooter\}/);
  expect(portfolioTranscriptSource).toMatch(/data-inline-footer=\{inlineFooter \? "true" : undefined\}/);

  const inlineContentRule = cssRuleFlexible(
    '.run-transcript-message[data-inline-footer="true"]:not([data-compact="true"]) .run-transcript-message-content',
  );
  expect(inlineContentRule).toMatch(/display:\s*block;/);

  const inlineTextRule = cssRuleFlexible(
    '.run-transcript-message[data-inline-footer="true"]:not([data-compact="true"]) .run-plain-message-text',
  );
  expect(inlineTextRule).toMatch(/display:\s*inline;/);

  const footerRule = cssRuleFlexible(
    '.run-transcript-message[data-inline-footer="true"]:not([data-compact="true"]) .run-msg-footer',
  );
  expect(footerRule).toMatch(/float:\s*right;/);
  expect(footerRule).toMatch(/margin-left:\s*0\.65rem;/);

  const clearfixRule = cssRuleFlexible(
    '.run-transcript-message[data-inline-footer="true"]:not([data-compact="true"]) .run-transcript-message-text::after',
  );
  expect(clearfixRule).toMatch(/clear:\s*both;/);
});

test("compact user message preview keeps controls on the same row", () => {
  // Collapse is a CSS restyle of the SAME nodes the expanded prompt renders:
  // the message text becomes a single flex row holding the prompt text and the
  // footer, so toggling never swaps elements (no remount, no flicker). The old
  // dedicated .run-msg-compact-text preview element is deleted end to end.
  expect(appSource.includes("run-msg-compact-text")).toBe(false);
  expect(indexCssSource.includes("run-msg-compact-text")).toBe(false);

  const compactTextRule = cssRule(
    '.run-transcript-message[data-compact="true"] .run-transcript-message-text',
  );
  expect(compactTextRule).toMatch(/display:\s*flex;/);
  expect(compactTextRule).toMatch(/align-items:\s*flex-end;/);
  expect(compactTextRule).toMatch(/min-width:\s*0;/);

  // The prompt text element itself (not a separate preview) truncates to one
  // line; nowrap collapses newlines so no flattened string is rendered.
  const compactPromptRule = cssRule(
    '.run-transcript-message[data-compact="true"] .run-plain-message-text',
  );
  expect(compactPromptRule).toMatch(/flex:\s*1\s+1\s+0;/);
  expect(compactPromptRule).toMatch(/white-space:\s*nowrap;/);
  expect(compactPromptRule).toMatch(/text-overflow:\s*ellipsis;/);

  const compactFooterRule = cssRule(
    '.run-transcript-message[data-compact="true"] .run-msg-footer',
  );
  expect(compactFooterRule).toMatch(/flex:\s*0\s+0\s+auto;/);
  expect(compactFooterRule).not.toMatch(/margin-left:\s*auto;/);
});

test("collapsed prompt footer anchors like the expanded inline footer so a one-line toggle does not jolt", () => {
  // A one-line user prompt looks identical collapsed vs expanded, so toggling
  // it must not nudge the arrow/copy/timestamp cluster. The expanded inline
  // footer floats with a small top nudge; the collapsed (compact) footer has
  // to top-align (it is shorter than the text line) and use the same nudge
  // instead of the flex container's flex-end baseline.
  const inlineFooterRule = cssRuleFlexible(
    '.run-transcript-message[data-inline-footer="true"]:not([data-compact="true"]) .run-msg-footer',
  );
  expect(inlineFooterRule).toMatch(/margin-top:\s*0\.08rem;/);

  const compactFooterRule = cssRule(
    '.run-transcript-message[data-compact="true"] .run-msg-footer',
  );
  expect(compactFooterRule).toMatch(/align-self:\s*flex-start;/);
  expect(compactFooterRule).toMatch(/margin-top:\s*0\.08rem;/);
});

test("inlined user-message footer links keep the action palette, not markdown-link blue", () => {
  // When a user message footer is inlined (expanded one-line prompts, and every
  // non-attachment user message in the transcript) it lives inside
  // .run-transcript-message-text, where `.run-transcript-message-text a` paints
  // anchors blue + underlined. Footer affordance links must opt out: the
  // "open in transcript" arrow stays muted, the turn arrow keeps its own blue,
  // the link button stays cyan, and none of them gain an underline.
  const proseLinkRule = cssRuleFlexible(".run-transcript-message-text a");
  expect(proseLinkRule).toMatch(/color:\s*#93c5fd;/);
  expect(proseLinkRule).toMatch(/text-decoration:\s*underline;/);

  const footerLinkRule = cssRuleFlexible(
    ".run-transcript-message-text .run-msg-footer a",
  );
  expect(footerLinkRule).toMatch(/color:\s*var\(--text-muted\);/);
  expect(footerLinkRule).toMatch(/text-decoration:\s*none;/);

  const footerTurnRule = cssRuleFlexible(
    ".run-transcript-message-text .run-msg-footer a.run-msg-turn",
  );
  expect(footerTurnRule).toMatch(/color:\s*#93c5fd;/);

  const footerLinkButtonRule = cssRuleFlexible(
    ".run-transcript-message-text .run-msg-footer a.run-msg-link",
  );
  expect(footerLinkButtonRule).toMatch(/color:\s*var\(--cyan\);/);
});

test("question page card stays scrollable when taller than the Turn view", () => {
  // Regression guard: the Turn-view outer scroller is intentionally
  // overflow-y:hidden because the inner body owns scrolling. If the question
  // page body is overflow:visible, a tall AskUserQuestion card has NO scroll
  // container anywhere — it is clipped by .run-main and its lower options and
  // the set-level Submit button drop below an unreachable fold. The question
  // body must therefore be its own scroll region, like the activity page kind.
  const turnViewMainRule = cssRule('.run-main[aria-label="Turn view"]');
  expect(turnViewMainRule).toMatch(/overflow-y:\s*hidden;/);

  const questionBodyRule = cssRule('.run-turn-view-body[data-page-kind="question"]');
  expect(questionBodyRule).toMatch(/overflow-y:\s*auto;/);
  expect(questionBodyRule).not.toMatch(/overflow:\s*visible;/);

  // The base body rule supplies min-height:0 so the shrink-to-fit flex child
  // can shrink below content height and actually engage overflow-y:auto.
  expect(cssRule(".run-turn-view-body")).toMatch(/min-height:\s*0;/);

  // The "Question N of M" head is pinned so it stays visible while scrolling.
  expect(cssRule(".run-turn-question-page-head")).toMatch(/flex:\s*0\s+0\s+auto;/);
});
