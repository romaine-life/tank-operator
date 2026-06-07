import { readFileSync } from "node:fs";

import { test, expect } from "vitest";

import { KEYBOARD_SHORTCUTS } from "./keyboardShortcuts";

const appSource = readFileSync(new URL("./App.tsx", import.meta.url), "utf8");
const refreshSource = readFileSync(
  new URL("./transcriptRefreshShortcut.ts", import.meta.url),
  "utf8",
);
const viewSource = readFileSync(
  new URL("./transcriptViewShortcuts.ts", import.meta.url),
  "utf8",
);
// Every place a user-facing keyboard shortcut is actually decided.
const handlerSource = appSource + refreshSource + viewSource;

test("the Help panel's Keyboard section renders from the registry", () => {
  // Rows are generated from KEYBOARD_SHORTCUTS, not hand-maintained, so the
  // documented set can't drift from the registry.
  expect(appSource).toMatch(/KEYBOARD_SHORTCUTS\.map\(/);
  // The retired hand-written rows must not linger beside the generated ones —
  // that duplication is exactly the drift this registry retires (the old list
  // also silently omitted the real T / Escape shortcuts).
  expect(appSource.includes('<span className="run-help-key">Home / End</span>')).toBe(
    false,
  );
});

test("registry entries are unique and well-formed", () => {
  const ids = new Set<string>();
  const labels = new Set<string>();
  for (const shortcut of KEYBOARD_SHORTCUTS) {
    expect(ids.has(shortcut.id), `duplicate id: ${shortcut.id}`).toBe(false);
    expect(
      labels.has(shortcut.keys),
      `duplicate key label: ${shortcut.keys}`,
    ).toBe(false);
    ids.add(shortcut.id);
    labels.add(shortcut.keys);
    expect(shortcut.tokens.length > 0, `${shortcut.id} declares no tokens`).toBe(
      true,
    );
    expect(
      shortcut.description.trim().length > 0,
      `${shortcut.id} has an empty description`,
    ).toBe(true);
  }
});

test("every documented shortcut key is wired in a real handler", () => {
  // Structural parity: each registered key token must appear as a quoted key in
  // a handler source, so the Help panel can't advertise a shortcut the code
  // doesn't honor (and a new shortcut can't ship undocumented).
  for (const shortcut of KEYBOARD_SHORTCUTS) {
    for (const token of shortcut.tokens) {
      expect(
        handlerSource.includes(`"${token}"`),
        `${shortcut.id}: key token "${token}" is not matched in any handler`,
      ).toBe(true);
    }
  }
});

test("the live T and Escape view shortcuts are documented", () => {
  // Regression guard for the specific gap that motivated the registry: these
  // tested predicates existed but the old hand-written help omitted them.
  const tokens = new Set(KEYBOARD_SHORTCUTS.flatMap((s) => s.tokens));
  expect(viewSource.includes('=== "t"'), "T predicate should still exist").toBe(
    true,
  );
  expect(
    viewSource.includes('=== "Escape"'),
    "Escape predicate should still exist",
  ).toBe(true);
  expect(tokens.has("t"), "T must be in the keyboard registry").toBe(true);
  expect(tokens.has("Escape"), "Escape must be in the keyboard registry").toBe(
    true,
  );
});
