import assert from "node:assert/strict";
import { test } from "node:test";

import {
  resolveComposerFocusShortcut,
  type ComposerFocusShortcutContext,
} from "./composerFocusShortcut.ts";

function ctx(
  overrides: Partial<ComposerFocusShortcutContext> = {},
): ComposerFocusShortcutContext {
  return {
    key: "/",
    altKey: false,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    isComposing: false,
    targetIsComposer: false,
    activeTab: "chat",
    ...overrides,
  };
}

test("plain / on the chat tab focuses the composer in place", () => {
  assert.equal(resolveComposerFocusShortcut(ctx()), "focus-in-place");
});

test("plain / on the turns tab focuses the composer in place", () => {
  // Regression: the turns view renders the same composer, so `/` must focus it
  // where it is instead of navigating back to the main transcript.
  assert.equal(
    resolveComposerFocusShortcut(ctx({ activeTab: "turns" })),
    "focus-in-place",
  );
});

test("/ on a tab without an in-page composer switches to turns first", () => {
  for (const activeTab of ["files", "background", "session-data", "terminal"]) {
    assert.equal(
      resolveComposerFocusShortcut(ctx({ activeTab })),
      "switch-to-turns",
      `expected switch-to-turns for ${activeTab}`,
    );
  }
});

test("/ is ignored once the composer textarea owns the keystroke", () => {
  // Typing `/` inside the composer must keep its normal slash-palette behavior.
  assert.equal(
    resolveComposerFocusShortcut(ctx({ targetIsComposer: true })),
    "ignore",
  );
  assert.equal(
    resolveComposerFocusShortcut(
      ctx({ targetIsComposer: true, activeTab: "turns" }),
    ),
    "ignore",
  );
});

test("/ shortcut ignores modifiers and IME composition", () => {
  assert.equal(resolveComposerFocusShortcut(ctx({ altKey: true })), "ignore");
  assert.equal(resolveComposerFocusShortcut(ctx({ ctrlKey: true })), "ignore");
  assert.equal(resolveComposerFocusShortcut(ctx({ metaKey: true })), "ignore");
  assert.equal(resolveComposerFocusShortcut(ctx({ shiftKey: true })), "ignore");
  assert.equal(
    resolveComposerFocusShortcut(ctx({ isComposing: true })),
    "ignore",
  );
});

test("only the / key triggers the shortcut", () => {
  for (const key of ["t", "Escape", "Enter", "?", "/ ", "Slash"]) {
    assert.equal(
      resolveComposerFocusShortcut(ctx({ key })),
      "ignore",
      `expected ignore for key ${JSON.stringify(key)}`,
    );
  }
});
