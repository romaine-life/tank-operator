import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test, expect } from "vitest";

import { BP_COMPACT } from "./breakpoints.ts";

const dir = import.meta.dirname;
const indexCss = readFileSync(join(dir, "index.css"), "utf8");
const appSource = readFileSync(join(dir, "App.tsx"), "utf8");

function cssRule(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = indexCss.match(
    new RegExp(`^\\s*${escaped}\\s*\\{([\\s\\S]*?)\\}`, "m"),
  );
  expect(match, `${selector} rule should exist`).toBeTruthy();
  return match[1];
}

test("compact shell collapses to a single column with a top-bar row", () => {
  const rule = cssRule(".shell.shell-compact");
  expect(rule).toMatch(/grid-template-columns:\s*1fr;/);
  expect(rule).toMatch(/grid-template-rows:\s*auto\s+1fr;/);
});

test("compact breakpoint in CSS matches the BP_COMPACT constant (no drift)", () => {
  expect(indexCss.includes(`@media (max-width: ${BP_COMPACT}px)`), `index.css should drive compact tuning from BP_COMPACT (${BP_COMPACT}px)`).toBeTruthy();
});

test("desktop sidebar is constrained so long session lists scroll internally", () => {
  const sidebarRule = cssRule(".sidebar");
  expect(sidebarRule).toMatch(/overflow:\s*hidden;/);
  expect(sidebarRule).toMatch(/min-height:\s*0;/);

  expect(indexCss).toMatch(
    /\.sidebar-list\s*\{[\s\S]*?overflow-y:\s*auto;[\s\S]*?min-height:\s*0;/,
  );
});

test("the shell wires the compact drawer, top bar, and desktop-only gate", () => {
  // The load-bearing pieces of the compact triage shell. A change that removes
  // one without a deliberate replacement should fail here.
  expect(appSource.includes('import { useViewport } from "./useViewport";')).toBeTruthy();
  expect(appSource.includes("<MobileTopBar")).toBeTruthy();
  expect(appSource.includes("<Sheet open={navDrawerOpen}")).toBeTruthy();
  expect(appSource.includes("<DesktopOnly")).toBeTruthy();
});

test("reorder-by-drag is a desktop-only enhancement (no dead gesture on touch)", () => {
  expect(appSource.includes(
          "draggable={!isClosing && !readOnlySessionView && !isCompact}",
        ), "session rows must be non-draggable on compact viewports").toBeTruthy();
});

test("compact nav drawer keeps the session delete affordance visible, not hover-only", () => {
  const rule = cssRule(".sidebar-in-drawer .session-delete");
  expect(rule).toMatch(/color:\s*var\(--text-muted\);/);
});

test("desktop-only boundary renders an honest card, not a blank surface", () => {
  expect(appSource.includes('feature="terminal sessions"')).toBeTruthy();
  const rule = cssRule(".desktop-only-title");
  expect(rule).toMatch(/color:\s*var\(--text-primary\);/);
});
