import { readFileSync } from "node:fs";
import { join } from "node:path";
import assert from "node:assert/strict";
import { test } from "node:test";

import { BP_COMPACT } from "./breakpoints.ts";

const dir = import.meta.dirname;
const indexCss = readFileSync(join(dir, "index.css"), "utf8");
const appSource = readFileSync(join(dir, "App.tsx"), "utf8");

function cssRule(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = indexCss.match(
    new RegExp(`^\\s*${escaped}\\s*\\{([\\s\\S]*?)\\}`, "m"),
  );
  assert.ok(match, `${selector} rule should exist`);
  return match[1];
}

test("compact shell collapses to a single column with a top-bar row", () => {
  const rule = cssRule(".shell.shell-compact");
  assert.match(rule, /grid-template-columns:\s*1fr;/);
  assert.match(rule, /grid-template-rows:\s*auto\s+1fr;/);
});

test("compact breakpoint in CSS matches the BP_COMPACT constant (no drift)", () => {
  assert.ok(
    indexCss.includes(`@media (max-width: ${BP_COMPACT}px)`),
    `index.css should drive compact tuning from BP_COMPACT (${BP_COMPACT}px)`,
  );
});

test("the shell wires the compact drawer, top bar, and desktop-only gate", () => {
  // The load-bearing pieces of the compact triage shell. A change that removes
  // one without a deliberate replacement should fail here.
  assert.ok(appSource.includes('import { useViewport } from "./useViewport";'));
  assert.ok(appSource.includes("<MobileTopBar"));
  assert.ok(appSource.includes("<Sheet open={navDrawerOpen}"));
  assert.ok(appSource.includes("<DesktopOnly"));
});

test("reorder-by-drag is a desktop-only enhancement (no dead gesture on touch)", () => {
  assert.ok(
    appSource.includes(
      "draggable={!isClosing && !readOnlySessionView && !isCompact}",
    ),
    "session rows must be non-draggable on compact viewports",
  );
});

test("compact nav drawer keeps the session delete affordance visible, not hover-only", () => {
  const rule = cssRule(".sidebar-in-drawer .session-delete");
  assert.match(rule, /color:\s*var\(--text-muted\);/);
});

test("desktop-only boundary renders an honest card, not a blank surface", () => {
  assert.ok(appSource.includes('feature="terminal sessions"'));
  const rule = cssRule(".desktop-only-title");
  assert.match(rule, /color:\s*var\(--text-primary\);/);
});
