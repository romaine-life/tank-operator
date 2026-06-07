import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test, expect } from "vitest";

// The "focused reading surface" affordance is the blue left-rail + soft wash
// painted on the scrollable <main> when it takes focus — by clicking empty
// space in the surface, or via slash→tab. It signals "this surface is now
// armed for keyboard navigation (R / Home / End / T)".
//
// Both primary reading surfaces must show it: the main transcript AND the Turns
// view, which is "the primary session view" per
// docs/features/transcript-navigation. The affordance is keyed off the
// surface's aria-label by codebase convention (migrationPolicy.test.ts pins the
// transcript selectors); the Turns surface carries aria-label "Turn view".

const here = import.meta.dirname;
const indexCssSource = readFileSync(join(here, "index.css"), "utf8");
const appSource = readFileSync(join(here, "App.tsx"), "utf8");

test("the shared <main> labels the Turns surface 'Turn view'", () => {
  // WorkspaceShell renders one focusable <main> for chat / turns / files /
  // settings; only chat ("Transcript") and turns ("Turn view") are reading
  // surfaces that should light up. Everything else stays "Workspace panel".
  expect(appSource).toMatch(/activeTab === "turns"\s*\?\s*"Turn view"/);
  expect(appSource).toMatch(/activeTab === "chat"\s*\?\s*"Transcript"/);
});

test("Turns view shares the transcript focus rail + wash affordance", () => {
  // The focus rule lists the Turns surface beside the transcript, so focusing
  // the Turns <main> paints the same rail + wash.
  expect(
    indexCssSource.includes(
      '.run-main[aria-label="Turn view"]:is(:focus, :focus-within),',
    ),
  ).toBe(true);
  // The transcript surface stays covered (reinforces the migration-policy pin).
  expect(
    indexCssSource.includes(
      '.run-main[aria-label="Transcript"]:is(:focus, :focus-within),',
    ),
  ).toBe(true);
  // The supporting rules — the focus transition and the rail-left custom
  // property the rail gradient reads — must cover the Turns surface too, or the
  // rail would not render / animate in like it does for chat. The selector line
  // appears in both of those grouped rules.
  expect(indexCssSource.includes('.run-main[aria-label="Turn view"],')).toBe(
    true,
  );
});
