import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test, expect } from "vitest";

import { BP_COMPACT, BP_PHONE } from "./breakpoints.ts";

const dir = import.meta.dirname;
const indexCss = readFileSync(join(dir, "index.css"), "utf8");
const appSource = readFileSync(join(dir, "App.tsx"), "utf8");
const hoverCardSource = readFileSync(
  join(dir, "components/ui/hover-card.tsx"),
  "utf8",
);
const sheetSource = readFileSync(join(dir, "components/ui/sheet.tsx"), "utf8");

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
          "const rowReadOnly = readOnlySessionView || s.read_only_hidden === true;",
        ), "session rows must include read-only hidden rows in the read-only guard").toBeTruthy();
  expect(appSource.includes(
          "draggable={!isClosing && !rowReadOnly && !isCompact}",
        ), "session rows must be non-draggable on compact viewports").toBeTruthy();
});

test("compact nav drawer keeps the session delete affordance visible, not hover-only", () => {
  const rule = cssRule(".sidebar-in-drawer .session-delete");
  expect(rule).toMatch(/color:\s*var\(--text-muted\);/);
});

test("index.css viewport breakpoints consolidate to the canonical tiers", () => {
  // Only @media viewport breakpoints are governed by the canonical-tier rule;
  // @container queries are component-level and intentionally out of scope.
  // BP_COMPACT toggles the shell, BP_PHONE is the densest tuning. This guard
  // stops ad-hoc widths from creeping back in (design-system.md: "do not
  // sprinkle new ad-hoc widths").
  const widths = [...indexCss.matchAll(/@media[^{]*max-width:\s*(\d+)px/g)].map(
    (m) => Number(m[1]),
  );
  const ALLOWED = new Set<number>([BP_COMPACT, BP_PHONE]);
  // Documented, justified exceptions (surface + reason):
  //  - 1100: the debug session-list diagnostic grid is a desktop-only admin
  //    surface, intentionally wide, and never part of phone triage.
  const DOCUMENTED_EXCEPTIONS = new Set<number>([1100]);
  const offenders = [
    ...new Set(
      widths.filter((w) => !ALLOWED.has(w) && !DOCUMENTED_EXCEPTIONS.has(w)),
    ),
  ];
  expect(
    offenders,
    `ad-hoc @media max-width breakpoints must use ${BP_COMPACT}/${BP_PHONE} or be a documented exception; found: ${offenders.join(", ")}`,
  ).toEqual([]);
});

test("drawer session rows are touch-sized on compact", () => {
  // Visible-on-touch is necessary but not sufficient; the target must be tappable.
  const del = cssRule(".sidebar-in-drawer .session-delete");
  expect(del).toMatch(/width:\s*2\.5rem;/);
  expect(del).toMatch(/height:\s*2\.5rem;/);
  const row = cssRule(".sidebar-in-drawer .session-open");
  expect(row).toMatch(/padding-top:\s*var\(--space-2\);/);
});

test("desktop-only boundary renders an honest card, not a blank surface", () => {
  expect(appSource.includes('feature="terminal sessions"')).toBeTruthy();
  const rule = cssRule(".desktop-only-title");
  expect(rule).toMatch(/color:\s*var\(--text-primary\);/);
});

test("overlay menus are viewport-clamped so none overflows a phone width", () => {
  // Custom + fixed-width popovers must shrink below their natural width on a
  // narrow viewport, mirroring the .run-turn-view-page-select clamp. Width-only
  // safety; the menus' contents are unchanged.
  for (const selector of [
    ".dropdown",
    ".run-tab-more-menu",
    ".session-tab-menu",
    ".run-turn-view-select-menu",
  ]) {
    expect(
      cssRule(selector),
      `${selector} should carry a calc(100vw - 2rem) clamp`,
    ).toMatch(/max-width:\s*min\([^;]*calc\(100vw - 2rem\)\)/);
  }
});

test("hover card content cannot exceed a phone viewport width", () => {
  expect(hoverCardSource).toContain("max-w-[calc(100vw-2rem)]");
});

test("turn/page pager collapses to a single sheet-backed button on compact", () => {
  // The desktop 7-control stepper does not fit a phone titlebar; on compact it
  // becomes one position button that opens the identical controls in a bottom
  // sheet. The same `controls` fragment feeds both viewports so navigation
  // cannot diverge.
  expect(appSource).toContain('className="run-turn-pager-compact-trigger"');
  expect(appSource).toContain('<SheetContent side="bottom"');
  expect(appSource).toContain("const controls = (");
  expect(appSource).toMatch(/run-turn-pager-sheet-body">\{controls\}/);
});

test("compact pager stays present with no turns instead of vanishing", () => {
  // Mirrors the transcript-navigation never-hidden invariant: the trigger renders
  // disabled ("No turns") rather than being omitted when there is nothing to page.
  expect(appSource).toMatch(
    /run-turn-pager-compact-trigger[\s\S]*?disabled=\{combinedEntries\.length === 0\}/,
  );
  expect(appSource).toContain(
    'className="run-turn-pager-compact-label">No turns',
  );
});

test("sheet primitive supports a bottom-anchored variant", () => {
  expect(sheetSource).toContain('"left" | "right" | "bottom"');
  expect(sheetSource).toContain("slide-in-from-bottom");
});

test("files view becomes single-column master-detail on a phone", () => {
  // The 18rem | 1fr split does not fit a phone; at <= BP_PHONE the list and
  // viewer stack and swap (list -> tap file -> full-width viewer + back),
  // driven by isPhone + the existing selectedFile state.
  expect(indexCss).toMatch(/\.run-files-body \{\s*grid-template-columns: 1fr;/);
  expect(indexCss).toContain(".run-files-body-detail .run-files-list");
  expect(appSource).toContain('className="run-files-viewer-back"');
  expect(appSource).toContain(
    'isPhone && selectedFile ? " run-files-body-detail"',
  );
});
